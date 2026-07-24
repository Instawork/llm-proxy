package providers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/Instawork/llm-proxy/internal/proxylog"
)

// NewBodyReadLoggingTransport wraps inner so the first error returned while
// reading a response body is emitted as one structured log line.
//
// Rationale: when httputil.ReverseProxy hits a body-read error while copying
// the upstream response to the client it panics with http.ErrAbortHandler,
// and for context.Canceled it does so *silently* (net/http skips its
// "ReverseProxy read error during body copy" log for canceled contexts). The
// abort itself is visible via AbortLoggingMiddleware, but the underlying
// error — who killed the stream, and whether the request context was already
// canceled at that moment — is not. This transport captures exactly the error
// the body consumer sees, without changing it.
//
// The wrapper is strictly pass-through: reads and Close are forwarded
// unbuffered and errors are returned unmodified, so streaming (SSE flushing,
// chunk boundaries) and connection reuse are unaffected. It should be
// installed as the OUTERMOST transport wrapper so its body reader is the one
// ReverseProxy actually reads from.
func NewBodyReadLoggingTransport(inner http.RoundTripper, provider string) http.RoundTripper {
	return &bodyReadLoggingTransport{inner: inner, provider: provider}
}

type bodyReadLoggingTransport struct {
	inner    http.RoundTripper
	provider string
}

func (t *bodyReadLoggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.inner.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	resp.Body = &bodyReadLoggingReader{
		inner:    resp.Body,
		ctx:      req.Context(),
		provider: t.provider,
		method:   req.Method,
		// Path only — Gemini carries the API key in the query string.
		path:   req.URL.Path,
		status: resp.StatusCode,
		start:  time.Now(),
	}
	return resp, nil
}

// bodyReadLoggingReader forwards reads to the real response body and logs the
// first non-EOF read error together with the request-context state at that
// instant. Logging once keeps retry/drain paths from repeating the line.
type bodyReadLoggingReader struct {
	inner     io.ReadCloser
	ctx       context.Context
	provider  string
	method    string
	path      string
	status    int
	start     time.Time
	bytesRead int64
	logged    bool
}

func (b *bodyReadLoggingReader) Read(p []byte) (int, error) {
	n, err := b.inner.Read(p)
	b.bytesRead += int64(n)
	if err != nil && err != io.EOF && !b.logged {
		b.logged = true
		slog.Error(
			proxylog.UpstreamMsg("response body read error"),
			slog.String("provider", b.provider),
			slog.String("method", b.method),
			slog.String("path", b.path),
			slog.Int("status", b.status),
			slog.String("error", err.Error()),
			slog.String("ctx_err", readErrText(b.ctx.Err())),
			slog.String("ctx_cause", readErrText(context.Cause(b.ctx))),
			slog.Int64("bytes_read", b.bytesRead),
			slog.Duration("since_headers", time.Since(b.start)),
		)
	}
	return n, err
}

func (b *bodyReadLoggingReader) Close() error {
	return b.inner.Close()
}

// readErrText renders an error for a slog attribute, with "" for nil.
func readErrText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
