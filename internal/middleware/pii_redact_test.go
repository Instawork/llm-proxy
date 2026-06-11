package middleware

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/redact"
)

// fakeRedactor implements PIIRedactor without an HTTP round trip — keeps
// the middleware tests focused on body capture, context plumbing, and
// fail-mode branching.
type fakeRedactor struct {
	called int
	mutate func(in string) (redact.Result, error)
}

func (f *fakeRedactor) Redact(_ context.Context, text string) (redact.Result, error) {
	f.called++
	if f.mutate != nil {
		return f.mutate(text)
	}
	return redact.Result{Text: text, EntityCounts: map[string]int{}}, nil
}

func newReq(t *testing.T, method, path, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

// captureHandler is the chained "next" handler for middleware tests. It
// reads the body so we can assert that the upstream still receives the
// ORIGINAL (unredacted) payload, and stashes the request for context
// inspection.
type captureHandler struct {
	bodySeen []byte
	reqSeen  *http.Request
}

func (c *captureHandler) ServeHTTP(_ http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	c.bodySeen = b
	c.reqSeen = r
}

func TestPIIRedactMiddleware_NonProviderRouteSkipped(t *testing.T) {
	r := &fakeRedactor{}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{})(cap)

	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/health", "{}"))

	if r.called != 0 {
		t.Errorf("redactor called for /health (count=%d)", r.called)
	}
}

func TestPIIRedactMiddleware_GETSkipped(t *testing.T) {
	r := &fakeRedactor{}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{})(cap)

	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodGet, "/openai/v1/chat/completions", ""))

	if r.called != 0 {
		t.Errorf("redactor called for GET request (count=%d)", r.called)
	}
}

func TestPIIRedactMiddleware_ModelsListSkipped(t *testing.T) {
	r := &fakeRedactor{}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{})(cap)

	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/models", "{}"))

	if r.called != 0 {
		t.Errorf("redactor called for /models route (count=%d)", r.called)
	}
}

func TestPIIRedactMiddleware_RedactsAndStashesInContext(t *testing.T) {
	original := `{"messages":[{"role":"user","content":"my ssn is 222-33-4444"}]}`
	r := &fakeRedactor{
		mutate: func(in string) (redact.Result, error) {
			out := strings.Replace(in, "222-33-4444", "[REDACTED:US_SSN]", 1)
			return redact.Result{
				Text:         out,
				EntityCounts: map[string]int{"US_SSN": 1},
			}, nil
		},
	}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{})(cap)

	mw.ServeHTTP(
		httptest.NewRecorder(),
		newReq(t, http.MethodPost, "/openai/v1/chat/completions", original),
	)

	if r.called != 1 {
		t.Fatalf("expected redactor called once, got %d", r.called)
	}
	if string(cap.bodySeen) != original {
		t.Errorf("upstream saw redacted body — must see original.\n got %q\nwant %q",
			cap.bodySeen, original)
	}
	red, ok := PIIRedactedBody(cap.reqSeen.Context())
	if !ok {
		t.Fatal("redacted body not stashed in context")
	}
	if !strings.Contains(string(red), "[REDACTED:US_SSN]") {
		t.Errorf("redacted body missing marker: %s", string(red))
	}
	if strings.Contains(string(red), "222-33-4444") {
		t.Errorf("raw ssn leaked into redacted body: %s", string(red))
	}
}

func TestPIIRedactMiddleware_EmptyBodyShortCircuits(t *testing.T) {
	r := &fakeRedactor{}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{})(cap)

	mw.ServeHTTP(
		httptest.NewRecorder(),
		newReq(t, http.MethodPost, "/openai/v1/chat/completions", ""),
	)

	if r.called != 0 {
		t.Errorf("redactor called for empty body (count=%d)", r.called)
	}
	if _, ok := PIIRedactedBody(cap.reqSeen.Context()); ok {
		t.Error("empty body must not stash a value in context")
	}
}

func TestPIIRedactMiddleware_FailOpenPassesThroughOnError(t *testing.T) {
	r := &fakeRedactor{
		mutate: func(_ string) (redact.Result, error) {
			return redact.Result{}, errors.New("sidecar down")
		},
	}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{FailClosed: false})(cap)

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, newReq(t, http.MethodPost, "/openai/v1/chat/completions", `{"x":1}`))

	if rec.Code != http.StatusOK {
		t.Errorf("fail-open expected 200 to upstream, got %d", rec.Code)
	}
	if cap.reqSeen == nil {
		t.Fatal("upstream handler was not called in fail-open mode")
	}
	if _, ok := PIIRedactedBody(cap.reqSeen.Context()); ok {
		t.Error("fail-open must NOT stash a redacted body — context must remain absent so callers can detect the gap")
	}
	if string(cap.bodySeen) != `{"x":1}` {
		t.Errorf("upstream body altered in fail-open: %q", cap.bodySeen)
	}
}

func TestPIIRedactMiddleware_FailClosedReturns503OnError(t *testing.T) {
	r := &fakeRedactor{
		mutate: func(_ string) (redact.Result, error) {
			return redact.Result{}, errors.New("sidecar down")
		},
	}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{FailClosed: true})(cap)

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, newReq(t, http.MethodPost, "/openai/v1/chat/completions", `{"x":1}`))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("fail-closed expected 503, got %d", rec.Code)
	}
	if cap.reqSeen != nil {
		t.Error("fail-closed must NOT call upstream on redactor failure")
	}
}

// captureLogger returns a slog.Logger that writes JSON records into
// the supplied buffer so a test can assert on the structured fields
// (provider, body_bytes, max_body_bytes) without parsing free-form text.
func captureLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func TestPIIRedactMiddleware_OversizeBodySkipped(t *testing.T) {
	big := strings.Repeat("a", 5000)
	r := &fakeRedactor{}
	cap := &captureHandler{}
	var logBuf bytes.Buffer
	mw := PIIRedactMiddleware(r, PIIRedactConfig{
		MaxBodyBytes: 1024,
		Logger:       captureLogger(&logBuf),
	})(cap)

	mw.ServeHTTP(
		httptest.NewRecorder(),
		newReq(t, http.MethodPost, "/openai/v1/chat/completions", big),
	)

	if r.called != 0 {
		t.Errorf("oversize body should skip redactor (count=%d)", r.called)
	}
	if string(cap.bodySeen) != big {
		t.Error("oversize body must still reach upstream intact")
	}
	if _, ok := PIIRedactedBody(cap.reqSeen.Context()); ok {
		t.Error("oversize body must NOT stash redacted copy")
	}
	// Operators rely on the WARN line being parseable: provider,
	// body_bytes and max_body_bytes are the three knobs they alert
	// on. If any go missing, dashboards silently lose signal.
	logOut := logBuf.String()
	for _, want := range []string{
		`"msg":"pii_redact: body exceeds max_body_bytes; skipping"`,
		`"provider":"openai"`,
		`"body_bytes":5000`,
		`"max_body_bytes":1024`,
	} {
		if !strings.Contains(logOut, want) {
			t.Errorf("oversize WARN missing %q\nfull log: %s", want, logOut)
		}
	}
}

// TestPIIRedactMiddleware_DefaultMaxBodyBytesIs1MiB locks in the
// "generous default" promise documented in PIIRedactConfig: leaving
// MaxBodyBytes at zero should accept up to 1 MiB without hitting
// the cap. A regression here would silently start dropping vision
// payloads from redaction in production.
func TestPIIRedactMiddleware_DefaultMaxBodyBytesIs1MiB(t *testing.T) {
	// 1 MiB minus a few bytes for the JSON envelope.
	body := `{"x":"` + strings.Repeat("a", 1024*1024-16) + `"}`
	r := &fakeRedactor{}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{ /* MaxBodyBytes: 0 → 1 MiB default */ })(cap)

	mw.ServeHTTP(
		httptest.NewRecorder(),
		newReq(t, http.MethodPost, "/openai/v1/chat/completions", body),
	)

	if r.called != 1 {
		t.Errorf("body just under 1 MiB must be redacted under default cap; redactor called %d times", r.called)
	}
}

func TestPIIRedactMiddleware_BodyAvailableToUpstream(t *testing.T) {
	// Belt-and-suspenders: confirms that even after the middleware reads
	// the body for redaction, the next handler can still read the same
	// payload. Regressions here would silently break upstream provider
	// calls in production.
	body := `{"hello":"world"}`
	r := &fakeRedactor{
		mutate: func(in string) (redact.Result, error) {
			return redact.Result{Text: in}, nil
		},
	}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{})(cap)

	mw.ServeHTTP(
		httptest.NewRecorder(),
		newReq(t, http.MethodPost, "/openai/v1/chat/completions", body),
	)
	if string(cap.bodySeen) != body {
		t.Errorf("upstream saw mutated body: %q != %q", cap.bodySeen, body)
	}
}

func TestPIIRedactedBody_AbsentReturnsFalse(t *testing.T) {
	if _, ok := PIIRedactedBody(context.Background()); ok {
		t.Error("PIIRedactedBody must return false when no value is set")
	}
}

// slowRedactor is a PIIRedactor that simulates a sidecar that's
// reachable but slow — same control flow as a real timeout (returns
// "context deadline exceeded") so the middleware sees the same error
// shape it would in production.
type slowRedactor struct {
	delay time.Duration
}

func (s *slowRedactor) Redact(ctx context.Context, _ string) (redact.Result, error) {
	select {
	case <-time.After(s.delay):
		return redact.Result{Text: "ok"}, nil
	case <-ctx.Done():
		return redact.Result{}, ctx.Err()
	}
}

// TestPIIRedactMiddleware_TimeoutFailOpenReturns200 proves that when
// the redactor returns a deadline-exceeded error AND fail_mode is
// "open", the upstream handler still serves the user. This is the
// path the proxy takes for an under-the-weather sidecar — the
// availability win that makes "open" the default for a first rollout.
func TestPIIRedactMiddleware_TimeoutFailOpenReturns200(t *testing.T) {
	r := &slowRedactor{delay: 50 * time.Millisecond}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{FailClosed: false})(cap)

	// Drive a context that's already past its deadline so slowRedactor
	// returns ctx.Err() immediately — equivalent to the real
	// timeout path inside redact.analyze.
	ctx, cancel := context.WithTimeout(context.Background(), time.Microsecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond)

	req := newReq(t, http.MethodPost, "/openai/v1/chat/completions", `{"x":1}`).WithContext(ctx)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("fail-open on timeout must return upstream status (200); got %d", rec.Code)
	}
	if cap.reqSeen == nil {
		t.Error("fail-open must invoke upstream even when the redactor times out")
	}
	if _, ok := PIIRedactedBody(cap.reqSeen.Context()); ok {
		t.Error("fail-open must not stash a redacted body when the redactor failed")
	}
}

// TestPIIRedactMiddleware_TimeoutFailClosedReturns503 is the negative
// twin: the same deadline-exceeded error must surface as 503 when the
// operator has explicitly opted into fail_mode: closed. This is the
// "I'd rather drop a single request than risk an unredacted log line"
// lever — proves that the timeout path goes through the same gate as
// every other redactor failure.
func TestPIIRedactMiddleware_TimeoutFailClosedReturns503(t *testing.T) {
	r := &slowRedactor{delay: 50 * time.Millisecond}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{FailClosed: true})(cap)

	ctx, cancel := context.WithTimeout(context.Background(), time.Microsecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond)

	req := newReq(t, http.MethodPost, "/openai/v1/chat/completions", `{"x":1}`).WithContext(ctx)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("fail-closed on timeout must return 503; got %d", rec.Code)
	}
	if cap.reqSeen != nil {
		t.Error("fail-closed must NOT invoke upstream on a redactor timeout")
	}
}
