package providers

import (
	"net/http"
	"strings"
	"testing"
)

func TestGeminiExtractRequestModelAndMessages(t *testing.T) {
	provider := NewGeminiProxy()

	// Test with v1beta path
	body := `{"contents":[{"parts":[{"text":"hello"}]}]}`
	req, _ := http.NewRequest("POST", "/v1beta/models/gemini-pro:generateContent", strings.NewReader(body))
	model, msgs := provider.ExtractRequestModelAndMessages(req)
	if model != "gemini-pro" {
		t.Errorf("expected gemini-pro, got %q", model)
	}
	if len(msgs) != 1 || msgs[0] != "hello" {
		t.Errorf("expected [hello], got %v", msgs)
	}

	// Test with explicit model in body
	body = `{"model":"models/gemini-1.5-flash", "contents":[{"parts":[{"text":"hello"}]}]}`
	req, _ = http.NewRequest("POST", "/gemini/v1/models/gemini-pro:generateContent", strings.NewReader(body))
	model, _ = provider.ExtractRequestModelAndMessages(req)
	if model != "gemini-1.5-flash" {
		t.Errorf("expected gemini-1.5-flash, got %q", model)
	}

	// Test with invalid path and no body model
	req, _ = http.NewRequest("POST", "/v1/invalid/path", strings.NewReader(`{}`))
	model, _ = provider.ExtractRequestModelAndMessages(req)
	if model != "" {
		t.Errorf("expected empty model, got %q", model)
	}
}

func TestOpenAIIsStreamingRequest(t *testing.T) {
	provider := NewOpenAIProxy()

	req, _ := http.NewRequest("POST", "/openai/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	if !provider.IsStreamingRequest(req) {
		t.Errorf("expected stream:true to be detected")
	}

	req, _ = http.NewRequest("POST", "/openai/v1/chat/completions", strings.NewReader(`{"stream":false}`))
	req.Header.Set("Content-Type", "application/json")
	if provider.IsStreamingRequest(req) {
		t.Errorf("expected stream:false to not be detected")
	}
}

func TestAnthropicIsStreamingRequest(t *testing.T) {
	provider := NewAnthropicProxy()

	req, _ := http.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(`{"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	if !provider.IsStreamingRequest(req) {
		t.Errorf("expected stream:true to be detected")
	}

	req, _ = http.NewRequest("POST", "/anthropic/v1/messages", strings.NewReader(`{"stream":false}`))
	req.Header.Set("Content-Type", "application/json")
	if provider.IsStreamingRequest(req) {
		t.Errorf("expected stream:false to not be detected")
	}
}

func TestGeminiIsStreamingRequest(t *testing.T) {
	provider := NewGeminiProxy()

	req, _ := http.NewRequest("POST", "/v1beta/models/gemini-pro:streamGenerateContent", nil)
	if !provider.IsStreamingRequest(req) {
		t.Errorf("expected streamGenerateContent to be detected")
	}

	req, _ = http.NewRequest("POST", "/v1beta/models/gemini-pro:generateContent", nil)
	if provider.IsStreamingRequest(req) {
		t.Errorf("expected generateContent to not be detected")
	}
}
