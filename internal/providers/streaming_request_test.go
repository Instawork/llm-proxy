package providers

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type errBodyReadCloser struct{}

func (errBodyReadCloser) Read(_ []byte) (int, error) { return 0, errors.New("read boom") }
func (errBodyReadCloser) Close() error               { return nil }

// TestProviderManager_IsStreamingRequest_RequiresPathOwnership asserts the
// post-fix contract: a request whose path is not owned by any registered
// provider is NOT streaming, even when carrying Accept: text/event-stream.
// Before this fix, ProviderManager.IsStreamingRequest OR'd all providers'
// IsStreamingRequest checks, and every individual provider treated
// Accept: text/event-stream as definitive — so any /health-style request
// carrying that header was silently flipped into the streaming wrapper.
//
// The fix lives at the manager layer (see ProviderForRequest); individual
// providers still treat the Accept header as definitive on the assumption
// that the caller has already routed correctly to them.
func TestProviderManager_IsStreamingRequest_RequiresPathOwnership(t *testing.T) {
	pm := NewProviderManager()
	pm.RegisterProvider(NewOpenAIProxy())
	pm.RegisterProvider(NewAnthropicProxy())
	pm.RegisterProvider(NewGeminiProxy())

	cases := []struct {
		name     string
		path     string
		wantStrm bool
	}{
		{"openai/own", "/openai/v1/chat/completions", true},
		{"anthropic/own", "/anthropic/v1/messages", true},
		{"gemini/own", "/gemini/v1beta/models/gemini-pro:streamGenerateContent", true},
		{"gemini/compat-v1", "/v1/models/gemini-pro:streamGenerateContent", true},
		{"gemini/compat-v1beta", "/v1beta/models/gemini-pro:streamGenerateContent", true},
		{"foreign/health", "/health", false},
		{"foreign/random", "/random", false},
		{"foreign/root", "/", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest("POST", tc.path, nil)
			req.Header.Set("Accept", "text/event-stream")
			assert.Equal(t, tc.wantStrm, pm.IsStreamingRequest(req),
				"manager streaming detection mismatch for %s", tc.path)
		})
	}
}

func TestOpenAI_IsStreamingRequest_NonOpenAIPath(t *testing.T) {
	op := NewOpenAIProxy()
	req, _ := http.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(`{"stream":true}`))
	assert.False(t, op.IsStreamingRequest(req))
}

func TestGemini_IsStreamingRequest_AltSSE(t *testing.T) {
	gp := NewGeminiProxy()
	req, _ := http.NewRequest("POST", "/gemini/v1/models/gemini-pro:generateContent?alt=sse", nil)
	assert.True(t, gp.IsStreamingRequest(req))
}

func TestProviders_IsStreamingResponse_ContentTypeChecks(t *testing.T) {
	op := NewOpenAIProxy()
	ap := NewAnthropicProxy()
	gp := NewGeminiProxy()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", r.URL.Query().Get("ct"))
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	doReq := func(ct string) *http.Response {
		resp, err := http.Get(srv.URL + "/?ct=" + ct)
		require.NoError(t, err)
		return resp
	}

	resp := doReq("text/event-stream")
	defer resp.Body.Close()
	assert.True(t, op.isStreamingResponse(resp))
	assert.True(t, ap.isStreamingResponse(resp))
	assert.True(t, gp.isStreamingResponse(resp))

	resp = doReq("application/json")
	defer resp.Body.Close()
	assert.False(t, op.isStreamingResponse(resp))
	assert.False(t, ap.isStreamingResponse(resp))
	assert.False(t, gp.isStreamingResponse(resp))

	resp = doReq("application/x-ndjson")
	defer resp.Body.Close()
	assert.True(t, gp.isStreamingResponse(resp))
}

func TestOpenAI_CheckStreamingInBody_GetBodyCached(t *testing.T) {
	op := NewOpenAIProxy()
	body := []byte(`{"stream":true}`)
	req, _ := http.NewRequest("POST", "/openai/v1/chat/completions", bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	assert.True(t, op.IsStreamingRequest(req))
}

func TestOpenAI_CheckStreamingInBody_GetBodyError(t *testing.T) {
	op := NewOpenAIProxy()
	req, _ := http.NewRequest("POST", "/openai/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	req.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("nope") }
	assert.False(t, op.IsStreamingRequest(req))
}

func TestOpenAI_CheckStreamingInBody_ReadError(t *testing.T) {
	op := NewOpenAIProxy()
	req, _ := http.NewRequest("POST", "/openai/v1/chat/completions", errBodyReadCloser{})
	assert.False(t, op.IsStreamingRequest(req))
}

func TestOpenAI_CheckStreamingInBody_NonBoolStream(t *testing.T) {
	op := NewOpenAIProxy()
	req, _ := http.NewRequest("POST", "/openai/v1/chat/completions", strings.NewReader(`{"stream":"yes"}`))
	assert.False(t, op.IsStreamingRequest(req))
}

func TestOpenAI_CheckStreamingInBody_NilBody(t *testing.T) {
	op := NewOpenAIProxy()
	req, _ := http.NewRequest("POST", "/openai/v1/chat/completions", nil)
	assert.False(t, op.IsStreamingRequest(req))
}

func TestOpenAI_CheckStreamingInBody_GetBodyReadError(t *testing.T) {
	op := NewOpenAIProxy()
	req, _ := http.NewRequest("POST", "/openai/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	req.GetBody = func() (io.ReadCloser, error) { return errBodyReadCloser{}, nil }
	assert.False(t, op.IsStreamingRequest(req))
}

func TestAnthropic_CheckStreamingInBody_GetBodyCached(t *testing.T) {
	ap := NewAnthropicProxy()
	body := []byte(`{"stream":true}`)
	req, _ := http.NewRequest("POST", "/anthropic/v1/messages", bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(body)), nil
	}
	assert.True(t, ap.IsStreamingRequest(req))
}

func TestAnthropic_CheckStreamingInBody_GetBodyError(t *testing.T) {
	ap := NewAnthropicProxy()
	req, _ := http.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(`{"stream":true}`))
	req.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("boom") }
	assert.False(t, ap.IsStreamingRequest(req))
}

func TestAnthropic_CheckStreamingInBody_NoGetBody_StreamTrue(t *testing.T) {
	ap := NewAnthropicProxy()
	req, _ := http.NewRequest("POST", "/anthropic/v1/messages", io.NopCloser(strings.NewReader(`{"stream":true}`)))
	req.GetBody = nil
	assert.True(t, ap.IsStreamingRequest(req))
}

func TestAnthropic_CheckStreamingInBody_ReadError(t *testing.T) {
	ap := NewAnthropicProxy()
	req, _ := http.NewRequest("POST", "/anthropic/v1/messages", errBodyReadCloser{})
	req.GetBody = nil
	assert.False(t, ap.IsStreamingRequest(req))
}

func TestAnthropic_CheckStreamingInBody_InvalidJSON(t *testing.T) {
	ap := NewAnthropicProxy()
	req, _ := http.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader("not json"))
	assert.False(t, ap.IsStreamingRequest(req))
}

func TestAnthropic_CheckStreamingInBody_NonBoolStream(t *testing.T) {
	ap := NewAnthropicProxy()
	req, _ := http.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(`{"stream":"yes"}`))
	assert.False(t, ap.IsStreamingRequest(req))
}

func TestAnthropic_CheckStreamingInBody_NilBody(t *testing.T) {
	ap := NewAnthropicProxy()
	req, _ := http.NewRequest("POST", "/anthropic/v1/messages", nil)
	assert.False(t, ap.IsStreamingRequest(req))
}

func TestAnthropic_CheckStreamingInBody_GetBodyReadError(t *testing.T) {
	ap := NewAnthropicProxy()
	req, _ := http.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(`{"stream":true}`))
	req.GetBody = func() (io.ReadCloser, error) { return errBodyReadCloser{}, nil }
	assert.False(t, ap.IsStreamingRequest(req))
}
