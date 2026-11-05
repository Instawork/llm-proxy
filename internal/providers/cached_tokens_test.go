package providers

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// OpenAI and Gemini count cached prompt tokens inside the prompt and total
// counts. Every parser must move them out of InputTokens into
// CacheReadInputTokens, and leave them in TotalTokens.
func TestParseResponseMetadata_CachedPromptTokensAreSplitOut(t *testing.T) {
	cases := []struct {
		name      string
		provider  Provider
		streaming bool
		body      string
	}{
		{
			name:     "openai chat completions",
			provider: NewOpenAIProxy(),
			body:     `{"id":"chatcmpl-1","model":"gpt-4o","usage":{"prompt_tokens":1000,"completion_tokens":50,"total_tokens":1050,"prompt_tokens_details":{"cached_tokens":800}},"choices":[{"finish_reason":"stop"}]}`,
		},
		{
			name:      "openai chat completions streaming",
			provider:  NewOpenAIProxy(),
			streaming: true,
			body: strings.Join([]string{
				`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[{"index":0,"delta":{"content":"hi"}}]}`,
				`data: {"id":"chatcmpl-1","object":"chat.completion.chunk","model":"gpt-4o","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":50,"total_tokens":1050,"prompt_tokens_details":{"cached_tokens":800}}}`,
				`data: [DONE]`,
				``,
			}, "\n"),
		},
		{
			name:     "openai responses",
			provider: NewOpenAIProxy(),
			body:     `{"id":"resp_1","model":"gpt-4o","object":"response","output":[{"type":"message","status":"completed"}],"usage":{"input_tokens":1000,"output_tokens":50,"total_tokens":1050,"input_tokens_details":{"cached_tokens":800}}}`,
		},
		{
			name:      "openai responses streaming",
			provider:  NewOpenAIProxy(),
			streaming: true,
			body: strings.Join([]string{
				`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-4o","usage":null}}`,
				`data: {"type":"response.output_text.delta","delta":"hi"}`,
				`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-4o","output":[{"type":"message","status":"completed"}],"usage":{"input_tokens":1000,"output_tokens":50,"total_tokens":1050,"input_tokens_details":{"cached_tokens":800}}}}`,
				``,
			}, "\n"),
		},
		{
			name:      "openai responses streaming untyped event",
			provider:  NewOpenAIProxy(),
			streaming: true,
			body: strings.Join([]string{
				`data: {"id":"resp_1","model":"gpt-4o","output":[{"type":"message","status":"completed"}],"usage":{"input_tokens":1000,"output_tokens":50,"total_tokens":1050,"input_tokens_details":{"cached_tokens":800}}}`,
				``,
			}, "\n"),
		},
		{
			name:     "gemini openai-compat chat completions",
			provider: NewGeminiProxy(),
			body:     `{"id":"c1","object":"chat.completion","model":"gemini-2.5-flash","usage":{"prompt_tokens":1000,"completion_tokens":50,"total_tokens":1050,"prompt_tokens_details":{"cached_tokens":800}},"choices":[{"finish_reason":"stop"}]}`,
		},
		{
			name:     "gemini generateContent",
			provider: NewGeminiProxy(),
			body:     `{"modelVersion":"gemini-2.5-flash","responseId":"r1","usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":50,"totalTokenCount":1050,"cachedContentTokenCount":800},"candidates":[{"finishReason":"STOP"}]}`,
		},
		{
			name:      "gemini streamGenerateContent sse",
			provider:  NewGeminiProxy(),
			streaming: true,
			body: strings.Join([]string{
				`data: {"modelVersion":"gemini-2.5-flash","candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`,
				`data: {"modelVersion":"gemini-2.5-flash","candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":50,"totalTokenCount":1050,"cachedContentTokenCount":800}}`,
				``,
			}, "\n"),
		},
		{
			name:      "gemini streamGenerateContent json array",
			provider:  NewGeminiProxy(),
			streaming: true,
			body:      `[{"modelVersion":"gemini-2.5-flash","candidates":[{"content":{"parts":[{"text":"hi"}]}}]},{"modelVersion":"gemini-2.5-flash","candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1000,"candidatesTokenCount":50,"totalTokenCount":1050,"cachedContentTokenCount":800}}]`,
		},
		{
			name:     "gemini interactions",
			provider: NewGeminiProxy(),
			body:     `{"id":"v1_1","object":"interaction","model":"gemini-3.8-flash","status":"completed","usage":{"total_input_tokens":1000,"total_output_tokens":50,"total_tokens":1050,"total_cached_tokens":800}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			md, err := tc.provider.ParseResponseMetadata(strings.NewReader(tc.body), tc.streaming)
			require.NoError(t, err)
			require.NotNil(t, md)
			assert.Equal(t, 200, md.InputTokens, "input excludes cached tokens")
			assert.Equal(t, 800, md.CacheReadInputTokens)
			assert.Equal(t, 0, md.CacheCreationInputTokens)
			assert.Equal(t, 50, md.OutputTokens)
			assert.Equal(t, 1050, md.TotalTokens, "total includes cached tokens")
		})
	}
}

func TestParseResponseMetadata_NoCachedTokensLeavesCountsAlone(t *testing.T) {
	md, err := NewOpenAIProxy().ParseResponseMetadata(strings.NewReader(
		`{"id":"chatcmpl-1","model":"gpt-4o","usage":{"prompt_tokens":1000,"completion_tokens":50,"total_tokens":1050,"prompt_tokens_details":{"cached_tokens":0}}}`,
	), false)
	require.NoError(t, err)
	assert.Equal(t, 1000, md.InputTokens)
	assert.Equal(t, 0, md.CacheReadInputTokens)
	assert.Equal(t, 1050, md.TotalTokens)
}

func TestSplitCachedInput_ClampsToPromptCount(t *testing.T) {
	md := &LLMResponseMetadata{InputTokens: 100, OutputTokens: 10, TotalTokens: 110}
	md.splitCachedInput(500)
	assert.Equal(t, 0, md.InputTokens)
	assert.Equal(t, 100, md.CacheReadInputTokens)
	assert.Equal(t, 110, md.TotalTokens)
}

// OpenAI reports cache writes as cache_write_tokens next to cached_tokens;
// both sit inside the prompt count and must be split out of InputTokens.
func TestParseResponseMetadata_OpenAICacheWritesAreSplitOut(t *testing.T) {
	cases := []struct {
		name      string
		streaming bool
		body      string
	}{
		{
			name: "chat completions",
			body: `{"id":"c1","model":"gpt-6-astra","usage":{"prompt_tokens":1000,"completion_tokens":50,"total_tokens":1050,"prompt_tokens_details":{"cached_tokens":600,"cache_write_tokens":300}}}`,
		},
		{
			name:      "chat completions streaming",
			streaming: true,
			body: `data: {"id":"c1","object":"chat.completion.chunk","model":"gpt-6-astra","choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":50,"total_tokens":1050,"prompt_tokens_details":{"cached_tokens":600,"cache_write_tokens":300}}}` + "\n" +
				`data: [DONE]` + "\n",
		},
		{
			name: "responses",
			body: `{"id":"r1","model":"gpt-6-astra","object":"response","output":[{"type":"message","status":"completed"}],"usage":{"input_tokens":1000,"output_tokens":50,"total_tokens":1050,"input_tokens_details":{"cached_tokens":600,"cache_write_tokens":300}}}`,
		},
		{
			name:      "responses streaming",
			streaming: true,
			body:      `data: {"type":"response.completed","response":{"id":"r1","model":"gpt-6-astra","output":[{"type":"message","status":"completed"}],"usage":{"input_tokens":1000,"output_tokens":50,"total_tokens":1050,"input_tokens_details":{"cached_tokens":600,"cache_write_tokens":300}}}}` + "\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			md, err := NewOpenAIProxy().ParseResponseMetadata(strings.NewReader(tc.body), tc.streaming)
			require.NoError(t, err)
			require.NotNil(t, md)
			assert.Equal(t, 100, md.InputTokens)
			assert.Equal(t, 600, md.CacheReadInputTokens)
			assert.Equal(t, 300, md.CacheCreationInputTokens)
			assert.Equal(t, 1050, md.TotalTokens)
		})
	}
}
