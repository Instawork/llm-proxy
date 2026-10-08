package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/proxylog"
)

type proxyKeyLookup interface {
	LookupProxyKey(ctx context.Context, bearer string) (*apikeys.APIKey, error)
}

type byoBanChecker interface {
	IsBYOCredentialBanned(ctx context.Context, provider, hash string) (bool, error)
}

// APIKeyValidationMiddleware validates and potentially replaces API keys for
// all providers. byoKeysEnabled is features.byo_keys.enabled — when false, raw provider
// credentials are rejected and callers must use proxy iw-* keys. authFailures
// (nil to disable) throttles clients that keep presenting rejected keys.
func APIKeyValidationMiddleware(providerManager *providers.ProviderManager, keyStore providers.APIKeyStore, byoKeysEnabled bool, authFailures *AuthFailureGuard) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/health" || r.URL.Path == "/redact" || strings.HasPrefix(r.URL.Path, "/admin/") {
				next.ServeHTTP(w, r)
				return
			}

			provider := GetProviderFromRequest(providerManager, r)
			if provider == nil {
				if isProviderRoute(r.URL.Path) {
					proxylog.Proxy("API key validation: provider route %q has no registered provider", r.URL.Path)
					proxylog.WriteProxyJSONError(w, http.StatusBadGateway, "Provider not configured for this route")
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			if keyStore != nil {
				if authFailures.Blocked(r) {
					proxylog.Proxy("API key validation: client throttled after repeated failures on %s", provider.GetName())
					authFailures.WriteBlocked(w)
					return
				}
				rejectUnauthorized := func(msg string) {
					authFailures.RecordFailure(r)
					proxylog.WriteProxyJSONError(w, http.StatusUnauthorized, msg)
				}

				// Capture the inbound iw: key before ValidateAPIKey may swap it
				// for the upstream provider credential.
				inboundKey := extractInboundProxyKey(r)

				// A proxy key in the URL lands in every access log between the
				// client and this process (ALB, WAF); only headers are accepted.
				if apikeys.HasKeyPrefix(inboundKey) && proxyKeyOnlyInQuery(r, inboundKey) && !allowsProxyKeyInQuery(provider, r) {
					proxylog.Proxy("API key validation: proxy key sent in query string for %s", provider.GetName())
					rejectUnauthorized("Invalid API key: proxy keys must be sent in a header (Authorization: Bearer or x-goog-api-key), not the ?key= query string")
					return
				}

				if inboundKey != "" && !apikeys.HasKeyPrefix(inboundKey) {
					if !byoKeysEnabled {
						proxylog.Proxy("BYO keys disabled for provider %s", provider.GetName())
						proxylog.WriteProxyJSONError(w, http.StatusForbidden, "Bring-your-own provider keys are not accepted; use a proxy key")
						return
					}
					checker, ok := keyStore.(byoBanChecker)
					if !ok {
						proxylog.Proxy("BYO ban lookup unavailable: key store does not implement ban checker")
						proxylog.WriteProxyJSONError(w, http.StatusInternalServerError, "Internal server error")
						return
					}
					hash := apikeys.CredentialHashSuffix(inboundKey)
					banned, err := checker.IsBYOCredentialBanned(r.Context(), provider.GetName(), hash)
					if err != nil {
						proxylog.Proxy("BYO ban lookup failed for %s: %v", provider.GetName(), err)
						proxylog.WriteProxyJSONError(w, http.StatusInternalServerError, "Internal server error")
						return
					}
					if banned {
						proxylog.Proxy("BYO credential banned for provider %s", provider.GetName())
						proxylog.WriteProxyJSONError(w, http.StatusForbidden, "API key is banned")
						return
					}
				}

				if err := provider.ValidateAPIKey(r, keyStore); err != nil {
					proxylog.Proxy("API key validation failed for %s: %v", provider.GetName(), err)
					rejectUnauthorized(fmt.Sprintf("Invalid API key: %s", err.Error()))
					return
				}

				proxyKeyAttached := false
				if lookup, ok := keyStore.(proxyKeyLookup); ok && inboundKey != "" {
					record, err := lookup.LookupProxyKey(r.Context(), inboundKey)
					if err != nil {
						// Only a prefixed proxy key reaches this branch with an error
						// (disabled, expired, or unknown); providers like Bedrock whose
						// ValidateAPIKey is a SigV4-passthrough no-op rely on this lookup
						// as their only gate, so a swallowed error here would let a
						// revoked key keep working.
						proxylog.Proxy("proxy key lookup failed for %s: %v", provider.GetName(), err)
						rejectUnauthorized(fmt.Sprintf("Invalid API key: %s", err.Error()))
						return
					}
					if record != nil {
						r = r.WithContext(apikeys.WithContext(r.Context(), record))
						proxyKeyAttached = true
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

// proxyKeyOnlyInQuery reports whether key reached us solely via ?key=.
func proxyKeyOnlyInQuery(r *http.Request, key string) bool {
	if extractBearerToken(r) != "" || r.Header.Get("x-api-key") != "" || r.Header.Get("x-goog-api-key") != "" {
		return false
	}
	return r.URL.Query().Get("key") == key
}

// allowsProxyKeyInQuery exempts all Gemini routes from the header-only rule.
// n8n's Google Gemini(PaLM) credential is query-string auth only
// (qs: { key: ... }) and sends the proxy key as ?key= on every call it makes
// — the model list, uploads, and :generateContent alike — not just the
// credential test (#94 scoped the exemption to the model list GET, which
// left every other Gemini call from n8n 401ing).
func allowsProxyKeyInQuery(provider providers.Provider, _ *http.Request) bool {
	return provider.GetName() == "gemini"
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
