package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/providers"
)

type mockAPIKeyStore struct{}

func (m *mockAPIKeyStore) ValidateAndGetActualKey(ctx context.Context, key string) (string, string, error) {
	if key == "valid" {
		return "actual-valid", "openai", nil
	}
	return "", "", errors.New("invalid key")
}

type mockProxyKeyStore struct {
	mockAPIKeyStore
}

func (m *mockProxyKeyStore) ValidateAndGetActualKey(ctx context.Context, key string) (string, string, error) {
	if key == apikeys.KeyPrefix+"proxy" {
		return "actual-proxy", "openai", nil
	}
	return m.mockAPIKeyStore.ValidateAndGetActualKey(ctx, key)
}

func (m *mockProxyKeyStore) LookupProxyKey(ctx context.Context, bearer string) (*apikeys.APIKey, error) {
	if bearer == apikeys.KeyPrefix+"proxy" {
		return &apikeys.APIKey{PK: bearer, Provider: "openai"}, nil
	}
	return nil, nil
}

func TestAPIKeyValidationMiddleware(t *testing.T) {
	pm := providers.NewProviderManager()

	// mock provider
	op := providers.NewOpenAIProxy()
	pm.RegisterProvider(op)

	store := &mockAPIKeyStore{}
	mw := APIKeyValidationMiddleware(pm, store, false)

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

func TestAPIKeyValidationMiddleware_StashesSkPrefixedProxyKeyInContext(t *testing.T) {
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())

	store := &mockProxyKeyStore{}
	mw := APIKeyValidationMiddleware(pm, store, false)

	iwKey := apikeys.KeyPrefix + "proxy"
	if !strings.HasPrefix(iwKey, "sk-") {
		t.Fatalf("proxy fixture should use sk- generation prefix, got %q", iwKey)
	}
	var captured *apikeys.APIKey
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec, ok := apikeys.FromContext(r.Context())
		if ok {
			captured = rec
		}
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/openai/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+iwKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if captured == nil || captured.PK != iwKey {
		t.Fatalf("expected proxy key in context, got %+v", captured)
	}
}

func TestAPIKeyValidationMiddleware_SkipsRedact(t *testing.T) {
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())

	store := &mockAPIKeyStore{}
	mw := APIKeyValidationMiddleware(pm, store, false)

	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/redact", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected /redact to bypass provider key validation, got %d", rec.Code)
	}
}
