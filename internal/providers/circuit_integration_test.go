package providers

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Instawork/llm-proxy/internal/circuit"
)

// roundTripFunc lets tests build a RoundTripper from a plain func.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestProviderCircuitBreaker_OpenAI_DegradedSignal ensures that after
// MaxTransientRetries+1 consecutive 503 responses a real request routed
// through OpenAIProxy.Proxy() receives a synthesised 503 containing
// DefaultDegradedSignal in its body.
//
// This exercises the actual provider wiring: WrapTransport → circuit
// transport → inner (pointed at the test server) → reverse proxy → client.
// The previous version bypassed WrapTransport entirely by invoking
// circuit.NewTransport directly, which meant a regression that broke the
// provider-level wrapping would not be caught.
func TestProviderCircuitBreaker_OpenAI_DegradedSignal(t *testing.T) {
	var upstreamCalls int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&upstreamCalls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	cfg := circuit.Config{
		Enabled:             true,
		Mode:                circuit.ModeEnforce,
		FailureThreshold:    10, // high so the circuit doesn't open yet
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 1, // one retry then give up
	}.Defaults()
	store := circuit.NewMemoryStore(cfg)

	provider := NewOpenAIProxy()

	// Install the circuit-breaking transport via the provider's own
	// WrapTransport hook — this is the production code path.  The inner
	// RoundTripper rewrites the request URL to target the test upstream
	// in place of api.openai.com, so the reverse proxy never actually
	// reaches the internet.
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatalf("parse upstream URL: %v", err)
	}
	provider.WrapTransport(func(_ http.RoundTripper) http.RoundTripper {
		inner := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			r.URL.Scheme = upstreamURL.Scheme
			r.URL.Host = upstreamURL.Host
			r.Host = upstreamURL.Host
			return http.DefaultTransport.RoundTrip(r)
		})
		return circuit.NewTransport(inner, store, cfg, "openai", nil)
	})

	// Front the provider with a local test server so we can issue a real
	// client request against the wired-up proxy.Handler.
	front := httptest.NewServer(provider.Proxy())
	defer front.Close()

	req, err := http.NewRequest(http.MethodPost,
		front.URL+"/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","messages":[]}`))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("want 503 degraded response, got %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), circuit.DefaultDegradedSignal) {
		t.Fatalf("DefaultDegradedSignal not found in body: %s", body)
	}

	// Retry budget is one, so we expect initial attempt + one retry before
	// giving up — the upstream must have been hit exactly twice.  If the
	// wrapping were not applied (regression) this would be 1.
	if got := atomic.LoadInt32(&upstreamCalls); got != 2 {
		t.Fatalf("expected upstream to be called twice (attempt + 1 retry), got %d", got)
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

	// Explicitly opt-in to honouring the X-LLM-Proxy-Test-Mode header;
	// the transport refuses to interpret it in the default (prod-safe)
	// configuration.  See TestTransport_TestMode_DisabledByDefault_*.
	cfg := circuit.Config{Enabled: true, Mode: circuit.ModeEnforce, TestModeEnabled: true}.Defaults()
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
