package middleware

import (
	"net/http"

	"github.com/Instawork/llm-proxy/internal/providers"
)

// CORSMiddleware adds CORS headers with streaming support
func CORSMiddleware(providerManager *providers.ProviderManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, Accept, Cache-Control")
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
