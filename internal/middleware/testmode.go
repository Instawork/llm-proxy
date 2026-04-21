package middleware

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Instawork/llm-proxy/internal/circuit"
)

// TestModeMiddleware intercepts requests that carry the X-LLM-Proxy-Test-Mode
// header and returns synthetic responses without hitting real LLM providers.
//
// This is intended exclusively for integration tests.  It should be disabled
// in production via the TestModeEnabled config flag.
//
// Supported header values:
//   - force_degraded: immediately return a 503 with the MagicString degraded
//     error body, exactly as the circuit breaker would when the circuit is open.
//
// The force_transient_recover scenario is handled at the Transport level (see
// internal/circuit/transport.go) because it needs to interact with the
// internal retry loop.
func TestModeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mode := testModeValue(r)
		if mode == "" {
			next.ServeHTTP(w, r)
			return
		}

		// Promote query-param to header so the circuit Transport sees it too,
		// then strip the param from the URL before forwarding downstream.
		if r.URL.Query().Get(circuit.TestModeQueryParam) != "" {
			r = r.Clone(r.Context())
			r.Header.Set(circuit.TestModeHeader, mode)
			q := r.URL.Query()
			q.Del(circuit.TestModeQueryParam)
			r.URL.RawQuery = q.Encode()
		}

		switch mode {
		case circuit.TestModeForceDegraded:
			provider := providerFromRequest(r)
			writeDegradedResponse(w, provider)
			return

		default:
			// Unknown test mode — pass through so the transport can handle it.
			next.ServeHTTP(w, r)
		}
	})
}

// testModeValue returns the test mode string from the header or, as a fallback,
// the URL query parameter.  Header takes precedence.
func testModeValue(r *http.Request) string {
	if v := r.Header.Get(circuit.TestModeHeader); v != "" {
		return v
	}
	return r.URL.Query().Get(circuit.TestModeQueryParam)
}

// providerFromRequest extracts the provider name from the URL path.
// E.g. "/openai/v1/chat/completions" → "openai".
func providerFromRequest(r *http.Request) string {
	return circuit.ProviderFromPath(r.URL.Path)
}

// writeDegradedResponse writes a 503 JSON response containing MagicString.
func writeDegradedResponse(w http.ResponseWriter, provider string) {
	msg := fmt.Sprintf("%s Provider %s is currently degraded or unavailable. Please try again later.",
		circuit.MagicString, provider)

	body := map[string]interface{}{
		"error": map[string]interface{}{
			"message": msg,
			"type":    "provider_degraded",
			"code":    "provider_degraded",
		},
	}
	b, _ := json.Marshal(body)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Llm-Proxy-Error-Class", string(circuit.FailureClassDegraded))
	w.WriteHeader(http.StatusServiceUnavailable)
	w.Write(b) //nolint:errcheck
}
