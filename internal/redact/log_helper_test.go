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
	// Long input must be capped BEFORE being sent to the analyzer so a
	// multi-megabyte body can't blow up analyzer cost. The cap includes a
	// small slack beyond maxLen so an entity straddling the maxLen boundary
	// is still analyzed whole (the final output is re-truncated to maxLen).
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
	out := LogPreview(context.Background(), full, 100)
	if len(bodySent) != 100+64 {
		t.Errorf("LogPreview should send maxLen+slack (%d) bytes to analyzer, sent %d", 100+64, len(bodySent))
	}
	if len(out) != 100 {
		t.Errorf("LogPreview output should be capped to maxLen (100), got %d", len(out))
	}
}

// TestLogPreview_EntityAtTruncationBoundary guards the truncate-then-redact
// leak: an SSN straddling the maxLen cut used to be sliced into a fragment
// that no recognizer matched, so the partial PII landed in the log verbatim.
func TestLogPreview_EntityAtTruncationBoundary(t *testing.T) {
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		var p map[string]any
		_ = json.NewDecoder(req.Body).Decode(&p)
		text, _ := p["text"].(string)
		if idx := strings.Index(text, "222-33-4444"); idx >= 0 {
			_ = json.NewEncoder(w).Encode([]Span{
				{Start: idx, End: idx + 11, EntityType: "US_SSN", Score: 0.95},
			})
			return
		}
		_ = json.NewEncoder(w).Encode([]Span{})
	})
	r, _ := New(Config{AnalyzerURL: srv.URL})
	SetGlobal(r)
	t.Cleanup(func() { SetGlobal(nil) })

	// Place the SSN so a hard cut at maxLen=100 would slice it mid-number.
	text := strings.Repeat("x", 95) + "222-33-4444 trailing"
	out := LogPreview(context.Background(), text, 100)
	if strings.Contains(out, "222-33") {
		t.Errorf("partial SSN leaked into log preview: %q", out)
	}
}
