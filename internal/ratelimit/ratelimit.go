package ratelimit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/providers"
)

// RateLimitDecision represents the decision made by the rate limiter
type RateLimitDecision struct {
	Allowed      bool
	Reason       string
	RetryAfter   time.Duration
	RemainingTokens int64
	ResetTime    time.Time
}

// RateLimitRequest contains information about a request for rate limiting
type RateLimitRequest struct {
	Provider     string
	Model        string
	UserID       string
	IPAddress    string
	Endpoint     string
	Method       string
	IsStreaming  bool
	EstimatedTokens int64  // Estimated tokens for the request (based on input)
}

// ProviderConfig represents the configuration for a provider (subset needed for alias resolution)
type ProviderConfig struct {
	Models map[string]ModelConfig `json:"models"`
}

// ModelConfig represents the configuration for a model (subset needed for alias resolution)
type ModelConfig struct {
	Enabled bool     `json:"enabled"`
	Aliases []string `json:"aliases"`
}

// ProviderConfigMap maps provider names to their configurations for alias resolution
type ProviderConfigMap map[string]ProviderConfig

// RateLimiter is the main interface for rate limiting functionality
type RateLimiter interface {
	// CheckRequest determines if a request should be allowed based on rate limits
	CheckRequest(req *RateLimitRequest) *RateLimitDecision
	
	// UpdateUsage updates the usage statistics after a request is completed
	UpdateUsage(req *RateLimitRequest, metadata *providers.LLMResponseMetadata) error
	
	// RefundTokens refunds the difference between estimated and actual tokens used
	RefundTokens(req *RateLimitRequest, estimatedTokens, actualTokens int64) error
	
	// GetUsageStats returns current usage statistics for debugging/monitoring
	GetUsageStats(userID string) map[string]interface{}
	
	// ResetUsage resets usage statistics (for testing or admin purposes)
	ResetUsage(userID string) error
}

// TokenBucketRateLimiter implements rate limiting using token bucket algorithm
type TokenBucketRateLimiter struct {
	config         *Config
	store          RateLimitStore
	providerConfig ProviderConfigMap // Added for alias resolution
}

// NewTokenBucketRateLimiter creates a new token bucket rate limiter
func NewTokenBucketRateLimiter(config *Config, store RateLimitStore) *TokenBucketRateLimiter {
	return &TokenBucketRateLimiter{
		config:         config,
		store:          store,
		providerConfig: make(ProviderConfigMap),
	}
}

// NewTokenBucketRateLimiterWithProviders creates a new token bucket rate limiter with provider configuration
func NewTokenBucketRateLimiterWithProviders(config *Config, store RateLimitStore, providerConfig ProviderConfigMap) *TokenBucketRateLimiter {
	return &TokenBucketRateLimiter{
		config:         config,
		store:          store,
		providerConfig: providerConfig,
	}
}

// SetProviderConfig sets the provider configuration for alias resolution
func (rl *TokenBucketRateLimiter) SetProviderConfig(providerConfig ProviderConfigMap) {
	rl.providerConfig = providerConfig
}

// resolveModelAlias resolves a model alias to its canonical model name
// Returns the canonical model name, or the original name if no resolution is needed
func (rl *TokenBucketRateLimiter) resolveModelAlias(provider, model string) string {
	// If no provider config available, return original model name
	providerCfg, exists := rl.providerConfig[provider]
	if !exists {
		return model
	}
	
	// First check if the model name is a canonical model (exists as a key)
	if modelCfg, exists := providerCfg.Models[model]; exists && modelCfg.Enabled {
		return model // Already canonical
	}
	
	// Search for the model as an alias
	for canonicalModel, modelCfg := range providerCfg.Models {
		if !modelCfg.Enabled {
			continue
		}
		
		// Check if the provided model name is an alias for this canonical model
		for _, alias := range modelCfg.Aliases {
			if alias == model {
				slog.Debug("Resolved model alias to canonical model", "alias", model, "canonical_model", canonicalModel, "provider", provider)
				return canonicalModel
			}
		}
	}
	
	// If no alias resolution found, return the original model name
	return model
}

// CheckRequest implements the RateLimiter interface
func (rl *TokenBucketRateLimiter) CheckRequest(req *RateLimitRequest) *RateLimitDecision {
	// Generate unique keys for different rate limit dimensions
	keys := rl.generateRateLimitKeys(req)
	
	var minRemainingTokens int64 = -1
	var resetTime time.Time
	
	// Check each rate limit rule
	for _, rule := range rl.config.Rules {
		if !rl.ruleMatches(rule, req) {
			continue
		}
		
		// Check the appropriate key based on the rule scope
		key := rl.getKeyForRule(rule, keys)
		
		// Get current bucket state
		bucket, err := rl.store.GetBucket(key)
		if err != nil {
			return &RateLimitDecision{
				Allowed: false,
				Reason:  fmt.Sprintf("Error retrieving rate limit data: %v", err),
			}
		}
		
		// Initialize bucket if it doesn't exist
		if bucket == nil {
			bucket = &TokenBucket{
				Capacity:     rule.Limit,
				Tokens:       rule.Limit,
				LastRefill:   time.Now(),
				RefillRate:   rule.RefillRate,
				RefillPeriod: rule.RefillPeriod,
				ExpiresAt:    time.Now().Add(time.Hour * 24), // Set expiration
			}
		}
		
		// Refill tokens based on time elapsed
		rl.refillBucket(bucket, rule)
		
		// Check if we have enough tokens
		tokensNeeded := rl.calculateTokensNeeded(rule, req)
		
		if bucket.Tokens < tokensNeeded {
			// Not enough tokens, request denied
			retryAfter := rl.calculateRetryAfter(bucket, rule, tokensNeeded)
			return &RateLimitDecision{
				Allowed:         false,
				Reason:          fmt.Sprintf("Rate limit exceeded for %s: %s", rule.Name, rule.Description),
				RetryAfter:      retryAfter,
				RemainingTokens: bucket.Tokens,
				ResetTime:       bucket.LastRefill.Add(rule.Window),
			}
		}
		
		// Consume tokens
		bucket.Tokens -= tokensNeeded
		
		// Track the minimum remaining tokens across all rules
		if minRemainingTokens == -1 || bucket.Tokens < minRemainingTokens {
			minRemainingTokens = bucket.Tokens
			resetTime = bucket.LastRefill.Add(rule.Window)
		}
		
		// Update the bucket in storage
		if err := rl.store.SetBucket(key, bucket); err != nil {
			return &RateLimitDecision{
				Allowed: false,
				Reason:  fmt.Sprintf("Error updating rate limit data: %v", err),
			}
		}
	}
	
	// All rate limit checks passed
	return &RateLimitDecision{
		Allowed:         true,
		Reason:          "Request allowed",
		RemainingTokens: minRemainingTokens,
		ResetTime:       resetTime,
	}
}

// UpdateUsage implements the RateLimiter interface
func (rl *TokenBucketRateLimiter) UpdateUsage(req *RateLimitRequest, metadata *providers.LLMResponseMetadata) error {
	// Generate tracking key for usage statistics
	keys := rl.generateRateLimitKeys(req)
	
	// Update usage statistics
	for _, rule := range rl.config.Rules {
		if !rl.ruleMatches(rule, req) {
			continue
		}
		
		key := rl.getKeyForRule(rule, keys)
		
		// Update usage tracking
		usage := &UsageRecord{
			Timestamp:    time.Now(),
			Provider:     metadata.Provider,
			Model:        metadata.Model,
			InputTokens:  int64(metadata.InputTokens),
			OutputTokens: int64(metadata.OutputTokens),
			TotalTokens:  int64(metadata.TotalTokens),
			IsStreaming:  metadata.IsStreaming,
			RequestID:    metadata.RequestID,
		}
		
		if err := rl.store.AddUsageRecord(key, usage); err != nil {
			return fmt.Errorf("failed to update usage record: %w", err)
		}
	}
	
	return nil
}

// RefundTokens implements the RateLimiter interface
func (rl *TokenBucketRateLimiter) RefundTokens(req *RateLimitRequest, estimatedTokens, actualTokens int64) error {
	// Only refund if we over-consumed tokens
	if actualTokens >= estimatedTokens {
		return nil // No refund needed
	}
	
	refundAmount := estimatedTokens - actualTokens
	keys := rl.generateRateLimitKeys(req)
	
	// Refund tokens to all matching rules
	for _, rule := range rl.config.Rules {
		if !rl.ruleMatches(rule, req) {
			continue
		}
		
		// Only refund for token-based rules
		if rule.Type != "tokens" && rule.Type != "tokens_per_minute" {
			continue
		}
		
		key := rl.getKeyForRule(rule, keys)
		
		// Get current bucket state
		bucket, err := rl.store.GetBucket(key)
		if err != nil {
			return fmt.Errorf("error retrieving bucket for refund: %w", err)
		}
		
		if bucket != nil {
			// Calculate how many tokens this rule would have consumed
			tokensConsumed := rl.calculateTokensNeeded(rule, req)
			
			// Calculate proportional refund for this rule
			proportionalRefund := refundAmount
			if tokensConsumed > estimatedTokens {
				// This rule consumed more than the total estimate (shouldn't happen, but be safe)
				proportionalRefund = refundAmount
			}
			
			// Add back the refund amount, but don't exceed capacity
			bucket.Tokens += proportionalRefund
			if bucket.Tokens > rule.Limit {
				bucket.Tokens = rule.Limit
			}
			
			// Update the bucket in storage
			if err := rl.store.SetBucket(key, bucket); err != nil {
				return fmt.Errorf("error updating bucket after refund: %w", err)
			}
		}
	}
	
	return nil
}

// GetUsageStats implements the RateLimiter interface
func (rl *TokenBucketRateLimiter) GetUsageStats(userID string) map[string]interface{} {
	stats := make(map[string]interface{})
	
	// Get usage statistics from store
	keys := rl.generateRateLimitKeysForUser(userID)
	
	for scope, key := range keys {
		if usage, err := rl.store.GetUsageStats(key); err == nil {
			stats[scope] = usage
		}
	}
	
	return stats
}

// ResetUsage implements the RateLimiter interface
func (rl *TokenBucketRateLimiter) ResetUsage(userID string) error {
	keys := rl.generateRateLimitKeysForUser(userID)
	
	for _, key := range keys {
		if err := rl.store.ResetUsage(key); err != nil {
			return fmt.Errorf("failed to reset usage for key %s: %w", key, err)
		}
	}
	
	return nil
}

// Helper methods

func (rl *TokenBucketRateLimiter) generateRateLimitKeys(req *RateLimitRequest) map[string]string {
	keys := make(map[string]string)
	
	// Resolve model alias to canonical model name for consistent bucketing
	canonicalModel := rl.resolveModelAlias(req.Provider, req.Model)
	
	// User-specific keys
	keys["user"] = fmt.Sprintf("user:%s", req.UserID)
	keys["user_provider"] = fmt.Sprintf("user:%s:provider:%s", req.UserID, req.Provider)
	keys["user_model"] = fmt.Sprintf("user:%s:model:%s", req.UserID, canonicalModel)
	keys["user_endpoint"] = fmt.Sprintf("user:%s:endpoint:%s", req.UserID, req.Endpoint)
	
	// IP-based keys
	keys["ip"] = fmt.Sprintf("ip:%s", req.IPAddress)
	keys["ip_provider"] = fmt.Sprintf("ip:%s:provider:%s", req.IPAddress, req.Provider)
	
	// Global keys
	keys["global"] = "global"
	keys["global_provider"] = fmt.Sprintf("global:provider:%s", req.Provider)
	keys["global_model"] = fmt.Sprintf("global:model:%s", canonicalModel)
	
	return keys
}

func (rl *TokenBucketRateLimiter) generateRateLimitKeysForUser(userID string) map[string]string {
	keys := make(map[string]string)
	
	keys["user"] = fmt.Sprintf("user:%s", userID)
	
	// Add provider-specific keys for this user
	for _, rule := range rl.config.Rules {
		if rule.Scope == "user_provider" {
			keys[fmt.Sprintf("user_provider_%s", rule.Provider)] = fmt.Sprintf("user:%s:provider:%s", userID, rule.Provider)
		}
		if rule.Scope == "user_model" {
			keys[fmt.Sprintf("user_model_%s", rule.Model)] = fmt.Sprintf("user:%s:model:%s", userID, rule.Model)
		}
	}
	
	return keys
}

func (rl *TokenBucketRateLimiter) ruleMatches(rule *RateLimitRule, req *RateLimitRequest) bool {
	// Check provider filter
	if rule.Provider != "" && rule.Provider != req.Provider {
		return false
	}
	
	// Check model filter - use canonical model name for comparison
	if rule.Model != "" {
		canonicalModel := rl.resolveModelAlias(req.Provider, req.Model)
		if rule.Model != canonicalModel {
			return false
		}
	}
	
	// Check endpoint filter
	if rule.Endpoint != "" && rule.Endpoint != req.Endpoint {
		return false
	}
	
	// Check streaming filter
	if rule.StreamingOnly && !req.IsStreaming {
		return false
	}
	
	return true
}

func (rl *TokenBucketRateLimiter) getKeyForRule(rule *RateLimitRule, keys map[string]string) string {
	baseKey := ""
	if key, exists := keys[rule.Scope]; exists {
		baseKey = key
	} else {
		// Fallback to user scope
		baseKey = keys["user"]
	}
	
	// Make the key unique by including the rule name to prevent different rules from sharing buckets
	return fmt.Sprintf("%s:rule:%s", baseKey, rule.Name)
}

func (rl *TokenBucketRateLimiter) refillBucket(bucket *TokenBucket, rule *RateLimitRule) {
	now := time.Now()
	timeSinceLastRefill := now.Sub(bucket.LastRefill)
	
	// Calculate tokens to add based on refill rate
	tokensToAdd := int64(timeSinceLastRefill / rule.RefillPeriod) * rule.RefillRate
	
	if tokensToAdd > 0 {
		bucket.Tokens += tokensToAdd
		if bucket.Tokens > rule.Limit {
			bucket.Tokens = rule.Limit
		}
		bucket.LastRefill = now
	}
}

func (rl *TokenBucketRateLimiter) calculateTokensNeeded(rule *RateLimitRule, req *RateLimitRequest) int64 {
	switch rule.Type {
	case "requests":
		return 1
	case "tokens":
		if req.EstimatedTokens > 0 {
			return req.EstimatedTokens
		}
		// Default estimated tokens for different endpoints
		return rl.getDefaultTokenEstimate(req)
	case "requests_per_minute":
		return 1
	case "tokens_per_minute":
		if req.EstimatedTokens > 0 {
			return req.EstimatedTokens
		}
		return rl.getDefaultTokenEstimate(req)
	default:
		return 1
	}
}

func (rl *TokenBucketRateLimiter) getDefaultTokenEstimate(req *RateLimitRequest) int64 {
	// Provider-aware default token estimates based on model and endpoint
	switch req.Provider {
	case "openai":
		if strings.Contains(req.Model, "gpt-4") {
			return 300
		} else if strings.Contains(req.Model, "gpt-3.5") {
			return 180
		}
		return 150
	case "anthropic":
		if strings.Contains(req.Model, "claude-3-opus") {
			return 350
		} else if strings.Contains(req.Model, "claude-3-sonnet") {
			return 220
		} else if strings.Contains(req.Model, "claude-3-haiku") {
			return 150
		}
		return 200
	case "gemini":
		if strings.Contains(req.Model, "gemini-1.5-pro") {
			return 280
		} else if strings.Contains(req.Model, "gemini-1.5-flash") {
			return 160
		}
		return 180
	default:
		// Generic fallback for unknown providers
		return 100
	}
}

func (rl *TokenBucketRateLimiter) calculateRetryAfter(bucket *TokenBucket, rule *RateLimitRule, tokensNeeded int64) time.Duration {
	tokensDeficit := tokensNeeded - bucket.Tokens
	refillsNeeded := (tokensDeficit + rule.RefillRate - 1) / rule.RefillRate // Ceiling division
	
	return time.Duration(refillsNeeded) * rule.RefillPeriod
}

// ExtractRateLimitRequest extracts rate limit information from an HTTP request
// This function now uses provider-specific token estimation for improved accuracy.
// Each provider (OpenAI, Anthropic, Gemini) implements their own token counting logic
// that takes into account provider-specific request formats, models, and characteristics.
func ExtractRateLimitRequest(req *http.Request, provider providers.Provider) *RateLimitRequest {
	// Extract user ID from various sources
	userID := extractUserID(req)
	
	// Extract model information from request body or URL
	model := extractModelFromRequest(req)
	
	// Use provider-specific token estimation for more accurate results
	// This replaces the generic estimation with provider-aware logic
	estimatedTokens := provider.EstimateTokensFromRequest(req)
	
	return &RateLimitRequest{
		Provider:        provider.GetName(),
		Model:           model,
		UserID:          userID,
		IPAddress:       extractIPAddress(req),
		Endpoint:        req.URL.Path,
		Method:          req.Method,
		IsStreaming:     provider.IsStreamingRequest(req),
		EstimatedTokens: estimatedTokens,
	}
}

func extractUserID(req *http.Request) string {
	// Try to extract user ID from various sources
	
	// Check Authorization header for API key or JWT token
	if auth := req.Header.Get("Authorization"); auth != "" {
		// For API keys, use the key as user ID (hashed for privacy)
		if strings.HasPrefix(auth, "Bearer ") {
			return fmt.Sprintf("token:%s", hashString(auth[7:]))
		}
	}
	
	// Check for custom user ID header
	if userID := req.Header.Get("X-User-ID"); userID != "" {
		return userID
	}
	
	// Fallback to IP address
	return extractIPAddress(req)
}

func extractIPAddress(req *http.Request) string {
	// Check for forwarded headers
	if forwarded := req.Header.Get("X-Forwarded-For"); forwarded != "" {
		return forwarded
	}
	
	if realIP := req.Header.Get("X-Real-IP"); realIP != "" {
		return realIP
	}
	
	return req.RemoteAddr
}

// Helper function to safely read request body once and cache it
func readAndCacheRequestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	
	// Check if we already cached the body (to avoid multiple reads)
	if req.GetBody != nil {
		// If GetBody is available, use it to get a fresh reader
		bodyReader, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		defer bodyReader.Close()
		return io.ReadAll(bodyReader)
	}
	
	// Read the body once
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	
	// Restore the body for future reads
	req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	
	// Create GetBody function for future use
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewBuffer(bodyBytes)), nil
	}
	
	return bodyBytes, nil
}

func extractModelFromRequest(req *http.Request) string {
	// Try to extract model from request body
	bodyBytes, err := readAndCacheRequestBody(req)
	if err != nil {
		slog.Debug("Error reading request body for model extraction", "error", err)
		return ""
	}
	
	// Parse JSON to extract model
	var data map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &data); err == nil {
		if model, ok := data["model"].(string); ok {
			return model
		}
	}
	
	// Try to extract from URL path (for some APIs)
	if strings.Contains(req.URL.Path, "/models/") {
		parts := strings.Split(req.URL.Path, "/models/")
		if len(parts) > 1 {
			modelPart := strings.Split(parts[1], "/")[0]
			return modelPart
		}
	}
	
	return ""
}

// Note: Generic token estimation function removed - now using provider-specific estimation

// Note: Generic endpoint-based token estimation function removed - now using provider-specific estimation

func hashString(s string) string {
	// Simple hash function for privacy (in production, use crypto/sha256)
	hash := 0
	for _, c := range s {
		hash = hash*31 + int(c)
	}
	return fmt.Sprintf("%x", hash)
} 
