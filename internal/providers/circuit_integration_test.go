package providers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/circuit"
)

// roundTripFunc lets tests build a RoundTripper from a plain func.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// upstreamServer creates a test HTTP server that returns the given status on
// every request.  The caller is responsible for calling server.Close().
func upstreamServer(status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
}

// wrapProviderTransport injects a circuit-breaking transport into provider p.
func wrapProviderTransport(
	p interface {
		WrapTransport(func(http.RoundTripper) http.RoundTripper)
	},
	store circuit.Store,
	cfg circuit.Config,
	name string,
) {
	p.WrapTransport(func(inner http.RoundTripper) http.RoundTripper {
		return circuit.NewTransport(inner, store, cfg, name, nil)
	})
}

// TestProviderCircuitBreaker_OpenAI_DegradedSignal ensures that after
// MaxTransientRetries+1 consecutive 503 responses the circuit-breaking
// transport returns a 503 containing the DefaultDegradedSignal to the upstream caller.
func TestProviderCircuitBreaker_OpenAI_DegradedSignal(t *testing.T) {
	server := upstreamServer(503)
	defer server.Close()

	cfg := circuit.Config{
		Enabled:             true,
		FailureThreshold:    10, // high so the circuit doesn't open yet
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 1, // one retry then give up
	}.Defaults()
	store := circuit.NewMemoryStore(cfg)

	provider := NewOpenAIProxy()
	wrapProviderTransport(provider, store, cfg, "openai")

	// Re-point the proxy's transport target at our test server.
	// The circuit transport wraps the existing transport, which would normally
	// point at OpenAI.  We use the test server instead by creating a request
	// directly against the circuit transport.
	innerTransport := &http.Transport{}
	cbTransport := circuit.NewTransport(innerTransport, store, cfg, "openai", nil)

	req, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := cbTransport.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503 degraded response, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), circuit.DefaultDegradedSignal) {
		t.Fatalf("DefaultDegradedSignal not found in body: %s", body)
	}
}

// TestProviderCircuitBreaker_TestMode_ForceDegraded verifies that the
// test-mode header causes an immediate degraded response without contacting
// the provider.
func TestProviderCircuitBreaker_TestMode_ForceDegraded(t *testing.T) {
	// If the inner transport is called at all, the test fails.
	callCount := 0
	inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		callCount++
		return &http.Response{
			StatusCode: 200,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Header:     make(http.Header),
		}, nil
	})

	cfg := circuit.Config{Enabled: true}.Defaults()
	store := circuit.NewMemoryStore(cfg)
	tr := circuit.NewTransport(inner, store, cfg, "anthropic", nil)

	req, _ := http.NewRequest(http.MethodPost,
		"http://proxy/anthropic/v1/messages",
		strings.NewReader(`{}`))
	req.Header.Set(circuit.TestModeHeader, circuit.TestModeForceDegraded)

	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if callCount != 0 {
		t.Fatalf("inner transport should not be called in force_degraded mode, called %d times", callCount)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), circuit.DefaultDegradedSignal) {
		t.Fatalf("DefaultDegradedSignal not in body: %s", body)
	}
}
