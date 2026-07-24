package providers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func captureBodyReadSlog(fn func()) string {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)
	fn()
	return buf.String()
}

// erroringBody yields some payload bytes and then a terminal error.
type erroringBody struct {
	data   *bytes.Reader
	err    error
	closed bool
}

func (b *erroringBody) Read(p []byte) (int, error) {
	if b.data.Len() > 0 {
		return b.data.Read(p)
	}
	return 0, b.err
}

func (b *erroringBody) Close() error {
	b.closed = true
	return nil
}

type staticRoundTripper struct {
	resp *http.Response
	err  error
}

func (s *staticRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return s.resp, s.err
}

func newLoggingBodyResponse(t *testing.T, body io.ReadCloser) io.ReadCloser {
	t.Helper()
	inner := &staticRoundTripper{resp: &http.Response{StatusCode: http.StatusOK, Body: body}}
	rt := NewBodyReadLoggingTransport(inner, "gemini")

	req, err := http.NewRequestWithContext(context.Background(), "POST", "https://upstream/v1beta/models/x:generateContent", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp.Body
}

// TestBodyReadLogging_PassThrough verifies data, EOF, and Close forward
// unchanged and produce no log output.
func TestBodyReadLogging_PassThrough(t *testing.T) {
	body := &erroringBody{data: bytes.NewReader([]byte("hello world")), err: io.EOF}
	wrapped := newLoggingBodyResponse(t, body)

	var got []byte
	logOutput := captureBodyReadSlog(func() {
		var err error
		got, err = io.ReadAll(wrapped)
		if err != nil {
			t.Fatalf("unexpected read error: %v", err)
		}
	})

	if string(got) != "hello world" {
		t.Errorf("expected pass-through body, got %q", got)
	}
	if logOutput != "" {
		t.Errorf("expected no log output for clean EOF, got: %s", logOutput)
	}
	if err := wrapped.Close(); err != nil {
		t.Fatalf("unexpected close error: %v", err)
	}
	if !body.closed {
		t.Error("expected Close to forward to the inner body")
	}
}

// TestBodyReadLogging_LogsFirstReadError verifies a non-EOF read error is
// logged exactly once with the error text and context state, and that the
// error itself is returned unchanged to the caller.
func TestBodyReadLogging_LogsFirstReadError(t *testing.T) {
	readErr := errors.New("stream reset by peer")
	body := &erroringBody{data: bytes.NewReader([]byte("partial")), err: readErr}
	wrapped := newLoggingBodyResponse(t, body)

	var firstErr error
	logOutput := captureBodyReadSlog(func() {
		_, firstErr = io.ReadAll(wrapped)
		// Second read after the error: must not log again.
		buf := make([]byte, 8)
		_, _ = wrapped.Read(buf)
	})

	if !errors.Is(firstErr, readErr) {
		t.Fatalf("expected original error returned to caller, got %v", firstErr)
	}
	if count := strings.Count(logOutput, "response body read error"); count != 1 {
		t.Errorf("expected exactly one read-error log line, got %d: %s", count, logOutput)
	}
	if !strings.Contains(logOutput, "stream reset by peer") {
		t.Errorf("expected error text in log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "provider=gemini") {
		t.Errorf("expected provider in log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "bytes_read=7") {
		t.Errorf("expected bytes_read=7 in log, got: %s", logOutput)
	}
	if strings.Contains(logOutput, "context canceled") {
		t.Errorf("expected empty ctx_err for live context, got: %s", logOutput)
	}
}

// TestBodyReadLogging_CanceledContextCause verifies the log line captures the
// request-context error and cancellation cause at the moment of the failed
// read — the discriminator this instrumentation exists for.
func TestBodyReadLogging_CanceledContextCause(t *testing.T) {
	inner := &staticRoundTripper{resp: &http.Response{
		StatusCode: http.StatusOK,
		Body:       &erroringBody{data: bytes.NewReader(nil), err: context.Canceled},
	}}
	rt := NewBodyReadLoggingTransport(inner, "gemini")

	ctx, cancel := context.WithCancelCause(context.Background())
	req, err := http.NewRequestWithContext(ctx, "POST", "https://upstream/v1beta/models/x:generateContent", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	cancel(errors.New("inbound connection lost"))

	logOutput := captureBodyReadSlog(func() {
		_, _ = io.ReadAll(resp.Body)
	})

	if !strings.Contains(logOutput, "ctx_err=\"context canceled\"") {
		t.Errorf("expected ctx_err=context canceled in log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "inbound connection lost") {
		t.Errorf("expected cancellation cause in log, got: %s", logOutput)
	}
}

// TestBodyReadLogging_TransportErrorPassThrough verifies RoundTrip errors and
// nil bodies flow through without wrapping.
func TestBodyReadLogging_TransportErrorPassThrough(t *testing.T) {
	rtErr := errors.New("dial failure")
	rt := NewBodyReadLoggingTransport(&staticRoundTripper{err: rtErr}, "openai")

	req, err := http.NewRequestWithContext(context.Background(), "GET", "https://upstream/v1/models", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, gotErr := rt.RoundTrip(req); !errors.Is(gotErr, rtErr) {
		t.Fatalf("expected transport error passed through, got %v", gotErr)
	}
}

// TestBodyReadLogging_QueryStringNotLogged verifies the log line uses only
// the URL path — Gemini requests carry the API key in the query string.
func TestBodyReadLogging_QueryStringNotLogged(t *testing.T) {
	inner := &staticRoundTripper{resp: &http.Response{
		StatusCode: http.StatusOK,
		Body:       &erroringBody{data: bytes.NewReader(nil), err: errors.New("boom")},
	}}
	rt := NewBodyReadLoggingTransport(inner, "gemini")

	req, err := http.NewRequestWithContext(context.Background(), "POST",
		"https://upstream/v1beta/models/x:generateContent?key=sk-super-secret", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}

	logOutput := captureBodyReadSlog(func() {
		_, _ = io.ReadAll(resp.Body)
	})

	if strings.Contains(logOutput, "sk-super-secret") {
		t.Errorf("API key must not appear in log output, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, ":generateContent") {
		t.Errorf("expected path in log output, got: %s", logOutput)
	}
}
