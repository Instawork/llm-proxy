package middleware

import (
	"net/http"

	"github.com/Instawork/llm-proxy/internal/providers"
)

// CORSMiddleware adds CORS headers with streaming support.
//
// Access-Control-Allow-Headers must include every provider-auth header a
// browser-based client might send, otherwise the browser silently fails the
// preflight: Anthropic uses `x-api-key` and `anthropic-version`, Gemini uses
// `x-goog-api-key`, and OpenAI uses `Authorization`. Missing any of these
// would have manifested as opaque CORS errors only when called from a web
// SDK — never from the Go test suite — which is why this drifted.
func CORSMiddleware(providerManager *providers.ProviderManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers",
				"Content-Type, Authorization, Accept, Cache-Control, "+
					"x-api-key, anthropic-version, x-goog-api-key, "+
					"X-LLM-Proxy-Test-Mode, X-Amz-Date, X-Amz-Security-Token, "+
					"X-Amz-Content-Sha256")
			if providerManager.IsStreamingRequest(r) {
				w.Header().Set("Access-Control-Expose-Headers", "Content-Type, Cache-Control")
			}
			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
