package middleware

import (
	"fmt"
	"log"
	"net/http"

	"github.com/Instawork/llm-proxy/internal/providers"
)

// APIKeyValidationMiddleware validates and potentially replaces API keys for all providers
func APIKeyValidationMiddleware(providerManager *providers.ProviderManager, keyStore providers.APIKeyStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip validation for health check endpoint
			if r.URL.Path == "/health" {
				next.ServeHTTP(w, r)
				return
			}

			// Get the provider from the request path
			provider := GetProviderFromRequest(providerManager, r)
			if provider == nil {
				// No provider found, continue without validation
				next.ServeHTTP(w, r)
				return
			}

			// If we have a key store, validate the API key
			if keyStore != nil {
				if err := provider.ValidateAPIKey(r, keyStore); err != nil {
					// Log the error
					log.Printf("❌ API key validation failed for %s: %v", provider.GetName(), err)

					// Return 401 Unauthorized
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprintf(w, `{"error": "Invalid API key: %s"}`, err.Error())
					return
				}
			}

			// Continue to the next handler
			next.ServeHTTP(w, r)
		})
	}
}
