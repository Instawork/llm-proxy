package providers

import (
	"net/http"
	"testing"
	"time"
)

func TestParseRateLimitFromResponse_OpenAI(t *testing.T) {
	// Create a mock response with OpenAI rate limit headers
	resp := &http.Response{
		Header: http.Header{
			"x-ratelimit-limit-requests":     []string{"60"},
			"x-ratelimit-remaining-requests": []string{"59"},
			"x-ratelimit-reset-requests":     []string{"1s"},
			"x-ratelimit-limit-tokens":       []string{"150000"},
			"x-ratelimit-remaining-tokens":   []string{"149984"},
			"x-ratelimit-reset-tokens":       []string{"6m0s"},
		},
	}

	openAI := NewOpenAIProxy()
	rateLimitInfo := openAI.ParseRateLimitFromResponse(resp)

	if rateLimitInfo == nil {
		t.Fatal("Expected rate limit info, got nil")
	}

	// Verify parsed values
	if rateLimitInfo.Provider != "openai" {
		t.Errorf("Expected provider 'openai', got '%s'", rateLimitInfo.Provider)
	}

	if rateLimitInfo.RequestLimit != 60 {
		t.Errorf("Expected request limit 60, got %d", rateLimitInfo.RequestLimit)
	}

	if rateLimitInfo.RequestRemaining != 59 {
		t.Errorf("Expected request remaining 59, got %d", rateLimitInfo.RequestRemaining)
	}

	if rateLimitInfo.RequestReset != 1*time.Second {
		t.Errorf("Expected request reset 1s, got %v", rateLimitInfo.RequestReset)
	}

	if rateLimitInfo.TokenLimit != 150000 {
		t.Errorf("Expected token limit 150000, got %d", rateLimitInfo.TokenLimit)
	}

	if rateLimitInfo.TokenRemaining != 149984 {
		t.Errorf("Expected token remaining 149984, got %d", rateLimitInfo.TokenRemaining)
	}

	if rateLimitInfo.TokenReset != 6*time.Minute {
		t.Errorf("Expected token reset 6m0s, got %v", rateLimitInfo.TokenReset)
	}

	if !rateLimitInfo.HasRateLimitInfo {
		t.Error("Expected HasRateLimitInfo to be true")
	}
}

func TestParseRateLimitFromResponse_Anthropic(t *testing.T) {
	// Create a mock response with Anthropic rate limit headers
	resp := &http.Response{
		Header: http.Header{
			"anthropic-ratelimit-requests-limit":           []string{"50"},
			"anthropic-ratelimit-requests-remaining":       []string{"49"},
			"anthropic-ratelimit-requests-reset":           []string{"60s"},
			"anthropic-ratelimit-tokens-limit":             []string{"100000"},
			"anthropic-ratelimit-tokens-remaining":         []string{"99950"},
			"anthropic-ratelimit-tokens-reset":             []string{"300s"},
			"anthropic-ratelimit-input-tokens-limit":       []string{"75000"},
			"anthropic-ratelimit-input-tokens-remaining":   []string{"74980"},
			"anthropic-ratelimit-input-tokens-reset":       []string{"120s"},
			"anthropic-ratelimit-output-tokens-limit":      []string{"25000"},
			"anthropic-ratelimit-output-tokens-remaining":  []string{"24970"},
			"anthropic-ratelimit-output-tokens-reset":      []string{"180s"},
		},
	}

	anthropic := NewAnthropicProxy()
	rateLimitInfo := anthropic.ParseRateLimitFromResponse(resp)

	if rateLimitInfo == nil {
		t.Fatal("Expected rate limit info, got nil")
	}

	// Verify parsed values
	if rateLimitInfo.Provider != "anthropic" {
		t.Errorf("Expected provider 'anthropic', got '%s'", rateLimitInfo.Provider)
	}

	if rateLimitInfo.RequestLimit != 50 {
		t.Errorf("Expected request limit 50, got %d", rateLimitInfo.RequestLimit)
	}

	if rateLimitInfo.InputTokenLimit != 75000 {
		t.Errorf("Expected input token limit 75000, got %d", rateLimitInfo.InputTokenLimit)
	}

	if rateLimitInfo.OutputTokenLimit != 25000 {
		t.Errorf("Expected output token limit 25000, got %d", rateLimitInfo.OutputTokenLimit)
	}

	if !rateLimitInfo.HasRateLimitInfo {
		t.Error("Expected HasRateLimitInfo to be true")
	}
}

func TestParseRateLimitFromResponse_Gemini(t *testing.T) {
	// Create a mock response - Gemini doesn't provide rate limit headers
	resp := &http.Response{
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}

	gemini := NewGeminiProxy()
	rateLimitInfo := gemini.ParseRateLimitFromResponse(resp)

	// Gemini should return nil since it doesn't provide rate limit headers
	if rateLimitInfo != nil {
		t.Errorf("Expected nil for Gemini rate limit info, got %v", rateLimitInfo)
	}
}

func TestParseRateLimitFromResponse_NoHeaders(t *testing.T) {
	// Test with response that has no rate limit headers
	resp := &http.Response{
		Header: http.Header{
			"Content-Type": []string{"application/json"},
		},
	}

	openAI := NewOpenAIProxy()
	rateLimitInfo := openAI.ParseRateLimitFromResponse(resp)

	// Should return nil when no rate limit headers are present
	if rateLimitInfo != nil {
		t.Errorf("Expected nil when no rate limit headers present, got %v", rateLimitInfo)
	}
}

func TestParseRateLimitFromResponse_NilResponse(t *testing.T) {
	openAI := NewOpenAIProxy()
	rateLimitInfo := openAI.ParseRateLimitFromResponse(nil)

	// Should handle nil response gracefully
	if rateLimitInfo != nil {
		t.Errorf("Expected nil for nil response, got %v", rateLimitInfo)
	}
} 
