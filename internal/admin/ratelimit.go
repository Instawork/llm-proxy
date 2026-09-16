package admin

import (
	"math"
	"net"
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

// sweepLocked evicts idle buckets so memory stays bounded under a churn of
// distinct keys. A bucket is only dropped once it has been idle long enough
// to have refilled completely, so eviction never hands a depleted key a
// fresh burst early. Caller must hold l.mu.
func (l *tokenBucketLimiter) sweepLocked(now time.Time) {
	if now.Sub(l.lastSweep) < l.idleTTL {
		return
	}
	l.lastSweep = now
	for k, b := range l.buckets {
		idle := now.Sub(b.last)
		if idle > l.idleTTL && b.tokens+idle.Seconds()*l.rate >= l.burst {
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
