package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/redact"
)

func TestWritePIIResponseHeaders_OK(t *testing.T) {
	holder := newPIISummary(PIIOutcomeOK, map[string]int{
		"EMAIL_ADDRESS": 1,
		"US_SSN":        1,
	})
	ctx := attachPIISummary(context.Background(), holder)
	holder.Restored = 1

	rec := httptest.NewRecorder()
	writePIIResponseHeaders(rec, ctx)

	if got := rec.Header().Get("X-LLM-PII-Outcome"); got != "ok" {
		t.Fatalf("outcome = %q, want ok", got)
	}
	if got := rec.Header().Get("X-LLM-PII-Detected"); got != "2" {
		t.Fatalf("detected = %q, want 2", got)
	}
	if got := rec.Header().Get("X-LLM-PII-Masked"); got != "1" {
		t.Fatalf("masked = %q, want 1 (EMAIL_ADDRESS)", got)
	}
	if got := rec.Header().Get("X-LLM-PII-Sealed"); got != "1" {
		t.Fatalf("sealed = %q, want 1 (US_SSN)", got)
	}
	if got := rec.Header().Get("X-LLM-PII-Restored"); got != "1" {
		t.Fatalf("restored = %q, want 1", got)
	}
	entities := rec.Header().Get("X-LLM-PII-Entities")
	if !strings.Contains(entities, "EMAIL_ADDRESS") {
		t.Fatalf("entities header missing types: %q", entities)
	}
}

func TestWritePIIResponseHeaders_FailOpen(t *testing.T) {
	ctx := attachPIISummary(context.Background(), newPIISummary(PIIOutcomeFailOpen, nil))
	rec := httptest.NewRecorder()
	writePIIResponseHeaders(rec, ctx)

	if got := rec.Header().Get("X-LLM-PII-Outcome"); got != "fail_open" {
		t.Fatalf("outcome = %q, want fail_open", got)
	}
	if rec.Header().Get("X-LLM-PII-Detected") != "" {
		t.Fatal("expected no detected header on fail_open")
	}
}

func TestFinalizePIILeaked_CountsRemainingPlaceholders(t *testing.T) {
	reg := redact.NewRegistry()
	ph := reg.Placeholder("EMAIL_ADDRESS", "leak@example.com")
	ctx := attachPIISummary(context.Background(), newPIISummary(PIIOutcomeOK, map[string]int{"EMAIL_ADDRESS": 1}))
	leaks := piiLeakCounter{reg: reg}
	leaks.observe([]byte(`{"text":"` + ph + `"}`))
	finalizePIILeaked(ctx, leaks.total())
	if got := piiSummaryHolderFromContext(ctx).Leaked; got != 1 {
		t.Fatalf("leaked = %d, want 1", got)
	}
}

// The incremental counter must agree with a whole-text count no matter how
// the restored response is split into segments — including splits that fall
// inside a placeholder — while retaining no more than the scan window.
func TestPIILeakCounter_MatchesWholeTextCountAcrossSplits(t *testing.T) {
	reg := redact.NewRegistry()
	email := reg.Placeholder("EMAIL_ADDRESS", "leak@example.com")
	person := reg.Placeholder("PERSON", "Jane Doe")
	reg.Placeholder("PHONE_NUMBER", "555-0100")
	text := `data: {"text":"hello ` + email + ` and ` + person + ` again ` + email + `"}` + "\n\n" +
		`data: {"text":"` + strings.Repeat("filler text ", 40) + person + `"}` + "\n\n"
	want := reg.MaskPlaceholdersRemaining(text)
	if want != 4 {
		t.Fatalf("whole-text count = %d, want 4 (fixture sanity)", want)
	}

	for _, size := range []int{1, 2, 3, 5, 7, 11, 16, 50, 97, 200, len(text), len(text) + 10} {
		c := piiLeakCounter{reg: reg}
		for off := 0; off < len(text); off += size {
			end := off + size
			if end > len(text) {
				end = len(text)
			}
			// Hand the counter a buffer we immediately clobber, as
			// ReverseProxy's reused copy buffer would.
			seg := []byte(text[off:end])
			c.observe(seg)
			for i := range seg {
				seg[i] = 'X'
			}
			if len(c.tail) > piiLeakScanWindow {
				t.Fatalf("segment size %d: retained %d bytes, cap is %d", size, len(c.tail), piiLeakScanWindow)
			}
		}
		if got := c.total(); got != want {
			t.Fatalf("segment size %d: leaked = %d, want %d", size, got, want)
		}
	}
}

func TestPIILeakCounter_NilRegistryAndEmptySegmentsAreNoOps(t *testing.T) {
	var c piiLeakCounter
	c.observe([]byte("<PII_PERSON_1>"))
	if c.total() != 0 {
		t.Fatalf("nil registry counted %d leaks", c.total())
	}
	c = piiLeakCounter{reg: redact.NewRegistry()}
	c.observe(nil)
	c.observe([]byte{})
	if c.total() != 0 || len(c.tail) != 0 {
		t.Fatalf("empty segments changed state: count=%d tail=%q", c.total(), c.tail)
	}
}

func TestProductionPIIWireStack_EmitsPIIHeaders(t *testing.T) {
	reg := redact.NewRegistry()
	email := "header-restore@example.com"
	ph := reg.Placeholder("EMAIL_ADDRESS", email)
	pm := wireTestProviderManager(t)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"` + ph + `"}]}`))
	})

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	summary := newPIISummary(PIIOutcomeOK, map[string]int{"EMAIL_ADDRESS": 1})
	req = req.WithContext(attachPIISummary(withPIIRegistry(req.Context(), reg), summary))

	rec := httptest.NewRecorder()
	productionPIIWireStack(pm, handler).ServeHTTP(rec, req)

	if got := rec.Header().Get("X-LLM-PII-Outcome"); got != "ok" {
		t.Fatalf("outcome = %q, want ok", got)
	}
	if got := piiMetricFromResponse(rec, "X-LLM-PII-Restored"); got != "1" {
		t.Fatalf("restored = %q, want 1", got)
	}
	if got := piiMetricFromResponse(rec, "X-LLM-PII-Leaked"); got != "0" {
		t.Fatalf("leaked = %q, want 0", got)
	}
	if !strings.Contains(rec.Body.String(), email) {
		t.Fatalf("body missing restored email")
	}
}
