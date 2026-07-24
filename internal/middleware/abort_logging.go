package middleware

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/Instawork/llm-proxy/internal/proxylog"
)

// AbortLoggingMiddleware makes silent request aborts visible.
//
// When httputil.ReverseProxy fails to copy the upstream response body to the
// client (upstream stream reset after headers, client connection already
// gone, ...), it panics with http.ErrAbortHandler. net/http deliberately
// suppresses that panic — no stack trace, no log line — and resets the client
// connection. Behind an ALB that reset surfaces to callers as an
// ALB-generated 502 (HTTPCode_ELB_502_Count) with nothing in our logs: the
// request has a "Started request" line but no TTFB / token-usage /
// "Completed request" lines.
//
// This middleware recovers http.ErrAbortHandler, emits one structured log
// line with enough context to correlate against ALB 502s (method, path,
// status, bytes written, headers-sent flag, duration), and re-panics so
// net/http's connection-teardown semantics are preserved. All other panic
// values are re-panicked untouched — net/http already logs those with a
// stack trace.
//
// It must be registered as the OUTERMOST middleware so it observes aborts
// raised anywhere in the chain, including the provider ReverseProxy handlers.
func AbortLoggingMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			aw := &abortTrackingWriter{ResponseWriter: w}

			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if rec == http.ErrAbortHandler {
					// ctx_err / ctx_cause disambiguate the two abort flavours:
					// a non-empty ctx_err means the inbound request context was
					// already canceled (net/http saw the client/ALB connection
					// die), while an empty ctx_err with an abort means the
					// failure was on the upstream side of the body copy.
					ctx := r.Context()
					slog.Error(
						proxylog.UpstreamMsg("request aborted mid-response; client connection will be reset"),
						slog.String("method", r.Method),
						slog.String("path", r.URL.Path),
						slog.String("remote_addr", r.RemoteAddr),
						slog.Int("status", aw.status),
						slog.Int64("bytes_written", aw.bytesWritten),
						slog.Bool("headers_sent", aw.wroteHeader),
						slog.Duration("duration", time.Since(start)),
						slog.String("ctx_err", errText(ctx.Err())),
						slog.String("ctx_cause", errText(context.Cause(ctx))),
						// The deferred recover runs while the panic frames are
						// still on this goroutine's stack, so this pinpoints the
						// exact panic(http.ErrAbortHandler) site — production
						// aborts have shown "no read error, no write, live
						// context" shapes that only a stack can disambiguate.
						slog.String("stack", string(debug.Stack())),
					)
				}
				panic(rec)
			}()

			next.ServeHTTP(aw, r)
		})
	}
}

// abortTrackingWriter records the status code and number of body bytes
// written so the abort log line can say how far the response got before the
// connection died (headers_sent=false + bytes_written=0 means the client saw
// the ALB's 502; a non-zero byte count means a truncated body). It forwards
// http.Flusher and http.Hijacker and exposes Unwrap so wrapping never
// degrades streaming or other writer capabilities (same contract as
// statusCapturingWriter and responseCapture).
type abortTrackingWriter struct {
	http.ResponseWriter
	status       int
	bytesWritten int64
	wroteHeader  bool
}

func (w *abortTrackingWriter) WriteHeader(code int) {
	// 1xx informational responses (100 Continue, 103 Early Hints — which
	// ReverseProxy forwards via WriteHeader) are non-terminal: net/http lets
	// the handler call WriteHeader again with the final status, so don't
	// latch tracking state on them.
	if code >= 100 && code < 200 {
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *abortTrackingWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		// Mirror net/http: the first Write implies a 200 if WriteHeader was
		// never called.
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytesWritten += int64(n)
	return n, err
}

func (w *abortTrackingWriter) Flush() {
	// Flush commits an implicit 200 in net/http when WriteHeader hasn't been
	// called yet; mirror that so abort logs never claim headers_sent=false
	// after headers already reached the client.
	if !w.wroteHeader {
		w.status = http.StatusOK
		w.wroteHeader = true
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *abortTrackingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := w.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
}

// Unwrap exposes the wrapped writer for http.ResponseController.
func (w *abortTrackingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// errText renders an error for a slog attribute, with "" for nil so log
// consumers can filter on attribute presence.
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
