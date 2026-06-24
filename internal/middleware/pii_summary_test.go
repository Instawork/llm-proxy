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
	finalizePIILeaked(ctx, reg, `{"text":"`+ph+`"}`)
	if got := piiSummaryHolderFromContext(ctx).Leaked; got != 1 {
		t.Fatalf("leaked = %d, want 1", got)
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
