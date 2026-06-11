package providers

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestUserIDFromRequest_ShortCircuits(t *testing.T) {
	op := NewOpenAIProxy()
	ap := NewAnthropicProxy()

	for _, p := range []Provider{op, ap} {
		req, _ := http.NewRequest("GET", "/openai/v1/chat", nil)
		assert.Equal(t, "", p.UserIDFromRequest(req))
	}

	req, _ := http.NewRequest("POST", "/random/path", strings.NewReader(`{}`))
	assert.Equal(t, "", op.UserIDFromRequest(req))
	assert.Equal(t, "", ap.UserIDFromRequest(req))

	req, _ = http.NewRequest("POST", "/openai/v1/chat/completions", strings.NewReader("nope"))
	assert.Equal(t, "", op.UserIDFromRequest(req))

	req, _ = http.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader("nope"))
	assert.Equal(t, "", ap.UserIDFromRequest(req))

	req, _ = http.NewRequest("POST", "/openai/v1/chat/completions", nil)
	req.Body = io.NopCloser(strings.NewReader(""))
	assert.Equal(t, "", op.UserIDFromRequest(req))
}

func TestOpenAI_ReadRequestBodyForUserID_GetBody(t *testing.T) {
	op := NewOpenAIProxy()
	body := []byte(`{"user":"u-1","messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest("POST", "/openai/v1/chat/completions", bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	assert.Equal(t, "u-1", op.UserIDFromRequest(req))
}

func TestAnthropic_ReadRequestBodyForUserID_GetBody(t *testing.T) {
	ap := NewAnthropicProxy()
	body := []byte(`{"metadata":{"user_id":"u-2"},"messages":[]}`)
	req, _ := http.NewRequest("POST", "/anthropic/v1/messages", bytes.NewReader(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	assert.Equal(t, "u-2", ap.UserIDFromRequest(req))
}

func TestAnthropic_ReadRequestBodyForUserID_NoGetBody(t *testing.T) {
	ap := NewAnthropicProxy()
	body := []byte(`{"metadata":{"user_id":"u-noget"},"messages":[]}`)
	req, _ := http.NewRequest("POST", "/anthropic/v1/messages", io.NopCloser(bytes.NewReader(body)))
	req.GetBody = nil
	assert.Equal(t, "u-noget", ap.UserIDFromRequest(req))
}

func TestOpenAI_ReadRequestBodyForUserID_NoGetBody(t *testing.T) {
	op := NewOpenAIProxy()
	body := []byte(`{"user":"u-noget","messages":[{"role":"user","content":"hi"}]}`)
	req, _ := http.NewRequest("POST", "/openai/v1/chat/completions", io.NopCloser(bytes.NewReader(body)))
	req.GetBody = nil
	assert.Equal(t, "u-noget", op.UserIDFromRequest(req))
}

func TestAnthropic_ReadRequestBodyForUserID_GetBodyError(t *testing.T) {
	ap := NewAnthropicProxy()
	req, _ := http.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(`{"metadata":{}}`))
	req.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("nope") }
	assert.Equal(t, "", ap.UserIDFromRequest(req))
}

func TestOpenAI_ReadRequestBodyForUserID_GetBodyError(t *testing.T) {
	op := NewOpenAIProxy()
	req, _ := http.NewRequest("POST", "/openai/v1/chat/completions", strings.NewReader(`{"user":"x"}`))
	req.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("boom") }
	assert.Equal(t, "", op.UserIDFromRequest(req))
}
