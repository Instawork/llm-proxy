package circuit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ─── Mock inner transport ─────────────────────────────────────────────────

// roundTripFunc lets tests build a RoundTripper from a plain func.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// makeResp is a small helper to build an *http.Response.
func makeResp(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader([]byte{})),
	}
}

// ─── Helpers ──────────────────────────────────────────────────────────────

func newTestTransport(inner http.RoundTripper) *Transport {
	cfg := Config{
		Enabled:             true,
		Mode:                ModeEnforce,
		FailureThreshold:    3,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 1, // small for tests
		MaxRateLimitRetries: 1,
		// Tests that exercise the X-LLM-Proxy-Test-Mode plumbing need
		// the gate open; tests that don't care are unaffected by the
		// setting.  Production gating is covered separately by
		// TestTransport_TestMode_DisabledByDefault_IgnoresHeader.
		TestModeEnabled: true,
	}.Defaults()
	store := NewMemoryStore(cfg)
	return NewTransport(inner, store, cfg, "openai", nil)
}

// dummyRequest creates a minimal POST request with a cacheable body.
func dummyRequest() *http.Request {
	body := `{"model":"gpt-4o","messages":[]}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/openai/v1/chat/completions",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// ─── Tests ────────────────────────────────────────────────────────────────

func TestTransport_SuccessPassthrough(t *testing.T) {
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return makeResp(200), nil
	})
	tr := newTestTransport(inner)
	resp, err := tr.RoundTrip(dummyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
}

func TestTransport_DegradedAfterTerminalFailures(t *testing.T) {
	// Always return 503.
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return makeResp(503), nil
	})
	tr := newTestTransport(inner)

	resp, err := tr.RoundTrip(dummyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// The transport retried once (MaxTransientRetries=1) then gave up.
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503 degraded response, got %d", resp.StatusCode)
	}
	// Body must contain DefaultDegradedSignal.
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), DefaultDegradedSignal) {
		t.Fatalf("DefaultDegradedSignal not found in response body: %s", b)
	}
}

func TestTransport_RetrySucceedsOnSecondAttempt(t *testing.T) {
	attempt := 0
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempt++
		if attempt == 1 {
			return makeResp(503), nil
		}
		return makeResp(200), nil
	})
	tr := newTestTransport(inner)

	resp, err := tr.RoundTrip(dummyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("want 200 on retry success, got %d", resp.StatusCode)
	}
	if attempt != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempt)
	}
}

func TestTransport_FastFailWhenCircuitOpen(t *testing.T) {
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		t.Error("inner transport should NOT be called when circuit is open")
		return makeResp(503), nil
	})
	cfg := Config{
		Enabled:             true,
		Mode:                ModeEnforce,
		FailureThreshold:    1,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 0, // no retries so circuit opens immediately
	}.Defaults()
	store := NewMemoryStore(cfg)
	// Open the circuit manually.
	store.RecordTerminalFailure(context.Background(), "openai") //nolint:errcheck

	tr := NewTransport(inner, store, cfg, "openai", nil)
	resp, err := tr.RoundTrip(dummyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503 fast-fail, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), DefaultDegradedSignal) {
		t.Fatalf("DefaultDegradedSignal not found in fast-fail body: %s", b)
	}
}

func TestTransport_TestMode_ForceDegraded(t *testing.T) {
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		t.Error("inner transport should NOT be called in force_degraded test mode")
		return makeResp(200), nil
	})
	tr := newTestTransport(inner)

	req := dummyRequest()
	req.Header.Set(TestModeHeader, TestModeForceDegraded)

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), DefaultDegradedSignal) {
		t.Fatalf("DefaultDegradedSignal not found in body: %s", b)
	}
}

// TestTransport_TestMode_DisabledByDefault_IgnoresHeader pins down the
// fix for the B1 security finding: with Config.TestModeEnabled left at
// its zero-value (false), the transport MUST NOT honour the
// X-LLM-Proxy-Test-Mode header.  If this regresses, a client could
// smuggle synthetic degraded responses past a production deployment by
// setting the header on any request.
func TestTransport_TestMode_DisabledByDefault_IgnoresHeader(t *testing.T) {
	innerCalled := false
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		innerCalled = true
		return makeResp(200), nil
	})
	cfg := Config{
		Enabled:             true,
		Mode:                ModeEnforce,
		FailureThreshold:    3,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 0,
		// TestModeEnabled intentionally left false.
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(inner, store, cfg, "openai", nil)

	req := dummyRequest()
	req.Header.Set(TestModeHeader, TestModeForceDegraded)

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !innerCalled {
		t.Fatal("inner transport should have been called — test-mode header must be ignored when TestModeEnabled is false")
	}
	if resp.StatusCode != 200 {
		t.Fatalf("want real 200 response, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(b), DefaultDegradedSignal) {
		t.Fatalf("synthetic DegradedSignal leaked into response body: %s", b)
	}
}

// TestTransport_TestMode_DisabledByDefault_IgnoresQueryParam is the
// query-param counterpart of the header regression test above.
func TestTransport_TestMode_DisabledByDefault_IgnoresQueryParam(t *testing.T) {
	innerCalled := false
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		innerCalled = true
		return makeResp(200), nil
	})
	cfg := Config{
		Enabled:             true,
		Mode:                ModeEnforce,
		FailureThreshold:    3,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 0,
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(inner, store, cfg, "openai", nil)

	req := httptest.NewRequest(http.MethodPost,
		"/openai/v1/chat/completions?"+TestModeQueryParam+"="+TestModeForceDegraded,
		strings.NewReader(`{}`))

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !innerCalled {
		t.Fatal("inner transport should have been called — test-mode query param must be ignored when TestModeEnabled is false")
	}
	if resp.StatusCode != 200 {
		t.Fatalf("want real 200 response, got %d", resp.StatusCode)
	}
}

func TestTransport_TestMode_ForceTransientRecover(t *testing.T) {
	attempts := 0
	inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		attempts++
		return makeResp(200), nil
	})
	tr := newTestTransport(inner)

	req := dummyRequest()
	req.Header.Set(TestModeHeader, TestModeForceTransientRecover)

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Attempt 0 is intercepted by test mode (returns degraded), then the
	// transport retries.  On attempt 1, force_transient_recover forwards to
	// the real inner transport which returns 200.
	if resp.StatusCode != 200 {
		t.Fatalf("want 200 on second attempt, got %d", resp.StatusCode)
	}
	if attempts != 1 {
		t.Fatalf("expected inner transport called once (on retry), got %d", attempts)
	}
}

func TestTransport_DegradedResponseBodyIsValidJSON(t *testing.T) {
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return makeResp(503), nil
	})
	cfg := Config{
		Enabled:             true,
		Mode:                ModeEnforce,
		FailureThreshold:    1,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 0,
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(inner, store, cfg, "anthropic", nil)

	resp, err := tr.RoundTrip(dummyRequest())
	if err != nil {
		t.Fatalf("unexpected RoundTrip error: %v", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(b, &payload); err != nil {
		t.Fatalf("degraded response body is not valid JSON: %v\nbody: %s", err, b)
	}
	errObj, _ := payload["error"].(map[string]interface{})
	if errObj == nil {
		t.Fatal("expected 'error' key in JSON body")
	}
	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, DefaultDegradedSignal) {
		t.Fatalf("DefaultDegradedSignal not in message: %s", msg)
	}
	if errObj["type"] != "provider_degraded" {
		t.Fatalf("expected type 'provider_degraded', got %v", errObj["type"])
	}
}

// TestTransport_DegradedResponse_SetsErrorClassHeader verifies the defense-in-
// depth signal: even if the response body is ever rewritten by a downstream
// transformer, clients can still detect provider degradation via the
// X-Llm-Proxy-Error-Class header.
func TestTransport_DegradedResponse_SetsErrorClassHeader(t *testing.T) {
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return makeResp(503), nil
	})
	cfg := Config{
		Enabled:             true,
		Mode:                ModeEnforce,
		FailureThreshold:    1,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 0,
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(inner, store, cfg, "openai", nil)

	resp, err := tr.RoundTrip(dummyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := resp.Header.Get("X-Llm-Proxy-Error-Class"); got != string(FailureClassDegraded) {
		t.Fatalf("want X-Llm-Proxy-Error-Class=%q, got %q", FailureClassDegraded, got)
	}
}

// TestTransport_RateLimitExhausted_SetsErrorClassHeader verifies the header
// flags rate-limit exhaustion distinctly from degradation.  The degraded
// signal is only emitted for true degradation; 429s must NOT be classified
// as degraded.
func TestTransport_RateLimitExhausted_SetsErrorClassHeader(t *testing.T) {
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		r := makeResp(429)
		r.Header.Set("Retry-After", "0")
		r.Header.Set("x-ratelimit-remaining-requests", "5")
		return r, nil
	})
	cfg := Config{
		Enabled:             true,
		Mode:                ModeEnforce,
		FailureThreshold:    5,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 1,
		MaxRateLimitRetries: 1,
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(inner, store, cfg, "openai", nil)

	resp, err := tr.RoundTrip(dummyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Llm-Proxy-Error-Class"); got != string(FailureClassLocalRateLimit) {
		t.Fatalf("want X-Llm-Proxy-Error-Class=%q, got %q", FailureClassLocalRateLimit, got)
	}
}

func TestTransport_GlobalRateLimitExhausted_SetsErrorClassHeader(t *testing.T) {
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		r := makeResp(429)
		r.Header.Set("Retry-After", "0")
		r.Header.Set("x-ratelimit-remaining-requests", "0")
		r.Header.Set("x-ratelimit-remaining-tokens", "0")
		return r, nil
	})
	cfg := Config{
		Enabled:             true,
		Mode:                ModeEnforce,
		FailureThreshold:    5,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 1,
		MaxRateLimitRetries: 1,
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(inner, store, cfg, "openai", nil)

	resp, err := tr.RoundTrip(dummyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Llm-Proxy-Error-Class"); got != string(FailureClassGlobalRateLimit) {
		t.Fatalf("want X-Llm-Proxy-Error-Class=%q, got %q", FailureClassGlobalRateLimit, got)
	}
}

func TestTransport_RateLimitRetry(t *testing.T) {
	attempts := 0
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			r := makeResp(429)
			r.Header.Set("Retry-After", "0")
			return r, nil
		}
		return makeResp(200), nil
	})
	tr := newTestTransport(inner)

	resp, err := tr.RoundTrip(dummyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("want 200 after rate-limit retry, got %d", resp.StatusCode)
	}
}

func TestTransport_BackoffSleepStopsOnContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempts++
		cancel()
		return makeResp(503), nil
	})
	cfg := Config{
		Enabled:             true,
		Mode:                ModeEnforce,
		FailureThreshold:    5,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 1,
		MaxRateLimitRetries: 1,
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(inner, store, cfg, "openai", nil)

	req := dummyRequest().WithContext(ctx)
	resp, err := tr.RoundTrip(req)
	if err == nil {
		t.Fatal("expected context cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if resp != nil {
		t.Fatalf("expected nil response on cancellation, got %d", resp.StatusCode)
	}
	if attempts != 1 {
		t.Fatalf("sleep cancellation should abort before retry; attempts=%d", attempts)
	}
}

func TestTransport_ProbeFails_ReturnsDegraded(t *testing.T) {
	cfg := Config{
		Enabled:             true,
		Mode:                ModeEnforce,
		FailureThreshold:    1,
		WindowSeconds:       60,
		CooldownSeconds:     0,
		MaxTransientRetries: 0,
	}.Defaults()
	store := NewMemoryStore(cfg)
	ctx := context.Background()

	// Open the circuit.
	store.RecordTerminalFailure(ctx, "openai") //nolint:errcheck
	// Expire cooldown.
	e := store.entry("openai")
	e.mu.Lock()
	e.cooldownUntil = time.Now().Add(-1 * time.Second)
	e.mu.Unlock()

	// Inner returns 503 — the probe fails.
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return makeResp(503), nil
	})
	tr := NewTransport(inner, store, cfg, "openai", nil)

	resp, err := tr.RoundTrip(dummyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503 degraded after probe failure, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), DefaultDegradedSignal) {
		t.Fatalf("DefaultDegradedSignal not found in probe-failed body: %s", b)
	}
	// Circuit should still be open (not closed).
	state, _ := store.GetState(ctx, "openai")
	if state == StateClosed {
		t.Fatal("circuit should NOT be closed after failed probe")
	}
}

func TestTransport_HalfOpen_ConcurrentRequestFastFails(t *testing.T) {
	cfg := Config{
		Enabled:             true,
		Mode:                ModeEnforce,
		FailureThreshold:    1,
		WindowSeconds:       60,
		CooldownSeconds:     0,
		MaxTransientRetries: 0,
	}.Defaults()
	store := NewMemoryStore(cfg)
	ctx := context.Background()

	// Open the circuit.
	store.RecordTerminalFailure(ctx, "openai") //nolint:errcheck
	// Expire cooldown.
	e := store.entry("openai")
	e.mu.Lock()
	e.cooldownUntil = time.Now().Add(-1 * time.Second)
	e.mu.Unlock()

	// Start a probe (simulating an in-flight probe).
	if !store.TryStartProbe(ctx, "openai") {
		t.Fatal("first TryStartProbe should succeed")
	}

	// Inner should NOT be called for the second request.
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		t.Error("inner transport should NOT be called when probe is in flight")
		return makeResp(200), nil
	})
	tr := NewTransport(inner, store, cfg, "openai", nil)

	resp, err := tr.RoundTrip(dummyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503 fast-fail while probe in flight, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), DefaultDegradedSignal) {
		t.Fatalf("DefaultDegradedSignal not found: %s", b)
	}
}

func TestTransport_RateLimitRetriesExhausted_Returns429(t *testing.T) {
	// Always return 429 with local rate limit headers.
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		r := makeResp(429)
		r.Header.Set("Retry-After", "0")
		r.Header.Set("x-ratelimit-remaining-requests", "5")
		return r, nil
	})
	cfg := Config{
		Enabled:             true,
		Mode:                ModeEnforce,
		FailureThreshold:    5,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 1,
		MaxRateLimitRetries: 1,
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(inner, store, cfg, "openai", nil)

	resp, err := tr.RoundTrip(dummyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("want 429 after rate-limit exhaustion, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(b), DefaultDegradedSignal) {
		t.Fatal("DefaultDegradedSignal should NOT be in rate-limit response (not degraded)")
	}
}

func TestTransport_NonRetryable400_Passthrough(t *testing.T) {
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return makeResp(400), nil
	})
	tr := newTestTransport(inner)

	resp, err := tr.RoundTrip(dummyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 400 {
		t.Fatalf("want 400 passthrough, got %d", resp.StatusCode)
	}
}

func TestTransport_RetryContributionMode_On(t *testing.T) {
	attempt := 0
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		attempt++
		return makeResp(503), nil
	})
	cfg := Config{
		Enabled:               true,
		Mode:                  ModeEnforce,
		FailureThreshold:      10,
		WindowSeconds:         60,
		CooldownSeconds:       300,
		MaxTransientRetries:   1,
		RetryContributionMode: "on",
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(inner, store, cfg, "openai", nil)

	tr.RoundTrip(dummyRequest()) //nolint:errcheck

	// With "on", each retry also records a terminal failure.
	// Initial attempt + 1 retry = 2 attempts. The retry contributes 1 extra
	// failure, plus the final terminal failure at exhaustion = 2 total.
	stats, _ := store.GetStats(context.Background(), "openai")
	if stats.Failures < 2 {
		t.Fatalf("expected at least 2 recorded failures with contribution_mode=on, got %d", stats.Failures)
	}
}

func TestTransport_RetryContributionMode_Off(t *testing.T) {
	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return makeResp(503), nil
	})
	cfg := Config{
		Enabled:               true,
		Mode:                  ModeEnforce,
		FailureThreshold:      10,
		WindowSeconds:         60,
		CooldownSeconds:       300,
		MaxTransientRetries:   1,
		RetryContributionMode: "off",
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(inner, store, cfg, "openai", nil)

	tr.RoundTrip(dummyRequest()) //nolint:errcheck

	// With "off", only the final terminal failure is recorded (1 total).
	stats, _ := store.GetStats(context.Background(), "openai")
	if stats.Failures != 1 {
		t.Fatalf("expected exactly 1 recorded failure with contribution_mode=off, got %d", stats.Failures)
	}
}

func TestTransport_ProbeSucceeds_ClosesCircuit(t *testing.T) {
	// Build a store where the circuit is already half-open.
	cfg := Config{
		Enabled:             true,
		Mode:                ModeEnforce,
		FailureThreshold:    1,
		WindowSeconds:       60,
		CooldownSeconds:     0,
		MaxTransientRetries: 0,
	}.Defaults()
	store := NewMemoryStore(cfg)
	ctx := context.Background()

	// Open the circuit.
	store.RecordTerminalFailure(ctx, "openai") //nolint:errcheck
	// Expire the cooldown by setting it to the past.
	e := store.entry("openai")
	e.mu.Lock()
	e.cooldownUntil = time.Now().Add(-1 * time.Second)
	e.mu.Unlock()

	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return makeResp(200), nil
	})
	tr := NewTransport(inner, store, cfg, "openai", nil)

	resp, err := tr.RoundTrip(dummyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("probe should succeed, got %d", resp.StatusCode)
	}

	state, _ := store.GetState(ctx, "openai")
	if state != StateClosed {
		t.Fatalf("circuit should be closed after successful probe, got %s", state)
	}
}

// TestTransport_CustomDegradedSignal verifies that operators can override
// the degraded-signal marker via Config.DegradedSignal and it shows up in
// synthetic degraded responses instead of DefaultDegradedSignal.
func TestTransport_CustomDegradedSignal(t *testing.T) {
	const custom = "[MY_COMPANY_UPSTREAM_DOWN]"

	inner := roundTripFunc(func(_ *http.Request) (*http.Response, error) {
		return makeResp(503), nil
	})
	cfg := Config{
		Enabled:             true,
		Mode:                ModeEnforce,
		FailureThreshold:    1,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 0,
		DegradedSignal:      custom,
	}.Defaults()
	store := NewMemoryStore(cfg)
	tr := NewTransport(inner, store, cfg, "openai", nil)

	resp, err := tr.RoundTrip(dummyRequest())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
	b, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(b), custom) {
		t.Fatalf("custom degraded signal not found in body: %s", b)
	}
	if strings.Contains(string(b), DefaultDegradedSignal) {
		t.Fatalf("default signal should NOT appear when custom one is set: %s", b)
	}
}
