package redact

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// globalRedactor holds an optional process-wide Redactor for use by
// log helpers that don't otherwise have one in scope (e.g. provider
// debug-preview lines deep in response-parsing code).
//
// Set once at startup from main(). Helpers that read it must tolerate
// nil — when redaction is disabled (the default), they fall back to a
// length-only summary so the operator still gets size context without
// dumping potentially-PII-bearing bytes.
var globalRedactor atomic.Pointer[Redactor]

// SetGlobal stores a Redactor that LogPreview can reach without
// threading the dependency through every call site. Pass nil to
// disable. Safe for concurrent use.
func SetGlobal(r *Redactor) { globalRedactor.Store(r) }

// LogPreview returns a short, log-safe excerpt of “text“ suitable for
// dropping into a debug log line. Behaviour:
//
//   - If a global Redactor is set AND the call to /analyze succeeds,
//     returns the redacted excerpt (PII collapsed to “[REDACTED:...]“).
//   - Otherwise returns “[len=N bytes; pii_redact disabled]“ —
//     enough size context to debug a parsing failure without leaking
//     the underlying body.
//
// The excerpt is capped to maxLen bytes so a multi-megabyte response
// can't blow up the log volume on its own. Callers that want the full
// body should construct a Redactor explicitly and call Redact, where
// they can also inspect EntityCounts.
//
// LogPreview is best-effort: the analyze deadline is bounded to a
// short slice of the redactor's configured timeout so a slow sidecar
// can never stall the calling request thread on a debug log line.
func LogPreview(ctx context.Context, text string, maxLen int) string {
	if maxLen <= 0 {
		maxLen = 200
	}
	r := globalRedactor.Load()
	if r == nil {
		return fmt.Sprintf("[len=%d bytes; pii_redact disabled]", len(text))
	}
	// Redact a slightly longer excerpt and truncate the REDACTED output:
	// cutting the raw text at maxLen first can slice an entity in half
	// (e.g. "222-33-4" from an SSN), and the fragment no longer matches any
	// recognizer, so it would land in the log verbatim — exactly what this
	// helper exists to prevent.
	const entitySlack = 64
	excerpt := text
	if len(excerpt) > maxLen+entitySlack {
		excerpt = excerpt[:maxLen+entitySlack]
	}
	// Bound to a tight slice of the redactor timeout so a slow sidecar
	// can never drag down the calling request — debug previews are
	// strictly best-effort.
	cctx, cancel := context.WithTimeout(ctx, AnalyzeTimeoutFromContext(ctx, r.cfg.Timeout)/2+10*time.Millisecond)
	defer cancel()
	res, err := r.Redact(cctx, excerpt)
	if err != nil {
		return fmt.Sprintf("[len=%d bytes; pii_redact unavailable: %v]", len(text), err)
	}
	out := res.Text
	if len(out) > maxLen {
		out = out[:maxLen]
	}
	return out
}
