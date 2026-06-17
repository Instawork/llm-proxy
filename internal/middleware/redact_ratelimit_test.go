package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/apikeys"
)

func TestRedactRateLimitMiddleware_ThrottlesPerKey(t *testing.T) {
	mw := RedactRateLimitMiddleware(2)
	called := 0
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
	}))

	key := apikeys.KeyPrefix + "hook"
	do := func() int {
		req := httptest.NewRequest(http.MethodPost, "/redact", nil)
		req.Header.Set("Authorization", "Bearer "+key)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := do(); code != http.StatusOK {
		t.Fatalf("first request status=%d", code)
	}
	if code := do(); code != http.StatusOK {
		t.Fatalf("second request status=%d", code)
	}
	if code := do(); code != http.StatusTooManyRequests {
		t.Fatalf("third request status=%d want 429", code)
	}
	if called != 2 {
		t.Fatalf("handler called %d times", called)
	}
}

func TestRedactRateLimitMiddleware_SkipsOtherRoutes(t *testing.T) {
	mw := RedactRateLimitMiddleware(1)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("request %d status=%d", i+1, rec.Code)
		}
	}
}
