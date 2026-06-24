package middleware

import (
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
			out := strings.Replace(in, "Jane Doe", "<PERSON_1>", 1)
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

	if !strings.Contains(string(cap.bodySeen), "<PERSON_1>") {
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
