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

func TestModelStatusMiddleware_RecordsUnmeteredEndpoint(t *testing.T) {
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())
	recorder := modelstatusstats.NewRecorder()
	metrics := &fakeDogstatsd{}

	called := 0
	chain := ModelStatusMiddleware(pm, &config.YAMLConfig{}, recorder, metrics)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called++ }),
	)

	for _, path := range []string{"/openai/v1/audio/transcriptions", "/openai/v1/audio/transcriptions", "/openai/v1/chat/completions", "/openai/v1/models"} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-5.5"}`))
		chain.ServeHTTP(httptest.NewRecorder(), req)
	}

	if called != 4 {
		t.Fatalf("unmetered endpoints must still be forwarded; next ran %d times", called)
	}
	snap := recorder.Snapshot()
	if snap["unmetered_total"] != int64(2) {
		t.Fatalf("unmetered_total=%v want 2", snap["unmetered_total"])
	}
	byUnmetered, _ := json.Marshal(snap["by_unmetered"])
	if !strings.Contains(string(byUnmetered), `"openai:/openai/v1/audio/transcriptions"`) {
		t.Fatalf("by_unmetered=%s", byUnmetered)
	}
	got := 0
	for _, c := range metrics.calls {
		if c == "endpoint.unmetered_call" {
			got++
		}
	}
	if got != 2 {
		t.Fatalf("endpoint.unmetered_call emitted %d times, want 2", got)
	}
}
