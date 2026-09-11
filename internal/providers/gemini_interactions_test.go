package providers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Fixtures follow https://ai.google.dev/api/interactions-api and
// https://ai.google.dev/gemini-api/docs/streaming.
const interactionsJSON = `{
  "created": "2025-11-26T12:25:15Z",
  "id": "v1_ChdPU0F4YWFtNkFwS2kxZThQZ05lbXdROBIX",
  "model": "gemini-3.8-flash",
  "object": "interaction",
  "status": "completed",
  "steps": [{"type": "model_output", "content": [{"type": "text", "text": "Hello!"}]}],
  "usage": {
    "input_tokens_by_modality": [{"modality": "text", "tokens": 7}],
    "total_cached_tokens": 3,
    "total_input_tokens": 7,
    "total_output_tokens": 20,
    "total_thought_tokens": 22,
    "total_tokens": 49,
    "total_tool_use_tokens": 0
  }
}`

const interactionsSSE = `event: interaction.created
data: {"interaction":{"id":"v1_abc","status":"in_progress","object":"interaction","model":"gemini-3.8-flash"},"event_type":"interaction.created"}

event: step.start
data: {"index":0,"step":{"type":"model_output"},"event_type":"step.start"}

event: step.delta
data: {"index":0,"delta":{"text":"1, 2, 3","type":"text"},"event_type":"step.delta"}

event: step.stop
data: {"index":0,"event_type":"step.stop","usage":{"total_tokens":100,"total_input_tokens":11,"total_output_tokens":40,"total_cached_tokens":0}}

event: interaction.completed
data: {"interaction":{"id":"v1_abc","status":"completed","usage":{"total_tokens":346,"total_input_tokens":11,"total_cached_tokens":0,"total_output_tokens":90,"total_tool_use_tokens":0,"total_thought_tokens":245},"object":"interaction","model":"gemini-3.8-flash"},"event_type":"interaction.completed"}
`

func TestGeminiParseInteractions_NonStreaming(t *testing.T) {
	g := NewGeminiProxy()
	md, err := g.ParseResponseMetadata(strings.NewReader(interactionsJSON), false)
	if err != nil {
		t.Fatal(err)
	}
	if md.Model != "gemini-3.8-flash" || md.InputTokens != 7 || md.OutputTokens != 20 ||
		md.TotalTokens != 49 || md.ThoughtTokens != 22 || md.CacheReadInputTokens != 3 ||
		md.RequestID != "v1_ChdPU0F4YWFtNkFwS2kxZThQZ05lbXdROBIX" || md.IsStreaming {
		t.Fatalf("unexpected metadata: %+v", md)
	}
}

func TestGeminiParseInteractions_Streaming(t *testing.T) {
	g := NewGeminiProxy()
	md, err := g.ParseResponseMetadata(strings.NewReader(interactionsSSE), true)
	if err != nil {
		t.Fatal(err)
	}
	if md.Model != "gemini-3.8-flash" || md.InputTokens != 11 || md.OutputTokens != 90 ||
		md.TotalTokens != 346 || md.ThoughtTokens != 245 || !md.IsStreaming || md.FinishReason != "completed" {
		t.Fatalf("unexpected metadata: %+v", md)
	}
}

func TestGeminiParseInteractions_StreamingTruncatedFallsBackToStepUsage(t *testing.T) {
	g := NewGeminiProxy()
	truncated := interactionsSSE[:strings.Index(interactionsSSE, "event: interaction.completed")]
	md, err := g.ParseResponseMetadata(strings.NewReader(truncated), true)
	if err != nil {
		t.Fatal(err)
	}
	if md.Model != "gemini-3.8-flash" || md.InputTokens != 11 || md.OutputTokens != 40 || md.TotalTokens != 100 {
		t.Fatalf("unexpected metadata: %+v", md)
	}
}

func TestGeminiIsStreamingRequest_Interactions(t *testing.T) {
	g := NewGeminiProxy()
	for body, want := range map[string]bool{
		`{"model":"gemini-3.8-flash","input":"hi","stream":true}`: true,
		`{"model":"gemini-3.8-flash","input":"hi"}`:               false,
	} {
		req := httptest.NewRequest(http.MethodPost, "/gemini/v1beta/interactions", strings.NewReader(body))
		if got := g.IsStreamingRequest(req); got != want {
			t.Errorf("%s: streaming=%v, want %v", body, got, want)
		}
	}
}
