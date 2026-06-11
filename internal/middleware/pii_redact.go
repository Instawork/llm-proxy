package middleware

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/redact"
)

// piiRedactCtxKey is the unexported context key under which the
// PIIRedactMiddleware stashes the redacted-body copy. Downstream
// consumers (cost transports, structured loggers) call
// PIIRedactedBody(r.Context()) — never look up the key directly.
type piiRedactCtxKey struct{}

// PIIRedactedBody returns the redacted copy of the request body, or
// (nil, false) when redaction is disabled, was skipped (GET, empty
// body, oversize), or failed in fail-open mode. Always type-assert via
// this helper instead of pulling the value yourself, so the unexported
// key stays single-source-of-truth.
func PIIRedactedBody(ctx context.Context) ([]byte, bool) {
	if v, ok := ctx.Value(piiRedactCtxKey{}).([]byte); ok {
		return v, true
	}
	return nil, false
}

// PIIRedactor is the subset of redact.Redactor that the middleware
// depends on. Defining the interface here lets tests inject a fake
// without standing up an httptest server, and makes the middleware
// trivially mockable in dependent packages.
type PIIRedactor interface {
	Redact(ctx context.Context, text string) (redact.Result, error)
}

// PIIRedactConfig controls the middleware behaviour. See
// internal/config.PIIRedactConfig for the YAML-facing shape; this is the
// runtime-friendly form (durations resolved, fail-mode parsed).
type PIIRedactConfig struct {
	// GlobalEnabled is the YAML features.pii_redact.enabled value.
	GlobalEnabled bool

	// FailClosed: when true, abort the request with 503 if the redactor
	// fails or times out. When false ("fail-open"), log a warning and
	// pass the request through with no redacted-body in context — the
	// upstream LLM still serves the user.
	FailClosed bool

	// MaxBodyBytes caps the size of bodies we redact. A multi-MB
	// embedding upload or vision payload would dominate latency for
	// marginal observability value; bodies above this threshold
	// short-circuit with a WARN log.
	//
	// Zero (or negative) selects a generous 1 MiB default. That fits
	// virtually every chat / completions / embeddings shape, and the
	// vast majority of vision payloads — the goal is "redact almost
	// everything and only let truly unusual uploads slip through".
	// Tune via Datadog by watching the
	// `pii_redact: body exceeds max_body_bytes; skipping` WARN counter.
	MaxBodyBytes int

	// Logger is the slog.Logger used for warnings and audit lines.
	// Zero-value falls back to slog.Default().
	Logger *slog.Logger
}

// PIIRedactMiddleware wires a PIIRedactor into the request lifecycle.
//
// What it does
// ------------
//  1. For provider routes (POST/PUT with a non-empty body), reads the
//     full body into memory, restores req.Body so the upstream proxy
//     still sees the original payload, and asks the redactor for a
//     redacted copy.
//  2. The redacted copy is stashed under piiRedactCtxKey for downstream
//     consumers (cost-tracking transports, structured loggers) to pick
//     up via PIIRedactedBody(r.Context()).
//  3. The unredacted body still reaches the upstream LLM — model quality
//     and tool-call accuracy are preserved.
//
// What it does NOT do
// -------------------
//   - Modify the response stream. Streaming output is passed through to
//     the user verbatim. Anything that wants to log/persist response
//     bodies has to redact at the point of writing, not here.
//   - Add a redact step for non-provider routes (/health, /openai/v1/models,
//     etc.). The whole feature is gated to provider POST/PUT to avoid
//     per-request latency on hot read paths.
//
// Fail modes
// ----------
//   - FailClosed=false (recommended for first rollout): a sidecar
//     timeout / 5xx logs a warning and passes the request through with
//     no redacted-body in context.
//   - FailClosed=true: the same failure responds 503 to the client.
func PIIRedactMiddleware(redactor PIIRedactor, cfg PIIRedactConfig) func(http.Handler) http.Handler {
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	maxBytes := cfg.MaxBodyBytes
	if maxBytes <= 0 {
		// 1 MiB. Generous on purpose — chat and embeddings always fit,
		// most vision payloads fit, and only true file-upload shapes
		// (multi-MB audio, large attachments) skip. Tune by watching
		// the oversize WARN counter in Datadog.
		maxBytes = 1024 * 1024
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !shouldRedactRequest(r) {
				next.ServeHTTP(w, r)
				return
			}

			keyRecord, _ := apikeys.FromContext(r.Context())
			if !apikeys.EffectiveRedactPII(cfg.GlobalEnabled, keyRecord) {
				next.ServeHTTP(w, r)
				return
			}

			body, oversize, err := readBoundedBody(r, maxBytes)
			if err != nil {
				logger.Warn("pii_redact: read body failed",
					slog.String("path", r.URL.Path),
					slog.String("error", err.Error()))
				next.ServeHTTP(w, r)
				return
			}
			// Always restore the body for the upstream proxy regardless
			// of redaction outcome — we never want to truncate or drop
			// the upstream payload.
			r.Body = io.NopCloser(bytes.NewReader(body))
			r.ContentLength = int64(len(body))

			if oversize {
				// Include the actual body_bytes so operators can see
				// HOW MUCH we overshot (a 5 % overshoot suggests a
				// trivial cap bump; a 10× overshoot suggests a
				// runaway upload) and provider so per-provider Datadog
				// monitors can be tuned independently.
				logger.Warn("pii_redact: body exceeds max_body_bytes; skipping",
					slog.String("path", r.URL.Path),
					slog.String("provider", getProviderFromPath(r.URL.Path)),
					slog.Int("body_bytes", len(body)),
					slog.Int("max_body_bytes", maxBytes))
				next.ServeHTTP(w, r)
				return
			}
			if len(body) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			redactStart := time.Now()
			result, redactErr := redactor.Redact(r.Context(), string(body))
			redactDuration := time.Since(redactStart)

			provider := getProviderFromPath(r.URL.Path)

			if redactErr != nil {
				if cfg.FailClosed {
					logger.Error("pii_redact: redactor failed; FailClosed -> 503",
						slog.String("path", r.URL.Path),
						slog.String("provider", provider),
						slog.Int("body_bytes", len(body)),
						slog.String("error", redactErr.Error()),
						slog.Duration("duration", redactDuration))
					http.Error(w, "service temporarily unavailable", http.StatusServiceUnavailable)
					return
				}
				logger.Warn("pii_redact: redactor failed; passing through unredacted (fail_open)",
					slog.String("path", r.URL.Path),
					slog.String("provider", provider),
					slog.Int("body_bytes", len(body)),
					slog.String("error", redactErr.Error()),
					slog.Duration("duration", redactDuration))
				next.ServeHTTP(w, r)
				return
			}

			// Stash the redacted bytes; collect entity-type tags
			// for the audit log without leaking raw values.
			redactedBytes := []byte(result.Text)
			ctx := context.WithValue(r.Context(), piiRedactCtxKey{}, redactedBytes)

			logger.Info("pii_redact: ok",
				slog.String("path", r.URL.Path),
				slog.String("provider", provider),
				slog.Int("body_bytes", len(body)),
				slog.Int("entity_types_detected", len(result.EntityCounts)),
				slog.Any("entity_counts", result.EntityCounts),
				slog.Duration("duration", redactDuration))

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// shouldRedactRequest filters out routes/methods that don't carry user
// PII payloads — health checks, model listings, and CORS preflight all
// skip the redactor entirely so the latency budget is reserved for
// provider POST/PUT bodies.
func shouldRedactRequest(r *http.Request) bool {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		return false
	}
	if !isProviderRoute(r.URL.Path) {
		return false
	}
	if !isAPIEndpoint(r.URL.Path) {
		return false
	}
	// /v1/models and any other GETish "list" routes never reach this
	// branch (caught by isAPIEndpoint already), but we keep the explicit
	// guard so a future addition to isAPIEndpoint that includes a non-
	// PII-bearing route doesn't silently start redacting it.
	if strings.HasSuffix(r.URL.Path, "/models") {
		return false
	}
	return true
}

// readBoundedBody reads up to maxBytes+1 bytes; if the body has more,
// returns oversize=true so the caller can short-circuit redaction
// without exhausting memory on a runaway upload. The full body is
// always restored to the request — we never truncate what reaches
// upstream.
func readBoundedBody(r *http.Request, maxBytes int) (body []byte, oversize bool, err error) {
	if r.Body == nil {
		return nil, false, nil
	}
	body, err = io.ReadAll(r.Body)
	if err != nil {
		return nil, false, err
	}
	oversize = len(body) > maxBytes
	return body, oversize, nil
}
