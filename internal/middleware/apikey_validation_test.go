package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/providers"
)

type mockAPIKeyStore struct{}

func (m *mockAPIKeyStore) ValidateAndGetActualKey(ctx context.Context, key string) (string, string, error) {
	if key == "valid" {
		return "actual-valid", "openai", nil
	}
	return "", "", errors.New("invalid key")
}

func TestAPIKeyValidationMiddleware(t *testing.T) {
	pm := providers.NewProviderManager()
	
	// mock provider
	op := providers.NewOpenAIProxy()
	pm.RegisterProvider(op)

	store := &mockAPIKeyStore{}
	mw := APIKeyValidationMiddleware(pm, store)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Test health check
	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for health, got %d", rec.Code)
	}

	// Test missing provider
	req = httptest.NewRequest("GET", "/unknown", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for unknown provider, got %d", rec.Code)
	}

	// Test valid key (OpenAI uses Bearer token)
	req = httptest.NewRequest("POST", "/openai/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer valid")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for valid key, got %d", rec.Code)
	}

	// Test invalid key
	req = httptest.NewRequest("POST", "/openai/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer invalid")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for invalid key, got %d", rec.Code)
	}
}
