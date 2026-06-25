package redact

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

func containsWirePlaceholderJSON(body, placeholder string) bool {
	if strings.Contains(body, placeholder) {
		return true
	}
	escaped := strings.NewReplacer("<", `\u003c`, ">", `\u003e`).Replace(placeholder)
	return strings.Contains(body, escaped)
}

func TestScrub_JSONUserEmailAfterNewlinePreservesValidJSON(t *testing.T) {
	const email = "eric@gmail.com"
	const welcome = "👋 Welcome"
	inner := fmt.Sprintf(
		"## User\n%s\n\n## Agent\nClaude4.6SonnetThinking\n\n## Conversation\nUSER: Hello\nASSISTANT: Hello! %s! How can I help you today?",
		email, welcome,
	)
	body, err := json.Marshal(map[string]any{
		"model": "gpt-4o-mini",
		"messages": []any{
			map[string]any{"role": "user", "content": inner},
		},
		"response_format": map[string]any{"type": "json_object"},
	})
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}

	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode analyzer payload: %v", err)
		}
		spans := []Span{
			spanForValue(t, payload.Text, email, "EMAIL_ADDRESS"),
			spanForValue(t, payload.Text, welcome, "PERSON"),
		}
		_ = json.NewEncoder(w).Encode(spans)
	})
	r, err := New(Config{AnalyzerURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := r.Scrub(context.Background(), string(body), NewRegistry())
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if !json.Valid([]byte(res.Text)) {
		t.Fatalf("scrubbed body is invalid JSON: %q", res.Text)
	}
	if strings.Contains(res.Text, email) {
		t.Fatalf("raw email leaked into scrubbed body: %q", res.Text)
	}
	if strings.Contains(res.Text, welcome) {
		t.Fatalf("raw welcome text leaked into scrubbed body: %q", res.Text)
	}
	if !containsWirePlaceholderJSON(res.Text, "<EMAIL_ADDRESS_1>") || !containsWirePlaceholderJSON(res.Text, "<PERSON_1>") {
		t.Fatalf("expected wire placeholders in %q", res.Text)
	}
}

func TestScrub_JSONRawBodySpliceWouldBreakEscapes(t *testing.T) {
	const email = "eric@gmail.com"
	body := fmt.Sprintf(`{"messages":[{"role":"user","content":"## User\n%s\n\n## Agent\nTest"}]}`, email)

	emailByteStart := strings.Index(body, email)
	if emailByteStart < 1 {
		t.Fatalf("email not found in body")
	}
	wrongStart := utf8.RuneCountInString(body[:emailByteStart-1])
	wrongEnd := wrongStart + utf8.RuneCountInString("n"+email)

	reg := NewRegistry()
	broken := spliceSpans(body, []Span{{
		Start: wrongStart, End: wrongEnd, EntityType: "EMAIL_ADDRESS", Score: 0.95,
	}}, 0.5, reg, false, true)

	if json.Valid([]byte(broken.Text)) {
		t.Fatalf("expected raw splice to produce invalid JSON for regression guard, got %q", broken.Text)
	}

	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode analyzer payload: %v", err)
		}
		spans := []Span{spanForValue(t, payload.Text, email, "EMAIL_ADDRESS")}
		_ = json.NewEncoder(w).Encode(spans)
	})
	r, err := New(Config{AnalyzerURL: srv.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	res, err := r.Scrub(context.Background(), body, NewRegistry())
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	if !json.Valid([]byte(res.Text)) {
		t.Fatalf("JSON-aware scrub must preserve valid JSON, got %q", res.Text)
	}
}

func TestScrub_JSONOnlyScrubsUserContentPaths(t *testing.T) {
	body := `{"max":"Alice","job_application_id":"Boston","nested":{"availability_flag":"Jess","email":"jess@gmail.com"},"messages":[{"role":"user","content":"reach jess@gmail.com"}]}`
	var analyzedText string
	srv := fakeAnalyzer(t, func(w http.ResponseWriter, req *http.Request) {
		var payload struct {
			Text string `json:"text"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode analyzer payload: %v", err)
		}
		analyzedText = payload.Text
		spans := []Span{spanForValue(t, payload.Text, "jess@gmail.com", "EMAIL_ADDRESS")}
		_ = json.NewEncoder(w).Encode(spans)
	})
	r, _ := New(Config{AnalyzerURL: srv.URL})

	res, err := r.Scrub(context.Background(), body, NewRegistry())
	if err != nil {
		t.Fatalf("Scrub: %v", err)
	}
	for _, key := range []string{"max", "job_application_id", "availability_flag", "email", "messages", "role", "content"} {
		if strings.Contains(analyzedText, key) {
			t.Fatalf("JSON key %q should not be sent to analyzer text: %q", key, analyzedText)
		}
	}
	if analyzedText != "reach jess@gmail.com" {
		t.Fatalf("analyzer text = %q, want decoded message content only", analyzedText)
	}
	if !strings.Contains(res.Text, `"max":"Alice"`) {
		t.Fatalf("non-user JSON values should remain untouched: %q", res.Text)
	}
	if !strings.Contains(res.Text, `"email":"jess@gmail.com"`) {
		t.Fatalf("non-user JSON email field should remain untouched: %q", res.Text)
	}
	if strings.Contains(res.Text, "reach jess@gmail.com") {
		t.Fatalf("email should be scrubbed from user content: %q", res.Text)
	}
	if !json.Valid([]byte(res.Text)) {
		t.Fatalf("scrubbed JSON is invalid: %q", res.Text)
	}
}
