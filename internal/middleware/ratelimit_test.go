package middleware

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

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/ratelimit"
	"github.com/gorilla/mux"
)

// fakeProvider implements providers.Provider minimally for tests.
type fakeProvider struct{}

func (f *fakeProvider) GetName() string                           { return "openai" }
func (f *fakeProvider) IsStreamingRequest(req *http.Request) bool { return false }
func (f *fakeProvider) ParseResponseMetadata(body io.Reader, isStreaming bool) (*providers.LLMResponseMetadata, error) {
	return nil, nil
}

func (f *fakeProvider) Proxy() http.Handler { return http.NotFoundHandler() }

func (f *fakeProvider) GetHealthStatus() map[string]interface{} { return map[string]interface{}{} }

func (f *fakeProvider) UserIDFromRequest(req *http.Request) string { return "" }

func (f *fakeProvider) RegisterExtraRoutes(r *mux.Router) {}

func (f *fakeProvider) ValidateAPIKey(req *http.Request, ks providers.APIKeyStore) error {
	return nil
}

func (f *fakeProvider) ExtractRequestModelAndMessages(req *http.Request) (string, []string) {
	return "", nil
}

func makeCfg() *config.YAMLConfig {
	cfg := config.GetDefaultYAMLConfig()
	cfg.Features.RateLimiting.Enabled = true
	cfg.Features.RateLimiting.Backend = "memory"
	cfg.Features.RateLimiting.Estimation.BytesPerToken = 4
	cfg.Features.RateLimiting.Estimation.MaxSampleBytes = 20000
	return cfg
}

func TestRateLimitingRequestsPerMinute(t *testing.T) {
	cfg := makeCfg()
	cfg.Features.RateLimiting.Limits.RequestsPerMinute = 1

	lim := ratelimit.NewMemoryLimiter(cfg)
	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	h := RateLimitingMiddleware(pm, cfg, lim, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))

	// First request allowed
	req := httptest.NewRequest("POST", "/openai/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-4o","messages":[]}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("expected 200, got %d", rr.Code)
	}

	// Second request blocked
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req)
	if rr2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rr2.Code)
	}

	// Verify informative rate limit headers
	if got := rr2.Header().Get("X-RateLimit-Reason"); got != "minute limit exceeded" {
		t.Fatalf("unexpected X-RateLimit-Reason: %q", got)
	}
	if got := rr2.Header().Get("X-RateLimit-Metric"); got != "requests" {
		t.Fatalf("unexpected X-RateLimit-Metric: %q", got)
	}
	if got := rr2.Header().Get("X-RateLimit-Window"); got != "minute" {
		t.Fatalf("unexpected X-RateLimit-Window: %q", got)
	}
	if got := rr2.Header().Get("X-RateLimit-Scope"); got != "global" {
		t.Fatalf("unexpected X-RateLimit-Scope: %q", got)
	}
	if got := rr2.Header().Get("X-RateLimit-Limit"); got != "1" {
		t.Fatalf("unexpected X-RateLimit-Limit: %q", got)
	}
	if got := rr2.Header().Get("X-RateLimit-Remaining"); got != "0" {
		t.Fatalf("unexpected X-RateLimit-Remaining: %q", got)
	}
	if got := rr2.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("unexpected Retry-After: %q", got)
	}
}

func TestRateLimitingTokensPerMinute(t *testing.T) {
	cfg := makeCfg()
	cfg.Features.RateLimiting.Limits.RequestsPerMinute = 100
	cfg.Features.RateLimiting.Limits.TokensPerMinute = 20 // small

	lim := ratelimit.NewMemoryLimiter(cfg)
	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	h := RateLimitingMiddleware(pm, cfg, lim, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate final token count header set by TokenParsingMiddleware (input tokens only)
		w.Header().Set("X-LLM-Input-Tokens", "25")
		w.WriteHeader(200)
	}))

	// Body of ~100 bytes -> est ~25 tokens assuming 4 bytes/token
	payload := map[string]interface{}{"model": "gpt-4o", "messages": []string{"hello", "world"}, "pad": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/openai/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	// First request should be allowed due to optimistic first-token policy
	rr1 := httptest.NewRecorder()
	h.ServeHTTP(rr1, req)
	if rr1.Code != http.StatusOK {
		t.Fatalf("expected 200 for first request, got %d", rr1.Code)
	}

	// Second request should be throttled by token limit
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req)
	if rr2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 due to token limit on second request, got %d", rr2.Code)
	}

	// Verify informative rate limit headers for token limit
	if got := rr2.Header().Get("X-RateLimit-Reason"); got != "minute limit exceeded" {
		t.Fatalf("unexpected X-RateLimit-Reason: %q", got)
	}
	if got := rr2.Header().Get("X-RateLimit-Metric"); got != "tokens" {
		t.Fatalf("unexpected X-RateLimit-Metric: %q", got)
	}
	if got := rr2.Header().Get("X-RateLimit-Window"); got != "minute" {
		t.Fatalf("unexpected X-RateLimit-Window: %q", got)
	}
	if got := rr2.Header().Get("X-RateLimit-Scope"); got != "global" {
		t.Fatalf("unexpected X-RateLimit-Scope: %q", got)
	}
	if got := rr2.Header().Get("X-RateLimit-Limit"); got != "20" {
		t.Fatalf("unexpected X-RateLimit-Limit: %q", got)
	}
	if got := rr2.Header().Get("X-RateLimit-Remaining"); got != "0" {
		t.Fatalf("unexpected X-RateLimit-Remaining: %q", got)
	}
	if got := rr2.Header().Get("Retry-After"); got != "60" {
		t.Fatalf("unexpected Retry-After: %q", got)
	}
}

func TestRateLimitingUserScoped(t *testing.T) {
	cfg := makeCfg()
	cfg.Features.RateLimiting.Limits = config.LimitsConfig{} // unlimited global
	cfg.Features.RateLimiting.Overrides.PerUser = map[string]config.LimitsConfig{
		"example-user": {RequestsPerMinute: 1},
	}
	lim := ratelimit.NewMemoryLimiter(cfg)
	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	h := RateLimitingMiddleware(pm, cfg, lim, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("POST", "/openai/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-4o","messages":[]}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", "example-user")

	rr1 := httptest.NewRecorder()
	h.ServeHTTP(rr1, req)
	if rr1.Code != 200 {
		t.Fatalf("expected 200, got %d", rr1.Code)
	}

	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req)
	if rr2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for user-scoped limit, got %d", rr2.Code)
	}

	// Verify headers indicate user scope and request metric
	if got := rr2.Header().Get("X-RateLimit-Scope"); got != "user:example-user" {
		t.Fatalf("unexpected X-RateLimit-Scope: %q", got)
	}
	if got := rr2.Header().Get("X-RateLimit-Metric"); got != "requests" {
		t.Fatalf("unexpected X-RateLimit-Metric: %q", got)
	}
}

// ─── disabled-config short-circuit ─────────────────────────────────────

// TestRateLimitingMiddleware_DisabledConfig_ReturnsNoOp asserts that the
// middleware short-circuits to a pass-through when any of the disabling
// conditions hold: nil limiter, nil cfg, or Features.RateLimiting.Enabled=false.
// This protects the hot path from running estimation/scope-key work when the
// feature is off.
func TestRateLimitingMiddleware_DisabledConfig_ReturnsNoOp(t *testing.T) {
	pm := providers.NewProviderManager()

	cases := []struct {
		name    string
		cfg     *config.YAMLConfig
		limiter ratelimit.RateLimiter
	}{
		{"nil_limiter", config.GetDefaultYAMLConfig(), nil},
		{"nil_cfg", nil, ratelimit.NewMemoryLimiter(makeCfg())},
		{
			"feature_disabled",
			func() *config.YAMLConfig {
				c := config.GetDefaultYAMLConfig()
				c.Features.RateLimiting.Enabled = false
				return c
			}(),
			ratelimit.NewMemoryLimiter(makeCfg()),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handlerCalled := 0
			h := RateLimitingMiddleware(pm, tc.cfg, tc.limiter, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handlerCalled++
				w.WriteHeader(200)
			}))
			req := httptest.NewRequest("POST", "/openai/v1/chat/completions", nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if handlerCalled != 1 {
				t.Fatalf("handler should be called exactly once when limiter disabled, got %d", handlerCalled)
			}
			if rr.Code != 200 {
				t.Fatalf("expected 200, got %d", rr.Code)
			}
		})
	}
}

// TestRateLimitingMiddleware_NoMatchingProvider_PassesThrough verifies that
// requests whose path matches no registered provider bypass the limiter
// entirely: rate-limit headers must not be set and the inner handler runs.
func TestRateLimitingMiddleware_NoMatchingProvider_PassesThrough(t *testing.T) {
	cfg := makeCfg()
	cfg.Features.RateLimiting.Limits.RequestsPerMinute = 1
	pm := providers.NewProviderManager() // no providers registered

	handlerCalled := false
	h := RateLimitingMiddleware(pm, cfg, ratelimit.NewMemoryLimiter(cfg), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(200)
	}))

	req := httptest.NewRequest("POST", "/unknown/path", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !handlerCalled {
		t.Fatal("handler must be called when no provider matches")
	}
	if rr.Header().Get("X-RateLimit-Reason") != "" {
		t.Errorf("rate-limit headers must not be set when path has no provider; got %q", rr.Header().Get("X-RateLimit-Reason"))
	}
}

// ─── limiter error handling ────────────────────────────────────────────

// erroringLimiter is a RateLimiter whose CheckAndReserve always fails — used to
// drive the "limiter returns err → 500" branch of the middleware.
type erroringLimiter struct{}

func (erroringLimiter) CheckAndReserve(ctx context.Context, id string, scope ratelimit.ScopeKeys, estTokens int, now time.Time) (ratelimit.ReservationResult, error) {
	return ratelimit.ReservationResult{}, errors.New("simulated limiter outage")
}

func (erroringLimiter) Adjust(ctx context.Context, id string, scope ratelimit.ScopeKeys, delta int, now time.Time) error {
	return errors.New("simulated adjust outage")
}

func (erroringLimiter) Cancel(ctx context.Context, id string, scope ratelimit.ScopeKeys, estTokens int, now time.Time) error {
	return nil
}

// TestRateLimitingMiddleware_CheckAndReserveError_FailsOpen verifies that a
// limiter outage during reservation fails OPEN: the request is allowed through
// to the handler rather than surfacing as a wholesale HTTP 500. A transient
// Redis blip must not take down all LLM traffic, mirroring the circuit
// breaker's fail-open behavior when its store is unreachable.
func TestRateLimitingMiddleware_CheckAndReserveError_FailsOpen(t *testing.T) {
	cfg := makeCfg()
	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	handlerRan := false
	h := RateLimitingMiddleware(pm, cfg, erroringLimiter{}, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerRan = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/openai/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if !handlerRan {
		t.Fatal("handler must run when limiter errors (fail open)")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("limiter error must fail open with handler status; got %d", rr.Code)
	}
}

// adjustErroringLimiter allows reservations but errors on Adjust.  Used to
// drive the post-reconciliation Adjust-error log branch in the middleware.
type adjustErroringLimiter struct{}

func (adjustErroringLimiter) CheckAndReserve(ctx context.Context, id string, scope ratelimit.ScopeKeys, estTokens int, now time.Time) (ratelimit.ReservationResult, error) {
	return ratelimit.ReservationResult{Allowed: true}, nil
}

func (adjustErroringLimiter) Adjust(ctx context.Context, id string, scope ratelimit.ScopeKeys, delta int, now time.Time) error {
	return errors.New("simulated adjust outage")
}

func (adjustErroringLimiter) Cancel(ctx context.Context, id string, scope ratelimit.ScopeKeys, estTokens int, now time.Time) error {
	return nil
}

// TestRateLimitingMiddleware_AdjustError_LoggedButRequestSucceeds asserts that
// when post-handler reconciliation fails (e.g. Redis blip during Adjust), the
// middleware logs the error but does NOT change the response status.  This
// matters because Adjust runs after the handler has already replied.
func TestRateLimitingMiddleware_AdjustError_LoggedButRequestSucceeds(t *testing.T) {
	cfg := makeCfg()
	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	chain := RateLimitingMiddleware(pm, cfg, adjustErroringLimiter{}, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Setting the input-tokens header is what triggers Adjust to run.
		w.Header().Set("X-LLM-Input-Tokens", "42")
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/openai/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	logOut := captureLogOutput(func() { chain.ServeHTTP(rr, req) })

	if rr.Code != http.StatusOK {
		t.Errorf("Adjust error must not fail the request; got %d", rr.Code)
	}
	if !strings.Contains(logOut, "ratelimit: adjust error") {
		t.Errorf("Adjust error must be logged; got %s", logOut)
	}
}

// cancelRecordingLimiter allows reservations and records whether Cancel was
// called, with the estTokens it received. Used to verify the middleware
// releases a reservation when the upstream hard-fails.
type cancelRecordingLimiter struct {
	cancelCalled bool
	cancelTokens int
}

func (l *cancelRecordingLimiter) CheckAndReserve(ctx context.Context, id string, scope ratelimit.ScopeKeys, estTokens int, now time.Time) (ratelimit.ReservationResult, error) {
	return ratelimit.ReservationResult{Allowed: true, ReservationID: id}, nil
}

func (l *cancelRecordingLimiter) Adjust(ctx context.Context, id string, scope ratelimit.ScopeKeys, delta int, now time.Time) error {
	return nil
}

func (l *cancelRecordingLimiter) Cancel(ctx context.Context, id string, scope ratelimit.ScopeKeys, estTokens int, now time.Time) error {
	l.cancelCalled = true
	l.cancelTokens = estTokens
	return nil
}

// TestRateLimitingMiddleware_CancelsReservationOnUpstream5xx asserts that when
// the handler responds 5xx (and no input-token header is set), the middleware
// releases the reservation so a failed upstream call doesn't permanently
// consume the caller's request/token quota for the window.
func TestRateLimitingMiddleware_CancelsReservationOnUpstream5xx(t *testing.T) {
	cfg := makeCfg()
	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	lim := &cancelRecordingLimiter{}
	chain := RateLimitingMiddleware(pm, cfg, lim, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))

	req := httptest.NewRequest("POST", "/openai/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-4o","messages":[]}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected upstream status 502 to pass through; got %d", rr.Code)
	}
	if !lim.cancelCalled {
		t.Fatal("Cancel must be called to release the reservation on a 5xx upstream")
	}
}

// TestRateLimitingMiddleware_DoesNotCancelOnSuccess asserts that a successful
// (2xx) response does NOT release the reservation — the request really
// happened and must count against the limit.
func TestRateLimitingMiddleware_DoesNotCancelOnSuccess(t *testing.T) {
	cfg := makeCfg()
	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	lim := &cancelRecordingLimiter{}
	chain := RateLimitingMiddleware(pm, cfg, lim, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/openai/v1/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-4o","messages":[]}`)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, req)

	if lim.cancelCalled {
		t.Fatal("Cancel must NOT be called on a successful (2xx) response")
	}
}

// ─── per-model scope keying ────────────────────────────────────────────

// modelExtractingProvider returns a non-empty model from
// ExtractRequestModelAndMessages so the middleware tags the scope with model:gpt-4o.
type modelExtractingProvider struct{ fakeProvider }

func (modelExtractingProvider) ExtractRequestModelAndMessages(req *http.Request) (string, []string) {
	return "gpt-4o", []string{"hello"}
}

// TestRateLimitingMiddleware_ExtractedModelFlowsIntoScopeKeys verifies that
// when the provider parses a model from the request body, that model name
// reaches the limiter as part of the scope keys.  We assert this indirectly:
// a per-model override of 1 req/min is exceeded on the second request, and
// the 429 response carries Scope=model:gpt-4o.
func TestRateLimitingMiddleware_ExtractedModelFlowsIntoScopeKeys(t *testing.T) {
	cfg := makeCfg()
	cfg.Features.RateLimiting.Limits = config.LimitsConfig{} // unlimited global
	cfg.Features.RateLimiting.Overrides.PerModel = map[string]config.LimitsConfig{
		"gpt-4o": {RequestsPerMinute: 1},
	}
	pm := providers.NewProviderManager()
	pm.RegisterProvider(&modelExtractingProvider{})

	h := RateLimitingMiddleware(pm, cfg, ratelimit.NewMemoryLimiter(cfg), nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	mkReq := func() *http.Request {
		r := httptest.NewRequest("POST", "/openai/v1/chat/completions", bytes.NewReader([]byte(body)))
		r.Header.Set("Content-Type", "application/json")
		return r
	}

	rr1 := httptest.NewRecorder()
	h.ServeHTTP(rr1, mkReq())
	if rr1.Code != 200 {
		t.Fatalf("first request expected 200, got %d", rr1.Code)
	}

	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, mkReq())
	if rr2.Code != http.StatusTooManyRequests {
		t.Fatalf("second request expected 429, got %d", rr2.Code)
	}
	if got := rr2.Header().Get("X-RateLimit-Scope"); got != "model:gpt-4o" {
		t.Fatalf("expected scope=model:gpt-4o (proves extracted model fed scope keys); got %q", got)
	}
}

// ─── prefix() helper ───────────────────────────────────────────────────

// TestPrefix_TruncatesAuthTokensForLogging covers the prefix() helper used by
// the middleware to log an authorization-token prefix without leaking the
// full secret.  Three branches: empty input, ≤12 chars (returned verbatim),
// and >12 chars (truncated to the first 12).
func TestPrefix_TruncatesAuthTokensForLogging(t *testing.T) {
	cases := map[string]string{
		"":                          "",
		"short":                     "short",
		"twelve-chars":              "twelve-chars", // exactly 12
		"this-is-too-long":          "this-is-too-",
		"verylongapikey-1234567890": "verylongapik",
	}
	for in, want := range cases {
		if got := prefix(in); got != want {
			t.Errorf("prefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRateLimitingAPIKeyScoped(t *testing.T) {
	cfg := makeCfg()
	cfg.Features.RateLimiting.Limits = config.LimitsConfig{} // unlimited global
	cfg.Features.RateLimiting.Overrides.PerKey = map[string]config.LimitsConfig{
		"devkey": {RequestsPerMinute: 1},
	}
	lim := ratelimit.NewMemoryLimiter(cfg)
	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	h := RateLimitingMiddleware(pm, cfg, lim, nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("POST", "/openai/chat/completions", bytes.NewReader([]byte(`{"model":"gpt-4o","messages":[]}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer devkey")

	rr1 := httptest.NewRecorder()
	h.ServeHTTP(rr1, req)
	if rr1.Code != 200 {
		t.Fatalf("expected 200, got %d", rr1.Code)
	}

	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, req)
	if rr2.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 for key-scoped limit, got %d", rr2.Code)
	}

	// Verify headers indicate key scope and request metric
	if got := rr2.Header().Get("X-RateLimit-Scope"); got != "key:devkey" {
		t.Fatalf("unexpected X-RateLimit-Scope: %q", got)
	}
	if got := rr2.Header().Get("X-RateLimit-Metric"); got != "requests" {
		t.Fatalf("unexpected X-RateLimit-Metric: %q", got)
	}
}
