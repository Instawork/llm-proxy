package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"

	"github.com/Instawork/llm-proxy/internal/redact"
)

type piiSummaryCtxKey struct{}

// PII outcome labels surfaced on X-LLM-PII-Outcome.
const (
	PIIOutcomeOK         = "ok"
	PIIOutcomeFailOpen   = "fail_open"
	PIIOutcomeFailClosed = "fail_closed"
	PIIOutcomeOversize   = "oversize"
)

type piiSummaryHolder struct {
	Outcome  string
	Detected int
	Masked   int
	Sealed   int
	Redacted int
	Restored int
	Leaked   int
	Entities map[string]int
}

func newPIISummary(outcome string, entityCounts map[string]int) *piiSummaryHolder {
	masked, sealed, redacted := redact.TierCounts(entityCounts)
	return &piiSummaryHolder{
		Outcome:  outcome,
		Detected: redact.TotalDetected(entityCounts),
		Masked:   masked,
		Sealed:   sealed,
		Redacted: redacted,
		Entities: entityCounts,
	}
}

func attachPIISummary(ctx context.Context, holder *piiSummaryHolder) context.Context {
	if holder == nil {
		return ctx
	}
	return context.WithValue(ctx, piiSummaryCtxKey{}, holder)
}

func piiSummaryHolderFromContext(ctx context.Context) *piiSummaryHolder {
	if ctx == nil {
		return nil
	}
	h, _ := ctx.Value(piiSummaryCtxKey{}).(*piiSummaryHolder)
	return h
}

func finalizePIIRestored(ctx context.Context, reg *redact.Registry) {
	if h := piiSummaryHolderFromContext(ctx); h != nil && reg != nil {
		h.Restored = reg.RestoredCount()
	}
}

func finalizePIILeaked(ctx context.Context, reg *redact.Registry, responseText string) {
	if h := piiSummaryHolderFromContext(ctx); h != nil && reg != nil {
		h.Leaked = reg.MaskPlaceholdersRemaining(responseText)
	}
}

func writePIIResponseHeadersPartial(w http.ResponseWriter, ctx context.Context) {
	h := piiSummaryHolderFromContext(ctx)
	if h == nil || h.Outcome == "" {
		return
	}
	w.Header().Set("X-LLM-PII-Outcome", h.Outcome)
	if h.Outcome != PIIOutcomeOK {
		return
	}
	w.Header().Set("X-LLM-PII-Detected", strconv.Itoa(h.Detected))
	w.Header().Set("X-LLM-PII-Masked", strconv.Itoa(h.Masked))
	w.Header().Set("X-LLM-PII-Sealed", strconv.Itoa(h.Sealed))
	w.Header().Set("X-LLM-PII-Redacted", strconv.Itoa(h.Redacted))
	if len(h.Entities) > 0 {
		if b, err := json.Marshal(h.Entities); err == nil {
			w.Header().Set("X-LLM-PII-Entities", string(b))
		}
	}
}

func writePIIResponseHeadersRestoredLeaked(w http.ResponseWriter, ctx context.Context) {
	h := piiSummaryHolderFromContext(ctx)
	if h == nil || h.Outcome != PIIOutcomeOK {
		return
	}
	w.Header().Set("X-LLM-PII-Restored", strconv.Itoa(h.Restored))
	w.Header().Set("X-LLM-PII-Leaked", strconv.Itoa(h.Leaked))
}

// writePIIResponseHeaders writes the full PII header set (used on error paths
// and in unit tests).
func writePIIResponseHeaders(w http.ResponseWriter, ctx context.Context) {
	writePIIResponseHeadersPartial(w, ctx)
	writePIIResponseHeadersRestoredLeaked(w, ctx)
}

// piiStreamHeaderResponseWriter delays committing response headers until the
// first Write/Flush. Streaming cannot set Restored/Leaked as normal headers
// (unknown until the body ends) and must not use trailers (Cloudflare 502 /
// broken stream on Trailer-bearing responses through a Cloudflare proxy).
// Early PII headers are flushed; Restored/Leaked are log-only.
type piiStreamHeaderResponseWriter struct {
	http.ResponseWriter
	ctx        context.Context
	statusCode int
	headerSent bool
	once       sync.Once
}

func (pw *piiStreamHeaderResponseWriter) WriteHeader(statusCode int) {
	if pw.headerSent {
		return
	}
	pw.statusCode = statusCode
}

func (pw *piiStreamHeaderResponseWriter) flushHeaders() {
	pw.once.Do(func() {
		writePIIResponseHeadersPartial(pw.ResponseWriter, pw.ctx)
		if pw.statusCode == 0 {
			pw.statusCode = http.StatusOK
		}
		pw.headerSent = true
		pw.ResponseWriter.WriteHeader(pw.statusCode)
	})
}

func (pw *piiStreamHeaderResponseWriter) Write(b []byte) (int, error) {
	pw.flushHeaders()
	return pw.ResponseWriter.Write(b)
}

func (pw *piiStreamHeaderResponseWriter) Flush() {
	pw.flushHeaders()
	if f, ok := pw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// piiBufferResponseWriter buffers a non-streaming response so Restored/Leaked
// can be written as normal headers before any bytes reach the client.
// httputil.ReverseProxy copies the origin body with io.Copy and commits
// headers on the first Write — without this buffer we would need trailers,
// which Cloudflare cannot proxy (see PIIResponseRestoreMiddleware).
type piiBufferResponseWriter struct {
	http.ResponseWriter
	ctx        context.Context
	statusCode int
	buf        bytes.Buffer
}

func (pw *piiBufferResponseWriter) WriteHeader(statusCode int) {
	if pw.statusCode == 0 {
		pw.statusCode = statusCode
	}
}

func (pw *piiBufferResponseWriter) Write(b []byte) (int, error) {
	return pw.buf.Write(b)
}

func (pw *piiBufferResponseWriter) Flush() {}

func (pw *piiBufferResponseWriter) commit() error {
	writePIIResponseHeaders(pw.ResponseWriter, pw.ctx)
	// Restore may change body length vs the upstream Content-Length that
	// ReverseProxy copied onto the shared header map; drop it so the server
	// can chunk or recompute rather than truncate.
	pw.ResponseWriter.Header().Del("Content-Length")
	code := pw.statusCode
	if code == 0 {
		code = http.StatusOK
	}
	pw.ResponseWriter.WriteHeader(code)
	_, err := pw.ResponseWriter.Write(pw.buf.Bytes())
	return err
}
