package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestTokenBucketLimiter_BurstThenBlock(t *testing.T) {
	l := NewTokenBucketLimiter(1, 5)
	now := time.Now()

	for i := 0; i < 5; i++ {
		assert.True(t, l.Allow("1.2.3.4", now), "request %d within burst should pass", i)
	}
	assert.False(t, l.Allow("1.2.3.4", now), "request beyond burst should be blocked")
}

func TestTokenBucketLimiter_RefillsOverTime(t *testing.T) {
	l := NewTokenBucketLimiter(1, 1)
	now := time.Now()

	assert.True(t, l.Allow("1.2.3.4", now))
	assert.False(t, l.Allow("1.2.3.4", now))
	// One token/sec → one second later exactly one more request is allowed.
	assert.True(t, l.Allow("1.2.3.4", now.Add(time.Second)))
	assert.False(t, l.Allow("1.2.3.4", now.Add(time.Second)))
}

func TestTokenBucketLimiter_IsolatesClients(t *testing.T) {
	l := NewTokenBucketLimiter(1, 1)
	now := time.Now()

	assert.True(t, l.Allow("1.1.1.1", now))
	assert.False(t, l.Allow("1.1.1.1", now))
	// A different client has its own bucket.
	assert.True(t, l.Allow("2.2.2.2", now))
}

// The idle sweep must not turn a slow-refilling bucket back into a full one:
// a depleted key stays depleted until its tokens would have refilled anyway.
func TestTokenBucketLimiter_SweepKeepsDepletedBuckets(t *testing.T) {
	l := NewTokenBucketLimiter(1.0/3600, 2) // one token per hour
	now := time.Now()

	assert.True(t, l.Allow("owner", now))
	assert.True(t, l.Allow("owner", now))
	assert.False(t, l.Allow("owner", now))

	// Well past idleTTL, another key triggers a sweep.
	later := now.Add(l.idleTTL + time.Hour)
	assert.True(t, l.Allow("someone-else", later))
	_, kept := l.buckets["owner"]
	assert.True(t, kept, "depleted bucket must survive the sweep")

	// Only one token has refilled in that time, not a fresh burst of two.
	assert.True(t, l.Allow("owner", later))
	assert.False(t, l.Allow("owner", later))

	// Once idle long enough to be full again, eviction is indistinguishable
	// from keeping it, so the sweep may drop it.
	full := later.Add(3 * time.Hour)
	assert.True(t, l.Allow("someone-else", full))
	_, kept = l.buckets["owner"]
	assert.False(t, kept, "refilled bucket should be evicted")
}

// Spend charges failures without gating; Exhausted then blocks until refill,
// and the debt floors at one token so a long failure streak recovers in
// bounded time.
func TestTokenBucketLimiter_SpendAndExhausted(t *testing.T) {
	l := NewTokenBucketLimiter(1, 2)
	now := time.Now()

	assert.False(t, l.Exhausted("ip", now))
	l.Spend("ip", now)
	l.Spend("ip", now)
	assert.True(t, l.Exhausted("ip", now))

	for i := 0; i < 100; i++ {
		l.Spend("ip", now)
	}
	// Two seconds refill from the -1 floor to 1 token: unblocked again.
	assert.True(t, l.Exhausted("ip", now.Add(time.Second)))
	assert.False(t, l.Exhausted("ip", now.Add(2*time.Second)))
}

func TestTrustedClientIP(t *testing.T) {
	tests := []struct {
		name   string
		xff    string
		remote string
		want   string
	}{
		{"xff single", "203.0.113.7", "10.0.0.1:1234", "203.0.113.7"},
		// The ALB appends the peer it accepted from; anything before it is caller-supplied.
		{"xff last hop wins", "1.1.1.1, 203.0.113.7", "10.0.0.1:1234", "203.0.113.7"},
		{"xff spoofed prefix ignored", "spoofed, 8.8.8.8, 203.0.113.7", "10.0.0.1:1234", "203.0.113.7"},
		{"xff via loopback proxy", "203.0.113.7", "127.0.0.1:4000", "203.0.113.7"},
		{"xff via docker bridge", "203.0.113.7", "172.18.0.1:4000", "203.0.113.7"},
		{"xff trailing comma falls back", "203.0.113.7,", "10.0.0.1:9999", "10.0.0.1"},
		// A public peer is not a trusted proxy: its header is ignored entirely.
		{"xff from public peer ignored", "1.1.1.1, 2.2.2.2", "198.51.100.9:9999", "198.51.100.9"},
		{"remote addr fallback", "", "192.0.2.5:9999", "192.0.2.5"},
		{"remote addr ipv6", "", "[2001:db8::1]:9999", "2001:db8::1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remote
			if tc.xff != "" {
				req.Header.Set("X-Forwarded-For", tc.xff)
			}
			assert.Equal(t, tc.want, TrustedClientIP(req))
		})
	}
}
