package providers

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type captureRT struct{ called int }

func (c *captureRT) RoundTrip(_ *http.Request) (*http.Response, error) {
	c.called++
	return nil, errors.New("not used")
}

type alwaysErrRT struct{}

func (alwaysErrRT) RoundTrip(_ *http.Request) (*http.Response, error) {
	return nil, errors.New("synthetic transport failure")
}

type cannedResponseRT struct {
	resp *http.Response
}

func (c cannedResponseRT) RoundTrip(_ *http.Request) (*http.Response, error) {
	return c.resp, nil
}

func runErrorHandler(t *testing.T, p interface {
	WrapTransport(func(http.RoundTripper) http.RoundTripper)
	Proxy() http.Handler
}, path string, accept string,
) *httptest.ResponseRecorder {
	t.Helper()
	p.WrapTransport(func(_ http.RoundTripper) http.RoundTripper { return alwaysErrRT{} })
	// Body shape is intentionally driven by `accept`: callers that want to
	// exercise the non-streaming ErrorHandler branch should not also send a
	// body with `"stream": true`, because providers honor the body hint as
	// authoritative even when Accept is missing.
	body := `{}`
	if accept != "" {
		body = `{"stream":true}`
	}
	req, _ := http.NewRequest("POST", path, strings.NewReader(body))
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	rr := httptest.NewRecorder()
	p.Proxy().ServeHTTP(rr, req)
	return rr
}

func runModifyResponse(t *testing.T, p interface {
	WrapTransport(func(http.RoundTripper) http.RoundTripper)
	Proxy() http.Handler
}, path, ct string,
) *http.Response {
	t.Helper()
	canned := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("{}")),
	}
	canned.Header.Set("Content-Type", ct)
	canned.Header.Set("Content-Length", "2")
	p.WrapTransport(func(_ http.RoundTripper) http.RoundTripper {
		return cannedResponseRT{resp: canned}
	})
	req, _ := http.NewRequest("POST", path, strings.NewReader(`{"stream":true}`))
	rr := httptest.NewRecorder()
	p.Proxy().ServeHTTP(rr, req)
	return rr.Result()
}

func TestProvider_WrapTransport_AppliesWrapper(t *testing.T) {
	for _, p := range []interface {
		WrapTransport(func(http.RoundTripper) http.RoundTripper)
	}{NewOpenAIProxy(), NewAnthropicProxy(), NewGeminiProxy()} {
		called := false
		p.WrapTransport(func(rt http.RoundTripper) http.RoundTripper {
			called = true
			return &captureRT{}
		})
		assert.True(t, called)
	}
}

func TestCreateGenericDirector_StripsPrefixAndGzip(t *testing.T) {
	op := NewOpenAIProxy()
	target, _ := http.NewRequest("GET", "https://api.openai.com", nil)
	originalDirector := func(req *http.Request) {
		req.URL.Scheme = target.URL.Scheme
		req.URL.Host = target.URL.Host
	}
	director := CreateGenericDirector(op, target.URL, originalDirector, true)

	req, _ := http.NewRequest("POST", "/openai/v1/chat/completions", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	director(req)
	assert.Equal(t, "/v1/chat/completions", req.URL.Path)
	assert.Equal(t, "", req.Header.Get("Accept-Encoding"))
	assert.Equal(t, target.URL.Host, req.Host)
}

func TestDecompressResponseIfNeeded_PlainPassthrough(t *testing.T) {
	r, err := DecompressResponseIfNeeded(strings.NewReader("hello"))
	require.NoError(t, err)
	data, _ := io.ReadAll(r)
	assert.Equal(t, "hello", string(data))
}

func TestOpenAIProxy_ErrorHandler_Streaming(t *testing.T) {
	rr := runErrorHandler(t, NewOpenAIProxy(), "/openai/v1/chat/completions", "text/event-stream")
	assert.Equal(t, http.StatusBadGateway, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), "text/event-stream")
	assert.Contains(t, rr.Body.String(), "[UPSTREAM]")
	assert.Contains(t, rr.Header().Get("X-Llm-Proxy-Error-Source"), "upstream")
}

func TestOpenAIProxy_ErrorHandler_NonStreaming(t *testing.T) {
	rr := runErrorHandler(t, NewOpenAIProxy(), "/openai/v1/chat/completions", "")
	assert.Equal(t, http.StatusBadGateway, rr.Code)
	assert.Contains(t, rr.Body.String(), "[UPSTREAM]")
	assert.Contains(t, rr.Body.String(), "openai transport")
}

func TestAnthropicProxy_ErrorHandler_Streaming(t *testing.T) {
	rr := runErrorHandler(t, NewAnthropicProxy(), "/anthropic/v1/messages", "text/event-stream")
	assert.Equal(t, http.StatusBadGateway, rr.Code)
	assert.Contains(t, rr.Body.String(), "[UPSTREAM]")
}

func TestAnthropicProxy_ErrorHandler_NonStreaming(t *testing.T) {
	rr := runErrorHandler(t, NewAnthropicProxy(), "/anthropic/v1/messages", "")
	assert.Equal(t, http.StatusBadGateway, rr.Code)
	assert.Contains(t, rr.Body.String(), "[UPSTREAM]")
	assert.Contains(t, rr.Body.String(), "anthropic transport")
}

func TestGeminiProxy_ErrorHandler_Streaming(t *testing.T) {
	rr := runErrorHandler(t, NewGeminiProxy(), "/gemini/v1/models/gemini-pro:streamGenerateContent?alt=sse", "text/event-stream")
	assert.Equal(t, http.StatusBadGateway, rr.Code)
	assert.Contains(t, rr.Body.String(), "[UPSTREAM]")
}

func TestGeminiProxy_ErrorHandler_NonStreaming(t *testing.T) {
	rr := runErrorHandler(t, NewGeminiProxy(), "/gemini/v1/models/gemini-pro:generateContent", "")
	assert.Equal(t, http.StatusBadGateway, rr.Code)
	assert.Contains(t, rr.Body.String(), "[UPSTREAM]")
	assert.Contains(t, rr.Body.String(), "gemini transport")
}

func TestBedrockProxy_ErrorHandler_Streaming(t *testing.T) {
	rr := runErrorHandler(t, NewBedrockProxy(), "/bedrock/model/anthropic.claude-3-sonnet/converse-stream", bedrockEventStreamMIME)
	assert.Equal(t, http.StatusBadGateway, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), bedrockEventStreamMIME)
	assert.Contains(t, rr.Body.String(), "[UPSTREAM]")
	assert.Contains(t, rr.Header().Get("X-Llm-Proxy-Error-Source"), "upstream")
}

func TestBedrockProxy_ErrorHandler_NonStreaming(t *testing.T) {
	rr := runErrorHandler(t, NewBedrockProxy(), "/bedrock/model/anthropic.claude-3-sonnet/converse", "")
	assert.Equal(t, http.StatusBadGateway, rr.Code)
	assert.Contains(t, rr.Body.String(), "[UPSTREAM]")
	assert.Contains(t, rr.Body.String(), "bedrock transport")
}

func TestBedrockMantleProxy_ErrorHandler_Streaming(t *testing.T) {
	mantle := newBedrockMantleProxy(
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
		ProxyOptions{},
	)
	rr := runErrorHandler(t, mantle, "/bedrock-mantle/anthropic/v1/messages", "text/event-stream")
	assert.Equal(t, http.StatusBadGateway, rr.Code)
	assert.Contains(t, rr.Body.String(), "[UPSTREAM]")
	assert.Contains(t, rr.Header().Get("X-Llm-Proxy-Error-Source"), "upstream")
}

func TestBedrockMantleProxy_ErrorHandler_NonStreaming(t *testing.T) {
	mantle := newBedrockMantleProxy(
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", "session"),
		ProxyOptions{},
	)
	rr := runErrorHandler(t, mantle, "/bedrock-mantle/anthropic/v1/messages", "")
	assert.Equal(t, http.StatusBadGateway, rr.Code)
	assert.Contains(t, rr.Body.String(), "[UPSTREAM]")
	assert.Contains(t, rr.Body.String(), "bedrock-mantle transport")
}

func TestProvider_ModifyResponse_SSEHeaders(t *testing.T) {
	cases := []struct {
		name string
		p    interface {
			WrapTransport(func(http.RoundTripper) http.RoundTripper)
			Proxy() http.Handler
		}
		path string
	}{
		{"openai", NewOpenAIProxy(), "/openai/v1/chat/completions"},
		{"anthropic", NewAnthropicProxy(), "/anthropic/v1/messages"},
		{"gemini", NewGeminiProxy(), "/gemini/v1/models/gemini-pro:streamGenerateContent?alt=sse"},
	}
	for _, c := range cases {
		t.Run(c.name+"_streaming_rewrites_headers", func(t *testing.T) {
			resp := runModifyResponse(t, c.p, c.path, "text/event-stream")
			defer resp.Body.Close()
			assert.Equal(t, "no-cache", resp.Header.Get("Cache-Control"))
			assert.Equal(t, "", resp.Header.Get("Content-Length"))
		})

		t.Run(c.name+"_non_streaming_keeps_headers", func(t *testing.T) {
			resp := runModifyResponse(t, c.p, c.path, "application/json")
			defer resp.Body.Close()
			assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))
		})
	}
}

func TestProviderInterface_NameIsConsistent(t *testing.T) {
	tests := []struct {
		p    Provider
		want string
	}{
		{NewOpenAIProxy(), "openai"},
		{NewAnthropicProxy(), "anthropic"},
		{NewGeminiProxy(), "gemini"},
	}
	for _, c := range tests {
		t.Run(c.want, func(t *testing.T) {
			assert.Equal(t, c.want, c.p.GetName())
		})
	}
}
