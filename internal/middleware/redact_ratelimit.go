package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/internal/proxylog"
)

// RedactRateLimitMiddleware enforces per-API-key request limits on POST /redact.
// It is independent of features.rate_limiting.enabled so hooks stay bounded
// even when provider rate limiting is off. The key is the presented (not yet
// validated) credential, so windows are evicted once they expire to keep an
// anonymous caller from growing the map without bound.
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
			key := extractBearerToken(r)
			if key == "" {
				key = r.Header.Get("x-api-key")
			}
			if key == "" {
				next.ServeHTTP(w, r)
				return
			}
			if !lim.allow(key, time.Now()) {
				w.Header().Set("Retry-After", "60")
				proxylog.ProxyHTTPError(w, "rate limit exceeded", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type redactKeyLimiter struct {
	rpm       int
	mu        sync.Mutex
	windows   map[string]*redactKeyWindow
	lastSweep time.Time
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

	if now.Sub(l.lastSweep) >= time.Minute {
		l.lastSweep = now
		for k, w := range l.windows {
			if now.Sub(w.start) >= time.Minute {
				delete(l.windows, k)
			}
		}
	}

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
