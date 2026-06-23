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

type byoBanChecker interface {
	IsBYOCredentialBanned(ctx context.Context, provider, hash string) (bool, error)
}

// APIKeyValidationMiddleware validates and potentially replaces API keys for
// all providers. globalPIIEnabled is features.pii_redact.enabled from YAML.
// byoKeysEnabled is features.byo_keys.enabled — when false, raw provider
// credentials are rejected and callers must use proxy iw-* keys.
func APIKeyValidationMiddleware(providerManager *providers.ProviderManager, keyStore providers.APIKeyStore, globalPIIEnabled, byoKeysEnabled bool) func(http.Handler) http.Handler {
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

				if inboundKey != "" && !apikeys.HasKeyPrefix(inboundKey) {
					if !byoKeysEnabled {
						log.Printf("❌ BYO keys disabled for provider %s", provider.GetName())
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusForbidden)
						fmt.Fprintf(w, `{"error": "Bring-your-own provider keys are not accepted; use a proxy key"}`)
						return
					}
					checker, ok := keyStore.(byoBanChecker)
					if !ok {
						log.Printf("❌ BYO ban lookup unavailable: key store does not implement ban checker")
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusInternalServerError)
						fmt.Fprintf(w, `{"error": "Internal server error"}`)
						return
					}
					hash := apikeys.CredentialHashSuffix(inboundKey)
					banned, err := checker.IsBYOCredentialBanned(r.Context(), provider.GetName(), hash)
					if err != nil {
						log.Printf("❌ BYO ban lookup failed for %s: %v", provider.GetName(), err)
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusInternalServerError)
						fmt.Fprintf(w, `{"error": "Internal server error"}`)
						return
					}
					if banned {
						log.Printf("❌ BYO credential banned for provider %s", provider.GetName())
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusForbidden)
						fmt.Fprintf(w, `{"error": "API key is banned"}`)
						return
					}
				}

				if err := provider.ValidateAPIKey(r, keyStore); err != nil {
					log.Printf("❌ API key validation failed for %s: %v", provider.GetName(), err)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusUnauthorized)
					fmt.Fprintf(w, `{"error": "Invalid API key: %s"}`, err.Error())
					return
				}

				proxyKeyAttached := false
				if lookup, ok := keyStore.(proxyKeyLookup); ok && inboundKey != "" {
					if record, err := lookup.LookupProxyKey(r.Context(), inboundKey); err == nil && record != nil {
						r = r.WithContext(apikeys.WithContext(r.Context(), record))
						proxyKeyAttached = true
						if err := apikeys.EnforcePIIOffBedrockProvider(globalPIIEnabled, record); err != nil {
							log.Printf("❌ PII-off Bedrock policy violation for %s: %v", provider.GetName(), err)
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusForbidden)
							fmt.Fprintf(w, `{"error": "%s"}`, err.Error())
							return
						}
					}
				}

				// Bring-your-own-key callers (raw sk-ant-/AIza/... credential,
				// no proxy record) get a masked, non-reversible identity stashed
				// for the admin "Recent scans" debug table. Captured from the
				// pre-swap inbound key so we never surface the upstream secret.
				if !proxyKeyAttached && inboundKey != "" {
					r = r.WithContext(withInboundCredentialID(r.Context(), MaskProviderCredential(inboundKey)))
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

// inboundCredIDCtxKey stashes a masked, non-reversible identity for a caller
// that authenticated with a raw provider credential (its own sk-ant-/AIza/...
// key) rather than a proxy sk-iw key. Used only for the admin "Recent scans"
// debug table — never the secret itself.
type inboundCredIDCtxKey struct{}

func withInboundCredentialID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, inboundCredIDCtxKey{}, id)
}

// InboundCredentialID returns the masked provider-credential identity stashed
// for non-proxy callers, or "" when the caller used a proxy key (or none).
func InboundCredentialID(ctx context.Context) string {
	if v, ok := ctx.Value(inboundCredIDCtxKey{}).(string); ok {
		return v
	}
	return ""
}

// providerCredentialPrefixes are the well-known, non-secret leading markers of
// raw provider credentials. Ordered so the more specific families (sk-ant-,
// sk-proj-) win over the generic sk- before we fall back to a tiny fixed lead.
var providerCredentialPrefixes = []string{
	"sk-ant-",                 // Anthropic
	"sk-proj-", "sk-svcacct-", // OpenAI project / service-account
	"sk-or-", // OpenRouter
	"sk-",    // OpenAI classic
	"AIza",   // Google / Gemini
	"gsk_",   // Groq
	"xai-",   // xAI
}

// MaskProviderCredential renders a debug-friendly, non-reversible identity for
// a raw provider credential (a caller that brought its own key instead of a
// proxy sk-iw key). It reveals only the well-known family prefix plus an
// FNV-1a hash of the whole credential, so distinct keys are distinguishable in
// the admin "Recent scans" table without ever surfacing secret bytes. Unknown
// shapes expose a 4-char lead at most.
func MaskProviderCredential(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	prefix := ""
	for _, p := range providerCredentialPrefixes {
		if strings.HasPrefix(raw, p) {
			prefix = p
			break
		}
	}
	if prefix == "" {
		n := 4
		if len(raw) < n {
			n = len(raw)
		}
		prefix = raw[:n]
	}
	return prefix + "…" + keyIDHashSuffix(raw)
}
