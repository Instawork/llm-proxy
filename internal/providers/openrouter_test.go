package providers

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var _ Provider = (*OpenRouterProxy)(nil)

const openRouterChatPath = "/openrouter/api/v1/chat/completions"

func TestOpenRouter_ForwardsToOpenRouterAPIWithTranslatedKey(t *testing.T) {
	or := NewOpenRouterProxy()
	var upstream *http.Request
	var upstreamBody string
	or.WrapTransport(func(http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			upstream = r
			b, _ := io.ReadAll(r.Body)
			upstreamBody = string(b)
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"id":"gen-1","choices":[]}`)),
				Request:    r,
			}, nil
		})
	})

	body := `{"model":"deepseek/deepseek-v4-pro","messages":[{"role":"user","content":"hi"}]}`
	req := httptest.NewRequest(http.MethodPost, openRouterChatPath, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer sk-iw-proxy")
	req.Header.Set("HTTP-Referer", "https://finch.instawork.com")
	require.NoError(t, or.ValidateAPIKey(req, &stubKeyStore{actual: "sk-or-v1-real", provider: "openrouter"}))

	rec := httptest.NewRecorder()
	or.Proxy().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, upstream)
	assert.Equal(t, "https://openrouter.ai/api/v1/chat/completions", upstream.URL.String())
	assert.Equal(t, "openrouter.ai", upstream.Host)
	assert.Equal(t, "Bearer sk-or-v1-real", upstream.Header.Get("Authorization"))
	assert.Equal(t, "https://finch.instawork.com", upstream.Header.Get("HTTP-Referer"))
	assert.JSONEq(t, body, upstreamBody)
}

func TestOpenRouter_IsStreamingRequest(t *testing.T) {
	or := NewOpenRouterProxy()

	tests := []struct {
		name   string
		accept string
		body   string
		want   bool
	}{
		{name: "stream true", body: `{"model":"x/y","stream":true}`, want: true},
		{name: "stream false", body: `{"model":"x/y","stream":false}`, want: false},
		{name: "no stream field", body: `{"model":"x/y"}`, want: false},
		{name: "sse accept header", accept: "text/event-stream", body: `{}`, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, openRouterChatPath, strings.NewReader(tc.body))
			if tc.accept != "" {
				req.Header.Set("Accept", tc.accept)
			}
			assert.Equal(t, tc.want, or.IsStreamingRequest(req))
			b, err := io.ReadAll(req.Body)
			require.NoError(t, err)
			assert.Equal(t, tc.body, string(b), "body must be restored for the upstream")
		})
	}

	get := httptest.NewRequest(http.MethodGet, "/openrouter/api/v1/models", nil)
	assert.False(t, or.IsStreamingRequest(get))
}

func TestOpenRouter_ParseResponseMetadata_NonStreaming(t *testing.T) {
	body := `{
		"id": "gen-123",
		"object": "chat.completion",
		"model": "deepseek/deepseek-v4-pro",
		"choices": [{"index": 0, "message": {"role": "assistant", "content": "hi"}, "finish_reason": "stop"}],
		"usage": {"prompt_tokens": 12, "completion_tokens": 5, "total_tokens": 17}
	}`

	md, err := NewOpenRouterProxy().ParseResponseMetadata(strings.NewReader(body), false)
	require.NoError(t, err)
	assert.Equal(t, "openrouter", md.Provider)
	assert.Equal(t, "deepseek/deepseek-v4-pro", md.Model)
	assert.Equal(t, 12, md.InputTokens)
	assert.Equal(t, 5, md.OutputTokens)
	assert.Equal(t, 17, md.TotalTokens)
	assert.Equal(t, "gen-123", md.RequestID)
	assert.Equal(t, "stop", md.FinishReason)
	assert.False(t, md.IsStreaming)
}

func TestOpenRouter_ParseResponseMetadata_Streaming(t *testing.T) {
	stream := strings.Join([]string{
		": OPENROUTER PROCESSING",
		"",
		`data: {"id":"gen-9","object":"chat.completion.chunk","model":"moonshotai/kimi-k3","choices":[{"index":0,"delta":{"role":"assistant","content":"Hel"}}]}`,
		"",
		`data: {"id":"gen-9","object":"chat.completion.chunk","model":"moonshotai/kimi-k3","choices":[{"index":0,"delta":{"content":"lo"},"finish_reason":"stop"}]}`,
		"",
		`data: {"id":"gen-9","object":"chat.completion.chunk","model":"moonshotai/kimi-k3","choices":[],"usage":{"prompt_tokens":20,"completion_tokens":2,"total_tokens":22}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")

	md, err := NewOpenRouterProxy().ParseResponseMetadata(strings.NewReader(stream), true)
	require.NoError(t, err)
	assert.Equal(t, "openrouter", md.Provider)
	assert.Equal(t, "moonshotai/kimi-k3", md.Model)
	assert.Equal(t, 20, md.InputTokens)
	assert.Equal(t, 2, md.OutputTokens)
	assert.Equal(t, 22, md.TotalTokens)
	assert.Equal(t, "gen-9", md.RequestID)
	assert.Equal(t, "stop", md.FinishReason)
	assert.True(t, md.IsStreaming)
}

func TestOpenRouter_ValidateAPIKey(t *testing.T) {
	or := NewOpenRouterProxy()

	req := httptest.NewRequest(http.MethodPost, openRouterChatPath, nil)
	req.Header.Set("Authorization", "Bearer sk-iw-proxy")
	require.NoError(t, or.ValidateAPIKey(req, &stubKeyStore{actual: "sk-or-v1-real", provider: "openrouter"}))
	assert.Equal(t, "Bearer sk-or-v1-real", req.Header.Get("Authorization"))

	req = httptest.NewRequest(http.MethodPost, openRouterChatPath, nil)
	store := &stubKeyStore{}
	require.NoError(t, or.ValidateAPIKey(req, store), "missing credentials are left for the upstream to reject")
	assert.Zero(t, store.called)

	req = httptest.NewRequest(http.MethodPost, openRouterChatPath, nil)
	req.Header.Set("Authorization", "Bearer sk-iw-proxy")
	assert.Error(t, or.ValidateAPIKey(req, &stubKeyStore{err: errors.New("disabled")}))

	req = httptest.NewRequest(http.MethodPost, openRouterChatPath, nil)
	req.Header.Set("Authorization", "Bearer sk-iw-openai")
	err := or.ValidateAPIKey(req, &stubKeyStore{actual: "sk-real", provider: "openai"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not openrouter")
}

func TestOpenRouter_ExtractRequestModelAndMessagesAndUser(t *testing.T) {
	or := NewOpenRouterProxy()
	body := `{"model":"z-ai/glm-5.3-flash","user":"finch-agent","messages":[{"role":"system","content":"be brief"},{"role":"user","content":[{"type":"text","text":"hello"}]}]}`

	req := httptest.NewRequest(http.MethodPost, openRouterChatPath, strings.NewReader(body))
	model, messages := or.ExtractRequestModelAndMessages(req)
	assert.Equal(t, "z-ai/glm-5.3-flash", model)
	assert.Equal(t, []string{"be brief", "hello"}, messages)
	assert.Equal(t, "finch-agent", or.UserIDFromRequest(req))

	upstream := httptest.NewRequest(http.MethodPost, "https://openrouter.ai/api/v1/chat/completions", strings.NewReader(body))
	model, _ = or.ExtractRequestModelAndMessages(upstream)
	assert.Equal(t, "z-ai/glm-5.3-flash", model, "circuit transport sees the rewritten upstream path")

	other := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(body))
	model, messages = or.ExtractRequestModelAndMessages(other)
	assert.Empty(t, model)
	assert.Empty(t, messages)
	assert.Empty(t, or.UserIDFromRequest(other))
}

func TestOpenRouter_ProviderManagerRoutesOpenRouterPrefix(t *testing.T) {
	pm := NewProviderManager()
	pm.RegisterProvider(NewOpenAIProxy())
	pm.RegisterProvider(NewOpenRouterProxy())

	req := httptest.NewRequest(http.MethodPost, openRouterChatPath, strings.NewReader(`{}`))
	p := pm.ProviderForRequest(req)
	require.NotNil(t, p)
	assert.Equal(t, "openrouter", p.GetName())

	req = httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(`{}`))
	p = pm.ProviderForRequest(req)
	require.NotNil(t, p)
	assert.Equal(t, "openai", p.GetName())
}
