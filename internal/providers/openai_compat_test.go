package providers

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// openAICompatBody is a real-world-shaped response from a vendor's
// OpenAI-compatibility /chat/completions endpoint (this is what Anthropic
// returns for /v1/chat/completions and Gemini for
// /v1beta/openai/chat/completions). Regression fixture for the Opik
// cost-tracking outage: the native parsers used to unmarshal this into zero
// token counts, which silently disabled cost tracking for that traffic.
const openAICompatBody = `{
	"id": "msg_011CdHFhDAE7TNRPBZLxENES",
	"object": "chat.completion",
	"created": 1784730000,
	"model": "claude-haiku-4-5-20251001",
	"choices": [
		{
			"index": 0,
			"message": {"role": "assistant", "content": "Hello!"},
			"finish_reason": "stop"
		}
	],
	"usage": {"prompt_tokens": 120, "completion_tokens": 34, "total_tokens": 154}
}`

const openAICompatStreamBody = `data: {"id":"msg_01","object":"chat.completion.chunk","created":1784730000,"model":"claude-haiku-4-5-20251001","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"msg_01","object":"chat.completion.chunk","created":1784730000,"model":"claude-haiku-4-5-20251001","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":120,"completion_tokens":34,"total_tokens":154}}

data: [DONE]
`

func assertCompatMetadata(t *testing.T, metadata *LLMResponseMetadata, provider string, isStreaming bool) {
	t.Helper()
	require.NotNil(t, metadata)
	assert.Equal(t, provider, metadata.Provider)
	assert.Equal(t, "claude-haiku-4-5-20251001", metadata.Model)
	assert.Equal(t, 120, metadata.InputTokens)
	assert.Equal(t, 34, metadata.OutputTokens)
	assert.Equal(t, 154, metadata.TotalTokens)
	assert.Equal(t, isStreaming, metadata.IsStreaming)
}

func TestAnthropic_ParseResponseMetadata_OpenAICompatNonStreaming(t *testing.T) {
	metadata, err := NewAnthropicProxy().ParseResponseMetadata(strings.NewReader(openAICompatBody), false)
	require.NoError(t, err)
	assertCompatMetadata(t, metadata, "anthropic", false)
}

func TestAnthropic_ParseResponseMetadata_OpenAICompatStreaming(t *testing.T) {
	metadata, err := NewAnthropicProxy().ParseResponseMetadata(strings.NewReader(openAICompatStreamBody), true)
	require.NoError(t, err)
	assertCompatMetadata(t, metadata, "anthropic", true)
}

func TestGemini_ParseResponseMetadata_OpenAICompatNonStreaming(t *testing.T) {
	metadata, err := NewGeminiProxy().ParseResponseMetadata(strings.NewReader(openAICompatBody), false)
	require.NoError(t, err)
	assertCompatMetadata(t, metadata, "gemini", false)
}

func TestGemini_ParseResponseMetadata_OpenAICompatStreaming(t *testing.T) {
	metadata, err := NewGeminiProxy().ParseResponseMetadata(strings.NewReader(openAICompatStreamBody), true)
	require.NoError(t, err)
	assertCompatMetadata(t, metadata, "gemini", true)
}

// Native-format bodies must keep flowing through the native parsers: the
// compat detection keys on "choices", which never appears in native
// Anthropic ("content") or Gemini ("candidates") responses.
func TestOpenAICompatDetection_DoesNotMisfireOnNativeBodies(t *testing.T) {
	anthropicNative := `{"id":"msg_1","type":"message","model":"claude-haiku-4-5","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":5}}`
	metadata, err := NewAnthropicProxy().ParseResponseMetadata(strings.NewReader(anthropicNative), false)
	require.NoError(t, err)
	assert.Equal(t, "anthropic", metadata.Provider)
	assert.Equal(t, 15, metadata.TotalTokens)

	geminiNative := `{"candidates":[{"content":{"parts":[{"text":"hi"}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"gemini-2.5-flash"}`
	metadata, err = NewGeminiProxy().ParseResponseMetadata(strings.NewReader(geminiNative), false)
	require.NoError(t, err)
	assert.Equal(t, "gemini", metadata.Provider)
	assert.Equal(t, 15, metadata.TotalTokens)

	anthropicNativeStream := "data: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"model\":\"claude-haiku-4-5\",\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n\n" +
		"data: {\"type\":\"message_stop\"}\n\n"
	metadata, err = NewAnthropicProxy().ParseResponseMetadata(strings.NewReader(anthropicNativeStream), true)
	require.NoError(t, err)
	assert.Equal(t, "anthropic", metadata.Provider)
	// message_delta usage is cumulative: 10 input + 5 output.
	assert.Equal(t, 15, metadata.TotalTokens)
}

// The OpenAI-compatibility endpoints signal streaming via "stream": true in
// the body, not via the URL path, so IsStreamingRequest must body-sniff
// /chat/completions for Anthropic and Gemini (OpenAI SDK clients do not
// reliably send Accept: text/event-stream).
func TestIsStreamingRequest_OpenAICompatEndpoints(t *testing.T) {
	cases := []struct {
		name     string
		provider Provider
		path     string
	}{
		{"anthropic", NewAnthropicProxy(), "/anthropic/v1/chat/completions"},
		{"gemini", NewGeminiProxy(), "/gemini/v1beta/openai/chat/completions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			streaming, _ := http.NewRequest("POST", tc.path, bytes.NewBufferString(`{"model":"m","stream":true}`))
			assert.True(t, tc.provider.IsStreamingRequest(streaming))

			nonStreaming, _ := http.NewRequest("POST", tc.path, bytes.NewBufferString(`{"model":"m"}`))
			assert.False(t, tc.provider.IsStreamingRequest(nonStreaming))
		})
	}
}
