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
// all providers. globalPIIEnabled is features.pii_redact.enabled from YAML.
func APIKeyValidationMiddleware(providerManager *providers.ProviderManager, keyStore providers.APIKeyStore, globalPIIEnabled bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" || r.URL.Path == "/redact" || strings.HasPrefix(r.URL.Path, "/admin/") {
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
				// Capture the inbound iw: key before ValidateAPIKey may swap it
				// for the upstream provider credential.
				inboundKey := extractInboundProxyKey(r)

				if err := provider.ValidateAPIKey(r, keyStore); err != nil {
					log.Printf("❌ API key validation failed for %s: %v", provider.GetName(), err)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprintf(w, `{"error": "Invalid API key: %s"}`, err.Error())
					return
				}

				if lookup, ok := keyStore.(proxyKeyLookup); ok && inboundKey != "" {
					if record, err := lookup.LookupProxyKey(r.Context(), inboundKey); err == nil && record != nil {
						r = r.WithContext(apikeys.WithContext(r.Context(), record))
						if err := apikeys.EnforcePIIOffBedrockProvider(globalPIIEnabled, record); err != nil {
							log.Printf("❌ PII-off Bedrock policy violation for %s: %v", provider.GetName(), err)
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusForbidden)
							fmt.Fprintf(w, `{"error": "%s"}`, err.Error())
							return
						}
					}
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

// extractInboundProxyKey reads the caller's credential from any supported
// provider auth surface before upstream key translation.
func extractInboundProxyKey(r *http.Request) string {
	if bearer := extractBearerToken(r); bearer != "" {
		return bearer
	}
	if k := r.Header.Get("x-api-key"); k != "" {
		return k
	}
	if k := r.Header.Get("x-goog-api-key"); k != "" {
		return k
	}
	return r.URL.Query().Get("key")
}

func extractBearerToken(r *http.Request) string {
	authHeader := r.Header.Get("Authorization")
	const bearerPrefix = "Bearer "
	if !strings.HasPrefix(authHeader, bearerPrefix) {
		return ""
	}
	return strings.TrimPrefix(authHeader, bearerPrefix)
}
