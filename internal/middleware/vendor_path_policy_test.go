package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/providers"
)

func TestVendorPathPolicyMiddleware(t *testing.T) {
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())
	pm.RegisterProvider(providers.NewAnthropicProxy())
	pm.RegisterProvider(providers.NewGeminiProxy())

	h := VendorPathPolicyMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	cases := []struct {
		path string
		want int
	}{
		{"/openai/v1/chat/completions", http.StatusOK},
		{"/openai/v1/organization/admin_api_keys", http.StatusForbidden},
		{"/openai/v1/organization", http.StatusForbidden},
		{"/openai/v1/fine_tuning/jobs", http.StatusForbidden},
		{"/anthropic/v1/messages", http.StatusOK},
		{"/anthropic/v1/organizations/api_keys", http.StatusForbidden},
		{"/gemini/v1beta/models/gemini-pro:generateContent", http.StatusOK},
		{"/gemini/v1beta/tunedModels", http.StatusForbidden},
		// Non-provider paths are left alone even if they look administrative.
		{"/admin/api/organization/", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.path, nil))
			if rec.Code != tc.want {
				t.Fatalf("%s: got %d, want %d", tc.path, rec.Code, tc.want)
			}
		})
	}
}
