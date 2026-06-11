package middleware

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/providers"
)

type proxyKeyLookup interface {
	LookupProxyKey(ctx context.Context, bearer string) (*apikeys.APIKey, error)
}

// APIKeyValidationMiddleware validates and potentially replaces API keys for
// all providers.
func APIKeyValidationMiddleware(providerManager *providers.ProviderManager, keyStore providers.APIKeyStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" || strings.HasPrefix(r.URL.Path, "/admin/") {
				next.ServeHTTP(w, r)
				return
			}

			provider := GetProviderFromRequest(providerManager, r)
			if provider == nil {
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

				if lookup, ok := keyStore.(proxyKeyLookup); ok {
					if bearer := extractBearerToken(r); bearer != "" {
						if record, err := lookup.LookupProxyKey(r.Context(), bearer); err == nil && record != nil {
							r = r.WithContext(apikeys.WithContext(r.Context(), record))
						}
					}
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

func extractBearerToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	const bearerPrefix = "Bearer "
	if !strings.HasPrefix(authHeader, bearerPrefix) {
		return ""
	}
	return strings.TrimPrefix(authHeader, bearerPrefix)
}
