package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/modelstatusstats"
	"github.com/Instawork/llm-proxy/internal/providers"
)

func TestModelStatusMiddleware_UnknownModelRecorded(t *testing.T) {
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())

	cfg := &config.YAMLConfig{
		Providers: map[string]config.ProviderConfig{
			"openai": {
				Enabled: true,
				Models: map[string]config.ModelConfig{
					"gpt-4o": {Enabled: true},
				},
			},
		},
	}

	recorder := modelstatusstats.NewRecorder()
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	chain := ModelStatusMiddleware(pm, cfg, recorder, nil)(next)

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(`{"model":"not-a-real-model","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if !called {
		t.Fatal("next handler should run for unknown model")
	}
	snap := recorder.Snapshot()
	if snap["unknown_total"] != int64(1) {
		t.Fatalf("unknown_total=%v want 1", snap["unknown_total"])
	}
}
