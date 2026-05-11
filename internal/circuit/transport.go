package circuit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// errRetryBodyTooLarge is an internal sentinel returned by cacheBody when
// the incoming request body exceeds Config.MaxRetryableBodyBytes.  We
// treat it as "body too large to buffer — fall through and disable
// retries" rather than a hard error back to the caller, so oversize
// requests still reach the upstream on a best-effort basis.
var errRetryBodyTooLarge = errors.New("circuit: request body exceeds MaxRetryableBodyBytes")

// retryAttemptKey is the context key used to pass the current attempt index
// down to the inner transport (used by the test-mode transport).
type retryAttemptKey struct{}

// Transport wraps an inner http.RoundTripper with circuit-breaker logic:
//
//   - checks the circuit state before every logical request;
//   - retries transient (degraded-class) failures up to MaxTransientRetries;
//   - retries rate-limit failures up to MaxRateLimitRetries with Retry-After
//     backoff;
//   - records terminal failures in the Store and opens the circuit when the
//     threshold is crossed;
//   - performs a single probe request when the circuit is Half-Open;
//   - injects Config.DegradedSignal into every synthesised degraded response
//     body so downstream clients can reliably detect provider degradation
//     even after SDK / framework exception wrapping hides headers and status
//     codes (see DefaultDegradedSignal for the full rationale).
type Transport struct {
	inner    http.RoundTripper
	store    Store
	cfg      Config
	provider string
	log      *slog.Logger
	metrics  MetricsSink
	modelFn  ModelFromRequestFunc
}

// NewTransport wraps inner with circuit-breaker behaviour for provider.
//
// Optional behaviour (dogstatsd metric emission, model-name
// extraction) is configured via Option values; see WithMetrics and
// WithModelExtractor.  Callers that pass no options get a transport
// that logs but does not emit metrics — which preserves the previous
// behaviour exactly.
func NewTransport(inner http.RoundTripper, store Store, cfg Config, provider string, log *slog.Logger, opts ...Option) *Transport {
	if log == nil {
		log = slog.Default()
	}
	t := &Transport{
		inner:    inner,
		store:    store,
		cfg:      cfg.Defaults(),
		provider: provider,
		log:      log,
		metrics:  noopMetrics{},
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// maxErrorStringLength bounds how much of an upstream / network error we
// embed in slog attributes and metric tags.  Long errors (e.g. wrapped
// TLS chains) blow up log volume and cardinality if untrimmed.
const maxErrorStringLength = 256

// failureContext is the shared, pre-computed payload that every
// circuit-breaker observability call site uses.  Building it once per
// event (and reusing it for both the slog line and the dogstatsd
// counter) means we parse the request body for model attribution at
// most once per failure even when the same event also crosses a state
// transition or a "threshold crossed" warn line.
//
// Field set is deliberately flat so Datadog can facet on each
// attribute and so the metric tag set stays low-cardinality (model is
// bounded by the upstream catalogue, kind by the FailureKind enum,
// status_code by HTTP).
type failureContext struct {
	Provider    string
	Model       string
	Path        string
	Method      string
	StatusCode  int
	Kind        FailureKind
	ErrorString string
}

// newFailureContext builds the canonical context for a failure event.
// Pass nil resp / nil err for synthetic events (fast-fail, probe slot
// taken) and follow up with .withKind(KindCircuitOpen) so the kind
// reflects "we deliberately didn't try" rather than "upstream returned
// nothing".
func (t *Transport) newFailureContext(req *http.Request, resp *http.Response, err error) failureContext {
	fc := failureContext{
		Provider:    t.provider,
		Kind:        ClassifyFailureKind(t.provider, resp, err),
		ErrorString: truncateError(err),
	}
	if req != nil {
		if req.URL != nil {
			fc.Path = req.URL.Path
		}
		fc.Method = req.Method
	}
	if resp != nil {
		fc.StatusCode = resp.StatusCode
	}
	if t.modelFn != nil && req != nil && req.GetBody != nil {
		// Best-effort: only call the extractor when the request body is
		// already replayable.  Synthetic fast-fail paths run before cacheBody,
		// and allowing extractors to fall back to req.Body there can turn an
		// open circuit into an unbounded body read just for observability.
		fc.Model = t.modelFn(req)
	}
	return fc
}

// withKind returns a copy of fc with the failure kind overridden.
// Used by synthetic-event call sites (fast-fail, probe slot taken,
// would-have-fast-failed, body too large) where the default
// classification (which keys off resp/err that don't exist) is
// misleading.
func (fc failureContext) withKind(k FailureKind) failureContext {
	fc.Kind = k
	return fc
}

// attrs returns a slog-friendly attribute slice with a stable schema.
// The exact field set is documented on failureContext above.
func (fc failureContext) attrs() []any {
	return []any{
		"provider", fc.Provider,
		"model", fc.Model,
		"path", fc.Path,
		"method", fc.Method,
		"status_code", fc.StatusCode,
		"failure_kind", string(fc.Kind),
		"error", fc.ErrorString,
	}
}

// metricTags returns the dogstatsd tag set matching attrs().
//
// Empty model is rewritten to "unknown" because Datadog silently drops
// tags with empty values, which would make the rest of the tag list
// shift and break facet filters.  All other empty values are
// preserved (status_code:0 for transport errors, etc.).
func (fc failureContext) metricTags() []string {
	model := fc.Model
	if model == "" {
		model = "unknown"
	}
	return []string{
		"provider:" + fc.Provider,
		"model:" + model,
		"status_code:" + strconv.Itoa(fc.StatusCode),
		"failure_kind:" + string(fc.Kind),
	}
}

// emit publishes a circuit.<event> counter using the supplied context.
// We deliberately ignore any dogstatsd error: UDP packet loss or an
// Agent restart must never affect request flow.  The cost-tracking
// transport follows the same convention (see internal/cost/datadog.go).
func (t *Transport) emit(event string, fc failureContext) {
	if t.metrics == nil {
		return
	}
	_ = t.metrics.Incr("circuit."+event, fc.metricTags(), 1.0)
}

// truncateError returns a slog-friendly truncation of err for use in
// the "error" attribute of failure log lines.
func truncateError(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	if len(s) > maxErrorStringLength {
		return s[:maxErrorStringLength] + "...(truncated)"
	}
	return s
}

// RoundTrip implements http.RoundTripper.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	// ── Observe-only / log-mode fast path ─────────────────────────────────
	// In ModeLog we intentionally skip the retry loop, fast-fail, and
	// synthetic-response machinery entirely.  We just do one round trip,
	// classify the result, record failures so /health and Redis counters
	// are accurate, emit counterfactual logs, and hand the real response
	// back to the caller.  This lets us roll out the feature to prod
	// without any behavioural change to traffic.
	if t.cfg.Mode == ModeLog {
		return t.runObserveOnly(req)
	}

	// ── Test-mode: force_degraded fast path ───────────────────────────────
	// force_transient_recover is handled inside runWithRetries so it can
	// interact with the retry loop's attempt counter.
	//
	// Test-mode honouring is gated on Config.TestModeEnabled so a prod
	// deployment cannot be tricked into emitting synthetic degraded
	// responses by a client setting X-LLM-Proxy-Test-Mode or
	// llm_proxy_test_mode.  Plumbed from the CLI's three-condition
	// security gate in cmd/llm-proxy/main.go.
	if t.testModeFromRequest(req) == TestModeForceDegraded {
		t.log.Info("circuit: test-mode force_degraded",
			t.newFailureContext(req, nil, nil).withKind(KindCircuitOpen).attrs()...)
		return t.degradedResponse(req), nil
	}

	// ── Circuit state check ───────────────────────────────────────────────
	state, err := t.store.GetState(ctx, t.provider)
	if err != nil {
		t.log.Warn("circuit: GetState error, treating as closed", "provider", t.provider, "error", err)
		state = StateClosed
	}

	switch state {
	case StateOpen:
		fc := t.newFailureContext(req, nil, nil).withKind(KindCircuitOpen)
		t.log.Warn("circuit: fast-fail (circuit open)",
			append(fc.attrs(), "mode", ModeEnforce)...)
		t.emit("fast_fail", fc)
		return t.degradedResponse(req), nil

	case StateHalfOpen:
		return t.runProbe(req)

	default: // StateClosed — normal path
		return t.runWithRetries(req)
	}
}

// runObserveOnly performs a single pass-through RoundTrip, records observed
// failures + emits counterfactual log lines, and returns the real upstream
// response.  No retries, no synthetic responses, no fast-fail on open
// circuit — this is the "shadow" rollout path (ModeLog).
//
// Even though we never replay the body in this mode, we still call
// cacheBody so the configured ModelFromRequestFunc has something to
// read on the failure path.  The cost matches what runWithRetries
// already pays for retry support, and an oversize body falls through
// to a streaming pass-through exactly like there.
func (t *Transport) runObserveOnly(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	if err := t.cacheBody(req); err != nil {
		if !errors.Is(err, errRetryBodyTooLarge) {
			// Hard cacheBody error: req.Body has been read and closed,
			// so we can't safely fall through to the inner transport.
			// Surface the error to the caller — this matches the
			// runWithRetries behaviour and is far better than silently
			// dispatching a half-consumed body.
			return nil, fmt.Errorf("circuit: cacheBody (log-mode): %w", err)
		}
		// Oversize body: req.Body has been rewired to a streaming
		// pass-through, GetBody is nil, model extraction will return
		// "" — that's acceptable for log mode.
		t.log.Warn("circuit: log-mode request body exceeds MaxRetryableBodyBytes, model attribution unavailable",
			"provider", t.provider,
			"path", req.URL.Path,
			"content_length", req.ContentLength,
			"max_retryable_body_bytes", t.cfg.MaxRetryableBodyBytes,
			"failure_kind", string(KindBodyTooLarge),
		)
	}

	// Log what ModeEnforce *would* have done given the current state, but
	// always let the real request through.
	if state, err := t.store.GetState(ctx, t.provider); err == nil && state == StateOpen {
		fc := t.newFailureContext(req, nil, nil).withKind(KindCircuitOpen)
		t.log.Info("circuit: log-mode would_have_fast_failed (circuit open, passing through)", fc.attrs()...)
		t.emit("would_have_fast_failed", fc)
	}

	resp, err := t.inner.RoundTrip(req)
	class := ClassifyResponse(t.provider, resp, err)

	switch class {
	case FailureClassDegraded:
		// Record the failure so cross-instance counters and /health stats
		// still reflect reality in shadow mode.  The Store is guaranteed
		// to fail-open on Redis errors (returns StateClosed, no error),
		// so this cannot cascade.
		newState, recErr := t.store.RecordTerminalFailure(ctx, t.provider)
		if recErr != nil {
			t.log.Error("circuit: log-mode RecordTerminalFailure error",
				"provider", t.provider, "error", recErr)
		}
		fc := t.newFailureContext(req, resp, err)
		t.log.Info("circuit: log-mode terminal_failure_observed (no synthetic response, passing through)",
			append(fc.attrs(),
				"would_be_new_state", newState.String(),
				"mode", ModeLog,
			)...)
		t.emit("terminal_failure", fc)

	case FailureClassGlobalRateLimit:
		fc := t.newFailureContext(req, resp, err)
		t.log.Info("circuit: log-mode global_rate_limit_observed (passing through)", fc.attrs()...)
		t.emit("global_rate_limit", fc)

	case FailureClassLocalRateLimit:
		fc := t.newFailureContext(req, resp, err)
		t.log.Info("circuit: log-mode local_rate_limit_observed (passing through)", fc.attrs()...)
		t.emit("local_rate_limit", fc)
	}

	return resp, err
}

// ─────────────────────────────────────────────────────────────────────────────
// Core retry loops
// ─────────────────────────────────────────────────────────────────────────────

// slowRequestThreshold is the floor above which the success path emits an
// info-level breakdown of where time was spent (cacheBody vs upstream
// RoundTrip).  Sub-threshold requests stay silent so happy traffic does not
// drown the logs; anything slower than this gets a single per-step
// breakdown line so latency regressions are immediately attributable.
const slowRequestThreshold = 5 * time.Second

// runWithRetries executes the request with the configured retry policies and
// records terminal failures in the circuit store.
func (t *Transport) runWithRetries(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	startedAt := time.Now()

	// Ensure the body can be re-read on retries.  When the body is too
	// large to buffer in memory we gracefully disable retries and fall
	// through to a single-pass RoundTrip so oversize requests (e.g. a
	// multi-megabyte vision payload) still reach the upstream without
	// giving a client an unbounded-memory DoS vector.
	cacheBodyStart := time.Now()
	if err := t.cacheBody(req); err != nil {
		if errors.Is(err, errRetryBodyTooLarge) {
			t.log.Warn("circuit: request body exceeds MaxRetryableBodyBytes, retries disabled for this request",
				"provider", t.provider,
				"path", req.URL.Path,
				"content_length", req.ContentLength,
				"max_retryable_body_bytes", t.cfg.MaxRetryableBodyBytes,
				"failure_kind", string(KindBodyTooLarge),
			)
			return t.inner.RoundTrip(req)
		}
		return nil, fmt.Errorf("circuit: cacheBody: %w", err)
	}
	cacheBodyDur := time.Since(cacheBodyStart)

	var (
		transientAttempts int
		rateLimitAttempts int
		// upstreamDur is the time spent inside the inner RoundTripper
		// (TLS handshake + body upload + upstream processing + first
		// response byte).  Accumulates across retries so the breakdown
		// log line below reflects the full wall-clock cost.
		upstreamDur time.Duration
	)

	// Track whether we've had any rate-limit failures for escalation logic.
	var firstRateLimitAt time.Time

	// Remember the most recent failure context so log lines / metrics
	// emitted after the response body is drained still carry the
	// upstream status code, kind, and error string.  StatusCode is the
	// only field we read off of resp post-drain so this is safe.
	var (
		lastResp *http.Response
		lastErr  error
	)

	for {
		// Bail out early if the caller has gone away.  Without this check
		// we would still consume retry budget and record terminal
		// failures for requests whose downstream has already cancelled.
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		attempt := transientAttempts + rateLimitAttempts
		attemptReq := req.WithContext(context.WithValue(ctx, retryAttemptKey{}, attempt))
		if attempt > 0 {
			// Restore the body for the retry.
			if req.GetBody != nil {
				body, err := req.GetBody()
				if err != nil {
					return nil, fmt.Errorf("circuit: GetBody on retry: %w", err)
				}
				attemptReq.Body = body
			}
		}

		// ── test-mode: force_transient_recover ────────────────────────────
		// Fail on attempt 0, forward to the real inner transport on attempt 1+.
		// Gated on Config.TestModeEnabled — see the header docstring on
		// Transport.testModeFromRequest for why that gate exists.
		if t.testModeFromRequest(req) == TestModeForceTransientRecover {
			if attempt == 0 {
				t.log.Info("circuit: test-mode force_transient_recover (attempt 0 → fail)",
					"provider", t.provider)
				// Simulate a degraded failure and let the retry loop continue.
				backoff := transientBackoff(transientAttempts)
				if err := t.sleep(ctx, backoff); err != nil {
					return nil, err
				}
				transientAttempts++
				continue
			}
			// Strip the test mode header on retries so the real inner transport
			// does not see it.
			inner := attemptReq.Clone(ctx)
			inner.Header.Del(TestModeHeader)
			attemptReq = inner
		}

		rtStart := time.Now()
		resp, err := t.inner.RoundTrip(attemptReq)
		upstreamDur += time.Since(rtStart)
		fc := ClassifyResponse(t.provider, resp, err)

		// ── Success ───────────────────────────────────────────────────────
		if fc == FailureClassNone {
			t.logTimingBreakdown(req, startedAt, cacheBodyDur, upstreamDur, attempt+1, resp)
			return resp, err
		}

		// Capture before drain so emitFailureMetric / failureAttrs see
		// the real upstream status.  Note: lastResp.Body is not safe to
		// read after the drain below, but lastResp.StatusCode is.
		lastResp = resp
		lastErr = err

		// ── Drain the response body before retrying so the connection is
		//    returned to the pool cleanly.
		if resp != nil {
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()              //nolint:errcheck
		}

		retryAfterSec := retryAfterSeconds(resp)

		// ── Rate-limit handling ───────────────────────────────────────────
		if fc == FailureClassLocalRateLimit || fc == FailureClassGlobalRateLimit {
			if rateLimitAttempts >= t.cfg.MaxRateLimitRetries {
				evt := t.newFailureContext(req, lastResp, lastErr)
				t.log.Warn("circuit: rate-limit retries exhausted",
					append(evt.attrs(),
						"attempts", rateLimitAttempts,
						"class", fc,
					)...)
				t.emit("rate_limit_exhausted", evt)

				// Escalate sustained global rate limits to degraded.
				if fc == FailureClassGlobalRateLimit && !firstRateLimitAt.IsZero() {
					elapsed := time.Since(firstRateLimitAt).Seconds()
					if int(elapsed) >= t.cfg.GlobalRateLimitEscalationWindow {
						t.log.Warn("circuit: global rate-limit escalated to provider_degraded",
							append(evt.attrs(), "elapsed_seconds", elapsed)...)
						return t.handleTerminalFailure(ctx, req, lastResp, lastErr)
					}
				}
				// Return a synthetic rate-limit error (no magic string — not
				// degraded, just throttled).
				return t.rateLimitResponse(fc), nil
			}
			if fc == FailureClassGlobalRateLimit && firstRateLimitAt.IsZero() {
				firstRateLimitAt = time.Now()
			}
			backoff := rateLimitBackoff(retryAfterSec, rateLimitAttempts)
			t.log.Info("circuit: rate-limit backoff",
				"provider", t.provider,
				"class", fc,
				"backoff_ms", backoff.Milliseconds(),
				"attempt", rateLimitAttempts+1,
			)
			if err := t.sleep(ctx, backoff); err != nil {
				return nil, err
			}
			rateLimitAttempts++
			continue
		}

		// ── Degraded / transient handling ─────────────────────────────────
		if fc == FailureClassDegraded {
			switch t.cfg.RetryContributionMode {
			case "on":
				t.log.Info("circuit: retried failure contributing to degradation score",
					append(t.newFailureContext(req, resp, err).attrs(),
						"attempt", transientAttempts)...)
				t.store.RecordTerminalFailure(ctx, t.provider) //nolint:errcheck
			case "log":
				t.log.Info("circuit: retried failure would_have_contributed_to_degradation",
					append(t.newFailureContext(req, resp, err).attrs(),
						"attempt", transientAttempts)...)
			}

			if transientAttempts >= t.cfg.MaxTransientRetries {
				t.log.Warn("circuit: transient retries exhausted, recording terminal failure",
					append(t.newFailureContext(req, lastResp, lastErr).attrs(),
						"attempts", transientAttempts)...)
				return t.handleTerminalFailure(ctx, req, lastResp, lastErr)
			}

			backoff := transientBackoff(transientAttempts)
			t.log.Info("circuit: transient backoff",
				"provider", t.provider,
				"backoff_ms", backoff.Milliseconds(),
				"attempt", transientAttempts+1,
			)
			if err := t.sleep(ctx, backoff); err != nil {
				return nil, err
			}
			transientAttempts++
			continue
		}

		// ── Unknown / unclassifiable failure — pass through as-is ─────────
		return resp, err
	}
}

// logTimingBreakdown emits a single per-request line showing where wall-clock
// time was spent on the happy path (cacheBody buffering vs. inner RoundTrip).
// We only log when the request crosses slowRequestThreshold so successful sub-
// second traffic stays quiet, but anything slow gets per-step attribution
// inline — this is what answers "did the circuit breaker add latency?"
// without needing to attach a profiler.
func (t *Transport) logTimingBreakdown(
	req *http.Request,
	startedAt time.Time,
	cacheBody, upstream time.Duration,
	attempts int,
	resp *http.Response,
) {
	total := time.Since(startedAt)
	if total < slowRequestThreshold {
		return
	}
	statusCode := 0
	if resp != nil {
		statusCode = resp.StatusCode
	}
	path := ""
	method := ""
	if req != nil {
		method = req.Method
		if req.URL != nil {
			path = req.URL.Path
		}
	}
	// overhead = total - upstream - cacheBody; should be ~0.  A positive
	// value means the circuit breaker bookkeeping (Store calls, retry
	// backoff sleeps, etc.) is contributing.
	overhead := total - upstream - cacheBody
	t.log.Info("circuit: slow request timing breakdown",
		"provider", t.provider,
		"path", path,
		"method", method,
		"status_code", statusCode,
		"attempts", attempts,
		"total_ms", total.Milliseconds(),
		"upstream_ms", upstream.Milliseconds(),
		"cache_body_ms", cacheBody.Milliseconds(),
		"overhead_ms", overhead.Milliseconds(),
	)
}

// runProbe executes a single half-open probe request (no retries).
func (t *Transport) runProbe(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	// Buffer the body up to MaxRetryableBodyBytes so the model extractor
	// has something replayable to read on the failure path, and so the
	// upstream RoundTrip is never handed a body that the extractor has
	// already drained.  Probes don't retry, but cacheBody is what makes
	// req.GetBody non-nil — without it the modelFn guard in
	// newFailureContext correctly skips extraction and probe_failed
	// metrics lose model attribution.  Oversize bodies fall through to
	// a streaming pass-through with model:"unknown" rather than failing
	// the probe.
	if err := t.cacheBody(req); err != nil && !errors.Is(err, errRetryBodyTooLarge) {
		return nil, fmt.Errorf("circuit: cacheBody (probe): %w", err)
	}

	// Only one goroutine/process should probe at a time.  Both MemoryStore
	// (single-process) and RedisStore (distributed) implement this via
	// TryStartProbe, which takes a context so Redis ops are bounded by the
	// request deadline.
	type probeStarter interface {
		TryStartProbe(ctx context.Context, provider string) bool
	}
	if ps, ok := t.store.(probeStarter); ok {
		if !ps.TryStartProbe(ctx, t.provider) {
			// Another probe is already in flight — fast-fail this request.
			evt := t.newFailureContext(req, nil, nil).withKind(KindCircuitOpen)
			t.log.Info("circuit: half-open probe already in flight, fast-failing", evt.attrs()...)
			t.emit("fast_fail", evt)
			return t.degradedResponse(req), nil
		}
	}

	// For distributed stores the probe slot is held by a TTL-bounded
	// Redis key; if the upstream round-trip exceeds that TTL a parallel
	// probe could win the slot.  KeepProbeAlive starts a background
	// lease-refresher that extends the TTL until we're done.  MemoryStore
	// does not expose this and is a no-op here.
	type probeLeaser interface {
		KeepProbeAlive(provider string) func()
	}
	if pl, ok := t.store.(probeLeaser); ok {
		stop := pl.KeepProbeAlive(t.provider)
		defer stop()
	}

	resp, err := t.inner.RoundTrip(req)

	// If the caller's context was cancelled or its deadline expired,
	// we learned nothing about the provider's actual health.  Silently
	// releasing the probe slot (without flipping state in either
	// direction) is the correct outcome: a subsequent request will try
	// the probe again, instead of either (a) prematurely closing the
	// circuit because we saw FailureClassNone, or (b) re-opening for a
	// full cooldown because we saw "some error".  We check ctx.Err()
	// directly rather than errors.Is(err, context.Canceled) because
	// http.RoundTrip wraps context errors inside *url.Error, and some
	// transports surface the cancellation as an io.EOF when the body
	// is drained mid-flight.
	if ctxErr := ctx.Err(); ctxErr != nil ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		t.log.Info("circuit: probe aborted by caller context, releasing probe slot without state change",
			append(t.newFailureContext(req, resp, err).attrs(),
				"ctx_err", truncateError(ctxErr),
			)...)
		if resp != nil {
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()              //nolint:errcheck
		}
		type probeReleaser interface {
			ReleaseProbe(ctx context.Context, provider string) error
		}
		if pr, ok := t.store.(probeReleaser); ok {
			// Use a detached, short-timeout context because the
			// caller's context is already dead.
			relCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			pr.ReleaseProbe(relCtx, t.provider) //nolint:errcheck
			cancel()
		}
		return nil, err
	}

	fc := ClassifyResponse(t.provider, resp, err)

	if fc == FailureClassNone {
		probeStatus := 0
		if resp != nil {
			probeStatus = resp.StatusCode
		}
		t.log.Info("circuit: probe succeeded, closing circuit",
			"provider", t.provider,
			"status_code", probeStatus,
			"new_state", StateClosed.String(),
		)
		t.store.RecordSuccess(ctx, t.provider) //nolint:errcheck
		return resp, err
	}

	evt := t.newFailureContext(req, resp, err)
	t.log.Warn("circuit: probe failed, re-opening circuit",
		append(evt.attrs(), "new_state", StateOpen.String())...)
	t.emit("probe_failed", evt)
	if resp != nil {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()              //nolint:errcheck
	}
	t.store.RecordProbeFailed(ctx, t.provider) //nolint:errcheck
	return t.degradedResponse(req), nil
}

// handleTerminalFailure records the failure, potentially opens the circuit,
// and returns the appropriate synthesised response to the caller.
//
// lastResp / lastErr describe the most recent upstream attempt (post-
// drain — only StatusCode / Header are safe to read on lastResp) so the
// emitted log line and dogstatsd counter retain status_code, model,
// failure_kind, and error context that would otherwise be lost between
// retry exhaustion and synthetic-response emission.
func (t *Transport) handleTerminalFailure(ctx context.Context, req *http.Request, lastResp *http.Response, lastErr error) (*http.Response, error) {
	newState, err := t.store.RecordTerminalFailure(ctx, t.provider)
	if err != nil {
		t.log.Error("circuit: RecordTerminalFailure error", "provider", t.provider, "error", err)
	}

	evt := t.newFailureContext(req, lastResp, lastErr)
	attrs := append(evt.attrs(),
		"new_state", newState.String(),
		"mode", ModeEnforce,
	)

	if newState == StateOpen {
		t.log.Warn("circuit: threshold crossed — circuit opened", attrs...)
	}

	t.log.Warn("circuit: terminal failure, returning degraded signal", attrs...)
	t.emit("terminal_failure", evt)
	return t.degradedResponse(req), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Synthetic response builders
// ─────────────────────────────────────────────────────────────────────────────

// degradedResponse returns a synthetic HTTP 503 response whose JSON body
// contains Config.DegradedSignal so downstream clients can detect
// proxy-originated provider degradation (see DefaultDegradedSignal).
func (t *Transport) degradedResponse(req *http.Request) *http.Response {
	body := buildDegradedBody(t.provider, t.cfg.DegradedSignal)
	return &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Status:     "503 Service Unavailable",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header: http.Header{
			"Content-Type":            []string{"application/json"},
			"X-Llm-Proxy-Error-Class": []string{string(FailureClassDegraded)},
		},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       req,
	}
}

// rateLimitResponse returns a synthetic 429 without the DegradedSignal — the
// request is throttled but the provider is not considered degraded.
func (t *Transport) rateLimitResponse(fc FailureClass) *http.Response {
	body := []byte(`{"error":{"message":"Rate limit exceeded; please retry later.","type":"rate_limit_error","code":"rate_limit_exceeded"}}`)
	return &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Status:     "429 Too Many Requests",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header: http.Header{
			"Content-Type":            []string{"application/json"},
			"X-Llm-Proxy-Error-Class": []string{string(fc)},
		},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

// buildDegradedBody returns a JSON error body containing the configured
// degraded signal.  An empty signal falls back to DefaultDegradedSignal so
// the body is never unmarked.
func buildDegradedBody(provider, signal string) []byte {
	if signal == "" {
		signal = DefaultDegradedSignal
	}
	msg := fmt.Sprintf("%s Provider %s is currently degraded or unavailable. Please try again later.",
		signal, provider)
	body := map[string]interface{}{
		"error": map[string]interface{}{
			"message": msg,
			"type":    "provider_degraded",
			"code":    "provider_degraded",
		},
	}
	b, _ := json.Marshal(body)
	return b
}

// ─────────────────────────────────────────────────────────────────────────────
// Backoff helpers
// ─────────────────────────────────────────────────────────────────────────────

// transientBackoff returns a jittered backoff duration for transient failures.
// attempt is 0-indexed (0 = first retry).
func transientBackoff(attempt int) time.Duration {
	base := 500 * time.Millisecond
	exp := base * (1 << uint(attempt)) // 500ms, 1s, 2s, ...
	jitter := time.Duration(rand.Int63n(int64(exp / 2)))
	return exp + jitter
}

// rateLimitBackoff returns a backoff duration for rate-limit failures,
// honouring a provider-supplied Retry-After value when available.
func rateLimitBackoff(retryAfterSec, attempt int) time.Duration {
	if retryAfterSec > 0 {
		return time.Duration(retryAfterSec) * time.Second
	}
	base := 1 * time.Second
	exp := base * (1 << uint(attempt)) // 1s, 2s, 4s, ...
	jitter := time.Duration(rand.Int63n(int64(exp / 2)))
	return exp + jitter
}

// retryAfterSeconds extracts the Retry-After header value as seconds.
func retryAfterSeconds(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return parseRetryAfterSeconds(resp.Header.Get("Retry-After"))
}

// ─────────────────────────────────────────────────────────────────────────────
// Misc helpers
// ─────────────────────────────────────────────────────────────────────────────

// cacheBody reads req.Body into memory and replaces it with a NopCloser
// backed by the bytes, setting GetBody so retries can re-read it.
// A no-op if req.Body is nil or already has GetBody.
//
// Returns errRetryBodyTooLarge (a sentinel the caller is expected to
// handle) when the body exceeds Config.MaxRetryableBodyBytes, rather
// than either (a) allocating the full payload anyway — which gives a
// client an easy memory-DoS primitive — or (b) returning a hard error
// that fails otherwise-legitimate large requests.  On overflow we
// leave req.Body as a streaming reader that still carries every byte
// we observed (via MultiReader over the peeked prefix + the rest of
// the original body), so the caller can still forward the request on
// a best-effort, no-retry basis.
func (t *Transport) cacheBody(req *http.Request) error {
	if req.Body == nil || req.GetBody != nil {
		return nil
	}
	cap := t.cfg.MaxRetryableBodyBytes
	if cap <= 0 {
		cap = DefaultMaxRetryableBodyBytes
	}

	// Content-Length short-circuit: if the client explicitly tells us
	// the body is too big we skip the read entirely and leave the body
	// intact for a streaming pass-through.
	if req.ContentLength > cap {
		return errRetryBodyTooLarge
	}

	// Read at most cap+1 bytes so we can detect overflow even when
	// Content-Length is unknown / chunked.  io.LimitReader is safe
	// against a body that lies about its length.
	limited := &io.LimitedReader{R: req.Body, N: cap + 1}
	prefix, err := io.ReadAll(limited)
	if err != nil {
		req.Body.Close() //nolint:errcheck
		return err
	}

	if int64(len(prefix)) > cap {
		// Overflow: rewind what we read into the front of the body so a
		// downstream streaming pass-through sees the full payload, and
		// let the caller know retries are not possible for this req.
		req.Body = struct {
			io.Reader
			io.Closer
		}{
			Reader: io.MultiReader(bytes.NewReader(prefix), req.Body),
			Closer: req.Body,
		}
		return errRetryBodyTooLarge
	}

	// Fits in memory — buffer it and enable retries.
	if err := req.Body.Close(); err != nil {
		return err
	}
	req.Body = io.NopCloser(bytes.NewReader(prefix))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(prefix)), nil
	}
	req.ContentLength = int64(len(prefix))
	return nil
}

// sleep blocks for d, respecting context cancellation.
func (t *Transport) sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// testModeFromRequest returns the test mode value from the header or, as a
// fallback, the URL query parameter.  When Config.TestModeEnabled is
// false it unconditionally returns "" so a client cannot smuggle
// X-LLM-Proxy-Test-Mode or llm_proxy_test_mode past a production
// deployment to force synthetic degraded responses.
//
// This is a method on Transport (rather than a free function) precisely
// so we always route through the config gate.  Callers inside this
// package MUST NOT read those fields directly.
func (t *Transport) testModeFromRequest(req *http.Request) string {
	if !t.cfg.TestModeEnabled {
		return ""
	}
	if v := req.Header.Get(TestModeHeader); v != "" {
		return v
	}
	if req.URL != nil {
		return req.URL.Query().Get(TestModeQueryParam)
	}
	return ""
}

// ProviderFromPath derives the provider name from the URL path prefix
// (e.g. "/openai/v1/chat/completions" → "openai").
func ProviderFromPath(path string) string {
	path = strings.TrimPrefix(path, "/")
	if idx := strings.Index(path, "/"); idx > 0 {
		return path[:idx]
	}
	return path
}
