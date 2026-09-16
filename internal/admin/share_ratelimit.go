package admin

import (
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// shareRateLimiter is a tiny per-client token-bucket limiter guarding the
// public, unauthenticated share-read endpoint. Guessing a share UUID is
// already infeasible (122-bit crypto/rand v4), so this is abuse/DoS hygiene —
// it caps how fast a single client can hammer the endpoint — not the primary
// access control. It is memory-only and per-instance (not shared across
// replicas), which is acceptable for this low-volume endpoint.
type shareRateLimiter struct {
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

// newShareRateLimiter builds a limiter allowing `burst` immediate requests per
// client, refilling at `ratePerSec` tokens/second.
func newShareRateLimiter(ratePerSec, burst float64) *shareRateLimiter {
	return &shareRateLimiter{
		buckets:   make(map[string]*tokenBucket),
		rate:      ratePerSec,
		burst:     burst,
		lastSweep: time.Now(),
		idleTTL:   10 * time.Minute,
	}
}

// allow reports whether a request from key is permitted at time now,
// consuming one token when it is.
func (l *shareRateLimiter) allow(key string, now time.Time) bool {
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
func (l *shareRateLimiter) sweepLocked(now time.Time) {
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
func (l *shareRateLimiter) middleware(next http.Handler) http.Handler {
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
// logging. X-Forwarded-For is only consulted when the immediate peer is a
// private or loopback address, i.e. the ALB (or a local reverse proxy) that
// appends the address it accepted the connection from; then the last hop is
// the trustworthy one and earlier hops are whatever the caller sent. A peer
// reaching the listener directly gets keyed on its own RemoteAddr.
func clientIP(r *http.Request) string {
	peer := r.RemoteAddr
	if h, _, err := net.SplitHostPort(peer); err == nil {
		peer = h
	}
	if ip := net.ParseIP(peer); ip != nil && (ip.IsPrivate() || ip.IsLoopback()) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			hops := strings.Split(xff, ",")
			if last := strings.TrimSpace(hops[len(hops)-1]); last != "" {
				return last
			}
		}
	}
	return peer
}
