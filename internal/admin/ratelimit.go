package admin

import (
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// tokenBucketLimiter is a tiny keyed token-bucket limiter. It guards the
// public, unauthenticated share-read endpoint per client IP and caps how
// fast one owner can mint upstream keys. Both are abuse hygiene, not the
// primary access control. It is memory-only and per-instance (not shared
// across replicas), which is acceptable for these low-volume paths.
type tokenBucketLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	rate    float64 // tokens added per second
	burst   float64 // max tokens (and initial allowance)

	lastSweep time.Time
	idleTTL   time.Duration
}

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// newTokenBucketLimiter builds a limiter allowing `burst` immediate requests per
// client, refilling at `ratePerSec` tokens/second.
func newTokenBucketLimiter(ratePerSec, burst float64) *tokenBucketLimiter {
	return &tokenBucketLimiter{
		buckets:   make(map[string]*tokenBucket),
		rate:      ratePerSec,
		burst:     burst,
		lastSweep: time.Now(),
		idleTTL:   10 * time.Minute,
	}
}

// allow reports whether a request from key is permitted at time now,
// consuming one token when it is.
func (l *tokenBucketLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.buckets[key]
	if b == nil {
		b = &tokenBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		elapsed := now.Sub(b.last).Seconds()
		if elapsed > 0 {
			b.tokens = math.Min(l.burst, b.tokens+elapsed*l.rate)
			b.last = now
		}
	}

	l.sweepLocked(now)

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// sweepLocked evicts buckets idle past idleTTL so memory stays bounded under a
// churn of distinct client IPs. Caller must hold l.mu.
func (l *tokenBucketLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.idleTTL {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		if now.Sub(b.last) > l.idleTTL {
			delete(l.buckets, k)
		}
	}
}

// middleware rejects requests from a client that has exhausted its bucket with
// a 429, otherwise passes through.
func (l *tokenBucketLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(clientIP(r), time.Now()) {
			w.Header().Set("Retry-After", "1")
			writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limited"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the best-effort client address for rate-limiting and audit
// logging: the first hop of X-Forwarded-For when present (the proxy runs
// behind an ALB that sets it), else the connection RemoteAddr with the port
// stripped.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, found := strings.Cut(xff, ","); found {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	return host
}
