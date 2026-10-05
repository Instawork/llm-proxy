package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/circuit"
	"github.com/Instawork/llm-proxy/internal/redact"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func emptyResp(status int) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(nil)),
	}
}

func ssnRedactor() *fakeRedactor {
	return &fakeRedactor{
		mutate: func(in string) (redact.Result, error) {
			out := strings.Replace(in, "222-33-4444", "[REDACTED:US_SSN]", 1)
			return redact.Result{Text: out, EntityCounts: map[string]int{"US_SSN": 1}}, nil
		},
	}
}

// Earlier middleware (model status / cost limit) caches the ORIGINAL body in
// req.GetBody. After wire-mode redaction swaps req.Body, GetBody must agree
// with it, otherwise GetBody-preferring readers see unredacted content.
func TestPIIRedactMiddleware_WireMode_UpdatesGetBody(t *testing.T) {
	original := `{"model":"gpt-4o","messages":[{"role":"user","content":"my ssn is 222-33-4444"}]}`

	var viaGetBody, viaBody []byte
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if r.GetBody == nil {
			t.Fatal("GetBody must be set after wire-mode redaction")
		}
		rc, err := r.GetBody()
		if err != nil {
			t.Fatal(err)
		}
		viaGetBody, _ = io.ReadAll(rc)
		viaBody, _ = io.ReadAll(r.Body)
	})

	mw := PIIRedactMiddleware(ssnRedactor(), PIIRedactConfig{GlobalEnabled: true, WirePlaceholders: true})(next)

	req := newReq(t, http.MethodPost, "/openai/v1/chat/completions", original)
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(original)), nil
	}
	mw.ServeHTTP(httptest.NewRecorder(), req)

	if !bytes.Equal(viaGetBody, viaBody) {
		t.Fatalf("GetBody and Body diverged after redaction:\n GetBody=%s\n Body=%s", viaGetBody, viaBody)
	}
	if strings.Contains(string(viaGetBody), "222-33-4444") {
		t.Fatalf("GetBody still returns the unredacted body: %s", viaGetBody)
	}
}

// End-to-end: the circuit transport replays req.GetBody on a transient retry.
// A stale GetBody would send the raw PII upstream on the second attempt.
func TestPIIRedactMiddleware_WireMode_CircuitRetryReplaysRedactedBody(t *testing.T) {
	original := `{"model":"gpt-4o","messages":[{"role":"user","content":"my ssn is 222-33-4444"}]}`

	var bodies [][]byte
	inner := rtFunc(func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		if len(bodies) == 1 {
			return emptyResp(http.StatusServiceUnavailable), nil
		}
		return emptyResp(http.StatusOK), nil
	})
	cfg := circuit.Config{
		Enabled:             true,
		Mode:                circuit.ModeEnforce,
		FailureThreshold:    3,
		WindowSeconds:       60,
		CooldownSeconds:     300,
		MaxTransientRetries: 1,
	}.Defaults()
	tr := circuit.NewTransport(inner, circuit.NewMemoryStore(cfg), cfg, "openai", nil)

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mirror what httputil.ReverseProxy does: the outbound request is a
		// clone, so it inherits GetBody from the inbound one.
		out := r.Clone(r.Context())
		resp, err := tr.RoundTrip(out)
		if err != nil {
			t.Fatalf("round trip: %v", err)
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
	})

	mw := PIIRedactMiddleware(ssnRedactor(), PIIRedactConfig{GlobalEnabled: true, WirePlaceholders: true})(next)

	req := newReq(t, http.MethodPost, "/openai/v1/chat/completions", original)
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(original)), nil
	}
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(bodies) != 2 {
		t.Fatalf("expected 2 upstream attempts, got %d", len(bodies))
	}
	for i, b := range bodies {
		if strings.Contains(string(b), "222-33-4444") {
			t.Fatalf("attempt %d sent raw PII upstream: %s", i+1, b)
		}
		if !strings.Contains(string(b), "[REDACTED:US_SSN]") {
			t.Fatalf("attempt %d did not carry the redacted body: %s", i+1, b)
		}
	}
	if !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("retry body differs from first attempt:\n first=%s\n retry=%s", bodies[0], bodies[1])
	}
}
