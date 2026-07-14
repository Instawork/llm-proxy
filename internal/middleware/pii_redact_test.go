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

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/redact"
)

// fakeRedactor implements PIIRedactor without an HTTP round trip — keeps
// the middleware tests focused on body capture, context plumbing, and
// fail-mode branching.
type fakeRedactor struct {
	called  int
	mutate  func(in string) (redact.Result, error)
	scrubFn func(in string, reg *redact.Registry) (redact.Result, error)
}

func (f *fakeRedactor) Redact(ctx context.Context, text string) (redact.Result, error) {
	f.called++
	if f.mutate != nil {
		return f.mutate(text)
	}
	return redact.Result{Text: text, EntityCounts: map[string]int{}}, nil
}

func (f *fakeRedactor) Scrub(ctx context.Context, text string, reg *redact.Registry) (redact.Result, error) {
	f.called++
	if f.scrubFn != nil {
		return f.scrubFn(text, reg)
	}
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
	mw := PIIRedactMiddleware(r, PIIRedactConfig{GlobalEnabled: true})(cap)

	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/health", "{}"))

	if r.called != 0 {
		t.Errorf("redactor called for /health (count=%d)", r.called)
	}
}

func TestPIIRedactMiddleware_GETSkipped(t *testing.T) {
	r := &fakeRedactor{}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{GlobalEnabled: true})(cap)

	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodGet, "/openai/v1/chat/completions", ""))

	if r.called != 0 {
		t.Errorf("redactor called for GET request (count=%d)", r.called)
	}
}

func TestPIIRedactMiddleware_ModelsListSkipped(t *testing.T) {
	r := &fakeRedactor{}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{GlobalEnabled: true})(cap)

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
	mw := PIIRedactMiddleware(r, PIIRedactConfig{GlobalEnabled: true})(cap)

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
	mw := PIIRedactMiddleware(r, PIIRedactConfig{GlobalEnabled: true})(cap)

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
	mw := PIIRedactMiddleware(r, PIIRedactConfig{GlobalEnabled: true, FailClosed: false})(cap)

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
	mw := PIIRedactMiddleware(r, PIIRedactConfig{GlobalEnabled: true, FailClosed: true})(cap)

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

func TestPIIRedactMiddleware_DevLogRawEntities(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"email josé@example.com"}]}`
	r := &fakeRedactor{
		scrubFn: func(in string, _ *redact.Registry) (redact.Result, error) {
			return redact.Result{
				Text:         strings.Replace(in, "josé@example.com", "<PII_EMAIL_ADDRESS_1>", 1),
				EntityCounts: map[string]int{"EMAIL_ADDRESS": 1},
				DetectedEntities: []redact.DetectedEntity{
					{EntityType: "EMAIL_ADDRESS", Text: "josé@example.com", Policy: "mask", Score: 0.95, Start: 45, End: 61},
				},
			}, nil
		},
	}
	cap := &captureHandler{}
	var logBuf bytes.Buffer
	mw := PIIRedactMiddleware(r, PIIRedactConfig{
		GlobalEnabled:     true,
		WirePlaceholders:  true,
		DevLogRawEntities: true,
		Logger:            captureLogger(&logBuf),
	})(cap)

	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/anthropic/v1/messages", body))

	logOut := logBuf.String()
	if !strings.Contains(logOut, `"msg":"pii_redact: dev raw entities"`) {
		t.Fatalf("expected dev raw entity log, got %s", logOut)
	}
	if !strings.Contains(logOut, "josé@example.com") {
		t.Fatalf("expected raw entity value in dev log, got %s", logOut)
	}
}

func TestPIIRedactMiddleware_LogsAllowedEntityCounts(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"Hi Jess in Massachusetts"}]}`
	r := &fakeRedactor{
		scrubFn: func(in string, _ *redact.Registry) (redact.Result, error) {
			return redact.Result{
				Text:         in,
				EntityCounts: map[string]int{},
				AllowedEntities: []redact.AllowedEntity{
					{EntityType: "PERSON", Text: "Jess", Score: 0.9, Reason: "single_token_person"},
					{EntityType: "LOCATION", Text: "Massachusetts", Score: 0.85, Reason: "non_street_location"},
				},
			}, nil
		},
	}
	cap := &captureHandler{}
	var logBuf bytes.Buffer
	mw := PIIRedactMiddleware(r, PIIRedactConfig{
		GlobalEnabled:    true,
		WirePlaceholders: true,
		Logger:           captureLogger(&logBuf),
	})(cap)

	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/anthropic/v1/messages", body))

	logOut := logBuf.String()
	if !strings.Contains(logOut, `"allowed_entity_counts":{"LOCATION":1,"PERSON":1}`) &&
		!strings.Contains(logOut, `"allowed_entity_counts":{"PERSON":1,"LOCATION":1}`) {
		t.Fatalf("expected allowed entity counts in ok log, got %s", logOut)
	}
	if strings.Contains(logOut, "Jess") || strings.Contains(logOut, "Massachusetts") {
		t.Fatalf("allowed entity values should not appear without dev flag: %s", logOut)
	}
}

func TestPIIRedactMiddleware_DevLogAllowedEntities(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"Hi Jess in Massachusetts"}]}`
	r := &fakeRedactor{
		scrubFn: func(in string, _ *redact.Registry) (redact.Result, error) {
			return redact.Result{
				Text:         in,
				EntityCounts: map[string]int{},
				AllowedEntities: []redact.AllowedEntity{
					{EntityType: "PERSON", Text: "Jess", Score: 0.9, Reason: "single_token_person"},
					{EntityType: "LOCATION", Text: "Massachusetts", Score: 0.85, Reason: "non_street_location"},
				},
			}, nil
		},
	}
	cap := &captureHandler{}
	var logBuf bytes.Buffer
	mw := PIIRedactMiddleware(r, PIIRedactConfig{
		GlobalEnabled:     true,
		WirePlaceholders:  true,
		DevLogRawEntities: true,
		Logger:            captureLogger(&logBuf),
	})(cap)

	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/anthropic/v1/messages", body))

	logOut := logBuf.String()
	if !strings.Contains(logOut, `"msg":"pii_redact: dev allowed entities"`) {
		t.Fatalf("expected dev allowed entity log, got %s", logOut)
	}
	if !strings.Contains(logOut, "Jess") || !strings.Contains(logOut, "Massachusetts") {
		t.Fatalf("expected allowed entity values in dev log, got %s", logOut)
	}
	if !strings.Contains(logOut, "single_token_person") || !strings.Contains(logOut, "non_street_location") {
		t.Fatalf("expected allow reasons in dev log, got %s", logOut)
	}
}

func TestPIIRedactMiddleware_RawEntitiesNotLoggedWithoutDevFlag(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"email josé@example.com"}]}`
	r := &fakeRedactor{
		scrubFn: func(in string, _ *redact.Registry) (redact.Result, error) {
			return redact.Result{
				Text:         strings.Replace(in, "josé@example.com", "<PII_EMAIL_ADDRESS_1>", 1),
				EntityCounts: map[string]int{"EMAIL_ADDRESS": 1},
				DetectedEntities: []redact.DetectedEntity{
					{EntityType: "EMAIL_ADDRESS", Text: "josé@example.com", Policy: "mask", Score: 0.95, Start: 45, End: 61},
				},
			}, nil
		},
	}
	cap := &captureHandler{}
	var logBuf bytes.Buffer
	mw := PIIRedactMiddleware(r, PIIRedactConfig{
		GlobalEnabled:    true,
		WirePlaceholders: true,
		Logger:           captureLogger(&logBuf),
	})(cap)

	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/anthropic/v1/messages", body))

	if strings.Contains(logBuf.String(), "josé@example.com") {
		t.Fatalf("raw entity should not be logged without dev flag: %s", logBuf.String())
	}
}

func TestPIIRedactMiddleware_OversizeBodyFailOpenPassthrough(t *testing.T) {
	big := strings.Repeat("a", 5000)
	r := &fakeRedactor{}
	cap := &captureHandler{}
	var logBuf bytes.Buffer
	mw := PIIRedactMiddleware(r, PIIRedactConfig{
		GlobalEnabled: true,
		MaxBodyBytes:  1024,
		Logger:        captureLogger(&logBuf),
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
		`"msg":"[PROXY] pii_redact: body exceeds max_body_bytes"`,
		`"provider":"openai"`,
		`"body_bytes":5000`,
		`"max_body_bytes":1024`,
	} {
		if !strings.Contains(logOut, want) {
			t.Errorf("oversize WARN missing %q\nfull log: %s", want, logOut)
		}
	}
}

func TestPIIRedactMiddleware_OversizeBodyFailClosed503(t *testing.T) {
	big := strings.Repeat("a", 5000)
	r := &fakeRedactor{}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{
		GlobalEnabled: true,
		MaxBodyBytes:  1024,
		FailClosed:    true,
	})(cap)

	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, newReq(t, http.MethodPost, "/openai/v1/chat/completions", big))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if r.called != 0 {
		t.Errorf("oversize fail-closed should skip redactor (count=%d)", r.called)
	}
	if cap.bodySeen != nil {
		t.Error("oversize fail-closed must not reach upstream")
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
	mw := PIIRedactMiddleware(r, PIIRedactConfig{GlobalEnabled: true /* MaxBodyBytes: 0 → 1 MiB default */})(cap)

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
	mw := PIIRedactMiddleware(r, PIIRedactConfig{GlobalEnabled: true})(cap)

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

func (s *slowRedactor) Scrub(ctx context.Context, text string, _ *redact.Registry) (redact.Result, error) {
	return s.Redact(ctx, text)
}

// TestPIIRedactMiddleware_TimeoutFailOpenReturns200 proves that when
// the redactor returns a deadline-exceeded error AND fail_mode is
// "open", the upstream handler still serves the user. This is the
// path the proxy takes for an under-the-weather sidecar — the
// availability win that makes "open" the default for a first rollout.
func TestPIIRedactMiddleware_TimeoutFailOpenReturns200(t *testing.T) {
	r := &slowRedactor{delay: 50 * time.Millisecond}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{GlobalEnabled: true, FailClosed: false})(cap)

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
	mw := PIIRedactMiddleware(r, PIIRedactConfig{GlobalEnabled: true, FailClosed: true})(cap)

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

func TestPIIRedactMiddleware_PerKeyOverrideMatrix(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"secret"}]}`
	r := &fakeRedactor{mutate: func(in string) (redact.Result, error) {
		return redact.Result{Text: "redacted"}, nil
	}}

	cases := []struct {
		name   string
		global bool
		key    *apikeys.APIKey
		called bool
	}{
		{"global on inherit", true, nil, true},
		{"global off inherit", false, nil, false},
		{"global off key on", false, func() *apikeys.APIKey { v := true; return &apikeys.APIKey{RedactPII: &v} }(), true},
		{"global on key off", true, func() *apikeys.APIKey { v := false; return &apikeys.APIKey{RedactPII: &v} }(), false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r.called = 0
			cap := &captureHandler{}
			mw := PIIRedactMiddleware(r, PIIRedactConfig{GlobalEnabled: tc.global})(cap)
			req := newReq(t, http.MethodPost, "/openai/v1/chat/completions", body)
			if tc.key != nil {
				req = req.WithContext(apikeys.WithContext(req.Context(), tc.key))
			}
			mw.ServeHTTP(httptest.NewRecorder(), req)
			if (r.called > 0) != tc.called {
				t.Fatalf("redactor called=%d want called=%v", r.called, tc.called)
			}
		})
	}
}

// bigB64 returns a base64-ish string of n chars — well past the
// piiImageMinStrippedBytes strip threshold, so it stands in for a real
// vision payload without pulling in an actual image.
func bigB64(n int) string {
	return strings.Repeat("A", n)
}

func TestStripImageDataForAnalysis_StripsAndRestores(t *testing.T) {
	img := bigB64(4096)
	cases := []struct {
		name string
		body string
	}{
		{
			name: "openai data url",
			body: `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + img + `"}}]}]}`,
		},
		{
			name: "anthropic source",
			body: `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + img + `"}}]}]}`,
		},
		{
			name: "gemini inlineData",
			body: `{"contents":[{"parts":[{"inlineData":{"mimeType":"image/png","data":"` + img + `"}}]}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prepared, restores := stripImageDataForAnalysis([]byte(tc.body))
			if len(restores) != 1 {
				t.Fatalf("expected 1 restore, got %d", len(restores))
			}
			if strings.Contains(string(prepared), img) {
				t.Fatalf("analyze payload still contains the image blob")
			}
			if len(prepared) >= len(tc.body) {
				t.Fatalf("analyze payload (%d) not smaller than original (%d)", len(prepared), len(tc.body))
			}
			// Whatever the analyzer returns (unchanged text here) must round-trip.
			restored := restoreImageData(string(prepared), restores)
			if restored != tc.body {
				t.Fatalf("restore did not reproduce original body\n got %q\nwant %q", restored, tc.body)
			}
		})
	}
}

func TestStripImageDataForAnalysis_NoImagesUnchanged(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"just text, no images here"}]}`)
	prepared, restores := stripImageDataForAnalysis(body)
	if restores != nil {
		t.Fatalf("expected no restores for text-only body, got %d", len(restores))
	}
	if string(prepared) != string(body) {
		t.Fatalf("text-only body must be byte-for-byte unchanged")
	}
}

func TestStripImageDataForAnalysis_SmallImageNotStripped(t *testing.T) {
	// A tiny inline icon under the threshold costs the analyzer nothing and
	// stays in the analyzed text.
	body := []byte(`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + bigB64(64) + `"}}]}]}`)
	_, restores := stripImageDataForAnalysis(body)
	if restores != nil {
		t.Fatalf("small image should not be stripped, got %d restores", len(restores))
	}
}

func TestStripImageDataForAnalysis_NonJSONUnchanged(t *testing.T) {
	body := []byte("not json at all " + bigB64(4096))
	prepared, restores := stripImageDataForAnalysis(body)
	if restores != nil || string(prepared) != string(body) {
		t.Fatalf("non-JSON body must pass through untouched")
	}
}

// TestPIIRedactMiddleware_StripsImageBeforeAnalyzeAndRestores proves the
// end-to-end contract: the analyzer never sees the base64 blob, text PII is
// still redacted, and the image is restored in the wire body.
func TestPIIRedactMiddleware_StripsImageBeforeAnalyzeAndRestores(t *testing.T) {
	img := bigB64(8192)
	body := `{"messages":[{"role":"user","content":[{"type":"text","text":"ssn 222-33-4444"},{"type":"image_url","image_url":{"url":"data:image/png;base64,` + img + `"}}]}]}`

	var sawByAnalyzer string
	r := &fakeRedactor{
		mutate: func(in string) (redact.Result, error) {
			sawByAnalyzer = in
			out := strings.Replace(in, "222-33-4444", "[REDACTED:US_SSN]", 1)
			return redact.Result{Text: out, EntityCounts: map[string]int{"US_SSN": 1}}, nil
		},
	}

	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{GlobalEnabled: true, WirePlaceholders: true})(cap)
	mw.ServeHTTP(httptest.NewRecorder(), newReq(t, http.MethodPost, "/openai/v1/chat/completions", body))

	if strings.Contains(sawByAnalyzer, img) {
		t.Fatalf("analyzer received the raw image blob")
	}
	if !strings.Contains(sawByAnalyzer, "222-33-4444") {
		t.Fatalf("analyzer should still see the real text; got %q", sawByAnalyzer)
	}
	wire := string(cap.bodySeen)
	if !strings.Contains(wire, img) {
		t.Fatalf("wire body lost the image — upstream vision would break")
	}
	if strings.Contains(wire, "222-33-4444") {
		t.Fatalf("ssn leaked to upstream wire body: %s", wire)
	}
	if !strings.Contains(wire, "[REDACTED:US_SSN]") {
		t.Fatalf("wire body missing redaction marker: %s", wire)
	}
}

type timeoutCaptureRedactor struct {
	timeout time.Duration
}

func (t *timeoutCaptureRedactor) Redact(ctx context.Context, text string) (redact.Result, error) {
	return t.Scrub(ctx, text, nil)
}

func (t *timeoutCaptureRedactor) Scrub(ctx context.Context, text string, _ *redact.Registry) (redact.Result, error) {
	t.timeout = redact.AnalyzeTimeoutFromContext(ctx, 0)
	return redact.Result{Text: text, EntityCounts: map[string]int{}}, nil
}

func TestPIIRedactMiddleware_ScalesAnalyzeTimeoutWithBodySize(t *testing.T) {
	cap := &captureHandler{}
	r := &timeoutCaptureRedactor{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{
		GlobalEnabled:           true,
		AnalyzeTimeout:          8 * time.Second,
		AnalyzeTimeoutPer100KiB: 2 * time.Second,
		AnalyzeTimeoutMax:       30 * time.Second,
	})(cap)

	body := strings.Repeat("x", 434_339)
	req := newReq(t, http.MethodPost, "/anthropic/v1/messages", body)
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if r.timeout != 16*time.Second {
		t.Fatalf("analyze timeout = %v, want 16s for 434339-byte body", r.timeout)
	}
}
