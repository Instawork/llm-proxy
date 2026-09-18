package middleware

import (
	"net/http"
	"time"

	"github.com/Instawork/llm-proxy/internal/proxylog"
)

const (
	// authFailureBurst is how many rejected credentials one client IP may
	// present before further attempts are refused without a store lookup.
	authFailureBurst = 30
	// authFailureRefillPerSec restills roughly one attempt every two seconds,
	// so a legitimate client that typo'd a key recovers quickly while a
	// scanner is held to ~30 lookups a minute.
	authFailureRefillPerSec = 0.5
)

// AuthFailureGuard throttles repeated credential rejections per client IP so
// an anonymous caller cannot drive unbounded key-store lookups or spend
// against on-demand DynamoDB capacity. Successful requests are never charged.
type AuthFailureGuard struct {
	limiter *TokenBucketLimiter
}

// NewAuthFailureGuard returns a guard with the package defaults.
func NewAuthFailureGuard() *AuthFailureGuard {
	return &AuthFailureGuard{limiter: NewTokenBucketLimiter(authFailureRefillPerSec, authFailureBurst)}
}

// Blocked reports whether r's client has exhausted its failure allowance.
func (g *AuthFailureGuard) Blocked(r *http.Request) bool {
	if g == nil {
		return false
	}
	return g.limiter.Exhausted(TrustedClientIP(r), time.Now())
}

// RecordFailure charges one rejected credential to r's client.
func (g *AuthFailureGuard) RecordFailure(r *http.Request) {
	if g == nil {
		return
	}
	g.limiter.Spend(TrustedClientIP(r), time.Now())
}

// WriteBlocked answers a throttled client with a 429.
func (g *AuthFailureGuard) WriteBlocked(w http.ResponseWriter) {
	w.Header().Set("Retry-After", "60")
	proxylog.WriteProxyJSONError(w, http.StatusTooManyRequests, "Too many failed authentication attempts; retry later")
}
