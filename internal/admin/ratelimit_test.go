package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/Instawork/llm-proxy/internal/middleware"
)

func TestIPLimitMiddleware429(t *testing.T) {
	h := ipLimitMiddleware(middleware.NewTokenBucketLimiter(1, 1))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
func TestIPLimitMiddleware_SpoofedForwardedForDoesNotBypass(t *testing.T) {
	h := ipLimitMiddleware(middleware.NewTokenBucketLimiter(1, 1))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
