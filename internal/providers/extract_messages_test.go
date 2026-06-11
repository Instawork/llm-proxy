package providers

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOpenAIExtractRequestModelAndMessages_Variants(t *testing.T) {
	op := NewOpenAIProxy()

	body := `{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`
	r, _ := http.NewRequest("POST", "/openai/v1/chat/completions", strings.NewReader(body))
	model, msgs := op.ExtractRequestModelAndMessages(r)
	assert.Equal(t, "gpt-4o", model)
	assert.Equal(t, []string{"hello"}, msgs)

	body = `{"model":"gpt-4o","messages":[{"role":"user","content":[{"type":"text","text":"alpha"},{"type":"text","text":"beta"}]}]}`
	r, _ = http.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body))
	_, msgs = op.ExtractRequestModelAndMessages(r)
	assert.Equal(t, []string{"alpha", "beta"}, msgs)

	body = `{"model":"gpt-4o","input":"howdy"}`
	r, _ = http.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	model, msgs = op.ExtractRequestModelAndMessages(r)
	assert.Equal(t, "gpt-4o", model)
	assert.Equal(t, []string{"howdy"}, msgs)

	body = `{"input":[{"type":"input_text","text":"foo"},{"type":"text","text":"bar"},"raw"]}`
	r, _ = http.NewRequest("POST", "/v1/responses", strings.NewReader(body))
	_, msgs = op.ExtractRequestModelAndMessages(r)
	assert.Equal(t, []string{"foo", "bar", "raw"}, msgs)

	body = `{"prompt":"continue this"}`
	r, _ = http.NewRequest("POST", "/v1/completions", strings.NewReader(body))
	_, msgs = op.ExtractRequestModelAndMessages(r)
	assert.Equal(t, []string{"continue this"}, msgs)

	r, _ = http.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(`{}`))
	model, msgs = op.ExtractRequestModelAndMessages(r)
	assert.Equal(t, "", model)
	assert.Empty(t, msgs)

	r, _ = http.NewRequest("GET", "/openai/v1/chat/completions", nil)
	model, _ = op.ExtractRequestModelAndMessages(r)
	assert.Equal(t, "", model)

	model, _ = op.ExtractRequestModelAndMessages(nil)
	assert.Equal(t, "", model)

	r, _ = http.NewRequest("POST", "/openai/v1/chat/completions", nil)
	r.Body = io.NopCloser(strings.NewReader(""))
	model, _ = op.ExtractRequestModelAndMessages(r)
	assert.Equal(t, "", model)

	r, _ = http.NewRequest("POST", "/openai/v1/chat/completions", strings.NewReader("not json"))
	model, _ = op.ExtractRequestModelAndMessages(r)
	assert.Equal(t, "", model)
}

func TestAnthropicExtractRequestModelAndMessages_Variants(t *testing.T) {
	ap := NewAnthropicProxy()

	body := `{"model":"claude-3","messages":[{"role":"user","content":[{"type":"text","text":"x"},{"type":"text","text":"y"}]}]}`
	r, _ := http.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(body))
	model, msgs := ap.ExtractRequestModelAndMessages(r)
	assert.Equal(t, "claude-3", model)
	assert.Equal(t, []string{"x", "y"}, msgs)

	r, _ = http.NewRequest("POST", "/openai/v1/chat/completions", strings.NewReader(body))
	model, _ = ap.ExtractRequestModelAndMessages(r)
	assert.Equal(t, "", model)

	r, _ = http.NewRequest("POST", "/anthropic/v1/embeddings", strings.NewReader(body))
	model, _ = ap.ExtractRequestModelAndMessages(r)
	assert.Equal(t, "", model)

	r, _ = http.NewRequest("GET", "/anthropic/v1/messages", nil)
	model, _ = ap.ExtractRequestModelAndMessages(r)
	assert.Equal(t, "", model)

	r, _ = http.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader("not json"))
	model, _ = ap.ExtractRequestModelAndMessages(r)
	assert.Equal(t, "", model)
}

func TestGemini_ExtractRequestModelAndMessages_ContentsParts(t *testing.T) {
	gp := NewGeminiProxy()
	body := `{"contents":[{"parts":[{"text":"hello"},{"text":"world"}]}]}`
	r, _ := http.NewRequest("POST", "/v1beta/models/gemini-1.5-pro:generateContent", strings.NewReader(body))
	model, msgs := gp.ExtractRequestModelAndMessages(r)
	assert.NotEmpty(t, model)
	assert.Equal(t, []string{"hello", "world"}, msgs)
}

func TestGemini_ExtractRequestModelAndMessages_NonGeminiPath(t *testing.T) {
	gp := NewGeminiProxy()
	r, _ := http.NewRequest("POST", "/openai/v1/chat/completions", strings.NewReader(`{}`))
	model, msgs := gp.ExtractRequestModelAndMessages(r)
	assert.Equal(t, "", model)
	assert.Empty(t, msgs)
}

func TestGemini_ExtractRequestModelAndMessages_GET_ShortCircuits(t *testing.T) {
	gp := NewGeminiProxy()
	r, _ := http.NewRequest("GET", "/gemini/v1/models", nil)
	model, _ := gp.ExtractRequestModelAndMessages(r)
	assert.Equal(t, "", model)
}

func TestGemini_ExtractRequestModelAndMessages_InvalidJSON(t *testing.T) {
	gp := NewGeminiProxy()
	r, _ := http.NewRequest("POST", "/gemini/v1/models/gemini-pro:generateContent", strings.NewReader("not json"))
	model, _ := gp.ExtractRequestModelAndMessages(r)
	assert.Equal(t, "", model)
}
