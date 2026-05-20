package middleware

import (
	"fmt"
	"log"
	"net/http"

	"github.com/Instawork/llm-proxy/internal/providers"
)

// APIKeyValidationMiddleware validates and potentially replaces API keys for
// all providers.
//
// A request whose path *looks like* a provider route but for which no
// matching provider is registered (e.g. /openai/... when openai is disabled
// in config) MUST NOT be silently forwarded — that would allow the proxy to
// 502/404 a request without ever rejecting an invalid key, exposing the
// proxy's behavior to /scan attacks that probe whether a key is configured.
// We reject such cases up-front with 502 Bad Gateway. Genuinely non-provider
// paths (e.g. /health, /metrics) bypass validation as before.
func APIKeyValidationMiddleware(providerManager *providers.ProviderManager, keyStore providers.APIKeyStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" {
				next.ServeHTTP(w, r)
				return
			}

			provider := GetProviderFromRequest(providerManager, r)
			if provider == nil {
				// If the path looks like a provider route, refuse — see
				// docstring above.
				if isProviderRoute(r.URL.Path) {
					log.Printf("❌ API key validation: provider route %q has no registered provider", r.URL.Path)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadGateway)
					fmt.Fprintf(w, `{"error": "Provider not configured for this route"}`)
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			if keyStore != nil {
				if err := provider.ValidateAPIKey(r, keyStore); err != nil {
					log.Printf("❌ API key validation failed for %s: %v", provider.GetName(), err)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprintf(w, `{"error": "Invalid API key: %s"}`, err.Error())
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}
