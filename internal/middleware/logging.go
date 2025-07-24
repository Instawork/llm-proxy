package middleware

import (
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/providers"
)

// isProviderRoute checks if the request is for a provider route
func isProviderRoute(path string) bool {
	return strings.HasPrefix(path, "/openai/") ||
		strings.HasPrefix(path, "/anthropic/") ||
		strings.HasPrefix(path, "/gemini/")
}

// isAPIEndpoint checks if the request is for an API endpoint that should be cost tracked
func isAPIEndpoint(path string) bool {
	return strings.Contains(path, "/chat/completions") ||
		strings.Contains(path, "/completions") ||
		strings.Contains(path, "/messages") ||
		strings.Contains(path, ":generateContent")
}

// getProviderFromPath extracts provider name from the request path
func getProviderFromPath(path string) string {
	if strings.HasPrefix(path, "/openai/") {
		return "openai"
	} else if strings.HasPrefix(path, "/anthropic/") {
		return "anthropic"
	} else if strings.HasPrefix(path, "/gemini/") {
		return "gemini"
	}
	return ""
}

// LoggingMiddleware logs all incoming requests and identifies non-tracked provider routes
func LoggingMiddleware(providerManager *providers.ProviderManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			
			// Detect if this might be a streaming request using the provider manager
			isStreaming := providerManager.IsStreamingRequest(r)
			
			// Check if this is a provider route
			isProvRoute := isProviderRoute(r.URL.Path)
			isAPIEndpt := isAPIEndpoint(r.URL.Path)
			provider := GetProviderFromRequest(providerManager, r)
			providerName := getProviderFromPath(r.URL.Path)
			
			// Determine if this request will be cost tracked
			willBeTracked := isProvRoute && isAPIEndpt && provider != nil
			
			// Log the request with additional context for provider routes
			if isStreaming {
				log.Printf("Started streaming %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
			} else {
				log.Printf("Started %s %s from %s", r.Method, r.URL.Path, r.RemoteAddr)
			}
			
			// Log non-tracked provider routes for production monitoring
			if isProvRoute && !willBeTracked {
				logMessage := ""
				if provider == nil && providerName != "" {
					logMessage = "🚨 NON-TRACKED PROVIDER ROUTE: Provider not found"
				} else if !isAPIEndpt {
					logMessage = "📊 NON-TRACKED PROVIDER ROUTE: Non-API endpoint"
				} else {
					logMessage = "⚠️  NON-TRACKED PROVIDER ROUTE: Unknown reason"
				}
				
				log.Printf("%s - %s %s (provider: %s, api_endpoint: %t, provider_found: %t)", 
					logMessage, r.Method, r.URL.Path, providerName, isAPIEndpt, provider != nil)
			}
			
			// Call the next handler
			next.ServeHTTP(w, r)
			
			// Log the completion with cost tracking status
			duration := time.Since(start)
			if isStreaming {
				log.Printf("Completed streaming %s %s in %v", r.Method, r.URL.Path, duration)
			} else {
				log.Printf("Completed %s %s in %v", r.Method, r.URL.Path, duration)
			}
			
			// Summary log for provider routes
			if isProvRoute {
				if willBeTracked {
					log.Printf("✅ TRACKED: %s %s - will be cost tracked", r.Method, r.URL.Path)
				} else {
					log.Printf("❌ NOT TRACKED: %s %s - provider route but not cost tracked", r.Method, r.URL.Path)
				}
			}
		})
	}
} 
