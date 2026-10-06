package providers

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countingProvider wraps the real OpenAI provider and counts how many times
// each body-derived accessor actually runs.
type countingProvider struct {
	*OpenAIProxy
	streamCalls, modelCalls, userCalls int
}

func (c *countingProvider) IsStreamingRequest(req *http.Request) bool {
	c.streamCalls++
	return c.OpenAIProxy.IsStreamingRequest(req)
}

func (c *countingProvider) ExtractRequestModelAndMessages(req *http.Request) (string, []string) {
	c.modelCalls++
	return c.OpenAIProxy.ExtractRequestModelAndMessages(req)
}

func (c *countingProvider) UserIDFromRequest(req *http.Request) string {
	c.userCalls++
	return c.OpenAIProxy.UserIDFromRequest(req)
}

func memoRequest(body string, withMemo bool) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	if withMemo {
		req = req.WithContext(WithRequestMemo(req.Context()))
	}
	return req
}

const memoBody = `{"model":"gpt-4o-mini","stream":true,"user":"u-123","messages":[{"role":"user","content":"hello"}]}`

func TestRequestMemo_ParsesOncePerFact(t *testing.T) {
	p := &countingProvider{OpenAIProxy: NewOpenAIProxy()}
	req := memoRequest(memoBody, true)

	for range 5 {
		assert.True(t, RequestIsStreaming(p, req))
		model, msgs := RequestModelAndMessages(p, req)
		assert.Equal(t, "gpt-4o-mini", model)
		assert.Equal(t, []string{"hello"}, msgs)
		assert.Equal(t, "u-123", RequestUserID(p, req))
	}
	assert.Equal(t, 1, p.streamCalls, "stream flag parsed once")
	assert.Equal(t, 1, p.modelCalls, "model/messages parsed once")
	assert.Equal(t, 1, p.userCalls, "user ID parsed once")

	// The ProviderManager entry point shares the same memo.
	pm := NewProviderManager()
	pm.RegisterProvider(p)
	assert.True(t, pm.IsStreamingRequest(req))
	assert.Equal(t, 1, p.streamCalls)
}

func TestRequestMemo_InvalidateRecomputesFromNewBody(t *testing.T) {
	p := &countingProvider{OpenAIProxy: NewOpenAIProxy()}
	req := memoRequest(memoBody, true)

	model, msgs := RequestModelAndMessages(p, req)
	require.Equal(t, "gpt-4o-mini", model)
	require.Equal(t, []string{"hello"}, msgs)

	// Simulate a body rewrite (what PII redaction does) and invalidate.
	redacted := []byte(`{"model":"gpt-4o-mini","stream":true,"user":"u-123","messages":[{"role":"user","content":"[REDACTED]"}]}`)
	req.Body = io.NopCloser(bytes.NewReader(redacted))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(redacted)), nil }
	InvalidateRequestMemo(req)

	model, msgs = RequestModelAndMessages(p, req)
	assert.Equal(t, "gpt-4o-mini", model)
	assert.Equal(t, []string{"[REDACTED]"}, msgs, "post-invalidate read must see the rewritten body")
	assert.Equal(t, 2, p.modelCalls)
}

func TestRequestMemo_AbsentMemoFallsThrough(t *testing.T) {
	p := &countingProvider{OpenAIProxy: NewOpenAIProxy()}
	req := memoRequest(memoBody, false)
	assert.Nil(t, RequestMemoFrom(req.Context()))

	for range 3 {
		assert.True(t, RequestIsStreaming(p, req))
	}
	assert.Equal(t, 3, p.streamCalls, "without a memo every call hits the provider")

	// Nil-safety.
	InvalidateRequestMemo(nil)
	assert.False(t, RequestIsStreaming(nil, req))
	m, msgs := RequestModelAndMessages(p, nil)
	assert.Equal(t, "", m)
	assert.Nil(t, msgs)
	assert.Equal(t, "", RequestUserID(nil, req))
}

func TestBodyStreamFlag(t *testing.T) {
	assert.True(t, bodyStreamFlag([]byte(`{"stream":true}`)))
	assert.False(t, bodyStreamFlag([]byte(`{"stream":false}`)))
	assert.False(t, bodyStreamFlag([]byte(`{}`)))
	assert.False(t, bodyStreamFlag([]byte(`{"stream":"true"}`)), "non-boolean stream reads as false")
	assert.False(t, bodyStreamFlag([]byte(`{"stream":1}`)))
	assert.False(t, bodyStreamFlag([]byte(`not json`)))
	assert.False(t, bodyStreamFlag([]byte(`[true]`)))
}

func TestExtractOpenAIModelAndMessages_TypedShapes(t *testing.T) {
	t.Run("chat string content", func(t *testing.T) {
		model, msgs := extractOpenAIModelAndMessages([]byte(`{"model":"m","messages":[{"role":"system","content":"a"},{"role":"user","content":""},{"role":"user","content":"b"}]}`))
		assert.Equal(t, "m", model)
		assert.Equal(t, []string{"a", "b"}, msgs)
	})
	t.Run("chat multimodal parts keep only text", func(t *testing.T) {
		_, msgs := extractOpenAIModelAndMessages([]byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"x"}},{"type":"text","text":""}]}]}`))
		assert.Equal(t, []string{"look"}, msgs)
	})
	t.Run("responses input string, blocks, and bare strings", func(t *testing.T) {
		_, msgs := extractOpenAIModelAndMessages([]byte(`{"model":"r","input":"plain"}`))
		assert.Equal(t, []string{"plain"}, msgs)
		_, msgs = extractOpenAIModelAndMessages([]byte(`{"input":[{"type":"input_text","text":"one"},{"type":"text","text":"two"},{"type":"input_image","image_url":"x"},"three"]}`))
		assert.Equal(t, []string{"one", "two", "three"}, msgs)
	})
	t.Run("legacy prompt", func(t *testing.T) {
		model, msgs := extractOpenAIModelAndMessages([]byte(`{"model":"gpt-3.5-turbo-instruct","prompt":"say hi"}`))
		assert.Equal(t, "gpt-3.5-turbo-instruct", model)
		assert.Equal(t, []string{"say hi"}, msgs)
	})
	t.Run("shape mismatches are skipped, not fatal", func(t *testing.T) {
		// messages is not an array and model is not a string: keep going.
		model, msgs := extractOpenAIModelAndMessages([]byte(`{"model":42,"messages":"nope","prompt":"p"}`))
		assert.Equal(t, "", model)
		assert.Equal(t, []string{"p"}, msgs)
	})
	t.Run("malformed JSON", func(t *testing.T) {
		model, msgs := extractOpenAIModelAndMessages([]byte(`{"model":`))
		assert.Equal(t, "", model)
		assert.Nil(t, msgs)
	})
}
