package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/redact"
)

func TestPIIRedactMiddleware_WireModeSendsScrubbedBodyUpstream(t *testing.T) {
	original := `{"messages":[{"role":"user","content":"call Jane Doe"}],"stream":true}`
	r := &fakeRedactor{
		mutate: func(in string) (redact.Result, error) {
			out := strings.Replace(in, "Jane Doe", "<PII_PERSON_1>", 1)
			return redact.Result{Text: out, EntityCounts: map[string]int{"PERSON": 1}}, nil
		},
	}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{
		GlobalEnabled:         true,
		WirePlaceholders:      true,
		DefaultAllowStreaming: true,
	})(cap)

	mw.ServeHTTP(
		httptest.NewRecorder(),
		newReq(t, http.MethodPost, "/openai/v1/chat/completions", original),
	)

	if !strings.Contains(string(cap.bodySeen), "<PII_PERSON_1>") {
		t.Fatalf("upstream body missing placeholder: %q", cap.bodySeen)
	}
	if strings.Contains(string(cap.bodySeen), "Jane Doe") {
		t.Fatalf("upstream must not see raw PII: %q", cap.bodySeen)
	}
	if _, ok := PIIRedactedBody(cap.reqSeen.Context()); !ok {
		t.Fatal("scrubbed body not stashed")
	}
}

func TestPIIRedactMiddleware_WireModeForceNonStreaming(t *testing.T) {
	body := `{"messages":[],"stream":true}`
	r := &fakeRedactor{mutate: func(in string) (redact.Result, error) {
		return redact.Result{Text: in}, nil
	}}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{
		GlobalEnabled:         true,
		WirePlaceholders:      true,
		DefaultAllowStreaming: false,
	})(cap)

	mw.ServeHTTP(
		httptest.NewRecorder(),
		newReq(t, http.MethodPost, "/openai/v1/chat/completions", body),
	)
	if !strings.Contains(string(cap.bodySeen), `"stream":false`) {
		t.Fatalf("expected stream forced off, got %q", cap.bodySeen)
	}
}

func TestPIIResponseRestoreMiddleware_RestoresMaskStreaming(t *testing.T) {
	reg := redact.NewRegistry()
	ph := reg.Placeholder("PERSON", "Jane Doe")
	pm := providers.NewProviderManager()
	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("hi " + ph))
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req.Header.Set("Accept", "text/event-stream")
	req = req.WithContext(withPIIRegistry(req.Context(), reg))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if rec.Body.String() != "hi Jane Doe" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestPIIResponseRestoreMiddleware_RestoresMaskNonStreaming(t *testing.T) {
	reg := redact.NewRegistry()
	ph := reg.Placeholder("PERSON", "Jane Doe")
	pm := providers.NewProviderManager()
	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"content":"` + ph + `"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(withPIIRegistry(req.Context(), reg))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	want := `{"content":"Jane Doe"}`
	if rec.Body.String() != want {
		t.Fatalf("body = %q want %q", rec.Body.String(), want)
	}
}

// TestPIIResponseRestoreMiddleware_QuoteBearingPersonStaysValidJSON is the
// end-to-end regression test for the production bug: a PERSON original
// containing a double quote and a newline must not corrupt the JSON
// response the client receives, even though the upstream handler here only
// ever emits the placeholder (mirroring how the real upstream LLM only
// sees a MASK placeholder, never the raw PII).
func TestPIIResponseRestoreMiddleware_QuoteBearingPersonStaysValidJSON(t *testing.T) {
	reg := redact.NewRegistry()
	original := "Robert \"Bob\"\nJohnson"
	ph := reg.Placeholder("PERSON", original)
	pm := providers.NewProviderManager()
	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"` + ph + `"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/gemini/v1beta/models/gemini-2.5-flash:generateContent", nil)
	req = req.WithContext(withPIIRegistry(req.Context(), reg))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	body := rec.Body.Bytes()
	if !json.Valid(body) {
		t.Fatalf("response is not valid JSON: %q", body)
	}
	var parsed struct{ Name string }
	if err := json.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("unmarshal: %v (body %q)", err, body)
	}
	if parsed.Name != original {
		t.Fatalf("Name = %q, want %q", parsed.Name, original)
	}
}

// TestPIIResponseRestoreMiddleware_QuoteBearingPersonStaysValidJSONStreaming
// is the streaming counterpart, using an SSE Content-Type as Gemini's
// streamGenerateContent endpoint does.
func TestPIIResponseRestoreMiddleware_QuoteBearingPersonStaysValidJSONStreaming(t *testing.T) {
	reg := redact.NewRegistry()
	original := "Robert \"Bob\"\nJohnson"
	ph := reg.Placeholder("PERSON", original)
	pm := providers.NewProviderManager()
	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(`data: {"name":"` + ph + `"}` + "\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))

	req := httptest.NewRequest(http.MethodPost, "/gemini/v1beta/models/gemini-2.5-flash:streamGenerateContent", nil)
	req.Header.Set("Accept", "text/event-stream")
	req = req.WithContext(withPIIRegistry(req.Context(), reg))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	for _, line := range strings.Split(rec.Body.String(), "\n") {
		jsonData, ok := strings.CutPrefix(line, "data: ")
		if !ok {
			continue
		}
		if jsonData == "" || jsonData == "[DONE]" {
			continue
		}
		if !json.Valid([]byte(jsonData)) {
			t.Fatalf("SSE data line is not valid JSON: %q (full body %q)", jsonData, rec.Body.String())
		}
		var parsed struct{ Name string }
		if err := json.Unmarshal([]byte(jsonData), &parsed); err != nil {
			t.Fatalf("unmarshal: %v (line %q)", err, jsonData)
		}
		if parsed.Name != original {
			t.Fatalf("Name = %q, want %q", parsed.Name, original)
		}
	}
}

// TestPIIResponseRestoreMiddleware_PlainTextFallsBackToVerbatimRestore
// checks that a non-JSON Content-Type is not JSON-escaped: a plain-text
// response should get the original back byte-for-byte, matching
// RestoreUserFacing rather than RestoreUserFacingJSON.
func TestPIIResponseRestoreMiddleware_PlainTextFallsBackToVerbatimRestore(t *testing.T) {
	reg := redact.NewRegistry()
	original := "Robert \"Bob\" Johnson"
	ph := reg.Placeholder("PERSON", original)
	pm := providers.NewProviderManager()
	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello " + ph))
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(withPIIRegistry(req.Context(), reg))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	want := "hello " + original
	if rec.Body.String() != want {
		t.Fatalf("body = %q want %q", rec.Body.String(), want)
	}
}

func TestPIIResponseRestoreMiddleware_SealStaysOpaque(t *testing.T) {
	reg := redact.NewRegistry()
	ph := reg.Placeholder("US_SSN", "222-33-4444")
	pm := providers.NewProviderManager()
	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"text":"` + ph + `"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(withPIIRegistry(req.Context(), reg))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), ph) {
		t.Fatalf("SEAL placeholder leaked restore: %q", rec.Body.String())
	}
}
