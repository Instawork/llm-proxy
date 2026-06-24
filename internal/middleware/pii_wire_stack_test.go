package middleware

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/redact"
)

type wireRestoreStackCase struct {
	name        string
	path        string
	streaming   bool
	splitChunk  bool
	requestBody string
	responseFmt string
}

func TestProductionPIIWireStack_RestoresMaskEmail(t *testing.T) {
	cases := []wireRestoreStackCase{
		{
			name:        "anthropic-non-streaming",
			path:        "/anthropic/v1/messages",
			responseFmt: `{"type":"message","content":[{"type":"text","text":"%s"}]}`,
		},
		{
			name:        "anthropic-streaming",
			path:        "/anthropic/v1/messages",
			streaming:   true,
			requestBody: `{"model":"claude-haiku-4-5","max_tokens":40,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			responseFmt: "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"%s\"}}\n\n",
		},
		{
			name:        "anthropic-streaming-split",
			path:        "/anthropic/v1/messages",
			streaming:   true,
			splitChunk:  true,
			requestBody: `{"model":"claude-haiku-4-5","max_tokens":40,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			responseFmt: "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"%s\"}}\n\n",
		},
		{
			name:        "openai-non-streaming",
			path:        "/openai/v1/chat/completions",
			responseFmt: `{"choices":[{"message":{"content":"%s"}}]}`,
		},
		{
			name:        "openai-streaming",
			path:        "/openai/v1/chat/completions",
			streaming:   true,
			requestBody: `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			responseFmt: "data: {\"choices\":[{\"delta\":{\"content\":\"%s\"}}]}\n\n",
		},
		{
			name:        "openai-streaming-split",
			path:        "/openai/v1/chat/completions",
			streaming:   true,
			splitChunk:  true,
			requestBody: `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			responseFmt: "data: {\"choices\":[{\"delta\":{\"content\":\"%s\"}}]}\n\n",
		},
		{
			name:        "gemini-non-streaming",
			path:        "/gemini/v1beta/models/gemini-2.5-flash:generateContent",
			responseFmt: `{"candidates":[{"content":{"parts":[{"text":"%s"}]}}]}`,
		},
		{
			name:        "gemini-streaming",
			path:        "/gemini/v1beta/models/gemini-2.5-flash:streamGenerateContent?alt=sse",
			streaming:   true,
			responseFmt: "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"%s\"}]}}]}\n\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runWireRestoreStackCase(t, tc)
		})
	}
}

func runWireRestoreStackCase(t *testing.T, tc wireRestoreStackCase) {
	t.Helper()
	reg := redact.NewRegistry()
	email := fmt.Sprintf("restore-%s@example.com", tc.name)
	ph := reg.Placeholder("EMAIL_ADDRESS", email)
	pm := wireTestProviderManager(t)

	full := fmt.Sprintf(tc.responseFmt, ph)
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if tc.streaming {
			w.Header().Set("Content-Type", "text/event-stream")
		} else {
			w.Header().Set("Content-Type", "application/json")
		}
		if tc.splitChunk {
			mid := strings.Index(full, ph) + len(ph)/2
			part1, part2 := full[:mid], full[mid:]
			_, _ = w.Write([]byte(part1))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			_, _ = w.Write([]byte(part2))
			return
		}
		_, _ = w.Write([]byte(full))
		if tc.streaming {
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	})

	var body io.Reader
	if tc.requestBody != "" {
		body = strings.NewReader(tc.requestBody)
	}
	req := httptest.NewRequest(http.MethodPost, tc.path, body)
	if tc.requestBody != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req = req.WithContext(withPIIRegistry(req.Context(), reg))

	rec := httptest.NewRecorder()
	productionPIIWireStack(pm, handler).ServeHTTP(rec, req)

	out := rec.Body.String()
	if strings.Contains(out, ph) {
		t.Fatalf("MASK placeholder leaked: %q", out)
	}
	if !strings.Contains(out, email) {
		t.Fatalf("expected restored email %q in %q", email, out)
	}
}

func TestPIIRedactWireStack_EndToEnd(t *testing.T) {
	cases := []wireStackProviderCase{
		wireStackOpenAICase(),
		wireStackAnthropicCase(),
		wireStackGeminiCase(),
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			testPIIRedactWireStackProviderEndToEnd(t, tc)
		})
	}
}

func testPIIRedactWireStackProviderEndToEnd(t *testing.T, tc wireStackProviderCase) {
	t.Helper()
	const email = "alice@example.com"
	redactor := &fakeRedactor{scrubFn: wireStackEmailScrubRedactor(email)}
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

func TestPIIRedactWireStack_MiddleGroundNamePolicy(t *testing.T) {
	const fullName = "Alice Johnson"
	const firstName = "Jess"
	const state = "Massachusetts"
	const email = "jess@gmail.com"
	prompt := fmt.Sprintf(
		"Hi %s, contact %s in %s at %s.",
		firstName, fullName, state, email,
	)
	detections := []wireDetection{
		{firstName, "PERSON"},
		{fullName, "PERSON"},
		{state, "LOCATION"},
		{email, "EMAIL_ADDRESS"},
	}

	providers := []wireStackProviderCase{
		func() wireStackProviderCase {
			c := wireStackOpenAICase()
			c.requestBody = wireStackOpenAINamePrompt(prompt)
			return c
		}(),
		func() wireStackProviderCase {
			c := wireStackAnthropicCase()
			c.requestBody = wireStackAnthropicNamePrompt(prompt)
			return c
		}(),
		func() wireStackProviderCase {
			c := wireStackGeminiCase()
			c.requestBody = wireStackGeminiNamePrompt(prompt)
			return c
		}(),
	}

	for _, tc := range providers {
		t.Run(tc.name, func(t *testing.T) {
			testPIIRedactWireStackNamePolicy(t, tc, detections, []string{firstName, state}, []string{fullName, email}, fullName, email)
		})
	}
}

func testPIIRedactWireStackNamePolicy(
	t *testing.T,
	tc wireStackProviderCase,
	detections []wireDetection,
	mustKeep, mustRedact []string,
	fullName, email string,
) {
	t.Helper()
	redactor := wireTestRedactorWithDetections(t, detections)
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

	req := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.requestBody("unused")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	stack.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("%s stack returned %d: %s", tc.name, rec.Code, rec.Body.String())
	}
	upstream := string(upstreamBody)
	for _, keep := range mustKeep {
		if !strings.Contains(upstream, keep) {
			t.Fatalf("%s upstream missing allowed value %q: %q", tc.name, keep, upstream)
		}
	}
	for _, redact := range mustRedact {
		if strings.Contains(upstream, redact) {
			t.Fatalf("%s upstream still contains %q: %q", tc.name, redact, upstream)
		}
	}
	if !strings.Contains(upstream, "<EMAIL_ADDRESS_1>") {
		t.Fatalf("%s upstream missing email placeholder: %q", tc.name, upstream)
	}
	if !strings.Contains(upstream, "<PERSON_1>") {
		t.Fatalf("%s upstream missing full-name placeholder: %q", tc.name, upstream)
	}
	if !strings.Contains(rec.Body.String(), email) {
		t.Fatalf("%s client response missing restored email: %q", tc.name, rec.Body.String())
	}
	for _, keep := range mustKeep {
		if !strings.Contains(rec.Body.String(), keep) {
			t.Fatalf("%s client response missing %q: %q", tc.name, keep, rec.Body.String())
		}
	}
	if !strings.Contains(rec.Body.String(), fullName) {
		t.Fatalf("%s client response missing restored full name: %q", tc.name, rec.Body.String())
	}
}
