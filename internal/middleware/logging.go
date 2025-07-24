package middleware

import (
	"log"
	"net/http"
	"time"

	"github.com/Instawork/llm-proxy/internal/providers"
)

// LoggingMiddleware logs all incoming requests
func LoggingMiddleware(providerManager *providers.ProviderManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			
			// Detect if this might be a streaming request using the provider manager
			isStreaming := providerManager.IsStreamingRequest(r)
			
			// Log the request
			if isStreaming {
				log.Printf("Started streaming %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
			} else {
				log.Printf("Started %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
			}
			
			// Call the next handler
			next.ServeHTTP(w, r)
			
			// Log the completion
			if isStreaming {
				log.Printf("Completed streaming %s %s in %v", r.Method, r.URL.Path, time.Since(start))
			} else {
				log.Printf("Completed %s %s in %v", r.Method, r.URL.Path, time.Since(start))
			}
		})
	}
} 
