package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

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
func (f *fakeProvider) Proxy() http.Handler                                              { return http.NotFoundHandler() }
func (f *fakeProvider) GetHealthStatus() map[string]interface{}                          { return map[string]interface{}{} }
func (f *fakeProvider) UserIDFromRequest(req *http.Request) string                       { return "" }
func (f *fakeProvider) RegisterExtraRoutes(r *mux.Router)                                {}
func (f *fakeProvider) ValidateAPIKey(req *http.Request, ks providers.APIKeyStore) error { return nil }

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

	h := RateLimitingMiddleware(pm, cfg, lim)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

	h := RateLimitingMiddleware(pm, cfg, lim)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate final token count header set by TokenParsingMiddleware
		w.Header().Set("X-LLM-Total-Tokens", "25")
		w.WriteHeader(200)
	}))

	// Body of ~100 bytes -> est ~25 tokens assuming 4 bytes/token, should be blocked
	payload := map[string]interface{}{"model": "gpt-4o", "messages": []string{"hello", "world"}, "pad": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest("POST", "/openai/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 due to token estimate, got %d", rr.Code)
	}

	// Verify informative rate limit headers for token limit
	if got := rr.Header().Get("X-RateLimit-Reason"); got != "minute limit exceeded" {
		t.Fatalf("unexpected X-RateLimit-Reason: %q", got)
	}
	if got := rr.Header().Get("X-RateLimit-Metric"); got != "tokens" {
		t.Fatalf("unexpected X-RateLimit-Metric: %q", got)
	}
	if got := rr.Header().Get("X-RateLimit-Window"); got != "minute" {
		t.Fatalf("unexpected X-RateLimit-Window: %q", got)
	}
	if got := rr.Header().Get("X-RateLimit-Scope"); got != "global" {
		t.Fatalf("unexpected X-RateLimit-Scope: %q", got)
	}
	if got := rr.Header().Get("X-RateLimit-Limit"); got != "20" {
		t.Fatalf("unexpected X-RateLimit-Limit: %q", got)
	}
	if got := rr.Header().Get("X-RateLimit-Remaining"); got != "0" {
		t.Fatalf("unexpected X-RateLimit-Remaining: %q", got)
	}
	if got := rr.Header().Get("Retry-After"); got != "60" {
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

	h := RateLimitingMiddleware(pm, cfg, lim)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestRateLimitingAPIKeyScoped(t *testing.T) {
	cfg := makeCfg()
	cfg.Features.RateLimiting.Limits = config.LimitsConfig{} // unlimited global
	cfg.Features.RateLimiting.Overrides.PerKey = map[string]config.LimitsConfig{
		"devkey": {RequestsPerMinute: 1},
	}
	lim := ratelimit.NewMemoryLimiter(cfg)
	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	h := RateLimitingMiddleware(pm, cfg, lim)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
