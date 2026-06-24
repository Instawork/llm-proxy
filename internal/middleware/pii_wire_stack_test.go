package middleware

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/redact"
)

// productionPIIWireStack mirrors cmd/llm-proxy/main.go middleware order for
// the response-restore path: TokenParsing → PIIResponseRestore → Streaming.
func productionPIIWireStack(pm *providers.ProviderManager, handler http.Handler) http.Handler {
	return TokenParsingMiddleware(pm)(
		PIIResponseRestoreMiddleware(pm)(
			StreamingMiddleware(pm)(handler),
		),
	)
}

func wireTestProviderManager(t *testing.T) *providers.ProviderManager {
	t.Helper()
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())
	pm.RegisterProvider(providers.NewAnthropicProxy())
	pm.RegisterProvider(providers.NewGeminiProxy())
	return pm
}

func TestProductionPIIWireStack_AnthropicNonStreaming_RestoresMaskEmail(t *testing.T) {
	reg := redact.NewRegistry()
	email := "pii-restore-test@example.com"
	ph := reg.Placeholder("EMAIL_ADDRESS", email)
	pm := wireTestProviderManager(t)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"type":"message","content":[{"type":"text","text":"%s"}]}`, ph)
	})

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", nil)
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withPIIRegistry(req.Context(), reg))

	rec := httptest.NewRecorder()
	productionPIIWireStack(pm, handler).ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, ph) {
		t.Fatalf("MASK placeholder leaked to client: %q", body)
	}
	if !strings.Contains(body, email) {
		t.Fatalf("expected restored email %q in %q", email, body)
	}
}

func TestProductionPIIWireStack_AnthropicStreaming_RestoresMaskEmail(t *testing.T) {
	reg := redact.NewRegistry()
	email := "stream-restore@example.com"
	ph := reg.Placeholder("EMAIL_ADDRESS", email)
	pm := wireTestProviderManager(t)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: content_block_delta\n")
		_, _ = fmt.Fprintf(w, "data: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"%s\"}}\n\n", ph)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	})

	body := `{"model":"claude-haiku-4-5","max_tokens":40,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withPIIRegistry(req.Context(), reg))

	rec := httptest.NewRecorder()
	productionPIIWireStack(pm, handler).ServeHTTP(rec, req)

	out := rec.Body.String()
	if strings.Contains(out, ph) {
		t.Fatalf("MASK placeholder leaked in streaming body: %q", out)
	}
	if !strings.Contains(out, email) {
		t.Fatalf("expected restored email %q in streaming body %q", email, out)
	}
}

func TestProductionPIIWireStack_AnthropicStreaming_SplitPlaceholder(t *testing.T) {
	reg := redact.NewRegistry()
	email := "split-restore@example.com"
	ph := reg.Placeholder("EMAIL_ADDRESS", email)
	pm := wireTestProviderManager(t)

	// Split the response bytes mid-placeholder — how httputil.ReverseProxy
	// delivers a streaming body when TCP chunks fall inside a token.
	full := fmt.Sprintf(
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"%s\"}}\n\n",
		ph,
	)
	mid := strings.Index(full, ph) + len(ph)/2
	part1, part2 := full[:mid], full[mid:]

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(part1))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte(part2))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	})

	body := `{"model":"claude-haiku-4-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withPIIRegistry(req.Context(), reg))

	rec := httptest.NewRecorder()
	productionPIIWireStack(pm, handler).ServeHTTP(rec, req)

	out := rec.Body.String()
	if strings.Contains(out, ph) {
		t.Fatalf("MASK placeholder leaked after split-chunk restore: %q", out)
	}
	if !strings.Contains(out, email) {
		t.Fatalf("expected restored email %q in %q", email, out)
	}
}

func TestProductionPIIWireStack_OpenAINonStreaming_RestoresMaskEmail(t *testing.T) {
	reg := redact.NewRegistry()
	email := "openai-restore@example.com"
	ph := reg.Placeholder("EMAIL_ADDRESS", email)
	pm := wireTestProviderManager(t)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"choices":[{"message":{"content":"%s"}}]}`, ph)
	})

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", nil)
	req = req.WithContext(withPIIRegistry(req.Context(), reg))

	rec := httptest.NewRecorder()
	productionPIIWireStack(pm, handler).ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, ph) {
		t.Fatalf("MASK placeholder leaked to client: %q", body)
	}
	if !strings.Contains(body, email) {
		t.Fatalf("expected restored email %q in %q", email, body)
	}
}

func TestProductionPIIWireStack_OpenAIStreaming_RestoresMaskEmail(t *testing.T) {
	reg := redact.NewRegistry()
	email := "openai-stream-restore@example.com"
	ph := reg.Placeholder("EMAIL_ADDRESS", email)
	pm := wireTestProviderManager(t)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"%s\"}}]}\n\n", ph)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	})

	body := `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(withPIIRegistry(req.Context(), reg))

	rec := httptest.NewRecorder()
	productionPIIWireStack(pm, handler).ServeHTTP(rec, req)

	out := rec.Body.String()
	if strings.Contains(out, ph) {
		t.Fatalf("MASK placeholder leaked in OpenAI streaming body: %q", out)
	}
	if !strings.Contains(out, email) {
		t.Fatalf("expected restored email %q in %q", email, out)
	}
}

func TestProductionPIIWireStack_OpenAIStreaming_SplitPlaceholder(t *testing.T) {
	reg := redact.NewRegistry()
	email := "openai-split-restore@example.com"
	ph := reg.Placeholder("EMAIL_ADDRESS", email)
	pm := wireTestProviderManager(t)

	full := fmt.Sprintf("data: {\"choices\":[{\"delta\":{\"content\":\"%s\"}}]}\n\n", ph)
	mid := strings.Index(full, ph) + len(ph)/2
	part1, part2 := full[:mid], full[mid:]

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(part1))
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = w.Write([]byte(part2))
	})

	body := `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(body))
	req = req.WithContext(withPIIRegistry(req.Context(), reg))

	rec := httptest.NewRecorder()
	productionPIIWireStack(pm, handler).ServeHTTP(rec, req)

	out := rec.Body.String()
	if strings.Contains(out, ph) {
		t.Fatalf("MASK placeholder leaked after split-chunk restore: %q", out)
	}
	if !strings.Contains(out, email) {
		t.Fatalf("expected restored email %q in %q", email, out)
	}
}

func TestProductionPIIWireStack_GeminiNonStreaming_RestoresMaskEmail(t *testing.T) {
	reg := redact.NewRegistry()
	email := "gemini-restore@example.com"
	ph := reg.Placeholder("EMAIL_ADDRESS", email)
	pm := wireTestProviderManager(t)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"candidates":[{"content":{"parts":[{"text":"%s"}]}}]}`, ph)
	})

	req := httptest.NewRequest(http.MethodPost, "/gemini/v1beta/models/gemini-2.5-flash:generateContent", nil)
	req = req.WithContext(withPIIRegistry(req.Context(), reg))

	rec := httptest.NewRecorder()
	productionPIIWireStack(pm, handler).ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, ph) {
		t.Fatalf("MASK placeholder leaked to client: %q", body)
	}
	if !strings.Contains(body, email) {
		t.Fatalf("expected restored email %q in %q", email, body)
	}
}

func TestProductionPIIWireStack_GeminiStreaming_RestoresMaskEmail(t *testing.T) {
	reg := redact.NewRegistry()
	email := "gemini-stream-restore@example.com"
	ph := reg.Placeholder("EMAIL_ADDRESS", email)
	pm := wireTestProviderManager(t)

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"%s\"}]}}]}\n\n", ph)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	})

	req := httptest.NewRequest(http.MethodPost, "/gemini/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse", nil)
	req = req.WithContext(withPIIRegistry(req.Context(), reg))

	rec := httptest.NewRecorder()
	productionPIIWireStack(pm, handler).ServeHTTP(rec, req)

	out := rec.Body.String()
	if strings.Contains(out, ph) {
		t.Fatalf("MASK placeholder leaked in Gemini streaming body: %q", out)
	}
	if !strings.Contains(out, email) {
		t.Fatalf("expected restored email %q in %q", email, out)
	}
}

func TestPIIRedactWireStack_OpenAI_EndToEnd(t *testing.T) {
	testPIIRedactWireStackProviderEndToEnd(t, wireStackOpenAICase())
}

func TestPIIRedactWireStack_Gemini_EndToEnd(t *testing.T) {
	testPIIRedactWireStackProviderEndToEnd(t, wireStackGeminiCase())
}

type wireStackProviderCase struct {
	name         string
	path         string
	requestBody  func(email string) string
	upstreamText func(body []byte) string
	responseBody func(echo string) string
}

func wireStackOpenAICase() wireStackProviderCase {
	return wireStackProviderCase{
		name: "openai",
		path: "/openai/v1/chat/completions",
		requestBody: func(email string) string {
			return fmt.Sprintf(
				`{"model":"gpt-4o-mini","max_tokens":40,"messages":[{"role":"user","content":"My email is %s. Reply with ONLY that email."}]}`,
				email,
			)
		},
		upstreamText: userContentFromChatBody,
		responseBody: func(echo string) string {
			return fmt.Sprintf(`{"choices":[{"message":{"content":"%s"}}]}`, echo)
		},
	}
}

func wireStackGeminiCase() wireStackProviderCase {
	return wireStackProviderCase{
		name: "gemini",
		path: "/gemini/v1beta/models/gemini-2.5-flash:generateContent",
		requestBody: func(email string) string {
			return fmt.Sprintf(
				`{"contents":[{"parts":[{"text":"My email is %s. Reply with ONLY that email."}]}]}`,
				email,
			)
		},
		upstreamText: geminiUserTextFromBody,
		responseBody: func(echo string) string {
			return fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"text":"%s"}]}}]}`, echo)
		},
	}
}

func testPIIRedactWireStackProviderEndToEnd(t *testing.T, tc wireStackProviderCase) {
	t.Helper()
	const email = "alice@example.com"
	redactor := &fakeRedactor{
		scrubFn: func(in string, reg *redact.Registry) (redact.Result, error) {
			ph := reg.Placeholder("EMAIL_ADDRESS", email)
			out := strings.Replace(in, email, ph, 1)
			return redact.Result{
				Text:         out,
				EntityCounts: map[string]int{"EMAIL_ADDRESS": 1},
			}, nil
		},
	}
	pm := wireTestProviderManager(t)

	var upstreamBody []byte
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		echo := tc.upstreamText(upstreamBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(tc.responseBody(echo)))
	})

	stack := PIIRedactMiddleware(redactor, PIIRedactConfig{
		GlobalEnabled:         true,
		WirePlaceholders:      true,
		DefaultAllowStreaming: true,
	})(productionPIIWireStack(pm, handler))

	req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.requestBody(email)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("%s stack returned %d: %s", tc.name, rec.Code, rec.Body.String())
	}
	if strings.Contains(string(upstreamBody), email) {
		t.Fatalf("%s upstream saw raw email: %q", tc.name, upstreamBody)
	}
	if !strings.Contains(string(upstreamBody), "<EMAIL_ADDRESS_1>") {
		t.Fatalf("%s upstream missing placeholder: %q", tc.name, upstreamBody)
	}
	respBody := rec.Body.String()
	if !strings.Contains(respBody, email) {
		t.Fatalf("%s client response missing restored email: %q", tc.name, respBody)
	}
	if strings.Contains(respBody, "<EMAIL_ADDRESS_") {
		t.Fatalf("%s MASK placeholder leaked after restore: %q", tc.name, respBody)
	}
}

func geminiUserTextFromBody(body []byte) string {
	var root struct {
		Contents []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
	}
	if err := json.Unmarshal(body, &root); err != nil || len(root.Contents) == 0 || len(root.Contents[0].Parts) == 0 {
		return ""
	}
	return root.Contents[0].Parts[0].Text
}

func TestPIIRedactWireStack_Anthropic_EndToEnd(t *testing.T) {
	const email = "alice@example.com"
	redactor := &fakeRedactor{
		scrubFn: func(in string, reg *redact.Registry) (redact.Result, error) {
			ph := reg.Placeholder("EMAIL_ADDRESS", email)
			out := strings.Replace(in, email, ph, 1)
			return redact.Result{
				Text:         out,
				EntityCounts: map[string]int{"EMAIL_ADDRESS": 1},
			}, nil
		},
	}
	pm := wireTestProviderManager(t)

	var upstreamBody []byte
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		echo := anthropicUserTextFromBody(upstreamBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"content":[{"type":"text","text":"%s"}]}`, echo)
	})

	stack := PIIRedactMiddleware(redactor, PIIRedactConfig{
		GlobalEnabled:         true,
		WirePlaceholders:      true,
		DefaultAllowStreaming: true,
	})(productionPIIWireStack(pm, handler))

	original := fmt.Sprintf(
		`{"model":"claude-haiku-4-5","max_tokens":40,"messages":[{"role":"user","content":"My email is %s. Reply with ONLY that email."}]}`,
		email,
	)
	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(original))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("stack returned %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(string(upstreamBody), email) {
		t.Fatalf("upstream saw raw email: %q", upstreamBody)
	}
	if !strings.Contains(string(upstreamBody), "<EMAIL_ADDRESS_1>") {
		t.Fatalf("upstream missing placeholder: %q", upstreamBody)
	}
	respBody := rec.Body.String()
	if !strings.Contains(respBody, email) {
		t.Fatalf("client response missing restored email: %q", respBody)
	}
	if strings.Contains(respBody, "<EMAIL_ADDRESS_") {
		t.Fatalf("MASK placeholder leaked after restore: %q", respBody)
	}
}

func anthropicUserTextFromBody(body []byte) string {
	var root struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &root); err != nil || len(root.Messages) == 0 {
		return ""
	}
	return root.Messages[0].Content
}
