package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/circuit"
)

// newTestMiddleware builds a TestModeMiddleware with the default signal,
// matching the behaviour of the old package-level function.
func newTestMiddleware() func(http.Handler) http.Handler {
	return NewTestModeMiddleware(circuit.DefaultDegradedSignal)
}

func TestTestModeMiddleware_ForceDegraded(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := newTestMiddleware()(next)

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req.Header.Set(circuit.TestModeHeader, circuit.TestModeForceDegraded)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if called {
		t.Fatal("next handler should NOT be called for force_degraded")
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), circuit.DefaultDegradedSignal) {
		t.Fatalf("DefaultDegradedSignal not found in response body: %s", body)
	}
	// Verify valid JSON.
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("body is not valid JSON: %v\nbody: %s", err, body)
	}
	errObj, _ := payload["error"].(map[string]interface{})
	if errObj == nil {
		t.Fatal("expected 'error' key in JSON")
	}
}

func TestTestModeMiddleware_NoHeader_PassThrough(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := newTestMiddleware()(next)

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if !called {
		t.Fatal("next handler should be called when no test-mode header")
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rr.Code)
	}
}

func TestTestModeMiddleware_UnknownMode_PassThrough(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := newTestMiddleware()(next)

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	req.Header.Set(circuit.TestModeHeader, "unknown_mode")
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if !called {
		t.Fatal("next handler should be called for unknown test mode")
	}
}

func TestTestModeMiddleware_QueryParam_ForceDegraded(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := newTestMiddleware()(next)

	req := httptest.NewRequest(http.MethodPost,
		"/gemini/v1beta/models/gemini-2.5-flash:generateContent?llm_proxy_test_mode=force_degraded", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if called {
		t.Fatal("next handler should NOT be called for force_degraded via query param")
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), circuit.DefaultDegradedSignal) {
		t.Fatalf("DefaultDegradedSignal not found in query-param response body: %s", body)
	}
}

func TestTestModeMiddleware_QueryParam_StrippedFromURL(t *testing.T) {
	var forwardedURL string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwardedURL = r.URL.String()
		w.WriteHeader(http.StatusOK)
	})

	handler := newTestMiddleware()(next)

	req := httptest.NewRequest(http.MethodPost,
		"/openai/v1/chat/completions?llm_proxy_test_mode=force_transient_recover&other=123", nil)
	rr := httptest.NewRecorder()

	handler.ServeHTTP(rr, req)

	if strings.Contains(forwardedURL, "llm_proxy_test_mode") {
		t.Fatalf("query param should be stripped from forwarded URL, got: %s", forwardedURL)
	}
	if !strings.Contains(forwardedURL, "other=123") {
		t.Fatalf("other query params should be preserved, got: %s", forwardedURL)
	}
}

func TestTestModeMiddleware_ForceDegraded_SetsErrorClassHeader(t *testing.T) {
	handler := newTestMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req.Header.Set(circuit.TestModeHeader, circuit.TestModeForceDegraded)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("X-Llm-Proxy-Error-Class"); got != string(circuit.FailureClassDegraded) {
		t.Fatalf("want X-Llm-Proxy-Error-Class=%q, got %q", circuit.FailureClassDegraded, got)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("want Content-Type application/json, got %q", got)
	}
}

// TestTestModeMiddleware_HeaderTakesPrecedenceOverQueryParam ensures that when
// both the header and query param are set, the header value wins.  This matters
// because the circuit Transport reads the header and we don't want query-param
// typos to silently override an explicit header value.
func TestTestModeMiddleware_HeaderTakesPrecedenceOverQueryParam(t *testing.T) {
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	handler := newTestMiddleware()(next)

	req := httptest.NewRequest(http.MethodPost,
		"/openai/v1/chat/completions?llm_proxy_test_mode=unknown_mode", nil)
	req.Header.Set(circuit.TestModeHeader, circuit.TestModeForceDegraded)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if called {
		t.Fatal("header force_degraded should take precedence over query param unknown_mode")
	}
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 from header-driven force_degraded, got %d", rr.Code)
	}
}

// TestTestModeMiddleware_QueryParamPromotedToHeader ensures that a
// query-param-driven test mode is forwarded downstream as a header so the
// circuit Transport (which only reads the header) can also see it when the
// middleware forwards transient-recover.
func TestTestModeMiddleware_QueryParamPromotedToHeader(t *testing.T) {
	var seenHeader string
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenHeader = r.Header.Get(circuit.TestModeHeader)
		w.WriteHeader(http.StatusOK)
	})

	handler := newTestMiddleware()(next)

	req := httptest.NewRequest(http.MethodPost,
		"/openai/v1/chat/completions?llm_proxy_test_mode=force_transient_recover", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if seenHeader != circuit.TestModeForceTransientRecover {
		t.Fatalf("query-param test mode should be promoted to header, got header=%q", seenHeader)
	}
}

func TestTestModeMiddleware_ForceDegraded_ContainsProvider(t *testing.T) {
	handler := newTestMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	for _, path := range []string{
		"/openai/v1/chat/completions",
		"/anthropic/v1/messages",
		"/gemini/v1beta/models/gemini-2.5-flash:generateContent",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		req.Header.Set(circuit.TestModeHeader, circuit.TestModeForceDegraded)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		body, _ := io.ReadAll(rr.Body)
		if rr.Code != http.StatusServiceUnavailable {
			t.Errorf("path %s: want 503, got %d", path, rr.Code)
		}
		if !strings.Contains(string(body), circuit.DefaultDegradedSignal) {
			t.Errorf("path %s: DefaultDegradedSignal not in body: %s", path, body)
		}
	}
}
