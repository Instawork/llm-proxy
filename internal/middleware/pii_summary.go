package middleware

import (
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

func announcePIITrailers(w http.ResponseWriter) {
	w.Header().Add("Trailer", "X-LLM-PII-Restored")
	w.Header().Add("Trailer", "X-LLM-PII-Leaked")
}

func writePIIResponseTrailers(w http.ResponseWriter, ctx context.Context) error {
	h := piiSummaryHolderFromContext(ctx)
	if h == nil || h.Outcome != PIIOutcomeOK {
		return nil
	}
	// Trailer keys were predeclared in announcePIITrailers; values are sent after
	// the body per net/http.ResponseWriter contract.
	w.Header().Set("X-LLM-PII-Restored", strconv.Itoa(h.Restored))
	w.Header().Set("X-LLM-PII-Leaked", strconv.Itoa(h.Leaked))
	return nil
}

// piiDeferHeadersResponseWriter delays committing response headers until the
// first Write. ReverseProxy calls WriteHeader before copying the body, which
// would flush headers before PII restore runs. Restored/Leaked are sent as HTTP
// trailers after the body so bytes can stream through without buffering.
type piiDeferHeadersResponseWriter struct {
	http.ResponseWriter
	ctx        context.Context
	statusCode int
	headerSent bool
	once       sync.Once
}

func (pw *piiDeferHeadersResponseWriter) WriteHeader(statusCode int) {
	if pw.headerSent {
		return
	}
	pw.statusCode = statusCode
}

func (pw *piiDeferHeadersResponseWriter) flushHeaders() {
	pw.once.Do(func() {
		writePIIResponseHeadersPartial(pw.ResponseWriter, pw.ctx)
		announcePIITrailers(pw.ResponseWriter)
		if pw.statusCode == 0 {
			pw.statusCode = http.StatusOK
		}
		pw.headerSent = true
		pw.ResponseWriter.WriteHeader(pw.statusCode)
	})
}

func (pw *piiDeferHeadersResponseWriter) Write(b []byte) (int, error) {
	pw.flushHeaders()
	return pw.ResponseWriter.Write(b)
}

func (pw *piiDeferHeadersResponseWriter) Flush() {
	pw.flushHeaders()
	if f, ok := pw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
