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

func TestMaskProviderCredential(t *testing.T) {
	cases := []struct {
		raw        string
		wantPrefix string
		secretTail string // a chunk that must NOT appear in the masked form
	}{
		{"sk-ant-api03-AbCdEfGhIjKlMnOp", "sk-ant-…", "AbCdEfGhIjKlMnOp"},
		{"sk-proj-1234567890abcdef", "sk-proj-…", "1234567890abcdef"},
		{"sk-svcacct-zzzzz", "sk-svcacct-…", "zzzzz"},
		{"sk-classic-secret", "sk-…", "classic-secret"},
		{"AIzaSyD-EXAMPLE-secret-bytes", "AIza…", "SyD-EXAMPLE-secret-bytes"},
		{"gsk_groqsecret", "gsk_…", "groqsecret"},
		{"weirdunknowntoken", "weir…", "unknowntoken"},
	}
	for _, tc := range cases {
		got := MaskProviderCredential(tc.raw)
		if !strings.HasPrefix(got, tc.wantPrefix) {
			t.Errorf("MaskProviderCredential(%q) = %q; want prefix %q", tc.raw, got, tc.wantPrefix)
		}
		if strings.Contains(got, tc.secretTail) {
			t.Errorf("MaskProviderCredential(%q) = %q leaks secret tail %q", tc.raw, got, tc.secretTail)
		}
		// Distinct keys with the same family must not collapse to one identity.
		if other := MaskProviderCredential(tc.raw + "X"); other == got {
			t.Errorf("MaskProviderCredential collided for %q and %qX", tc.raw, tc.raw)
		}
	}
	if MaskProviderCredential("") != "" {
		t.Errorf("MaskProviderCredential(\"\") should be empty")
	}
}

func TestAPIKeyValidationMiddleware_StashesByoCredentialID(t *testing.T) {
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())

	store := &mockProxyKeyStore{}
	mw := APIKeyValidationMiddleware(pm, store, false)

	var byoID string
	var proxyRecord *apikeys.APIKey
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		byoID = InboundCredentialID(r.Context())
		proxyRecord, _ = apikeys.FromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	// "valid" authenticates but is not a proxy key -> BYO path.
	req := httptest.NewRequest("POST", "/openai/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer valid")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if proxyRecord != nil {
		t.Fatalf("BYO caller must not have a proxy record, got %+v", proxyRecord)
	}
	if want := MaskProviderCredential("valid"); byoID != want {
		t.Fatalf("InboundCredentialID = %q, want %q", byoID, want)
	}
}

func TestAPIKeyValidationMiddleware_ProxyKeyHasNoByoCredentialID(t *testing.T) {
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())

	store := &mockProxyKeyStore{}
	mw := APIKeyValidationMiddleware(pm, store, false)

	var byoID = "sentinel"
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		byoID = InboundCredentialID(r.Context())
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("POST", "/openai/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer "+apikeys.KeyPrefix+"proxy")
	handler.ServeHTTP(httptest.NewRecorder(), req)

	if byoID != "" {
		t.Fatalf("proxy-key caller should have no BYO credential id, got %q", byoID)
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
