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

func TestClientAcceptsGzip(t *testing.T) {
	cases := []struct {
		ae   string
		want bool
	}{
		{"", false},
		{"identity", false},
		{"gzip", true},
		{"gzip, deflate", true},
		{"deflate, gzip;q=0.9", true},
		{"*", true},
	}
	for _, tc := range cases {
		if got := clientAcceptsGzip(tc.ae); got != tc.want {
			t.Fatalf("clientAcceptsGzip(%q) = %v, want %v", tc.ae, got, tc.want)
		}
	}
}

func TestClientGzipMiddleware_CompressesNonStreaming(t *testing.T) {
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())

	body := strings.Repeat("restore-me ", 64)
	chain := ClientGzipMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	if len(rec.Body.Bytes()) < 2 || rec.Body.Bytes()[0] != 0x1f || rec.Body.Bytes()[1] != 0x8b {
		t.Fatal("expected gzip magic in response body")
	}
	gr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gr.Close()
	plain, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	if string(plain) != body {
		t.Fatalf("decompressed = %q", string(plain))
	}
}

func TestClientGzipMiddleware_SkipsStreaming(t *testing.T) {
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())

	chain := ClientGzipMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("event: ping\ndata: {}\n\n"))
	}))

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if rec.Header().Get("Content-Encoding") == "gzip" {
		t.Fatal("streaming response should not be client-gzipped")
	}
	if !strings.Contains(rec.Body.String(), "event: ping") {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestClientGzipMiddleware_StashesAcceptEncodingInContext(t *testing.T) {
	pm := providers.NewProviderManager()
	var seen string
	chain := ClientGzipMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = clientAcceptEncodingFromContext(r.Context())
		_, _ = w.Write([]byte("ok"))
	}))

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Accept-Encoding", "gzip, deflate")
	rec := httptest.NewRecorder()
	chain.ServeHTTP(rec, req)

	if seen != "gzip, deflate" {
		t.Fatalf("stashed Accept-Encoding = %q", seen)
	}
}

func TestClientGzipWithPIIRestoreStack(t *testing.T) {
	reg := redact.NewRegistry()
	email := "gzip-pii@example.com"
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

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	gr, err := gzip.NewReader(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gr.Close()
	plain, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read gzip: %v", err)
	}
	if !strings.Contains(string(plain), email) {
		t.Fatalf("decompressed body missing email: %q", string(plain))
	}
	if strings.Contains(string(plain), ph) {
		t.Fatalf("placeholder leaked in decompressed body: %q", string(plain))
	}
	if got := piiMetricFromResponse(rec, "X-LLM-PII-Restored"); got != "1" {
		t.Fatalf("restored = %q, want 1", got)
	}
}

func TestClientGzipResponseWriter_WriteAfterFinishReturnsError(t *testing.T) {
	rec := httptest.NewRecorder()
	cw := &clientGzipResponseWriter{ResponseWriter: rec}
	_, _ = cw.Write([]byte("hello"))
	if err := cw.finish(); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if _, err := cw.Write([]byte("late")); err == nil {
		t.Fatal("expected error on write after finish")
	}
}
