package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/redact"
)

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

func newLiveRedactor(t *testing.T, analyzerURL string, entityTypes []string) *redact.Redactor {
	t.Helper()
	redactor, err := redact.New(redact.Config{
		AnalyzerURL: analyzerURL,
		Timeout:     10 * time.Second,
		EntityTypes: entityTypes,
	})
	if err != nil {
		t.Fatalf("redact.New: %v", err)
	}
	return redactor
}

// TestIntegration_PIIRedactMiddleware_LegacyObservabilityMode verifies the
// pre-wire contract: upstream still sees the original body while the
// redacted copy is stashed for logs/cost transports.
func TestIntegration_PIIRedactMiddleware_LegacyObservabilityMode(t *testing.T) {
	analyzerURL := requirePresidioForMiddleware(t)
	redactor := newLiveRedactor(t, analyzerURL, redact.DefaultEntityTypes)

	originalBody := `{"messages":[{"role":"user","content":"my ssn is 222-33-4444 and email alice@gmail.com"}]}`

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
		t.Fatalf("upstream MUST see the original body in observability mode.\n got %q\nwant %q",
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
	for _, leak := range []string{"222-33-4444", "alice@gmail.com"} {
		if strings.Contains(string(redacted), leak) {
			t.Errorf("raw value %q leaked into redacted body: %q", leak, string(redacted))
		}
	}
}

// TestIntegration_PIIWireMode_ScrubAndRestore_EndToEnd is the headline
// wire-mode test: Presidio scrubs the outbound body, a mock upstream
// echoes the scrubbed user content, and the response restore middleware
// round-trips MASK placeholders back to the client while SEAL values stay
// opaque.
func TestIntegration_PIIWireMode_ScrubAndRestore_EndToEnd(t *testing.T) {
	analyzerURL := requirePresidioForMiddleware(t)
	redactor := newLiveRedactor(t, analyzerURL, []string{"US_SSN", "EMAIL_ADDRESS"})

	const ssn = "222-33-4444"
	const email = "alice@gmail.com"
	originalBody := `{"messages":[{"role":"user","content":"my ssn is ` + ssn + ` and email ` + email + `"}]}`

	var upstreamBody []byte
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		echo := userContentFromChatBody(upstreamBody)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"content": echo}},
			},
		})
	})

	pm := providers.NewProviderManager()
	stack := PIIRedactMiddleware(redactor, PIIRedactConfig{
		GlobalEnabled:    true,
		WirePlaceholders: true,
	})(PIIResponseRestoreMiddleware(pm)(next))

	req := httptest.NewRequest(http.MethodPost,
		"/openai/v1/chat/completions",
		bytes.NewBufferString(originalBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	stack.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("stack returned %d: %s", rec.Code, rec.Body.String())
	}

	up := string(upstreamBody)
	if strings.Contains(up, ssn) {
		t.Errorf("upstream saw raw SSN: %q", up)
	}
	if strings.Contains(up, email) {
		t.Errorf("upstream saw raw email: %q", up)
	}
	if !strings.Contains(up, "<PII_US_SSN_") {
		t.Errorf("upstream missing SSN placeholder: %q", up)
	}
	if !strings.Contains(up, "<PII_EMAIL_ADDRESS_") {
		t.Errorf("upstream missing email placeholder: %q", up)
	}

	respBody := rec.Body.String()
	if !strings.Contains(respBody, email) {
		t.Errorf("client response missing restored MASK email: %q", respBody)
	}
	if strings.Contains(respBody, ssn) {
		t.Errorf("client response leaked raw SEAL SSN: %q", respBody)
	}
	if !strings.Contains(respBody, "<PII_US_SSN_") && !strings.Contains(respBody, `\u003cPII_US_SSN_`) {
		t.Errorf("client response should keep SEAL placeholder opaque: %q", respBody)
	}
	if got := piiMetricFromResponse(rec, "X-LLM-PII-Leaked"); got != "0" {
		t.Errorf("client response MASK placeholder leaked (X-LLM-PII-Leaked=%q): %q", got, respBody)
	}
}

// TestIntegration_PIIWireMode_AnthropicScrubAndRestore verifies MASK-tier
// wire restore through the production middleware stack on /anthropic/v1/messages.
func TestIntegration_PIIWireMode_AnthropicScrubAndRestore_EndToEnd(t *testing.T) {
	testIntegrationPIIWireModeScrubAndRestore(t, wireStackAnthropicCase())
}

func TestIntegration_PIIWireMode_GeminiScrubAndRestore_EndToEnd(t *testing.T) {
	testIntegrationPIIWireModeScrubAndRestore(t, wireStackGeminiCase())
}

func testIntegrationPIIWireModeScrubAndRestore(t *testing.T, tc wireStackProviderCase) {
	t.Helper()
	analyzerURL := requirePresidioForMiddleware(t)
	redactor := newLiveRedactor(t, analyzerURL, []string{"EMAIL_ADDRESS"})

	const email = "alice@gmail.com"
	originalBody := tc.requestBody(email)

	var upstreamBody []byte
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		echo := tc.upstreamText(upstreamBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tc.responseBody(echo)))
	})

	pm := wireTestProviderManager(t)
	stack := PIIRedactMiddleware(redactor, PIIRedactConfig{
		GlobalEnabled:    true,
		WirePlaceholders: true,
	})(productionPIIWireStack(pm, next))

	req := httptest.NewRequest(http.MethodPost, tc.path, bytes.NewBufferString(originalBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("%s stack returned %d: %s", tc.name, rec.Code, rec.Body.String())
	}

	up := string(upstreamBody)
	if strings.Contains(up, email) {
		t.Errorf("%s upstream saw raw email: %q", tc.name, up)
	}
	if !strings.Contains(up, "<PII_EMAIL_ADDRESS_") {
		t.Errorf("%s upstream missing email placeholder: %q", tc.name, up)
	}

	respBody := rec.Body.String()
	if !strings.Contains(respBody, email) {
		t.Errorf("%s client response missing restored MASK email: %q", tc.name, respBody)
	}
	if got := piiMetricFromResponse(rec, "X-LLM-PII-Leaked"); got != "0" {
		t.Errorf("%s client response MASK placeholder leaked (X-LLM-PII-Leaked=%q): %q", tc.name, got, respBody)
	}
}

// TestIntegration_PIIRedactMiddleware_FailOpenAgainstUnreachable pretends the sidecar is unreachable by pointing the redactor at a
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
