package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/Instawork/llm-proxy/internal/providers"
)

// Context key for storing user ID extracted from meta URL
type contextKey string

const (
	userIDContextKey contextKey = "user_id"
)

// MetaURLRewritingMiddleware centralizes URL path rewriting for all providers
// It handles both direct provider paths (/openai/, /anthropic/, /gemini/)
// and meta paths (/meta/{userID}/openai/, /meta/{userID}/anthropic/, /meta/{userID}/gemini/)
// It also extracts and stores the user ID in context for later use by other middleware
func MetaURLRewritingMiddleware(providerManager *providers.ProviderManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Handle /meta/{userID}/provider/ pattern
			if strings.HasPrefix(r.URL.Path, "/meta/") {
				parts := strings.Split(r.URL.Path, "/")
				if len(parts) >= 4 { // ["", "meta", "userID", "provider", ...]
					userID := parts[2]
					providerName := parts[3]

					// Store user ID in context for later middleware to use
					if userID != "" {
						ctx := context.WithValue(r.Context(), userIDContextKey, userID)
						r = r.WithContext(ctx)
					}

					if providerManager.IsValidProvider(providerName) {
						// Rewrite path: /meta/{userID}/provider/rest -> /provider/rest
						providerPath := "/" + providerName + "/"
						restPath := strings.Join(parts[4:], "/")
						if restPath != "" {
							r.URL.Path = providerPath + restPath
						} else {
							r.URL.Path = providerPath
						}
					}
				}
			}

			// Continue to next handler (which will include the provider's reverse proxy)
			next.ServeHTTP(w, r)
		})
	}
}
