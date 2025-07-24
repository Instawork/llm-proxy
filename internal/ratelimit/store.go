package ratelimit

import (
	"fmt"
	"time"
)

// RateLimitStore defines the interface for storing rate limiting data
type RateLimitStore interface {
	// Token bucket operations
	GetBucket(key string) (*TokenBucket, error)
	SetBucket(key string, bucket *TokenBucket) error
	DeleteBucket(key string) error
	
	// Usage tracking operations
	AddUsageRecord(key string, record *UsageRecord) error
	GetUsageStats(key string) (*UsageStats, error)
	GetUsageRecords(key string, since time.Time) ([]*UsageRecord, error)
	ResetUsage(key string) error
	
	// Cleanup operations
	CleanupExpired() error
	GetAllKeys() ([]string, error)
	
	// Store management
	Close() error
}

// TokenBucket represents a token bucket for rate limiting
type TokenBucket struct {
	// Current number of tokens available
	Tokens int64 `json:"tokens"`
	
	// Maximum capacity of the bucket
	Capacity int64 `json:"capacity"`
	
	// Last time the bucket was refilled
	LastRefill time.Time `json:"last_refill"`
	
	// Rate at which tokens are added
	RefillRate int64 `json:"refill_rate"`
	
	// Period between refills
	RefillPeriod time.Duration `json:"refill_period"`
	
	// Expiration time for cleanup
	ExpiresAt time.Time `json:"expires_at"`
}

// UsageRecord represents a single usage record for tracking
type UsageRecord struct {
	// When the request was made
	Timestamp time.Time `json:"timestamp"`
	
	// Provider information
	Provider string `json:"provider"`
	Model    string `json:"model"`
	
	// Token usage
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	TotalTokens  int64 `json:"total_tokens"`
	
	// Request metadata
	IsStreaming bool   `json:"is_streaming"`
	RequestID   string `json:"request_id"`
	
	// Additional metadata
	Endpoint string `json:"endpoint"`
	UserID   string `json:"user_id"`
	IPAddress string `json:"ip_address"`
}

// UsageStats represents aggregated usage statistics
type UsageStats struct {
	// Time window for the stats
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`
	
	// Request counts
	TotalRequests     int64 `json:"total_requests"`
	StreamingRequests int64 `json:"streaming_requests"`
	
	// Token usage
	TotalTokens  int64 `json:"total_tokens"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	
	// Breakdown by provider
	ProviderStats map[string]*ProviderUsageStats `json:"provider_stats"`
	
	// Breakdown by model
	ModelStats map[string]*ModelUsageStats `json:"model_stats"`
	
	// Rate limiting info
	RateLimitHits     int64 `json:"rate_limit_hits"`
	LastRateLimitHit  time.Time `json:"last_rate_limit_hit"`
	
	// Performance metrics
	AverageTokensPerRequest float64 `json:"average_tokens_per_request"`
	RequestsPerMinute       float64 `json:"requests_per_minute"`
	TokensPerMinute         float64 `json:"tokens_per_minute"`
}

// ProviderUsageStats represents usage statistics for a specific provider
type ProviderUsageStats struct {
	Provider      string `json:"provider"`
	Requests      int64  `json:"requests"`
	TotalTokens   int64  `json:"total_tokens"`
	InputTokens   int64  `json:"input_tokens"`
	OutputTokens  int64  `json:"output_tokens"`
	LastUsed      time.Time `json:"last_used"`
}

// ModelUsageStats represents usage statistics for a specific model
type ModelUsageStats struct {
	Model         string `json:"model"`
	Provider      string `json:"provider"`
	Requests      int64  `json:"requests"`
	TotalTokens   int64  `json:"total_tokens"`
	InputTokens   int64  `json:"input_tokens"`
	OutputTokens  int64  `json:"output_tokens"`
	LastUsed      time.Time `json:"last_used"`
}

// NewTokenBucket creates a new token bucket with the given parameters
func NewTokenBucket(capacity, refillRate int64, refillPeriod time.Duration) *TokenBucket {
	return &TokenBucket{
		Tokens:       capacity,
		Capacity:     capacity,
		LastRefill:   time.Now(),
		RefillRate:   refillRate,
		RefillPeriod: refillPeriod,
		ExpiresAt:    time.Now().Add(time.Hour * 24), // Default 24 hour expiration
	}
}

// IsExpired checks if the token bucket has expired
func (tb *TokenBucket) IsExpired() bool {
	return time.Now().After(tb.ExpiresAt)
}

// RefillTokens refills the token bucket based on elapsed time
func (tb *TokenBucket) RefillTokens() {
	now := time.Now()
	timeSinceLastRefill := now.Sub(tb.LastRefill)
	
	if timeSinceLastRefill < tb.RefillPeriod {
		return // Not enough time has passed
	}
	
	// Calculate how many refill periods have passed
	refillPeriods := int64(timeSinceLastRefill / tb.RefillPeriod)
	tokensToAdd := refillPeriods * tb.RefillRate
	
	// Add tokens, but don't exceed capacity
	tb.Tokens += tokensToAdd
	if tb.Tokens > tb.Capacity {
		tb.Tokens = tb.Capacity
	}
	
	// Update the last refill time
	tb.LastRefill = now
}

// TryConsume attempts to consume the specified number of tokens
func (tb *TokenBucket) TryConsume(tokens int64) bool {
	tb.RefillTokens()
	
	if tb.Tokens >= tokens {
		tb.Tokens -= tokens
		return true
	}
	
	return false
}

// GetTimeUntilRefill returns the time until the next refill
func (tb *TokenBucket) GetTimeUntilRefill() time.Duration {
	nextRefill := tb.LastRefill.Add(tb.RefillPeriod)
	return time.Until(nextRefill)
}

// NewUsageRecord creates a new usage record
func NewUsageRecord(provider, model, userID, ipAddress, endpoint string) *UsageRecord {
	return &UsageRecord{
		Timestamp: time.Now(),
		Provider:  provider,
		Model:     model,
		UserID:    userID,
		IPAddress: ipAddress,
		Endpoint:  endpoint,
	}
}

// NewUsageStats creates a new usage stats object
func NewUsageStats(windowStart, windowEnd time.Time) *UsageStats {
	return &UsageStats{
		WindowStart:   windowStart,
		WindowEnd:     windowEnd,
		ProviderStats: make(map[string]*ProviderUsageStats),
		ModelStats:    make(map[string]*ModelUsageStats),
	}
}

// AddRecord adds a usage record to the statistics
func (us *UsageStats) AddRecord(record *UsageRecord) {
	// Update totals
	us.TotalRequests++
	us.TotalTokens += record.TotalTokens
	us.InputTokens += record.InputTokens
	us.OutputTokens += record.OutputTokens
	
	if record.IsStreaming {
		us.StreamingRequests++
	}
	
	// Update provider stats
	if providerStats, exists := us.ProviderStats[record.Provider]; exists {
		providerStats.Requests++
		providerStats.TotalTokens += record.TotalTokens
		providerStats.InputTokens += record.InputTokens
		providerStats.OutputTokens += record.OutputTokens
		providerStats.LastUsed = record.Timestamp
	} else {
		us.ProviderStats[record.Provider] = &ProviderUsageStats{
			Provider:     record.Provider,
			Requests:     1,
			TotalTokens:  record.TotalTokens,
			InputTokens:  record.InputTokens,
			OutputTokens: record.OutputTokens,
			LastUsed:     record.Timestamp,
		}
	}
	
	// Update model stats
	modelKey := fmt.Sprintf("%s:%s", record.Provider, record.Model)
	if modelStats, exists := us.ModelStats[modelKey]; exists {
		modelStats.Requests++
		modelStats.TotalTokens += record.TotalTokens
		modelStats.InputTokens += record.InputTokens
		modelStats.OutputTokens += record.OutputTokens
		modelStats.LastUsed = record.Timestamp
	} else {
		us.ModelStats[modelKey] = &ModelUsageStats{
			Model:        record.Model,
			Provider:     record.Provider,
			Requests:     1,
			TotalTokens:  record.TotalTokens,
			InputTokens:  record.InputTokens,
			OutputTokens: record.OutputTokens,
			LastUsed:     record.Timestamp,
		}
	}
}

// CalculateRates calculates derived statistics like requests per minute
func (us *UsageStats) CalculateRates() {
	duration := us.WindowEnd.Sub(us.WindowStart)
	minutes := duration.Minutes()
	
	if minutes > 0 {
		us.RequestsPerMinute = float64(us.TotalRequests) / minutes
		us.TokensPerMinute = float64(us.TotalTokens) / minutes
	}
	
	if us.TotalRequests > 0 {
		us.AverageTokensPerRequest = float64(us.TotalTokens) / float64(us.TotalRequests)
	}
}

// GetProviderList returns a list of all providers in the stats
func (us *UsageStats) GetProviderList() []string {
	var providers []string
	for provider := range us.ProviderStats {
		providers = append(providers, provider)
	}
	return providers
}

// GetModelList returns a list of all models in the stats
func (us *UsageStats) GetModelList() []string {
	var models []string
	for model := range us.ModelStats {
		models = append(models, model)
	}
	return models
}

// GetTopProviders returns the top N providers by request count
func (us *UsageStats) GetTopProviders(n int) []*ProviderUsageStats {
	var providers []*ProviderUsageStats
	for _, stats := range us.ProviderStats {
		providers = append(providers, stats)
	}
	
	// Simple bubble sort by request count (descending)
	for i := 0; i < len(providers)-1; i++ {
		for j := 0; j < len(providers)-i-1; j++ {
			if providers[j].Requests < providers[j+1].Requests {
				providers[j], providers[j+1] = providers[j+1], providers[j]
			}
		}
	}
	
	if n > len(providers) {
		n = len(providers)
	}
	
	return providers[:n]
}

// GetTopModels returns the top N models by request count
func (us *UsageStats) GetTopModels(n int) []*ModelUsageStats {
	var models []*ModelUsageStats
	for _, stats := range us.ModelStats {
		models = append(models, stats)
	}
	
	// Simple bubble sort by request count (descending)
	for i := 0; i < len(models)-1; i++ {
		for j := 0; j < len(models)-i-1; j++ {
			if models[j].Requests < models[j+1].Requests {
				models[j], models[j+1] = models[j+1], models[j]
			}
		}
	}
	
	if n > len(models) {
		n = len(models)
	}
	
	return models[:n]
}

// Merge merges another UsageStats object into this one
func (us *UsageStats) Merge(other *UsageStats) {
	// Update totals
	us.TotalRequests += other.TotalRequests
	us.StreamingRequests += other.StreamingRequests
	us.TotalTokens += other.TotalTokens
	us.InputTokens += other.InputTokens
	us.OutputTokens += other.OutputTokens
	us.RateLimitHits += other.RateLimitHits
	
	// Update window if needed
	if other.WindowStart.Before(us.WindowStart) {
		us.WindowStart = other.WindowStart
	}
	if other.WindowEnd.After(us.WindowEnd) {
		us.WindowEnd = other.WindowEnd
	}
	
	// Update last rate limit hit
	if other.LastRateLimitHit.After(us.LastRateLimitHit) {
		us.LastRateLimitHit = other.LastRateLimitHit
	}
	
	// Merge provider stats
	for provider, otherStats := range other.ProviderStats {
		if existingStats, exists := us.ProviderStats[provider]; exists {
			existingStats.Requests += otherStats.Requests
			existingStats.TotalTokens += otherStats.TotalTokens
			existingStats.InputTokens += otherStats.InputTokens
			existingStats.OutputTokens += otherStats.OutputTokens
			if otherStats.LastUsed.After(existingStats.LastUsed) {
				existingStats.LastUsed = otherStats.LastUsed
			}
		} else {
			us.ProviderStats[provider] = &ProviderUsageStats{
				Provider:     otherStats.Provider,
				Requests:     otherStats.Requests,
				TotalTokens:  otherStats.TotalTokens,
				InputTokens:  otherStats.InputTokens,
				OutputTokens: otherStats.OutputTokens,
				LastUsed:     otherStats.LastUsed,
			}
		}
	}
	
	// Merge model stats
	for model, otherStats := range other.ModelStats {
		if existingStats, exists := us.ModelStats[model]; exists {
			existingStats.Requests += otherStats.Requests
			existingStats.TotalTokens += otherStats.TotalTokens
			existingStats.InputTokens += otherStats.InputTokens
			existingStats.OutputTokens += otherStats.OutputTokens
			if otherStats.LastUsed.After(existingStats.LastUsed) {
				existingStats.LastUsed = otherStats.LastUsed
			}
		} else {
			us.ModelStats[model] = &ModelUsageStats{
				Model:        otherStats.Model,
				Provider:     otherStats.Provider,
				Requests:     otherStats.Requests,
				TotalTokens:  otherStats.TotalTokens,
				InputTokens:  otherStats.InputTokens,
				OutputTokens: otherStats.OutputTokens,
				LastUsed:     otherStats.LastUsed,
			}
		}
	}
	
	// Recalculate rates
	us.CalculateRates()
} 
