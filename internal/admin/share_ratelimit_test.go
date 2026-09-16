package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestShareRateLimiter_BurstThenBlock(t *testing.T) {
	l := newShareRateLimiter(1, 5)
	now := time.Now()

	for i := 0; i < 5; i++ {
		assert.True(t, l.allow("1.2.3.4", now), "request %d within burst should pass", i)
	}
	assert.False(t, l.allow("1.2.3.4", now), "request beyond burst should be blocked")
}

func TestShareRateLimiter_RefillsOverTime(t *testing.T) {
	l := newShareRateLimiter(1, 1)
	now := time.Now()

	assert.True(t, l.allow("1.2.3.4", now))
	assert.False(t, l.allow("1.2.3.4", now))
	// One token/sec → one second later exactly one more request is allowed.
	assert.True(t, l.allow("1.2.3.4", now.Add(time.Second)))
	assert.False(t, l.allow("1.2.3.4", now.Add(time.Second)))
}

func TestShareRateLimiter_IsolatesClients(t *testing.T) {
	l := newShareRateLimiter(1, 1)
	now := time.Now()

	assert.True(t, l.allow("1.1.1.1", now))
	assert.False(t, l.allow("1.1.1.1", now))
	// A different client has its own bucket.
	assert.True(t, l.allow("2.2.2.2", now))
}

func TestShareRateLimiter_Middleware429(t *testing.T) {
	l := newShareRateLimiter(1, 1)
	h := l.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/api/share/x", nil)
	req.RemoteAddr = "9.9.9.9:5555"
	h.ServeHTTP(first, req)
	assert.Equal(t, http.StatusOK, first.Code)

	second := httptest.NewRecorder()
	h.ServeHTTP(second, req)
	assert.Equal(t, http.StatusTooManyRequests, second.Code)
	assert.NotEmpty(t, second.Header().Get("Retry-After"))
}

// A caller cannot reset their bucket by rotating a forged first hop; the
// ALB-appended last hop is the key.
func TestShareRateLimiter_SpoofedForwardedForDoesNotBypass(t *testing.T) {
	l := newShareRateLimiter(1, 1)
	h := l.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	for i, forged := range []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"} {
		req := httptest.NewRequest(http.MethodGet, "/admin/api/share/x", nil)
		req.RemoteAddr = "10.0.0.1:5555"
		req.Header.Set("X-Forwarded-For", forged+", 203.0.113.7")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if i == 0 {
			assert.Equal(t, http.StatusOK, rec.Code)
		} else {
			assert.Equal(t, http.StatusTooManyRequests, rec.Code, "forged hop %s must not get a fresh bucket", forged)
		}
	}

	// Reaching the listener directly from a public address, the caller owns
	// the whole header, so it is ignored and the peer address is the key.
	for i, forged := range []string{"4.4.4.4", "5.5.5.5"} {
		req := httptest.NewRequest(http.MethodGet, "/admin/api/share/x", nil)
		req.RemoteAddr = "198.51.100.9:5555"
		req.Header.Set("X-Forwarded-For", forged)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if i == 0 {
			assert.Equal(t, http.StatusOK, rec.Code)
		} else {
			assert.Equal(t, http.StatusTooManyRequests, rec.Code, "direct peer must be keyed on RemoteAddr")
		}
	}
}

func TestClientIP(t *testing.T) {
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
			assert.Equal(t, tc.want, clientIP(req))
		})
	}
}
