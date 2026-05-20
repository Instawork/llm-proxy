package providers

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnthropic_ParseNonStreamingResponse_InvalidJSON(t *testing.T) {
	ap := NewAnthropicProxy()
	_, err := ap.ParseResponseMetadata(strings.NewReader("not json"), false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

func TestAnthropic_ParseStreamingResponse_NoData(t *testing.T) {
	ap := NewAnthropicProxy()
	md, err := ap.ParseResponseMetadata(strings.NewReader(""), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no usage information")
	assert.Nil(t, md)
}

func TestAnthropic_ParseStreamingResponse_DoneMarkerOnly(t *testing.T) {
	ap := NewAnthropicProxy()
	md, err := ap.ParseResponseMetadata(strings.NewReader("data: [DONE]\n"), true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no usage information")
	assert.Nil(t, md)
}

func TestAnthropic_ParseStreamingResponse_ContentBlockEvents(t *testing.T) {
	ap := NewAnthropicProxy()
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-sonnet","usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":5,"cache_creation_input_tokens":2}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":42}}`,
		`data: {"type":"message_stop"}`,
		`data: [DONE]`,
		``,
	}, "\n")
	md, err := ap.ParseResponseMetadata(strings.NewReader(stream), true)
	require.NoError(t, err)
	require.NotNil(t, md)
	assert.Equal(t, "claude-3-5-sonnet", md.Model)
	assert.Equal(t, "msg_1", md.RequestID)
	assert.Equal(t, "end_turn", md.FinishReason)
	assert.Equal(t, 10, md.InputTokens)
	assert.GreaterOrEqual(t, md.OutputTokens, 1)
}

func TestAnthropic_ParseStreamingResponse_Truncated(t *testing.T) {
	ap := NewAnthropicProxy()
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_2","model":"claude","usage":{"input_tokens":1,"output_tokens":1}}}`,
		``,
	}, "\n")
	md, err := ap.ParseResponseMetadata(strings.NewReader(stream), true)
	require.NoError(t, err)
	require.NotNil(t, md)
	assert.Equal(t, "claude", md.Model)
	assert.Equal(t, 1, md.InputTokens)
}

func TestAnthropic_ParseStreamingResponse_PartialOnly(t *testing.T) {
	ap := NewAnthropicProxy()
	stream := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_3","model":"claude"}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	md, err := ap.ParseResponseMetadata(strings.NewReader(stream), true)
	require.NoError(t, err)
	require.NotNil(t, md)
	assert.Equal(t, "claude", md.Model)
}

func TestGemini_ParseNonStreamingResponse_InvalidJSON(t *testing.T) {
	gp := NewGeminiProxy()
	_, err := gp.ParseResponseMetadata(strings.NewReader("not json"), false)
	require.Error(t, err)
}

func TestGemini_ParseNonStreamingResponse_ModelVersionStripsPrefix(t *testing.T) {
	gp := NewGeminiProxy()
	body := `{"modelVersion":"models/gemini-pro","usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5},"candidates":[{"finishReason":"STOP"}]}`
	md, err := gp.ParseResponseMetadata(strings.NewReader(body), false)
	require.NoError(t, err)
	assert.Equal(t, "gemini-pro", md.Model)
	assert.Equal(t, "STOP", md.FinishReason)
}

func TestGemini_ParseNonStreamingResponse_NoModelVersionFallback(t *testing.T) {
	gp := NewGeminiProxy()
	body := `{"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
	md, err := gp.ParseResponseMetadata(strings.NewReader(body), false)
	require.NoError(t, err)
	assert.Equal(t, "gemini", md.Model)
}

func TestGemini_ParseStreamingResponse_NoUsageMetadata(t *testing.T) {
	gp := NewGeminiProxy()
	stream := `data: {"modelVersion":"models/gemini-pro","candidates":[{"finishReason":"STOP"}]}
data: [DONE]
`
	md, err := gp.ParseResponseMetadata(strings.NewReader(stream), true)
	require.NoError(t, err)
	require.NotNil(t, md)
	assert.Equal(t, "gemini-pro", md.Model)
	assert.Equal(t, "STOP", md.FinishReason)
	assert.Equal(t, 0, md.InputTokens, "no usageMetadata present, token counts must default to zero")
	assert.Equal(t, 0, md.OutputTokens)
	assert.Equal(t, 0, md.TotalTokens)
}

func TestGemini_ParseStreamingResponse_BadJSONLine(t *testing.T) {
	gp := NewGeminiProxy()
	stream := `data: not json
data: {"modelVersion":"models/gemini-pro","usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}
data: [DONE]
`
	md, err := gp.ParseResponseMetadata(strings.NewReader(stream), true)
	require.NoError(t, err)
	assert.Equal(t, "gemini-pro", md.Model)
}

func TestOpenAI_ParseNonStreamingResponse_InvalidJSON(t *testing.T) {
	op := NewOpenAIProxy()
	_, err := op.ParseResponseMetadata(strings.NewReader("not json"), false)
	require.Error(t, err)
}

func TestOpenAI_ParseStreamingResponse_BadJSON_Skipped(t *testing.T) {
	op := NewOpenAIProxy()
	stream := strings.Join([]string{
		`data: not json`,
		`data: {"id":"r1","model":"gpt-4o","object":"chat.completion.chunk","choices":[{"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`,
		`data: [DONE]`,
		``,
	}, "\n")
	md, err := op.ParseResponseMetadata(strings.NewReader(stream), true)
	require.NoError(t, err)
	require.NotNil(t, md)
	assert.Equal(t, "gpt-4o", md.Model)
}

func TestOpenAI_ParseResponsesAPI_InvalidJSON(t *testing.T) {
	op := NewOpenAIProxy()
	_, err := op.ParseResponseMetadata(strings.NewReader("{not even close"), false)
	require.Error(t, err)
}
