package middleware

import (
	"net/http"

	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/proxylog"
)

// VendorPathPolicyMiddleware refuses provider requests aimed at vendor
// account-administration APIs (see providers.IsBlockedVendorPath). The
// upstream credential the proxy swaps in is entitled to those surfaces; a
// proxy key must not inherit that reach.
func VendorPathPolicyMiddleware(providerManager *providers.ProviderManager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if GetProviderFromRequest(providerManager, r) != nil && providers.IsBlockedVendorPath(r.URL.Path) {
				proxylog.Proxy("vendor path policy: refused %s %s", r.Method, r.URL.Path)
				proxylog.WriteProxyJSONError(w, http.StatusForbidden, "This vendor administration endpoint is not available through the proxy")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
