package circuit

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"strings"
	"time"
)

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
}

// NewTransport wraps inner with circuit-breaker behaviour for provider.
func NewTransport(inner http.RoundTripper, store Store, cfg Config, provider string, log *slog.Logger) *Transport {
	if log == nil {
		log = slog.Default()
	}
	return &Transport{
		inner:    inner,
		store:    store,
		cfg:      cfg.Defaults(),
		provider: provider,
		log:      log,
	}
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
	if testModeFromRequest(req) == TestModeForceDegraded {
		t.log.Info("circuit: test-mode force_degraded", "provider", t.provider)
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
		t.log.Info("circuit: fast-fail (circuit open)", "provider", t.provider)
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
func (t *Transport) runObserveOnly(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	// Log what ModeEnforce *would* have done given the current state, but
	// always let the real request through.
	if state, err := t.store.GetState(ctx, t.provider); err == nil && state == StateOpen {
		t.log.Info("circuit: log-mode would_have_fast_failed (circuit open, passing through)",
			"provider", t.provider)
	}

	resp, err := t.inner.RoundTrip(req)
	fc := ClassifyResponse(t.provider, resp, err)

	switch fc {
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
		t.log.Info("circuit: log-mode terminal_failure_observed (no synthetic response, passing through)",
			"provider", t.provider,
			"would_be_new_state", newState.String(),
		)

	case FailureClassGlobalRateLimit:
		t.log.Info("circuit: log-mode global_rate_limit_observed (passing through)",
			"provider", t.provider)

	case FailureClassLocalRateLimit:
		t.log.Info("circuit: log-mode local_rate_limit_observed (passing through)",
			"provider", t.provider)
	}

	return resp, err
}

// ─────────────────────────────────────────────────────────────────────────────
// Core retry loops
// ─────────────────────────────────────────────────────────────────────────────

// runWithRetries executes the request with the configured retry policies and
// records terminal failures in the circuit store.
func (t *Transport) runWithRetries(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	// Ensure the body can be re-read on retries.
	if err := cacheBody(req); err != nil {
		return nil, fmt.Errorf("circuit: cacheBody: %w", err)
	}

	var (
		transientAttempts int
		rateLimitAttempts int
	)

	// Track whether we've had any rate-limit failures for escalation logic.
	var firstRateLimitAt time.Time

	for {
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
		if testModeFromRequest(req) == TestModeForceTransientRecover {
			if attempt == 0 {
				t.log.Info("circuit: test-mode force_transient_recover (attempt 0 → fail)",
					"provider", t.provider)
				// Simulate a degraded failure and let the retry loop continue.
				backoff := transientBackoff(transientAttempts)
				t.sleep(ctx, backoff)
				transientAttempts++
				continue
			}
			// Strip the test mode header on retries so the real inner transport
			// does not see it.
			inner := attemptReq.Clone(ctx)
			inner.Header.Del(TestModeHeader)
			attemptReq = inner
		}

		resp, err := t.inner.RoundTrip(attemptReq)
		fc := ClassifyResponse(t.provider, resp, err)

		// ── Success ───────────────────────────────────────────────────────
		if fc == FailureClassNone {
			return resp, err
		}

		// ── Drain the response body before retrying so the connection is
		//    returned to the pool cleanly.
		if resp != nil {
			io.Copy(io.Discard, resp.Body) //nolint:errcheck
			resp.Body.Close()
		}

		retryAfterSec := retryAfterSeconds(resp)

		// ── Rate-limit handling ───────────────────────────────────────────
		if fc == FailureClassLocalRateLimit || fc == FailureClassGlobalRateLimit {
			if rateLimitAttempts >= t.cfg.MaxRateLimitRetries {
				t.log.Warn("circuit: rate-limit retries exhausted",
					"provider", t.provider,
					"attempts", rateLimitAttempts,
					"class", fc,
				)
				// Escalate sustained global rate limits to degraded.
				if fc == FailureClassGlobalRateLimit && !firstRateLimitAt.IsZero() {
					elapsed := time.Since(firstRateLimitAt).Seconds()
					if int(elapsed) >= t.cfg.GlobalRateLimitEscalationWindow {
						t.log.Warn("circuit: global rate-limit escalated to provider_degraded",
							"provider", t.provider,
							"elapsed_seconds", elapsed,
						)
						return t.handleTerminalFailure(ctx, req)
					}
				}
				// Return a synthetic rate-limit error (no magic string — not
				// degraded, just throttled).
				return t.rateLimitResponse(), nil
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
			t.sleep(ctx, backoff)
			rateLimitAttempts++
			continue
		}

		// ── Degraded / transient handling ─────────────────────────────────
		if fc == FailureClassDegraded {
			if t.cfg.RetryContributionMode == "on" {
				t.log.Info("circuit: retried failure contributing to degradation score",
					"provider", t.provider, "attempt", transientAttempts)
				t.store.RecordTerminalFailure(ctx, t.provider) //nolint:errcheck
			} else if t.cfg.RetryContributionMode == "log" {
				t.log.Info("circuit: retried failure would_have_contributed_to_degradation",
					"provider", t.provider, "attempt", transientAttempts)
			}

			if transientAttempts >= t.cfg.MaxTransientRetries {
				t.log.Warn("circuit: transient retries exhausted, recording terminal failure",
					"provider", t.provider,
					"attempts", transientAttempts,
				)
				return t.handleTerminalFailure(ctx, req)
			}

			backoff := transientBackoff(transientAttempts)
			t.log.Info("circuit: transient backoff",
				"provider", t.provider,
				"backoff_ms", backoff.Milliseconds(),
				"attempt", transientAttempts+1,
			)
			t.sleep(ctx, backoff)
			transientAttempts++
			continue
		}

		// ── Unknown / unclassifiable failure — pass through as-is ─────────
		return resp, err
	}
}

// runProbe executes a single half-open probe request (no retries).
func (t *Transport) runProbe(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	// Only one goroutine/process should probe at a time.
	type probeStarter interface {
		TryStartProbe(provider string) bool
	}
	if ps, ok := t.store.(probeStarter); ok {
		if !ps.TryStartProbe(t.provider) {
			// Another probe is already in flight — fast-fail this request.
			t.log.Info("circuit: half-open probe already in flight, fast-failing",
				"provider", t.provider)
			return t.degradedResponse(req), nil
		}
	}

	resp, err := t.inner.RoundTrip(req)
	fc := ClassifyResponse(t.provider, resp, err)

	if fc == FailureClassNone {
		t.log.Info("circuit: probe succeeded, closing circuit", "provider", t.provider)
		t.store.RecordSuccess(ctx, t.provider) //nolint:errcheck
		return resp, err
	}

	t.log.Warn("circuit: probe failed, re-opening circuit", "provider", t.provider)
	if resp != nil {
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		resp.Body.Close()
	}
	t.store.RecordProbeFailed(ctx, t.provider) //nolint:errcheck
	return t.degradedResponse(req), nil
}

// handleTerminalFailure records the failure, potentially opens the circuit,
// and returns the appropriate synthesised response to the caller.
func (t *Transport) handleTerminalFailure(ctx context.Context, req *http.Request) (*http.Response, error) {
	newState, err := t.store.RecordTerminalFailure(ctx, t.provider)
	if err != nil {
		t.log.Error("circuit: RecordTerminalFailure error", "provider", t.provider, "error", err)
	}

	if newState == StateOpen {
		t.log.Warn("circuit: threshold crossed — circuit opened",
			"provider", t.provider)
	}

	t.log.Warn("circuit: terminal failure, returning degraded signal",
		"provider", t.provider,
		"new_state", newState.String(),
	)
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
func (t *Transport) rateLimitResponse() *http.Response {
	body := []byte(`{"error":{"message":"Rate limit exceeded; please retry later.","type":"rate_limit_error","code":"rate_limit_exceeded"}}`)
	return &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Status:     "429 Too Many Requests",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header: http.Header{
			"Content-Type":            []string{"application/json"},
			"X-Llm-Proxy-Error-Class": []string{string(FailureClassLocalRateLimit)},
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
func cacheBody(req *http.Request) error {
	if req.Body == nil || req.GetBody != nil {
		return nil
	}
	b, err := io.ReadAll(req.Body)
	req.Body.Close()
	if err != nil {
		return err
	}
	req.Body = io.NopCloser(bytes.NewReader(b))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(b)), nil
	}
	req.ContentLength = int64(len(b))
	return nil
}

// sleep blocks for d, respecting context cancellation.
func (t *Transport) sleep(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-ctx.Done():
	}
}

// testModeFromRequest returns the test mode value from the header or, as a
// fallback, the URL query parameter.
func testModeFromRequest(req *http.Request) string {
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
