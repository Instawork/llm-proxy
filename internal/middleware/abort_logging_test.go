package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestAbortLoggingMiddleware_LogsErrAbortHandler verifies that an
// http.ErrAbortHandler panic (the ReverseProxy body-copy failure signal) is
// logged with request context and then re-panicked so net/http still resets
// the connection.
func TestAbortLoggingMiddleware_LogsErrAbortHandler(t *testing.T) {
	handler := AbortLoggingMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("partial bo"))
		panic(http.ErrAbortHandler)
	}))

	req := httptest.NewRequest("POST", "/openai/v1/responses", nil)
	req.RemoteAddr = "172.31.0.1:12345"
	rec := httptest.NewRecorder()

	var recovered any
	logOutput := captureSlogOutput(func() {
		defer func() { recovered = recover() }()
		handler.ServeHTTP(rec, req)
	})

	if recovered != http.ErrAbortHandler {
		t.Fatalf("expected http.ErrAbortHandler to be re-panicked, got %v", recovered)
	}
	if !strings.Contains(logOutput, "request aborted mid-response") {
		t.Errorf("expected abort log line, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "/openai/v1/responses") {
		t.Errorf("expected path in abort log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "bytes_written=10") {
		t.Errorf("expected bytes_written=10 in abort log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "status=200") {
		t.Errorf("expected status=200 in abort log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "headers_sent=true") {
		t.Errorf("expected headers_sent=true in abort log, got: %s", logOutput)
	}
}

// TestAbortLoggingMiddleware_AbortBeforeHeaders covers the observed
// production shape: upstream returned headers to the transport but the body
// copy failed before the first client write, so nothing was sent.
func TestAbortLoggingMiddleware_AbortBeforeHeaders(t *testing.T) {
	handler := AbortLoggingMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic(http.ErrAbortHandler)
	}))

	req := httptest.NewRequest("POST", "/openai/v1/responses", nil)
	rec := httptest.NewRecorder()

	var recovered any
	logOutput := captureSlogOutput(func() {
		defer func() { recovered = recover() }()
		handler.ServeHTTP(rec, req)
	})

	if recovered != http.ErrAbortHandler {
		t.Fatalf("expected http.ErrAbortHandler to be re-panicked, got %v", recovered)
	}
	if !strings.Contains(logOutput, "headers_sent=false") {
		t.Errorf("expected headers_sent=false in abort log, got: %s", logOutput)
	}
	if !strings.Contains(logOutput, "bytes_written=0") {
		t.Errorf("expected bytes_written=0 in abort log, got: %s", logOutput)
	}
}

// TestAbortLoggingMiddleware_OtherPanicsPassThrough verifies that non-abort
// panics re-panic without the abort log line (net/http logs those itself,
// with a stack trace).
func TestAbortLoggingMiddleware_OtherPanicsPassThrough(t *testing.T) {
	handler := AbortLoggingMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("boom")
	}))

	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()

	var recovered any
	logOutput := captureSlogOutput(func() {
		defer func() { recovered = recover() }()
		handler.ServeHTTP(rec, req)
	})

	if recovered != "boom" {
		t.Fatalf("expected original panic value to propagate, got %v", recovered)
	}
	if strings.Contains(logOutput, "request aborted mid-response") {
		t.Errorf("abort log line should not fire for non-abort panics, got: %s", logOutput)
	}
}

// TestAbortLoggingMiddleware_NormalRequestUntouched verifies the middleware
// is transparent for successful requests.
func TestAbortLoggingMiddleware_NormalRequestUntouched(t *testing.T) {
	handler := AbortLoggingMiddleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()

	logOutput := captureSlogOutput(func() {
		handler.ServeHTTP(rec, req)
	})

	if rec.Code != http.StatusCreated {
		t.Errorf("expected status 201, got %d", rec.Code)
	}
	if rec.Body.String() != "ok" {
		t.Errorf("expected body 'ok', got %q", rec.Body.String())
	}
	if strings.Contains(logOutput, "request aborted mid-response") {
		t.Errorf("abort log line should not fire for normal requests, got: %s", logOutput)
	}
}

// TestAbortTrackingWriter_ForwardsFlush verifies the wrapper keeps SSE
// streaming working by forwarding Flush to the underlying writer.
func TestAbortTrackingWriter_ForwardsFlush(t *testing.T) {
	rec := httptest.NewRecorder()
	w := &abortTrackingWriter{ResponseWriter: rec}

	if _, ok := interface{}(w).(http.Flusher); !ok {
		t.Fatal("abortTrackingWriter must implement http.Flusher")
	}
	w.Write([]byte("chunk"))
	w.Flush()
	if !rec.Flushed {
		t.Error("expected Flush to be forwarded to the underlying writer")
	}
}

// TestAbortTrackingWriter_InformationalHeadersNonTerminal verifies that 1xx
// responses (e.g. 103 Early Hints, which ReverseProxy forwards) do not latch
// the tracked status — the later final WriteHeader wins.
func TestAbortTrackingWriter_InformationalHeadersNonTerminal(t *testing.T) {
	rec := httptest.NewRecorder()
	w := &abortTrackingWriter{ResponseWriter: rec}

	w.WriteHeader(http.StatusEarlyHints)
	if w.wroteHeader {
		t.Error("1xx must not mark headers as terminally written")
	}
	w.WriteHeader(http.StatusAccepted)
	if w.status != http.StatusAccepted || !w.wroteHeader {
		t.Errorf("expected final status 202 tracked, got status=%d wroteHeader=%v", w.status, w.wroteHeader)
	}
}

// TestAbortTrackingWriter_FlushCommitsImplicit200 verifies a flush-only
// response is tracked as headers committed with the implicit 200 that
// net/http sends, so abort logs never report headers_sent=false after the
// client already received headers.
func TestAbortTrackingWriter_FlushCommitsImplicit200(t *testing.T) {
	rec := httptest.NewRecorder()
	w := &abortTrackingWriter{ResponseWriter: rec}

	w.Flush()
	if !w.wroteHeader || w.status != http.StatusOK {
		t.Errorf("expected flush to commit implicit 200, got status=%d wroteHeader=%v", w.status, w.wroteHeader)
	}
	if !rec.Flushed {
		t.Error("expected Flush to be forwarded to the underlying writer")
	}
}
