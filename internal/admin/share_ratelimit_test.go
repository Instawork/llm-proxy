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

func TestClientIP(t *testing.T) {
	tests := []struct {
		name   string
		xff    string
		remote string
		want   string
	}{
		{"xff single", "203.0.113.7", "10.0.0.1:1234", "203.0.113.7"},
		{"xff first hop", "203.0.113.7, 10.0.0.1", "10.0.0.1:1234", "203.0.113.7"},
		{"remote addr fallback", "", "192.0.2.5:9999", "192.0.2.5"},
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
