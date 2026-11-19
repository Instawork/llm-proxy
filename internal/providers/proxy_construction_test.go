package providers

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
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

func TestCreateGenericRewrite_StripsPrefixAndGzip(t *testing.T) {
	op := NewOpenAIProxy()
	target, _ := url.Parse("https://api.openai.com")
	rewrite := CreateGenericRewrite(op, target, true)

	in, _ := http.NewRequest("POST", "/openai/v1/chat/completions", nil)
	in.Header.Set("Accept-Encoding", "gzip")
	out := in.Clone(in.Context())
	rewrite(&httputil.ProxyRequest{In: in, Out: out})

	assert.Equal(t, "https", out.URL.Scheme)
	assert.Equal(t, target.Host, out.URL.Host)
	assert.Equal(t, "/v1/chat/completions", out.URL.Path)
	assert.Equal(t, "", out.Header.Get("Accept-Encoding"))
	assert.Equal(t, target.Host, out.Host)
	// The inbound request is never mutated in Rewrite mode.
	assert.Equal(t, "/openai/v1/chat/completions", in.URL.Path)
	assert.Equal(t, "gzip", in.Header.Get("Accept-Encoding"))
}

func TestGunzipIfNeeded(t *testing.T) {
	plain := []byte(`{"output":[]}`)
	out, err := gunzipIfNeeded(plain)
	require.NoError(t, err)
	assert.Equal(t, plain, out)
	assert.Same(t, &plain[0], &out[0], "plain bodies are returned without copying")

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write(plain)
	require.NoError(t, gz.Close())
	out, err = gunzipIfNeeded(buf.Bytes())
	require.NoError(t, err)
	assert.Equal(t, plain, out)

	_, err = gunzipIfNeeded([]byte{0x1f, 0x8b, 0xff, 0xff})
	assert.Error(t, err, "truncated gzip must surface an error, not silently parse")
	assert.Empty(t, mustGunzip(t, nil))
}

func mustGunzip(t *testing.T, b []byte) []byte {
	t.Helper()
	out, err := gunzipIfNeeded(b)
	require.NoError(t, err)
	return out
}

func TestOpenAIBodyHasOutput(t *testing.T) {
	assert.True(t, openAIBodyHasOutput([]byte(`{"id":"r","output":[{"type":"message"}]}`)))
	assert.True(t, openAIBodyHasOutput([]byte(`{"output":null}`)), "key present with null still routes to Responses parser")
	assert.False(t, openAIBodyHasOutput([]byte(`{"id":"c","choices":[]}`)))
	assert.False(t, openAIBodyHasOutput([]byte(`not json`)))
	assert.False(t, openAIBodyHasOutput(nil))
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
	assertJSONTransportError(t, rr, "openai transport")
}

// assertJSONTransportError verifies the non-streaming ErrorHandler body is
// the {"error":"[UPSTREAM] <provider> transport: ..."} JSON shape (so API
// clients like n8n can surface a readable message) rather than plain text.
func assertJSONTransportError(t *testing.T, rr *httptest.ResponseRecorder, wantFragment string) {
	t.Helper()
	assert.Contains(t, rr.Header().Get("Content-Type"), "application/json")
	assert.Contains(t, rr.Header().Get("X-Llm-Proxy-Error-Source"), "upstream")
	var body struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Contains(t, body.Error, "[UPSTREAM]")
	assert.Contains(t, body.Error, wantFragment)
}

func TestAnthropicProxy_ErrorHandler_Streaming(t *testing.T) {
	rr := runErrorHandler(t, NewAnthropicProxy(), "/anthropic/v1/messages", "text/event-stream")
	assert.Equal(t, http.StatusBadGateway, rr.Code)
	assert.Contains(t, rr.Body.String(), "[UPSTREAM]")
}

func TestAnthropicProxy_ErrorHandler_NonStreaming(t *testing.T) {
	rr := runErrorHandler(t, NewAnthropicProxy(), "/anthropic/v1/messages", "")
	assert.Equal(t, http.StatusBadGateway, rr.Code)
	assertJSONTransportError(t, rr, "anthropic transport")
}

func TestGeminiProxy_ErrorHandler_Streaming(t *testing.T) {
	rr := runErrorHandler(t, NewGeminiProxy(), "/gemini/v1/models/gemini-pro:streamGenerateContent?alt=sse", "text/event-stream")
	assert.Equal(t, http.StatusBadGateway, rr.Code)
	assert.Contains(t, rr.Body.String(), "[UPSTREAM]")
}

func TestGeminiProxy_ErrorHandler_NonStreaming(t *testing.T) {
	rr := runErrorHandler(t, NewGeminiProxy(), "/gemini/v1/models/gemini-pro:generateContent", "")
	assert.Equal(t, http.StatusBadGateway, rr.Code)
	assertJSONTransportError(t, rr, "gemini transport")
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
	assertJSONTransportError(t, rr, "bedrock transport")
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
	assertJSONTransportError(t, rr, "bedrock-mantle transport")
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

func TestNormalizeUpstreamAcceptEncoding(t *testing.T) {
	cases := map[string]struct {
		in   []string
		want string
	}{
		"httpx with brotli and zstd installed": {[]string{"gzip, deflate, br, zstd"}, "gzip"},
		"gzip only":                            {[]string{"gzip"}, "gzip"},
		"wildcard":                             {[]string{"*"}, "gzip"},
		"gzip in a second header line":         {[]string{"br", "GZIP;q=0.5"}, "gzip"},
		"no gzip offered":                      {[]string{"br, zstd"}, ""},
		"gzip refused":                         {[]string{"gzip;q=0, br"}, ""},
		"identity only":                        {[]string{"identity"}, ""},
		"header absent":                        {nil, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			h := http.Header{}
			for _, v := range tc.in {
				h.Add("Accept-Encoding", v)
			}
			normalizeUpstreamAcceptEncoding(h)
			assert.Equal(t, tc.want, h.Get("Accept-Encoding"))
			assert.LessOrEqual(t, len(h.Values("Accept-Encoding")), 1)
		})
	}
}

// encodingNegotiatingRT stands in for an upstream that, like api.anthropic.com,
// answers in the best encoding the request offers: brotli when "br" is
// listed, gzip otherwise. The brotli body is opaque bytes, which is all the
// proxy can make of it.
type encodingNegotiatingRT struct {
	json         []byte
	seenEncoding string
}

func (rt *encodingNegotiatingRT) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.seenEncoding = req.Header.Get("Accept-Encoding")
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Request:    req,
	}
	if strings.Contains(rt.seenEncoding, "br") {
		resp.Header.Set("Content-Encoding", "br")
		resp.Body = io.NopCloser(bytes.NewReader([]byte{0x83, 0x12, 0x00, 0xf8, 0x05, 0xfe}))
		return resp, nil
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write(rt.json)
	_ = gz.Close()
	resp.Header.Set("Content-Encoding", "gzip")
	resp.Body = io.NopCloser(&buf)
	return resp, nil
}

// A client that advertises br/zstd must still get a response whose usage the
// proxy can read: otherwise the call is billed upstream and never metered.
func TestAnthropicProxy_ClientAdvertisingBrotliStaysMeterable(t *testing.T) {
	upstream := &encodingNegotiatingRT{json: []byte(
		`{"id":"msg_1","type":"message","model":"claude-haiku-5-5","usage":{"input_tokens":1200,"output_tokens":34}}`,
	)}
	p := NewAnthropicProxy()
	p.WrapTransport(func(http.RoundTripper) http.RoundTripper { return upstream })

	req := httptest.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(`{"model":"claude-haiku-5-5"}`))
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	rr := httptest.NewRecorder()
	p.Proxy().ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "gzip", upstream.seenEncoding)
	assert.Equal(t, "gzip", rr.Header().Get("Content-Encoding"), "the client still gets an encoding it asked for")

	metadata, err := p.ParseResponseMetadata(bytes.NewReader(rr.Body.Bytes()), false)
	require.NoError(t, err)
	require.NotNil(t, metadata)
	assert.Equal(t, 1200, metadata.InputTokens)
	assert.Equal(t, 34, metadata.OutputTokens)
}
