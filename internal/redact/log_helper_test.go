package redact

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestLogPreview_NoGlobalRedactorReturnsLengthOnly(t *testing.T) {
	// Belt-and-suspenders: clear the package-level Redactor in case a
	// prior test set it. We don't want this assertion to depend on test
	// ordering since SetGlobal mutates a shared atomic.
	SetGlobal(nil)

	out := LogPreview(context.Background(), "user 222-33-4444 here", 200)
	if !strings.Contains(out, "len=21") {
		t.Errorf("expected length-only summary, got %q", out)
	}
	if strings.Contains(out, "222-33-4444") {
		t.Errorf("raw PII leaked: %q", out)
	}
}

func TestLogPreview_RedactsThroughGlobalRedactor(t *testing.T) {
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode([]Span{
			{Start: 5, End: 16, EntityType: "US_SSN", Score: 0.95},
		})
	})
	r, err := New(Config{AnalyzerURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	SetGlobal(r)
	t.Cleanup(func() { SetGlobal(nil) })

	out := LogPreview(context.Background(), "ssn: 222-33-4444 here", 200)
	if !strings.Contains(out, "[REDACTED:US_SSN]") {
		t.Errorf("expected redacted marker, got %q", out)
	}
	if strings.Contains(out, "222-33-4444") {
		t.Errorf("raw ssn leaked: %q", out)
	}
}

func TestLogPreview_TruncatesBeforeRedaction(t *testing.T) {
	// Long input should be capped to maxLen bytes BEFORE being sent to
	// the analyzer. We assert this by capturing the request payload.
	var bodySent string
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(req.Body).Decode(&p)
		bodySent, _ = p["text"].(string)
		_ = json.NewEncoder(w).Encode([]Span{})
	})
	r, _ := New(Config{AnalyzerURL: srv.URL})
	SetGlobal(r)
	t.Cleanup(func() { SetGlobal(nil) })

	full := strings.Repeat("a", 5000)
	_ = LogPreview(context.Background(), full, 100)
	if len(bodySent) != 100 {
		t.Errorf("LogPreview should send %d bytes to analyzer, sent %d", 100, len(bodySent))
	}
}
