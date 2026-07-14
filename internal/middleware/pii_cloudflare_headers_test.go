package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/redact"
)

// orderSpyResponseWriter records whether any body bytes are written before
// X-LLM-PII-Restored appears on response headers — the Cloudflare-safe
// non-streaming contract (buffer, then headers including Restored/Leaked, then body).
type orderSpyResponseWriter struct {
	http.ResponseWriter
	sawBodyBeforeRestored bool
	wroteBody             bool
}

func (w *orderSpyResponseWriter) Write(b []byte) (int, error) {
	if len(b) > 0 {
		w.wroteBody = true
		if w.Header().Get("X-LLM-PII-Restored") == "" {
			w.sawBodyBeforeRestored = true
		}
	}
	return w.ResponseWriter.Write(b)
}

func assertNoPIITrailers(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if got := rec.Header().Get("Trailer"); got != "" {
		t.Fatalf("Trailer = %q, want empty (Cloudflare breaks trailers)", got)
	}
	for _, name := range []string{"X-LLM-PII-Restored", "X-LLM-PII-Leaked"} {
		if got := rec.Result().Trailer.Get(name); got != "" {
			t.Fatalf("%s trailer = %q, want empty", name, got)
		}
	}
}

func TestPIIResponseRestore_NonStreaming_RestoredLeakedAreNormalHeaders(t *testing.T) {
	reg := redact.NewRegistry()
	ph := reg.Placeholder("PERSON", "Ada Lovelace")
	pm := providers.NewProviderManager()

	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"text":"` + ph + `"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	summary := newPIISummary(PIIOutcomeOK, map[string]int{"PERSON": 1})
	req = req.WithContext(attachPIISummary(withPIIRegistry(req.Context(), reg), summary))

	rec := httptest.NewRecorder()
	spy := &orderSpyResponseWriter{ResponseWriter: rec}
	mw.ServeHTTP(spy, req)

	assertNoPIITrailers(t, rec)
	if !spy.wroteBody {
		t.Fatal("expected body to be written")
	}
	if spy.sawBodyBeforeRestored {
		t.Fatal("body written before X-LLM-PII-Restored header was set")
	}
	if got := rec.Header().Get("X-LLM-PII-Restored"); got != "1" {
		t.Fatalf("Restored header = %q, want 1", got)
	}
	if got := rec.Header().Get("X-LLM-PII-Leaked"); got != "0" {
		t.Fatalf("Leaked header = %q, want 0", got)
	}
	if got := rec.Header().Get("X-LLM-PII-Detected"); got != "1" {
		t.Fatalf("Detected header = %q, want 1", got)
	}
	if !strings.Contains(rec.Body.String(), "Ada Lovelace") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestPIIResponseRestore_NonStreaming_ChunkedWritesStillEmitHeaders(t *testing.T) {
	// ReverseProxy often copies the upstream body in multiple Write calls.
	reg := redact.NewRegistry()
	ph := reg.Placeholder("EMAIL_ADDRESS", "chunks@example.com")
	pm := providers.NewProviderManager()

	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		prefix := `{"text":"`
		suffix := `"}`
		_, _ = w.Write([]byte(prefix))
		_, _ = w.Write([]byte(ph))
		_, _ = w.Write([]byte(suffix))
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	summary := newPIISummary(PIIOutcomeOK, map[string]int{"EMAIL_ADDRESS": 1})
	req = req.WithContext(attachPIISummary(withPIIRegistry(req.Context(), reg), summary))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	assertNoPIITrailers(t, rec)
	if got := rec.Header().Get("X-LLM-PII-Restored"); got != "1" {
		t.Fatalf("Restored header = %q, want 1", got)
	}
	if got := rec.Header().Get("X-LLM-PII-Leaked"); got != "0" {
		t.Fatalf("Leaked header = %q, want 0", got)
	}
	if !strings.Contains(rec.Body.String(), "chunks@example.com") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestPIIResponseRestore_StreamingBodyFlag_OmitsRestoredLeaked(t *testing.T) {
	reg := redact.NewRegistry()
	ph := reg.Placeholder("EMAIL_ADDRESS", "body-stream@example.com")
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())

	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected Flusher")
		}
		_, _ = w.Write([]byte("data: {\"delta\":\""))
		flusher.Flush()
		_, _ = w.Write([]byte(ph + "\"}\n\n"))
		flusher.Flush()
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Content-Type", "application/json")
	summary := newPIISummary(PIIOutcomeOK, map[string]int{"EMAIL_ADDRESS": 1})
	req = req.WithContext(attachPIISummary(withPIIRegistry(req.Context(), reg), summary))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	assertNoPIITrailers(t, rec)
	if got := rec.Header().Get("X-LLM-PII-Restored"); got != "" {
		t.Fatalf("Restored = %q, want empty for streaming", got)
	}
	if got := rec.Header().Get("X-LLM-PII-Leaked"); got != "" {
		t.Fatalf("Leaked = %q, want empty for streaming", got)
	}
	if got := rec.Header().Get("X-LLM-PII-Detected"); got != "1" {
		t.Fatalf("Detected = %q, want 1", got)
	}
	if !strings.Contains(rec.Body.String(), "body-stream@example.com") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestPIIResponseRestore_Streaming_FlushDoesNotAnnounceTrailers(t *testing.T) {
	reg := redact.NewRegistry()
	ph := reg.Placeholder("PERSON", "Grace Hopper")
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())

	var trailerAtFirstFlush string
	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("data: {\"t\":\"" + ph + "\"}\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		trailerAtFirstFlush = w.Header().Get("Trailer")
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req.Header.Set("Accept", "text/event-stream")
	summary := newPIISummary(PIIOutcomeOK, map[string]int{"PERSON": 1})
	req = req.WithContext(attachPIISummary(withPIIRegistry(req.Context(), reg), summary))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if trailerAtFirstFlush != "" {
		t.Fatalf("Trailer at first flush = %q, want empty", trailerAtFirstFlush)
	}
	assertNoPIITrailers(t, rec)
}

func TestClientGzipWithPIIRestore_RestoredIsHeaderNotTrailer(t *testing.T) {
	reg := redact.NewRegistry()
	email := "gzip-header@example.com"
	ph := reg.Placeholder("EMAIL_ADDRESS", email)
	pm := wireTestProviderManager(t)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"text":"` + ph + `"}`))
	})
	chain := ClientGzipMiddleware(pm)(PIIResponseRestoreMiddleware(pm)(inner))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	summary := newPIISummary(PIIOutcomeOK, map[string]int{"EMAIL_ADDRESS": 1})
	req = req.WithContext(attachPIISummary(withPIIRegistry(req.Context(), reg), summary))
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	assertNoPIITrailers(t, rec)
	if got := rec.Header().Get("X-LLM-PII-Restored"); got != "1" {
		t.Fatalf("Restored header = %q, want 1 (gzip+PII must not rely on trailers)", got)
	}
	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
}
