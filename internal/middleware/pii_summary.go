package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

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

func writePIIResponseHeaders(w http.ResponseWriter, ctx context.Context) {
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
	w.Header().Set("X-LLM-PII-Restored", strconv.Itoa(h.Restored))
	if len(h.Entities) > 0 {
		if b, err := json.Marshal(h.Entities); err == nil {
			w.Header().Set("X-LLM-PII-Entities", string(b))
		}
	}
}
