package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/coststats"
	"github.com/Instawork/llm-proxy/internal/providers"
)

func TestCostLimitMiddleware_AllowsUnderCap(t *testing.T) {
	rec := coststats.NewRecorder()
	rec.RecordRequest("openai", "iw:abc123456789", "", "gpt-4o-mini", 0.0005, 0, 0, 10, 10)

	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	key := &apikeys.APIKey{PK: "iw:abc123456789", DailyCostLimit: 100}
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(apikeys.WithContext(req.Context(), key))
	rr := httptest.NewRecorder()
	CostLimitMiddleware(pm, rec)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestCostLimitMiddleware_BlocksAtCap(t *testing.T) {
	rec := coststats.NewRecorder()
	masked := MaskKeyID("iw:abc123456789")
	rec.RecordRequest("openai", masked, "", "gpt-4o-mini", 1.0, 0, 0, 10, 10)

	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	key := &apikeys.APIKey{PK: "iw:abc123456789", DailyCostLimit: 100}
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(apikeys.WithContext(req.Context(), key))
	rr := httptest.NewRecorder()
	CostLimitMiddleware(pm, rec)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("next should not be called")
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusPaymentRequired {
		t.Fatalf("expected 402, got %d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get(costLimitReasonHeader); got != costLimitExceeded {
		t.Fatalf("reason header = %q", got)
	}
}

func TestCostLimitMiddleware_ZeroLimitUnlimited(t *testing.T) {
	rec := coststats.NewRecorder()
	rec.RecordRequest("openai", "iw:abc123456789", "", "gpt-4o-mini", 99.0, 0, 0, 10, 10)

	pm := providers.NewProviderManager()
	pm.RegisterProvider(&fakeProvider{})

	key := &apikeys.APIKey{PK: "iw:abc123456789", DailyCostLimit: 0}
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(apikeys.WithContext(req.Context(), key))
	rr := httptest.NewRecorder()
	CostLimitMiddleware(pm, rec)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
}

func TestCostLimitMiddleware_SkipsNonProviderRoutes(t *testing.T) {
	rec := coststats.NewRecorder()
	pm := providers.NewProviderManager()
	key := &apikeys.APIKey{PK: "iw:abc123456789", DailyCostLimit: 1}
	rec.RecordRequest("openai", "iw:abc123456789", "", "", 99.0, 0, 0, 0, 0)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req = req.WithContext(apikeys.WithContext(req.Context(), key))
	rr := httptest.NewRecorder()
	CostLimitMiddleware(pm, rec)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 on /health, got %d", rr.Code)
	}
}
