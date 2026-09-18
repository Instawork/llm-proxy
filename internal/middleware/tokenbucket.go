package middleware

import (
	"math"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TokenBucketLimiter is a tiny keyed token-bucket limiter for abuse hygiene
// on low-volume paths (public share reads, key minting, failed credential
// lookups). It is memory-only and per-instance, not shared across replicas.
type TokenBucketLimiter struct {
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

// NewTokenBucketLimiter builds a limiter allowing `burst` immediate requests
// per key, refilling at `ratePerSec` tokens/second.
func NewTokenBucketLimiter(ratePerSec, burst float64) *TokenBucketLimiter {
	return &TokenBucketLimiter{
		buckets:   make(map[string]*tokenBucket),
		rate:      ratePerSec,
		burst:     burst,
		lastSweep: time.Now(),
		idleTTL:   10 * time.Minute,
	}
}

// Allow reports whether a request from key is permitted at time now,
// consuming one token when it is.
func (l *TokenBucketLimiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.refillLocked(key, now)
	l.sweepLocked(now)

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// Spend consumes one token from key without checking the balance, so a
// caller can charge failures and let Exhausted gate the next attempt. The
// balance floors at -1 so a burst of failures cannot dig an unbounded hole.
func (l *TokenBucketLimiter) Spend(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()

	b := l.refillLocked(key, now)
	l.sweepLocked(now)
	b.tokens = math.Max(-1, b.tokens-1)
}

// Exhausted reports whether key has no tokens left at time now.
func (l *TokenBucketLimiter) Exhausted(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	return l.refillLocked(key, now).tokens < 1
}

// refillLocked returns key's bucket after crediting elapsed time. Caller must
// hold l.mu.
func (l *TokenBucketLimiter) refillLocked(key string, now time.Time) *tokenBucket {
	b := l.buckets[key]
	if b == nil {
		b = &tokenBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
		return b
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens = math.Min(l.burst, b.tokens+elapsed*l.rate)
		b.last = now
	}
	return b
}

// sweepLocked evicts idle buckets so memory stays bounded under a churn of
// distinct keys. A bucket is only dropped once it has been idle long enough
// to have refilled completely, so eviction never hands a depleted key a
// fresh burst early. Caller must hold l.mu.
func (l *TokenBucketLimiter) sweepLocked(now time.Time) {
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

// TrustedClientIP returns the best-effort client address for rate-limiting
// and audit logging. X-Forwarded-For is only consulted when the immediate
// peer is a private or loopback address, i.e. the ALB (or a local reverse
// proxy) that appends the address it accepted the connection from; then the
// last hop is the trustworthy one and earlier hops are whatever the caller
// sent. A peer reaching the listener directly gets keyed on its own
// RemoteAddr.
func TrustedClientIP(r *http.Request) string {
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
