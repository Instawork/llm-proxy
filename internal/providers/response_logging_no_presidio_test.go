package providers

import (
	"bytes"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Instawork/llm-proxy/internal/redact"
)

// TestHotPathResponseParsing_NoPresidioCallsAndNoBodyLeak guards against
// regressing hot-path provider response debug logging back to synchronous
// redact.LogPreview Presidio /analyze calls or raw body dumps.
func TestHotPathResponseParsing_NoPresidioCallsAndNoBodyLeak(t *testing.T) {
	var presidioCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		presidioCalls.Add(1)
		_ = json.NewEncoder(w).Encode([]redact.Span{})
	}))
	defer srv.Close()

	r, err := redact.New(redact.Config{AnalyzerURL: srv.URL})
	if err != nil {
		t.Fatalf("redact.New: %v", err)
	}
	redact.SetGlobal(r)
	t.Cleanup(func() { redact.SetGlobal(nil) })

	var logBuf bytes.Buffer
	origOut := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(origOut) })

	const secretText = "SUPER_SECRET_SSN_222-33-4444_BODY_TOKEN"

	openaiProxy := NewOpenAIProxy()
	anthropicProxy := NewAnthropicProxy()

	t.Run("openai non-streaming chat completion", func(t *testing.T) {
		body := `{"id":"chatcmpl-1","model":"gpt-4o-mini","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"` + secretText + `"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
		meta, err := openaiProxy.ParseResponseMetadata(strings.NewReader(body), false)
		if err != nil {
			t.Fatalf("ParseResponseMetadata: %v", err)
		}
		if meta.TotalTokens != 15 {
			t.Fatalf("TotalTokens = %d, want 15", meta.TotalTokens)
		}
	})

	t.Run("openai responses non-streaming", func(t *testing.T) {
		body := `{"id":"resp_1","model":"gpt-4o-mini","output":[{"type":"message","status":"completed","content":[{"type":"output_text","text":"` + secretText + `"}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`
		meta, err := parseOpenAIResponsesNonStreamingResponse(strings.NewReader(body))
		if err != nil {
			t.Fatalf("parseOpenAIResponsesNonStreamingResponse: %v", err)
		}
		if meta.TotalTokens != 15 {
			t.Fatalf("TotalTokens = %d, want 15", meta.TotalTokens)
		}
	})

	t.Run("openai responses streaming created and completed", func(t *testing.T) {
		stream := strings.Join([]string{
			`data: {"type":"response.created","response":{"id":"resp_stream_1","model":"gpt-4o-mini","output":[{"text":"` + secretText + `"}]}}`,
			``,
			`data: {"type":"response.completed","response":{"id":"resp_stream_1","model":"gpt-4o-mini","status":"completed","output":[{"text":"` + secretText + `"}],"usage":{"input_tokens":12,"output_tokens":8,"total_tokens":20}}}`,
			``,
			`data: [DONE]`,
			``,
		}, "\n")
		meta, err := openaiProxy.ParseResponseMetadata(strings.NewReader(stream), true)
		if err != nil {
			t.Fatalf("ParseResponseMetadata stream: %v", err)
		}
		if meta.TotalTokens != 20 {
			t.Fatalf("TotalTokens = %d, want 20", meta.TotalTokens)
		}
	})

	t.Run("anthropic native non-streaming", func(t *testing.T) {
		body := `{"id":"msg_1","model":"claude-3-5-haiku","stop_reason":"end_turn","content":[{"type":"text","text":"` + secretText + `"}],"usage":{"input_tokens":11,"output_tokens":9}}`
		meta, err := anthropicProxy.ParseResponseMetadata(strings.NewReader(body), false)
		if err != nil {
			t.Fatalf("ParseResponseMetadata anthropic: %v", err)
		}
		if meta.TotalTokens != 20 {
			t.Fatalf("TotalTokens = %d, want 20", meta.TotalTokens)
		}
	})

	t.Run("anthropic openai-compat non-streaming", func(t *testing.T) {
		body := `{"id":"msg_compat_1","model":"claude-3-5-haiku","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"` + secretText + `"}}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}`
		meta, err := anthropicProxy.ParseResponseMetadata(strings.NewReader(body), false)
		if err != nil {
			t.Fatalf("ParseResponseMetadata anthropic compat: %v", err)
		}
		if meta.TotalTokens != 10 {
			t.Fatalf("TotalTokens = %d, want 10", meta.TotalTokens)
		}
	})

	if got := presidioCalls.Load(); got != 0 {
		t.Fatalf("hot-path response parsing made %d synchronous Presidio calls, want 0", got)
	}
	if strings.Contains(logBuf.String(), secretText) {
		t.Fatalf("raw response body leaked into logs: %s", logBuf.String())
	}
}
