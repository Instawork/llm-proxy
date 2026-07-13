package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/modelstatusstats"
	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/proxylog"
)

func TestModelStatusMiddleware_RetiredModelShortCircuits(t *testing.T) {
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())

	cfg := &config.YAMLConfig{
		RetiredModels: map[string]map[string]config.RetiredModelEntry{
			"openai": {
				"o1-mini": {
					RetiredDate: "2025-10-27",
					Replacement: "o4-mini",
					Aliases:     []string{"o1-mini-2024-09-12"},
				},
			},
		},
	}

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	chain := ModelStatusMiddleware(pm, cfg, modelstatusstats.NewRecorder(), nil)(next)

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(`{"model":"o1-mini","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if called {
		t.Fatal("next handler should not run for retired model")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rec.Code)
	}
	if got := rec.Header().Get(providers.HeaderModelRetired); got != "model_retired" {
		t.Fatalf("header=%q", got)
	}
	if got := rec.Header().Get(proxylog.HeaderErrorSource); got != proxylog.ErrorSourceProxy {
		t.Fatalf("error_source=%q", got)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	errObj := body["error"].(map[string]any)
	if errObj["code"] != "model_not_found" {
		t.Fatalf("code=%v", errObj["code"])
	}
}

func TestModelStatusMiddleware_RetiredModelAliasShortCircuits(t *testing.T) {
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())

	cfg := &config.YAMLConfig{
		RetiredModels: map[string]map[string]config.RetiredModelEntry{
			"openai": {
				"o1-mini": {
					RetiredDate: "2025-10-27",
					Replacement: "o4-mini",
					Aliases:     []string{"o1-mini-2024-09-12"},
				},
			},
		},
	}

	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	chain := ModelStatusMiddleware(pm, cfg, modelstatusstats.NewRecorder(), nil)(next)

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(`{"model":"o1-mini-2024-09-12","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if called {
		t.Fatal("next handler should not run for retired alias")
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", rec.Code)
	}
}
