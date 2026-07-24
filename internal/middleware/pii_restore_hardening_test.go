package middleware

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/redact"
)

func TestPIIResponseRestoreMiddleware_RestoresGzipNonStreaming(t *testing.T) {
	reg := redact.NewRegistry()
	ph := reg.Placeholder("PERSON", "Jane Doe")
	pm := providers.NewProviderManager()

	var plain bytes.Buffer
	gz := gzip.NewWriter(&plain)
	_, _ = gz.Write([]byte(`{"text":"` + ph + `"}`))
	_ = gz.Close()

	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(plain.Bytes())
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(withPIIRegistry(req.Context(), reg))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	want := `{"text":"Jane Doe"}`
	if rec.Body.String() != want {
		t.Fatalf("body = %q want %q", rec.Body.String(), want)
	}
}

func TestPIIResponseRestoreMiddleware_LeakedPlaceholderHeader(t *testing.T) {
	reg := redact.NewRegistry()
	ph := reg.Placeholder("EMAIL_ADDRESS", "leak@example.com")
	pm := providers.NewProviderManager()

	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"text":"` + ph + `"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	summary := newPIISummary(PIIOutcomeOK, map[string]int{"EMAIL_ADDRESS": 1})
	req = req.WithContext(attachPIISummary(withPIIRegistry(req.Context(), reg), summary))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if got := piiMetricFromResponse(rec, "X-LLM-PII-Leaked"); got != "0" {
		t.Fatalf("leaked = %q, want 0 after successful restore", got)
	}
	if got := piiMetricFromResponse(rec, "X-LLM-PII-Restored"); got != "1" {
		t.Fatalf("restored = %q, want 1", got)
	}
}

func TestPIIResponseRestoreMiddleware_DetectsLeakedPlaceholder(t *testing.T) {
	reg := redact.NewRegistry()
	_ = reg.Placeholder("EMAIL_ADDRESS", "hidden@example.com")
	pm := providers.NewProviderManager()

	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"text":"model paraphrased without token"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	summary := newPIISummary(PIIOutcomeOK, map[string]int{"EMAIL_ADDRESS": 1})
	req = req.WithContext(attachPIISummary(withPIIRegistry(req.Context(), reg), summary))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if got := piiMetricFromResponse(rec, "X-LLM-PII-Restored"); got != "0" {
		t.Fatalf("restored = %q, want 0", got)
	}
	if got := piiMetricFromResponse(rec, "X-LLM-PII-Leaked"); got != "0" {
		t.Fatalf("leaked = %q, want 0 when placeholder not echoed", got)
	}
}

func TestPIIResponseRestoreMiddleware_RestoresInPlace(t *testing.T) {
	reg := redact.NewRegistry()
	ph := reg.Placeholder("EMAIL_ADDRESS", "still@example.com")
	pm := providers.NewProviderManager()

	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"text":"prefix ` + ph + ` suffix"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	summary := newPIISummary(PIIOutcomeOK, map[string]int{"EMAIL_ADDRESS": 1})
	req = req.WithContext(attachPIISummary(withPIIRegistry(req.Context(), reg), summary))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if got := piiMetricFromResponse(rec, "X-LLM-PII-Restored"); got != "1" {
		t.Fatalf("restored = %q, want 1", got)
	}
	if got := piiMetricFromResponse(rec, "X-LLM-PII-Leaked"); got != "0" {
		t.Fatalf("leaked = %q, want 0 after restore", got)
	}
}

func TestPIIResponseRestoreMiddleware_StreamingOmitsRestoredLeakedHeaders(t *testing.T) {
	reg := redact.NewRegistry()
	ph := reg.Placeholder("EMAIL_ADDRESS", "stream@example.com")
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())

	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("data: {\"text\":\"" + ph + "\"}\n\n"))
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req.Header.Set("Accept", "text/event-stream")
	summary := newPIISummary(PIIOutcomeOK, map[string]int{"EMAIL_ADDRESS": 1})
	req = req.WithContext(attachPIISummary(withPIIRegistry(req.Context(), reg), summary))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	// Restored/Leaked are only known after the stream ends; sending them as
	// trailers breaks Cloudflare. Streaming responses expose the early PII
	// headers but omit Restored/Leaked.
	if got := rec.Header().Get("Trailer"); got != "" {
		t.Fatalf("Trailer header = %q, want empty (Cloudflare-unsafe)", got)
	}
	if got := piiMetricFromResponse(rec, "X-LLM-PII-Restored"); got != "" {
		t.Fatalf("restored = %q, want empty on streaming responses", got)
	}
	if got := piiMetricFromResponse(rec, "X-LLM-PII-Detected"); got != "1" {
		t.Fatalf("detected = %q, want 1", got)
	}
	if !strings.Contains(rec.Body.String(), "stream@example.com") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestPIIResponseRestoreMiddleware_NonStreamingRestoredInHeaders(t *testing.T) {
	reg := redact.NewRegistry()
	ph := reg.Placeholder("EMAIL_ADDRESS", "header@example.com")
	pm := providers.NewProviderManager()

	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"text":"` + ph + `"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	summary := newPIISummary(PIIOutcomeOK, map[string]int{"EMAIL_ADDRESS": 1})
	req = req.WithContext(attachPIISummary(withPIIRegistry(req.Context(), reg), summary))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if got := rec.Header().Get("Trailer"); got != "" {
		t.Fatalf("Trailer header = %q, want empty", got)
	}
	if got := rec.Header().Get("X-LLM-PII-Restored"); got != "1" {
		t.Fatalf("restored header = %q, want 1", got)
	}
	if got := rec.Result().Trailer.Get("X-LLM-PII-Restored"); got != "" {
		t.Fatalf("restored trailer = %q, want empty", got)
	}
}

func TestPIIResponseRestoreMiddleware_RestoresHTMLEscapedPlaceholder(t *testing.T) {
	reg := redact.NewRegistry()
	ph := reg.Placeholder("EMAIL_ADDRESS", "html@example.com")
	escaped := strings.ReplaceAll(strings.ReplaceAll(ph, "<", "&lt;"), ">", "&gt;")
	pm := providers.NewProviderManager()

	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<p>` + escaped + `</p>`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(withPIIRegistry(req.Context(), reg))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "html@example.com") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestPIIRedactMiddleware_WireModeStripsAcceptEncoding(t *testing.T) {
	r := &fakeRedactor{scrubFn: func(in string, reg *redact.Registry) (redact.Result, error) {
		out := strings.Replace(in, "secret@example.com", reg.Placeholder("EMAIL_ADDRESS", "secret@example.com"), 1)
		return redact.Result{Text: out, EntityCounts: map[string]int{"EMAIL_ADDRESS": 1}}, nil
	}}
	cap := &captureHandler{}
	mw := PIIRedactMiddleware(r, PIIRedactConfig{
		GlobalEnabled:    true,
		WirePlaceholders: true,
	})(cap)

	body := `{"messages":[{"role":"user","content":"secret@example.com"}]}`
	req := newReq(t, http.MethodPost, "/openai/v1/chat/completions", body)
	req.Header.Set("Accept-Encoding", "gzip")
	mw.ServeHTTP(httptest.NewRecorder(), req)

	if got := cap.reqSeen.Header.Get("Accept-Encoding"); got != "" {
		t.Fatalf("Accept-Encoding should be stripped in wire mode, got %q", got)
	}
}

func TestPIIResponseRestoreMiddleware_RestoresSquareBracketPIIPlaceholder(t *testing.T) {
	reg := redact.NewRegistry()
	_ = reg.Placeholder("PERSON", "Eric Hagman")
	pm := providers.NewProviderManager()

	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"content":"Your full name is **[PII_PERSON_1]**!"}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	summary := newPIISummary(PIIOutcomeOK, map[string]int{"PERSON": 1})
	req = req.WithContext(attachPIISummary(withPIIRegistry(req.Context(), reg), summary))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !strings.Contains(rec.Body.String(), "Eric Hagman") {
		t.Fatalf("body = %q", rec.Body.String())
	}
	if reg.MaskPlaceholdersRemaining(rec.Body.String()) != 0 {
		t.Fatalf("placeholder leaked: %q", rec.Body.String())
	}
	if got := piiMetricFromResponse(rec, "X-LLM-PII-Leaked"); got != "0" {
		t.Fatalf("leaked = %q, want 0", got)
	}
}

// TestPIIResponseRestoreMiddleware_WriteHonorsIoCopyContract reproduces the
// production ALB 502s: httputil.ReverseProxy copies the upstream body with
// io.Copy, which errors (io.ErrShortWrite / invalid write) whenever Write
// reports n != len(input) — and ReverseProxy then panics with
// http.ErrAbortHandler, resetting the client connection. Restoring a
// placeholder changes the byte count, so Write must report the input length,
// not the restored length.
func TestPIIResponseRestoreMiddleware_WriteHonorsIoCopyContract(t *testing.T) {
	reg := redact.NewRegistry()
	ph := reg.Placeholder("PERSON", "Jane Doe")
	pm := providers.NewProviderManager()

	body := `{"content":"` + ph + `"}`
	var copyErr error
	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, copyErr = io.Copy(w, strings.NewReader(body))
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(withPIIRegistry(req.Context(), reg))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if copyErr != nil {
		t.Fatalf("io.Copy failed (ReverseProxy would panic with ErrAbortHandler): %v", copyErr)
	}
	want := `{"content":"Jane Doe"}`
	if rec.Body.String() != want {
		t.Fatalf("body = %q want %q", rec.Body.String(), want)
	}
}

// Gzip variant of the io.Copy contract test: the decompressed+restored body
// is much longer than the compressed input chunk, so a Write that reports
// the restored length returns n > len(input), which io.Copy also treats as
// a fatal error.
func TestPIIResponseRestoreMiddleware_WriteHonorsIoCopyContractGzip(t *testing.T) {
	reg := redact.NewRegistry()
	ph := reg.Placeholder("PERSON", "Jane Doe")
	pm := providers.NewProviderManager()

	var compressed bytes.Buffer
	gz := gzip.NewWriter(&compressed)
	_, _ = gz.Write([]byte(`{"content":"` + ph + `","pad":"` + strings.Repeat("a", 2048) + `"}`))
	_ = gz.Close()

	var copyErr error
	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, copyErr = io.Copy(w, bytes.NewReader(compressed.Bytes()))
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(withPIIRegistry(req.Context(), reg))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if copyErr != nil {
		t.Fatalf("io.Copy failed (ReverseProxy would panic with ErrAbortHandler): %v", copyErr)
	}
	if !strings.Contains(rec.Body.String(), "Jane Doe") {
		t.Fatalf("body = %q, want restored PII", rec.Body.String())
	}
}

func TestPIIResponseRestoreMiddleware_GunzipFailurePassthrough(t *testing.T) {
	reg := redact.NewRegistry()
	_ = reg.Placeholder("EMAIL_ADDRESS", "a@b.com")
	pm := providers.NewProviderManager()

	corruptGzip := []byte{0x1f, 0x8b, 0x00, 0x00}
	mw := PIIResponseRestoreMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(corruptGzip)
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(withPIIRegistry(req.Context(), reg))
	rec := httptest.NewRecorder()
	mw.ServeHTTP(rec, req)

	if !bytes.Equal(rec.Body.Bytes(), corruptGzip) {
		t.Fatalf("expected corrupt gzip passthrough, got %x", rec.Body.Bytes())
	}
}
