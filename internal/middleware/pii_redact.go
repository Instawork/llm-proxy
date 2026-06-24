package middleware

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/observability"
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
	Scrub(ctx context.Context, text string, reg *redact.Registry) (redact.Result, error)
}

// PIIStatsRecorder receives a metadata-only summary of every redaction
// outcome so the admin dashboard can surface live PII stats. It is
// defined here (rather than imported from internal/pii) to keep the
// middleware free of a dependency on the stats package; *pii.Recorder
// satisfies it. entityCounts carries only entity-type names and counts —
// never raw values. keyID is already masked by the middleware.
type PIIStatsRecorder interface {
	RecordRedaction(provider, keyID string, entityCounts map[string]int, bodyBytes int, duration time.Duration, outcome string)
}

// Redaction outcome labels passed to PIIStatsRecorder. Kept in sync with
// the constants in internal/pii.
const (
	piiOutcomeOK         = "ok"
	piiOutcomeFailOpen   = "fail_open"
	piiOutcomeFailClosed = "fail_closed"
	piiOutcomeOversize   = "oversize"
)

// MaskKeyID truncates a proxy key to a short, non-reversible identity so
// per-key stats (and cost-limit enforcement) can be attributed without
// surfacing the full secret.
//
// A bare 12-char prefix is NOT a safe identity: proxy keys are "iw:" + a long
// hex digest, so any two keys that share the first 12 characters would collapse
// into one spend bucket — silently breaking per-key cost-limit isolation (one
// key's spend would count against the other's cap). We append an FNV-1a hash of
// the WHOLE key so distinct keys cannot collide on the truncated prefix alone.
// FNV-1a is sufficient here (collisions are the only concern, keys are
// server-generated and not attacker-chosen) and is trivial to mirror byte-for-
// byte in the web UI (see web/src/lib/format.ts maskKeyId), which recomputes
// this value to join spend stats. Keys are ASCII so byte/char encodings agree.
func MaskKeyID(key string) string {
	if key == "" {
		return ""
	}
	if len(key) <= 12 {
		return key
	}
	return key[:12] + "…" + keyIDHashSuffix(key)
}

// keyIDHashSuffix returns the 8-char lowercase hex FNV-1a/32 digest of key.
func keyIDHashSuffix(key string) string {
	return apikeys.CredentialHashSuffix(key)
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

	// Recorder, when non-nil, receives a metadata-only summary of every
	// redaction outcome (detections, failures, oversize) for the admin
	// dashboard's PII section. nil disables stats collection.
	Recorder PIIStatsRecorder

	// Metrics, when non-nil, emits dogstatsd counters/distributions for
	// pii.redaction / pii.entity_detected (see features.pii_redact.datadog).
	Metrics observability.MetricsSink

	// WirePlaceholders sends scrubbed placeholder text to the upstream LLM
	// instead of observability-only redaction. When true, a per-request
	// Registry is stashed for response restore middleware.
	WirePlaceholders bool

	// DefaultAllowStreaming is the global default for whether wire-mode
	// requests may keep stream:true. Per-key allow_streaming overrides this.
	DefaultAllowStreaming bool
}

// PIIRedactMiddleware wires a PIIRedactor into the request lifecycle.
//
// What it does
// ------------
//  1. For provider routes (POST/PUT with a non-empty body), reads the
//     full body into memory and asks the redactor for a scrubbed copy.
//  2. When WirePlaceholders is true, the scrubbed copy (with MASK/SEAL
//     placeholders) replaces r.Body for the upstream proxy, and a
//     per-request Registry is stashed for response restore middleware.
//     When WirePlaceholders is false (legacy observability mode), the
//     original body still reaches the upstream LLM and only the redacted
//     copy is stashed for logs/cost transports.
//  3. Optionally forces stream:false on the outbound request when the
//     effective allow_streaming policy is false.
//
// What it does NOT do
// -------------------
//   - Restore placeholders in responses — see PIIResponseRestoreMiddleware.
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

			// Display identity for the admin "Recent scans" table: the masked
			// proxy key when present, otherwise the masked raw provider
			// credential a BYO-key caller used (sk-ant-…, AIza…) for debugging.
			keyID := piiDisplayKeyID(keyRecord, r.Context())

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
				logger.Warn("pii_redact: body exceeds max_body_bytes",
					slog.String("path", r.URL.Path),
					slog.String("provider", getProviderFromPath(r.URL.Path)),
					slog.Int("body_bytes", len(body)),
					slog.Int("max_body_bytes", maxBytes))
				recordPII(cfg.Recorder, cfg.Metrics, getProviderFromPath(r.URL.Path), keyID, nil, len(body), 0, piiOutcomeOversize)
				ctx := attachPIISummary(r.Context(), newPIISummary(PIIOutcomeOversize, nil))
				if cfg.FailClosed {
					writePIIResponseHeadersPartial(w, ctx)
					http.Error(w, "request body too large for PII redaction", http.StatusServiceUnavailable)
					return
				}
				writePIIResponseHeadersPartial(w, ctx)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}
			if len(body) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			// The deployed Presidio analyzer is text-only; handing it
			// hundreds of KB of base64 image data makes /analyze time out
			// (the 503 fail_closed incident). Embedded-image PII is covered
			// by the ID gate (OCR), so strip large image payloads to short
			// sentinels for the analyze call and restore them afterward.
			// Text-only bodies are returned unchanged, so their behaviour
			// is byte-for-byte identical to before.
			analysisBody, imageRestores := stripImageDataForAnalysis(body)
			provider := getProviderFromPath(r.URL.Path)

			redactStart := time.Now()
			var (
				result    redact.Result
				redactErr error
				registry  *redact.Registry
			)
			if cfg.WirePlaceholders {
				registry = redact.NewRegistry()
				if logger.Enabled(r.Context(), slog.LevelDebug) {
					logger.Debug("pii_redact: wire request before scrub",
						piiWireBodyLogAttrs(r.Context(), r.URL.Path, provider, body)...)
				}
				result, redactErr = redactor.Scrub(r.Context(), string(analysisBody), registry)
			} else {
				result, redactErr = redactor.Redact(r.Context(), string(analysisBody))
			}
			redactDuration := time.Since(redactStart)

			if redactErr != nil {
				if cfg.FailClosed {
					logger.Error("pii_redact: redactor failed; FailClosed -> 503",
						slog.String("path", r.URL.Path),
						slog.String("provider", provider),
						slog.Int("body_bytes", len(body)),
						slog.String("error", redactErr.Error()),
						slog.Duration("duration", redactDuration))
					recordPII(cfg.Recorder, cfg.Metrics, provider, keyID, nil, len(body), redactDuration, piiOutcomeFailClosed)
					ctx := attachPIISummary(r.Context(), newPIISummary(PIIOutcomeFailClosed, nil))
					writePIIResponseHeadersPartial(w, ctx)
					http.Error(w, "service temporarily unavailable", http.StatusServiceUnavailable)
					return
				}
				logger.Warn("pii_redact: redactor failed; passing through unredacted (fail_open)",
					slog.String("path", r.URL.Path),
					slog.String("provider", provider),
					slog.Int("body_bytes", len(body)),
					slog.String("error", redactErr.Error()),
					slog.Duration("duration", redactDuration))
				recordPII(cfg.Recorder, cfg.Metrics, provider, keyID, nil, len(body), redactDuration, piiOutcomeFailOpen)
				ctx := attachPIISummary(r.Context(), newPIISummary(PIIOutcomeFailOpen, nil))
				writePIIResponseHeadersPartial(w, ctx)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Swap the original image payloads back in. Sentinels are
			// plain ASCII tokens that carry no PII spans and are never
			// registered for response restore, so this is a clean reverse
			// of the pre-analyze strip.
			if len(imageRestores) > 0 {
				result.Text = restoreImageData(result.Text, imageRestores)
			}

			// Stash the redacted/scrubbed bytes; collect entity-type tags
			// for the audit log without leaking raw values.
			redactedBytes := []byte(result.Text)
			summary := newPIISummary(PIIOutcomeOK, result.EntityCounts)
			ctx := attachPIISummary(r.Context(), summary)
			ctx = context.WithValue(ctx, piiRedactCtxKey{}, redactedBytes)
			if cfg.WirePlaceholders {
				r.Header.Del("Accept-Encoding")
				if registry != nil && registry.Len() > 0 {
					ctx = context.WithValue(ctx, piiRegistryCtxKey{}, registry)
				}
				upstreamBody := redactedBytes
				if !apikeys.EffectiveAllowStreaming(cfg.DefaultAllowStreaming, keyRecord) {
					upstreamBody = forceStreamingOff(upstreamBody)
				}
				if logger.Enabled(ctx, slog.LevelDebug) {
					logger.Debug("pii_redact: wire request after scrub",
						piiWireBodyLogAttrs(ctx, r.URL.Path, provider, upstreamBody)...)
				}
				r.Body = io.NopCloser(bytes.NewReader(upstreamBody))
				r.ContentLength = int64(len(upstreamBody))
			}

			logger.Info("pii_redact: ok",
				slog.String("path", r.URL.Path),
				slog.String("provider", provider),
				slog.Int("body_bytes", len(body)),
				slog.Int("images_stripped", len(imageRestores)),
				slog.Int("entity_types_detected", len(result.EntityCounts)),
				slog.Any("entity_counts", result.EntityCounts),
				slog.Duration("duration", redactDuration))

			recordPII(cfg.Recorder, cfg.Metrics, provider, keyID, result.EntityCounts, len(body), redactDuration, piiOutcomeOK)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func piiWireBodyLogAttrs(ctx context.Context, path, provider string, body []byte) []any {
	sum := sha256.Sum256(body)
	return []any{
		slog.String("path", path),
		slog.String("provider", provider),
		slog.Int("body_bytes", len(body)),
		slog.Bool("utf8_valid", utf8.Valid(body)),
		slog.Bool("json_valid", json.Valid(body)),
		slog.String("sha256_prefix", hex.EncodeToString(sum[:8])),
		slog.String("preview", redact.LogPreview(ctx, string(body), 600)),
	}
}

// recordPII forwards a redaction outcome to the stats recorder when one is
// configured, masking the key ID. It is a no-op when recorder is nil.
func recordPII(
	recorder PIIStatsRecorder,
	metrics observability.MetricsSink,
	provider string,
	keyID string,
	entityCounts map[string]int,
	bodyBytes int,
	duration time.Duration,
	outcome string,
) {
	if recorder != nil {
		recorder.RecordRedaction(provider, keyID, entityCounts, bodyBytes, duration, outcome)
	}
	emitPIIRedactionMetrics(metrics, provider, outcome, entityCounts, duration)
}

// piiDisplayKeyID resolves the masked identity shown in the admin "Recent
// scans" table. A proxy-key caller is identified by its masked sk-iw key; a
// bring-your-own-key caller (no proxy record) falls back to the masked raw
// provider credential stashed by APIKeyValidationMiddleware. Both forms are
// non-reversible. Returns "" when neither is available.
func piiDisplayKeyID(keyRecord *apikeys.APIKey, ctx context.Context) string {
	if keyRecord != nil && keyRecord.PK != "" {
		return MaskKeyID(keyRecord.PK)
	}
	return InboundCredentialID(ctx)
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

// piiImageMinStrippedBytes is the smallest base64 image payload worth
// replacing before /analyze. Small inline icons cost Presidio nothing;
// the timeouts come from 100 KB–500 KB photo/scan uploads. Anything
// under this stays in the analyzed text untouched.
const piiImageMinStrippedBytes = 1024

const (
	piiImageSentinelPrefix = "__LLMPROXY_IMG_REDACTED_"
	piiImageSentinelSuffix = "__"
)

// imageRestore maps a sentinel placed in the analyze payload back to the
// original base64 image bytes it replaced.
type imageRestore struct {
	sentinel string
	original string
}

// stripImageDataForAnalysis returns a copy of body with large embedded
// image payloads replaced by short sentinels, plus the restores needed
// to swap the originals back after redaction.
//
// The deployed Presidio analyzer (presidio-analyzer) is text-only: it has
// no OCR and treats a base64 image as one enormous string, which blows the
// /analyze deadline on vision requests. Embedded-image PII is already
// handled by the ID gate, so the analyzer only needs the real text.
//
// Returns (body, nil) unchanged when the body is not JSON or carries no
// large image payloads, keeping text-only requests byte-for-byte identical.
func stripImageDataForAnalysis(body []byte) ([]byte, []imageRestore) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return body, nil
	}
	var values []string
	collectImageDataStrings(root, &values)
	if len(values) == 0 {
		return body, nil
	}

	s := string(body)
	var restores []imageRestore
	for _, v := range values {
		if len(v) < piiImageMinStrippedBytes {
			continue
		}
		sentinel := fmt.Sprintf("%s%d%s", piiImageSentinelPrefix, len(restores), piiImageSentinelSuffix)
		// Single replace: an image repeated verbatim gets one sentinel per
		// occurrence, each mapped back to the same bytes on restore.
		replaced := strings.Replace(s, v, sentinel, 1)
		if replaced == s {
			// Value not present verbatim (e.g. JSON-escaped); leave it be.
			continue
		}
		s = replaced
		restores = append(restores, imageRestore{sentinel: sentinel, original: v})
	}
	if len(restores) == 0 {
		return body, nil
	}
	return []byte(s), restores
}

// restoreImageData reverses stripImageDataForAnalysis, swapping each
// sentinel back to its original base64 payload.
func restoreImageData(text string, restores []imageRestore) string {
	for _, r := range restores {
		text = strings.Replace(text, r.sentinel, r.original, 1)
	}
	return text
}

// collectImageDataStrings walks an OpenAI / Anthropic / Gemini chat body
// and collects the raw base64 payload substrings worth stripping before
// /analyze. It mirrors the image surfaces understood by the ID gate
// (collectImages) but keeps the raw strings instead of decoding them.
func collectImageDataStrings(v any, out *[]string) {
	switch val := v.(type) {
	case map[string]any:
		if imageURL, ok := val["image_url"].(map[string]any); ok {
			if u, ok := imageURL["url"].(string); ok {
				if data := dataURLBase64(u); data != "" {
					*out = append(*out, data)
				}
			}
		}
		if source, ok := val["source"].(map[string]any); ok {
			if typ, _ := source["type"].(string); typ == "base64" {
				if data, ok := source["data"].(string); ok {
					*out = append(*out, data)
				}
			}
		}
		if inline, ok := val["inlineData"].(map[string]any); ok {
			if data, ok := inline["data"].(string); ok {
				*out = append(*out, data)
			}
		}
		for _, child := range val {
			collectImageDataStrings(child, out)
		}
	case []any:
		for _, item := range val {
			collectImageDataStrings(item, out)
		}
	}
}

// dataURLBase64 returns the base64 payload of a "data:...;base64,<payload>"
// URL, or "" if raw is not such a data URL. Only the payload is returned so
// the "data:image/png;base64," prefix stays in the body for the upstream LLM.
func dataURLBase64(raw string) string {
	const prefix = "data:"
	if !strings.HasPrefix(raw, prefix) {
		return ""
	}
	comma := strings.Index(raw, ",")
	if comma < 0 {
		return ""
	}
	if !strings.Contains(raw[len(prefix):comma], ";base64") {
		return ""
	}
	return raw[comma+1:]
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
