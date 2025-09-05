package providers

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/ratelimit"
)

// validateMetadata is a helper function to validate metadata parsing
func validateGeminiMetadata(t *testing.T, metadata *LLMResponseMetadata, expectedProvider string, isStreaming bool) {
	if metadata == nil {
		t.Fatal("Metadata is nil")
	}

	if metadata.Provider != expectedProvider {
		t.Errorf("Expected provider %s, got %s", expectedProvider, metadata.Provider)
	}

	if metadata.IsStreaming != isStreaming {
		t.Errorf("Expected IsStreaming %v, got %v", isStreaming, metadata.IsStreaming)
	}

	if metadata.Model == "" {
		t.Error("Model should not be empty")
	}

	// For streaming responses, usage information might not be available in all chunks
	// So we're more lenient and only check if tokens are non-negative
	if isStreaming {
		if metadata.TotalTokens < 0 {
			t.Error("Total tokens should not be negative")
		}
		if metadata.InputTokens < 0 {
			t.Error("Input tokens should not be negative")
		}
		if metadata.OutputTokens < 0 {
			t.Error("Output tokens should not be negative")
		}

		// For streaming, we might have partial or complete usage information
		if metadata.TotalTokens > 0 {
			t.Logf("Complete usage information found in streaming response")
		} else {
			t.Logf("Partial usage information in streaming response (expected for some chunks)")
		}
	} else {
		// For non-streaming responses, we expect positive token counts
		if metadata.TotalTokens <= 0 {
			t.Error("Total tokens should be positive for non-streaming responses")
		}
		if metadata.InputTokens <= 0 {
			t.Error("Input tokens should be positive for non-streaming responses")
		}
		if metadata.OutputTokens <= 0 {
			t.Error("Output tokens should be positive for non-streaming responses")
		}
	}

	// Verify total tokens calculation
	// For Gemini, when there are no thought tokens: TotalTokens = InputTokens + OutputTokens
	// When there are thought tokens: TotalTokens = InputTokens + OutputTokens + ThoughtTokens
	if metadata.TotalTokens > 0 && metadata.InputTokens > 0 && metadata.OutputTokens > 0 {
		expectedTotal := metadata.InputTokens + metadata.OutputTokens
		if metadata.ThoughtTokens > 0 {
			expectedTotal += metadata.ThoughtTokens
		}
		if metadata.TotalTokens != expectedTotal {
			// Some models may include thought tokens in the total without reporting them separately
			// This is acceptable as long as the total is greater than input + output
			if metadata.ThoughtTokens == 0 && metadata.TotalTokens > expectedTotal {
				// Inferred thought tokens
				inferredThoughtTokens := metadata.TotalTokens - expectedTotal
				t.Logf("Total tokens include inferred thought tokens: %d", inferredThoughtTokens)
			} else {
				t.Errorf("Total tokens mismatch: expected %d, got %d (thought tokens: %d)", expectedTotal, metadata.TotalTokens, metadata.ThoughtTokens)
			}
		}
	}

	// Thought tokens are optional for Gemini
	if metadata.ThoughtTokens > 0 {
		t.Logf("Thought tokens found: %d", metadata.ThoughtTokens)
	}

	t.Logf("Metadata validation passed: Model=%s, InputTokens=%d, OutputTokens=%d, TotalTokens=%d, ThoughtTokens=%d, IsStreaming=%v",
		metadata.Model, metadata.InputTokens, metadata.OutputTokens, metadata.TotalTokens, metadata.ThoughtTokens, metadata.IsStreaming)
}

func TestGemini_ModelNameStripping(t *testing.T) {
	// Test non-streaming response with models/ prefix
	nonStreamingResponse := `{
		"candidates": [{
			"content": {"parts": [{"text": "Hello"}]},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 10,
			"candidatesTokenCount": 5,
			"totalTokenCount": 15
		},
		"modelVersion": "models/gemini-2.5-flash-preview-05-20"
	}`

	geminiProvider := NewGeminiProxy()
	metadata, err := geminiProvider.ParseResponseMetadata(strings.NewReader(nonStreamingResponse), false)
	if err != nil {
		t.Fatalf("Failed to parse non-streaming response: %v", err)
	}

	expectedModel := "gemini-2.5-flash-preview-05-20"
	if metadata.Model != expectedModel {
		t.Errorf("Expected model name '%s', got '%s'", expectedModel, metadata.Model)
	}

	// Test streaming response with models/ prefix
	streamingResponse := `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},"modelVersion":"models/gemini-2.5-flash-preview-05-20"}

data: [DONE]
`

	metadata, err = geminiProvider.ParseResponseMetadata(strings.NewReader(streamingResponse), true)
	if err != nil {
		t.Fatalf("Failed to parse streaming response: %v", err)
	}

	if metadata.Model != expectedModel {
		t.Errorf("Expected model name '%s', got '%s'", expectedModel, metadata.Model)
	}

	// Test model name without models/ prefix (should remain unchanged)
	nonStreamingResponseNoPrefix := `{
		"candidates": [{
			"content": {"parts": [{"text": "Hello"}]},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 10,
			"candidatesTokenCount": 5,
			"totalTokenCount": 15
		},
		"modelVersion": "gemini-2.5-flash-preview-05-20"
	}`

	metadata, err = geminiProvider.ParseResponseMetadata(strings.NewReader(nonStreamingResponseNoPrefix), false)
	if err != nil {
		t.Fatalf("Failed to parse non-streaming response without prefix: %v", err)
	}

	if metadata.Model != expectedModel {
		t.Errorf("Expected model name '%s', got '%s'", expectedModel, metadata.Model)
	}
}

// Token-based limiter behavior scoped by API key and user for Gemini
func TestGemini_TokenRateLimit_ByKeyAndUser(t *testing.T) {
	cfg := config.GetDefaultYAMLConfig()
	cfg.Features.RateLimiting.Enabled = true
	cfg.Features.RateLimiting.Backend = "memory"
	cfg.Features.RateLimiting.Limits = config.LimitsConfig{}
	if cfg.Features.RateLimiting.Overrides.PerKey == nil {
		cfg.Features.RateLimiting.Overrides.PerKey = map[string]config.LimitsConfig{}
	}
	if cfg.Features.RateLimiting.Overrides.PerUser == nil {
		cfg.Features.RateLimiting.Overrides.PerUser = map[string]config.LimitsConfig{}
	}
	cfg.Features.RateLimiting.Overrides.PerKey["devkey"] = config.LimitsConfig{TokensPerMinute: 30}
	cfg.Features.RateLimiting.Overrides.PerUser["example-user"] = config.LimitsConfig{TokensPerMinute: 30}

	lim := ratelimit.NewMemoryLimiter(cfg)
	now := time.Now()
	scope := ratelimit.ScopeKeys{Provider: "gemini", Model: "gemini-2.5-flash-preview-05-20", APIKey: "devkey", UserID: "example-user"}

	res1, err := lim.CheckAndReserve(context.TODO(), "", scope, 20, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res1.Allowed {
		t.Fatalf("expected first reservation allowed, got denied: %+v", res1)
	}

	res2, err := lim.CheckAndReserve(context.TODO(), "", scope, 15, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res2.Allowed {
		t.Fatalf("expected second reservation to be rate limited, but allowed")
	}
}

// Key has enough tokens but user hits limit
func TestGemini_TokenRateLimit_UserLimited_KeyOK(t *testing.T) {
	cfg := config.GetDefaultYAMLConfig()
	cfg.Features.RateLimiting.Enabled = true
	cfg.Features.RateLimiting.Backend = "memory"
	cfg.Features.RateLimiting.Limits = config.LimitsConfig{}
	if cfg.Features.RateLimiting.Overrides.PerKey == nil {
		cfg.Features.RateLimiting.Overrides.PerKey = map[string]config.LimitsConfig{}
	}
	if cfg.Features.RateLimiting.Overrides.PerUser == nil {
		cfg.Features.RateLimiting.Overrides.PerUser = map[string]config.LimitsConfig{}
	}
	cfg.Features.RateLimiting.Overrides.PerKey["devkey"] = config.LimitsConfig{TokensPerMinute: 100}
	cfg.Features.RateLimiting.Overrides.PerUser["example-user"] = config.LimitsConfig{TokensPerMinute: 30}

	lim := ratelimit.NewMemoryLimiter(cfg)
	now := time.Now()
	scope := ratelimit.ScopeKeys{Provider: "gemini", Model: "gemini-2.5-flash-preview-05-20", APIKey: "devkey", UserID: "example-user"}

	res1, err := lim.CheckAndReserve(context.TODO(), "", scope, 20, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res1.Allowed {
		t.Fatalf("expected first reservation allowed, got denied: %+v", res1)
	}

	// total 35 (>30 user limit), key still under 100
	res2, err := lim.CheckAndReserve(context.TODO(), "", scope, 15, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res2.Allowed {
		t.Fatalf("expected second reservation denied by user limit, but allowed")
	}
}

// User has enough tokens but key hits limit
func TestGemini_TokenRateLimit_KeyLimited_UserOK(t *testing.T) {
	cfg := config.GetDefaultYAMLConfig()
	cfg.Features.RateLimiting.Enabled = true
	cfg.Features.RateLimiting.Backend = "memory"
	cfg.Features.RateLimiting.Limits = config.LimitsConfig{}
	if cfg.Features.RateLimiting.Overrides.PerKey == nil {
		cfg.Features.RateLimiting.Overrides.PerKey = map[string]config.LimitsConfig{}
	}
	if cfg.Features.RateLimiting.Overrides.PerUser == nil {
		cfg.Features.RateLimiting.Overrides.PerUser = map[string]config.LimitsConfig{}
	}
	cfg.Features.RateLimiting.Overrides.PerKey["devkey"] = config.LimitsConfig{TokensPerMinute: 30}
	cfg.Features.RateLimiting.Overrides.PerUser["example-user"] = config.LimitsConfig{TokensPerMinute: 100}

	lim := ratelimit.NewMemoryLimiter(cfg)
	now := time.Now()
	scope := ratelimit.ScopeKeys{Provider: "gemini", Model: "gemini-2.5-flash-preview-05-20", APIKey: "devkey", UserID: "example-user"}

	res1, err := lim.CheckAndReserve(context.TODO(), "", scope, 20, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res1.Allowed {
		t.Fatalf("expected first reservation allowed, got denied: %+v", res1)
	}

	// total 35 (>30 key limit), user still under 100
	res2, err := lim.CheckAndReserve(context.TODO(), "", scope, 15, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res2.Allowed {
		t.Fatalf("expected second reservation denied by key limit, but allowed")
	}
}

// Header verification via middleware is covered in middleware-specific tests to avoid package import cycles.
