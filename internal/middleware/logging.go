package middleware

import (
	"context"
	"log/slog"
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
				slog.Info("Started streaming request",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("remote_addr", r.RemoteAddr),
					slog.Bool("streaming", true))
			} else {
				slog.Info("Started request",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("remote_addr", r.RemoteAddr),
					slog.Bool("streaming", false))
			}

			// Log non-tracked provider routes for production monitoring
			if isProvRoute && !willBeTracked {
				var reason string
				var level slog.Level = slog.LevelWarn
				if provider == nil && providerName != "" {
					reason = "Provider not found"
					level = slog.LevelError
				} else if !isAPIEndpt {
					reason = "Non-API endpoint"
					level = slog.LevelInfo
				} else {
					reason = "Unknown reason"
				}

				slog.Log(context.TODO(), level, "Non-tracked provider route",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("provider", providerName),
					slog.String("reason", reason),
					slog.Bool("api_endpoint", isAPIEndpt),
					slog.Bool("provider_found", provider != nil))
			}

			// Call the next handler
			next.ServeHTTP(w, r)

			// Log the completion with cost tracking status
			duration := time.Since(start)
			if isStreaming {
				slog.Info("Completed streaming request",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Duration("duration", duration),
					slog.Bool("streaming", true))
			} else {
				slog.Info("Completed request",
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.Duration("duration", duration),
					slog.Bool("streaming", false))
			}

			// Summary log for provider routes
			if isProvRoute {
				if willBeTracked {
					slog.Info("Provider route tracked",
						slog.String("method", r.Method),
						slog.String("path", r.URL.Path),
						slog.Bool("cost_tracked", true))
				} else {
					slog.Warn("Provider route not tracked",
						slog.String("method", r.Method),
						slog.String("path", r.URL.Path),
						slog.Bool("cost_tracked", false))
				}
			}
		})
	}
}
