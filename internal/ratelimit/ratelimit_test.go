package ratelimit

import (
	"fmt"
	"testing"
	"time"
)

// TestTokenBucketBasic tests basic token bucket functionality
func TestTokenBucketBasic(t *testing.T) {
	bucket := NewTokenBucket(10, 1, time.Second)
	
	// Test initial state
	if bucket.Tokens != 10 {
		t.Errorf("Expected 10 tokens, got %d", bucket.Tokens)
	}
	
	// Test token consumption
	if !bucket.TryConsume(5) {
		t.Error("Should be able to consume 5 tokens")
	}
	
	if bucket.Tokens != 5 {
		t.Errorf("Expected 5 tokens remaining, got %d", bucket.Tokens)
	}
	
	// Test overconsumption
	if bucket.TryConsume(10) {
		t.Error("Should not be able to consume 10 tokens when only 5 available")
	}
	
	// Test refill
	time.Sleep(2 * time.Second)
	bucket.RefillTokens()
	
	if bucket.Tokens < 7 {
		t.Errorf("Expected at least 7 tokens after refill, got %d", bucket.Tokens)
	}
}

// TestDefaultConfig tests the default configuration
func TestDefaultConfig(t *testing.T) {
	config := NewDefaultConfig()
	
	if !config.Enabled {
		t.Error("Default config should be enabled")
	}
	
	if len(config.Rules) == 0 {
		t.Error("Default config should have rules")
	}
	
	// Test config validation
	if err := config.ValidateConfig(); err != nil {
		t.Errorf("Default config should be valid: %v", err)
	}
}

// TestMemoryStore tests the memory store functionality
func TestMemoryStore(t *testing.T) {
	store := NewMemoryStore()
	defer store.Close()
	
	// Test bucket operations
	bucket := NewTokenBucket(100, 10, time.Second)
	
	err := store.SetBucket("test:user:123", bucket)
	if err != nil {
		t.Errorf("Failed to set bucket: %v", err)
	}
	
	retrieved, err := store.GetBucket("test:user:123")
	if err != nil {
		t.Errorf("Failed to get bucket: %v", err)
	}
	
	if retrieved == nil {
		t.Error("Retrieved bucket should not be nil")
	}
	
	if retrieved.Tokens != 100 {
		t.Errorf("Expected 100 tokens, got %d", retrieved.Tokens)
	}
	
	// Test usage records
	record := &UsageRecord{
		Timestamp:    time.Now(),
		Provider:     "openai",
		Model:        "gpt-3.5-turbo",
		InputTokens:  50,
		OutputTokens: 25,
		TotalTokens:  75,
		IsStreaming:  false,
		RequestID:    "test-request-123",
	}
	
	err = store.AddUsageRecord("test:user:123", record)
	if err != nil {
		t.Errorf("Failed to add usage record: %v", err)
	}
	
	stats, err := store.GetUsageStats("test:user:123")
	if err != nil {
		t.Errorf("Failed to get usage stats: %v", err)
	}
	
	if stats.TotalRequests != 1 {
		t.Errorf("Expected 1 request, got %d", stats.TotalRequests)
	}
	
	if stats.TotalTokens != 75 {
		t.Errorf("Expected 75 tokens, got %d", stats.TotalTokens)
	}
}

// TestRateLimitDecision tests rate limit decision logic
func TestRateLimitDecision(t *testing.T) {
	// Create a very restrictive configuration for testing
	config := &Config{
		Enabled: true,
		Rules: []*RateLimitRule{
			{
				Name:         "test_requests_per_minute",
				Description:  "Test rule - 2 requests per minute",
				Scope:        "user",
				Type:         "requests",
				Limit:        2,
				Window:       time.Minute,
				RefillRate:   1,
				RefillPeriod: 30 * time.Second, // Very slow refill
				Priority:     100,
				Enabled:      true,
			},
		},
	}
	
	store := NewMemoryStore()
	defer store.Close()
	
	rateLimiter := NewTokenBucketRateLimiter(config, store)
	
	// Test basic rate limiting
	req := &RateLimitRequest{
		Provider:        "openai",
		Model:           "gpt-3.5-turbo",
		UserID:          "test-user",
		IPAddress:       "127.0.0.1",
		Endpoint:        "/openai/v1/chat/completions",
		Method:          "POST",
		IsStreaming:     false,
		EstimatedTokens: 1, // Very small token count
	}
	
	// First request should be allowed (1 of 2 tokens consumed)
	decision1 := rateLimiter.CheckRequest(req)
	if !decision1.Allowed {
		t.Errorf("First request should be allowed: %s", decision1.Reason)
	}
	
	if decision1.Reason == "" {
		t.Error("Rate limit decision should have a reason")
	}
	
	t.Logf("First request result: %v, reason: %s, remaining: %d", decision1.Allowed, decision1.Reason, decision1.RemainingTokens)
	
	// Second request should be allowed (2 of 2 tokens consumed)
	decision2 := rateLimiter.CheckRequest(req)
	if !decision2.Allowed {
		t.Errorf("Second request should be allowed: %s", decision2.Reason)
	}
	
	t.Logf("Second request result: %v, reason: %s, remaining: %d", decision2.Allowed, decision2.Reason, decision2.RemainingTokens)
	
	// Third request should be denied (no tokens left)
	decision3 := rateLimiter.CheckRequest(req)
	if decision3.Allowed {
		t.Errorf("Third request should be denied but was allowed: %s", decision3.Reason)
	}
	
	if decision3.RetryAfter <= 0 {
		t.Error("Rate limited request should have RetryAfter > 0")
	}
	
	t.Logf("Third request result: %v, reason: %s, retry after: %v", decision3.Allowed, decision3.Reason, decision3.RetryAfter)
	
	// Test token consumption tracking by checking the bucket directly
	// Generate the user key based on the known format: "user:{userID}"
	userKey := fmt.Sprintf("user:%s", req.UserID)
	bucket, err := store.GetBucket(userKey)
	if err != nil {
		t.Errorf("Failed to get bucket: %v", err)
	}
	
	if bucket == nil {
		t.Error("Bucket should exist after requests")
	} else {
		t.Logf("Bucket state: %d tokens remaining, capacity: %d", bucket.Tokens, bucket.Capacity)
		
		// After 2 requests, bucket should have 0 tokens left
		if bucket.Tokens != 0 {
			t.Errorf("Expected 0 tokens remaining after 2 requests, got %d", bucket.Tokens)
		}
	}
	
	// Test token refill after waiting
	t.Log("Testing token refill...")
	time.Sleep(31 * time.Second) // Wait longer than refill period (30s)
	
	// Fourth request should be allowed after refill (1 token should be available)
	decision4 := rateLimiter.CheckRequest(req)
	if !decision4.Allowed {
		t.Errorf("Fourth request after refill should be allowed: %s", decision4.Reason)
	}
	
	t.Logf("Fourth request after refill result: %v, reason: %s, remaining: %d", decision4.Allowed, decision4.Reason, decision4.RemainingTokens)
	
	// Verify bucket state after refill and consumption
	bucket, err = store.GetBucket(userKey)
	if err != nil {
		t.Errorf("Failed to get bucket after refill: %v", err)
	}
	
	if bucket != nil {
		t.Logf("Bucket state after refill: %d tokens remaining, capacity: %d", bucket.Tokens, bucket.Capacity)
		
		// After refill and 1 more request, should have 0 tokens left
		if bucket.Tokens != 0 {
			t.Errorf("Expected 0 tokens remaining after refill and 1 request, got %d", bucket.Tokens)
		}
	}
}

// TestUsageStats tests usage statistics functionality
func TestUsageStats(t *testing.T) {
	stats := NewUsageStats(time.Now().Add(-time.Hour), time.Now())
	
	record1 := &UsageRecord{
		Timestamp:    time.Now(),
		Provider:     "openai",
		Model:        "gpt-3.5-turbo",
		InputTokens:  50,
		OutputTokens: 25,
		TotalTokens:  75,
		IsStreaming:  false,
	}
	
	record2 := &UsageRecord{
		Timestamp:    time.Now(),
		Provider:     "anthropic",
		Model:        "claude-3-sonnet",
		InputTokens:  30,
		OutputTokens: 20,
		TotalTokens:  50,
		IsStreaming:  true,
	}
	
	stats.AddRecord(record1)
	stats.AddRecord(record2)
	
	if stats.TotalRequests != 2 {
		t.Errorf("Expected 2 requests, got %d", stats.TotalRequests)
	}
	
	if stats.TotalTokens != 125 {
		t.Errorf("Expected 125 tokens, got %d", stats.TotalTokens)
	}
	
	if stats.StreamingRequests != 1 {
		t.Errorf("Expected 1 streaming request, got %d", stats.StreamingRequests)
	}
	
	if len(stats.ProviderStats) != 2 {
		t.Errorf("Expected 2 providers, got %d", len(stats.ProviderStats))
	}
	
	stats.CalculateRates()
	
	if stats.AverageTokensPerRequest != 62.5 {
		t.Errorf("Expected average tokens per request 62.5, got %f", stats.AverageTokensPerRequest)
	}
}

// TestRateLimitRule tests rate limit rule validation
func TestRateLimitRule(t *testing.T) {
	rule := &RateLimitRule{
		Name:         "test-rule",
		Description:  "Test rule",
		Scope:        "user",
		Type:         "tokens",
		Limit:        1000,
		Window:       time.Hour,
		RefillRate:   100,
		RefillPeriod: time.Minute,
		Priority:     100,
		Enabled:      true,
	}
	
	if err := rule.Validate(); err != nil {
		t.Errorf("Valid rule should pass validation: %v", err)
	}
	
	// Test invalid rule
	invalidRule := &RateLimitRule{
		Name:         "",
		Scope:        "invalid",
		Type:         "invalid",
		Limit:        -1,
		Window:       -1,
		RefillRate:   -1,
		RefillPeriod: -1,
	}
	
	if err := invalidRule.Validate(); err == nil {
		t.Error("Invalid rule should fail validation")
	}
}

// BenchmarkTokenBucket benchmarks token bucket performance
func BenchmarkTokenBucket(b *testing.B) {
	bucket := NewTokenBucket(1000, 10, time.Millisecond)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		bucket.TryConsume(1)
	}
}

// BenchmarkMemoryStore benchmarks memory store performance
func BenchmarkMemoryStore(b *testing.B) {
	store := NewMemoryStore()
	defer store.Close()
	
	bucket := NewTokenBucket(1000, 10, time.Second)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := "test:user:123"
		store.SetBucket(key, bucket)
		store.GetBucket(key)
	}
}

func TestAliasResolution(t *testing.T) {
	// Create provider configuration with aliases
	providerConfig := ProviderConfigMap{
		"openai": ProviderConfig{
			Models: map[string]ModelConfig{
				"gpt-4.1": ModelConfig{
					Enabled: true,
					Aliases: []string{"gpt-4.1-turbo", "gpt-4.1-0613", "openai/gpt-4.1"},
				},
			},
		},
		"anthropic": ProviderConfig{
			Models: map[string]ModelConfig{
				"claude-3-5-sonnet-20241022": ModelConfig{
					Enabled: true,
					Aliases: []string{"claude-3.5-sonnet", "claude-3-5-sonnet", "claude-sonnet-3.5"},
				},
			},
		},
	}
	
	// Create rate limiter with very restrictive limits for testing
	config := &Config{
		Enabled: true,
		Rules: []*RateLimitRule{
			{
				Name:         "gpt41_tokens_per_minute",
				Description:  "Limit GPT-4.1 to 100 tokens per minute per user",
				Scope:        "user_model",
				Provider:     "openai",
				Model:        "gpt-4.1", // Rule uses canonical model name
				Type:         "tokens",
				Limit:        100,
				Window:       time.Minute,
				RefillRate:   2, // Very slow refill for testing
				RefillPeriod: time.Second,
				Priority:     100,
				Enabled:      true,
			},
		},
	}
	
	store := NewMemoryStore()
	defer store.Close()
	
	rateLimiter := NewTokenBucketRateLimiterWithProviders(config, store, providerConfig)
	
	// Test that canonical model name is correctly used
	reqCanonical := &RateLimitRequest{
		Provider:        "openai",
		Model:           "gpt-4.1", // Canonical model name
		UserID:          "test-user",
		IPAddress:       "127.0.0.1",
		Endpoint:        "/openai/v1/chat/completions",
		Method:          "POST",
		IsStreaming:     false,
		EstimatedTokens: 50,
	}
	
	// First request with canonical name should work
	decision1 := rateLimiter.CheckRequest(reqCanonical)
	if !decision1.Allowed {
		t.Errorf("Request with canonical model name should be allowed: %s", decision1.Reason)
	}
	t.Logf("Canonical model request allowed, remaining tokens: %d", decision1.RemainingTokens)
	
	// Test with alias - should share the same bucket
	reqAlias := &RateLimitRequest{
		Provider:        "openai",
		Model:           "gpt-4.1-turbo", // This is an alias
		UserID:          "test-user", // Same user
		IPAddress:       "127.0.0.1",
		Endpoint:        "/openai/v1/chat/completions",
		Method:          "POST",
		IsStreaming:     false,
		EstimatedTokens: 50,
	}
	
	// Second request with alias should work but consume from the same bucket
	decision2 := rateLimiter.CheckRequest(reqAlias)
	if !decision2.Allowed {
		t.Errorf("Request with alias should be allowed: %s", decision2.Reason)
	}
	t.Logf("Alias request allowed, remaining tokens: %d", decision2.RemainingTokens)
	
	// Remaining tokens should be 0 (100 - 50 - 50 = 0)
	if decision2.RemainingTokens != 0 {
		t.Errorf("Expected 0 remaining tokens, got %d", decision2.RemainingTokens)
	}
	
	// Third request with different alias should be denied (same bucket exhausted)
	reqAlias2 := &RateLimitRequest{
		Provider:        "openai",
		Model:           "openai/gpt-4.1", // Different alias
		UserID:          "test-user", // Same user
		IPAddress:       "127.0.0.1",
		Endpoint:        "/openai/v1/chat/completions",
		Method:          "POST",
		IsStreaming:     false,
		EstimatedTokens: 1,
	}
	
	decision3 := rateLimiter.CheckRequest(reqAlias2)
	if decision3.Allowed {
		t.Errorf("Third request should be denied as bucket is exhausted, but was allowed")
	}
	t.Logf("Third request correctly denied: %s", decision3.Reason)
	
	// Test with different user - should get fresh bucket
	reqDifferentUser := &RateLimitRequest{
		Provider:        "openai",
		Model:           "gpt-4.1-0613", // Alias again
		UserID:          "different-user", // Different user
		IPAddress:       "127.0.0.1",
		Endpoint:        "/openai/v1/chat/completions",
		Method:          "POST",
		IsStreaming:     false,
		EstimatedTokens: 50,
	}
	
	decision4 := rateLimiter.CheckRequest(reqDifferentUser)
	if !decision4.Allowed {
		t.Errorf("Request from different user should be allowed: %s", decision4.Reason)
	}
	t.Logf("Different user request allowed, remaining tokens: %d", decision4.RemainingTokens)
}

func TestAliasResolutionFunction(t *testing.T) {
	// Create provider configuration
	providerConfig := ProviderConfigMap{
		"openai": ProviderConfig{
			Models: map[string]ModelConfig{
				"gpt-4.1": ModelConfig{
					Enabled: true,
					Aliases: []string{"gpt-4.1-turbo", "gpt-4.1-0613", "openai/gpt-4.1"},
				},
				"gpt-4o": ModelConfig{
					Enabled: true,
					Aliases: []string{"gpt-4-omni", "openai/gpt-4o"},
				},
			},
		},
	}
	
	config := NewDefaultConfig()
	store := NewMemoryStore()
	defer store.Close()
	
	rateLimiter := NewTokenBucketRateLimiterWithProviders(config, store, providerConfig)
	
	// Test cases for alias resolution
	testCases := []struct {
		provider string
		input    string
		expected string
	}{
		// Canonical names should return themselves
		{"openai", "gpt-4.1", "gpt-4.1"},
		{"openai", "gpt-4o", "gpt-4o"},
		
		// Aliases should resolve to canonical names
		{"openai", "gpt-4.1-turbo", "gpt-4.1"},
		{"openai", "gpt-4.1-0613", "gpt-4.1"},
		{"openai", "openai/gpt-4.1", "gpt-4.1"},
		{"openai", "gpt-4-omni", "gpt-4o"},
		{"openai", "openai/gpt-4o", "gpt-4o"},
		
		// Unknown models should return themselves
		{"openai", "unknown-model", "unknown-model"},
		
		// Unknown providers should return original
		{"unknown-provider", "some-model", "some-model"},
	}
	
	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%s_%s", tc.provider, tc.input), func(t *testing.T) {
			result := rateLimiter.resolveModelAlias(tc.provider, tc.input)
			if result != tc.expected {
				t.Errorf("Expected %s, got %s", tc.expected, result)
			}
		})
	}
} 
