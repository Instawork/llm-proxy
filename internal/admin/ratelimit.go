package admin

import (
	"net/http"
	"time"

	"github.com/Instawork/llm-proxy/internal/middleware"
)

// ipLimitMiddleware rejects requests from a client that has exhausted its
// bucket with a 429, otherwise passes through. Buckets are keyed on the
// trusted client IP so a forged X-Forwarded-For prefix cannot reset them.
func ipLimitMiddleware(l *middleware.TokenBucketLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.Allow(middleware.TrustedClientIP(r), time.Now()) {
				w.Header().Set("Retry-After", "1")
				writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limited"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
