package middleware

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/redact"
)

// requirePresidioForMiddleware mirrors internal/redact/integration_test.go's
// gating exactly. Duplicated rather than imported because moving the
// helper to a shared package would force callers in two different
// internal-only packages to depend on a third — not worth it for ~20
// lines of TCP dial.
func requirePresidioForMiddleware(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("Skipping --pii integration test in -short mode")
	}
	if os.Getenv("LLM_PROXY_PII_INTEGRATION") != "1" {
		t.Skip("Skipping --pii integration test; set LLM_PROXY_PII_INTEGRATION=1 to enable " +
			"(and `docker compose --profile pii_redact up -d presidio`)")
	}
	target := os.Getenv("PRESIDIO_ANALYZER_URL")
	if target == "" {
		target = "http://localhost:5004"
	}
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("PRESIDIO_ANALYZER_URL %q: invalid URL: %v", target, err)
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		host += ":80"
	}
	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		t.Skipf("Skipping --pii integration test; cannot reach Presidio at %s: %v",
			target, err)
	}
	_ = conn.Close()
	return target
}

// TestIntegration_PIIRedactMiddleware_EndToEnd is the headline test for
// the proxy's PII story:
//
//   - A request body carrying real PII enters the middleware.
//   - The upstream handler still receives the ORIGINAL body verbatim
//     (model quality preserved).
//   - The redacted copy is in context, available to downstream
//     transports / loggers — and crucially, the raw values do not
//     appear in it.
//
// If this test passes against a live sidecar, the wire contract between
// the proxy and Presidio is provably correct.
func TestIntegration_PIIRedactMiddleware_EndToEnd(t *testing.T) {
	analyzerURL := requirePresidioForMiddleware(t)

	redactor, err := redact.New(redact.Config{
		AnalyzerURL: analyzerURL,
		Timeout:     10 * time.Second,
		EntityTypes: redact.DefaultEntityTypes,
	})
	if err != nil {
		t.Fatalf("redact.New: %v", err)
	}

	originalBody := `{"messages":[{"role":"user","content":"my ssn is 222-33-4444 and email alice@example.com"}]}`

	var (
		upstreamBody []byte
		ctxFromNext  context.Context
	)
	next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r.Body)
		upstreamBody = buf.Bytes()
		ctxFromNext = r.Context()
	})

	handler := PIIRedactMiddleware(redactor, PIIRedactConfig{GlobalEnabled: true})(next)

	req := httptest.NewRequest(http.MethodPost,
		"/openai/v1/chat/completions",
		bytes.NewBufferString(originalBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("middleware returned %d", rec.Code)
	}
	if string(upstreamBody) != originalBody {
		t.Fatalf("upstream MUST see the original body verbatim — model quality depends on it.\n got %q\nwant %q",
			upstreamBody, originalBody)
	}

	redacted, ok := PIIRedactedBody(ctxFromNext)
	if !ok {
		t.Fatal("redacted body missing from request context")
	}
	for _, marker := range []string{"[REDACTED:US_SSN]", "[REDACTED:EMAIL_ADDRESS]"} {
		if !strings.Contains(string(redacted), marker) {
			t.Errorf("expected %q in redacted body, got %q", marker, string(redacted))
		}
	}
	for _, leak := range []string{"222-33-4444", "alice@example.com"} {
		if strings.Contains(string(redacted), leak) {
			t.Errorf("raw value %q leaked into redacted body: %q", leak, string(redacted))
		}
	}
}

// TestIntegration_PIIRedactMiddleware_FailOpenAgainstUnreachable
// pretends the sidecar is unreachable by pointing the redactor at a
// dead port. Even with a live Presidio in the test environment, this
// case still must hold: the middleware's fail-open contract is what
// stops a degraded sidecar from cascading 503s into user requests.
//
// Important: this test does NOT require Presidio itself to be running,
// since the whole point is what happens when it ISN'T. We still gate
// on LLM_PROXY_PII_INTEGRATION because adding a 100ms TCP-refused
// detour to every `go test ./...` run would be unfriendly.
func TestIntegration_PIIRedactMiddleware_FailOpenAgainstUnreachable(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping --pii integration test in -short mode")
	}
	if os.Getenv("LLM_PROXY_PII_INTEGRATION") != "1" {
		t.Skip("Skipping --pii integration test; set LLM_PROXY_PII_INTEGRATION=1 to enable")
	}

	// Guaranteed-unreachable: high port on loopback that nothing should
	// be listening on. Connection-refused returns immediately so the
	// test stays fast even though we're going through an HTTP client.
	redactor, err := redact.New(redact.Config{
		AnalyzerURL: "http://127.0.0.1:1",
		Timeout:     200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("redact.New: %v", err)
	}

	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	})
	handler := PIIRedactMiddleware(redactor, PIIRedactConfig{GlobalEnabled: true, FailClosed: false})(next)

	req := httptest.NewRequest(http.MethodPost,
		"/openai/v1/chat/completions",
		bytes.NewBufferString(`{"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("fail-open must return upstream status (200); got %d", rec.Code)
	}
	if !called {
		t.Error("fail-open must still invoke upstream handler")
	}
}

// TestIntegration_PIIRedactMiddleware_FailClosedAgainstUnreachable is
// the negative companion of the above: with FailClosed=true and a dead
// sidecar, the middleware MUST 503 and never invoke upstream. This is
// the operational lever for "I'd rather drop this request than persist
// a potentially-PII log line".
func TestIntegration_PIIRedactMiddleware_FailClosedAgainstUnreachable(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping --pii integration test in -short mode")
	}
	if os.Getenv("LLM_PROXY_PII_INTEGRATION") != "1" {
		t.Skip("Skipping --pii integration test; set LLM_PROXY_PII_INTEGRATION=1 to enable")
	}

	redactor, err := redact.New(redact.Config{
		AnalyzerURL: "http://127.0.0.1:1",
		Timeout:     200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("redact.New: %v", err)
	}

	called := false
	next := http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		called = true
	})
	handler := PIIRedactMiddleware(redactor, PIIRedactConfig{GlobalEnabled: true, FailClosed: true})(next)

	req := httptest.NewRequest(http.MethodPost,
		"/openai/v1/chat/completions",
		bytes.NewBufferString(`{"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("fail-closed must return 503; got %d", rec.Code)
	}
	if called {
		t.Error("fail-closed must NOT invoke upstream handler")
	}
}
