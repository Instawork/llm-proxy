package middleware

import (
	"net/http"
	"sync"
	"time"
)

// RedactRateLimitMiddleware enforces per-API-key request limits on POST /redact.
// It is independent of features.rate_limiting.enabled so hooks stay bounded
// even when provider rate limiting is off.
func RedactRateLimitMiddleware(requestsPerMinute int) func(http.Handler) http.Handler {
	if requestsPerMinute <= 0 {
		return func(next http.Handler) http.Handler { return next }
	}
	lim := newRedactKeyLimiter(requestsPerMinute)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/redact" || r.Method != http.MethodPost {
				next.ServeHTTP(w, r)
				return
			}
			key := extractInboundProxyKey(r)
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}
			if !lim.allow(key, time.Now()) {
				w.Header().Set("Retry-After", "60")
				http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type redactKeyLimiter struct {
	rpm     int
	mu      sync.Mutex
	windows map[string]*redactKeyWindow
}

type redactKeyWindow struct {
	start time.Time
	count int
}

func newRedactKeyLimiter(rpm int) *redactKeyLimiter {
	return &redactKeyLimiter{rpm: rpm, windows: make(map[string]*redactKeyWindow)}
}

func (l *redactKeyLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	win := l.windows[key]
	if win == nil || now.Sub(win.start) >= time.Minute {
		l.windows[key] = &redactKeyWindow{start: now, count: 1}
		return true
	}
	if win.count >= l.rpm {
		return false
	}
	win.count++
	return true
}
