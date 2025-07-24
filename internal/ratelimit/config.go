package ratelimit

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Config holds the rate limiting configuration
type Config struct {
	// Global settings
	Enabled bool `json:"enabled"`
	
	// Rules define the rate limiting rules
	Rules []*RateLimitRule `json:"rules"`
	
	// Storage configuration
	Storage StorageConfig `json:"storage"`
	
	// Default settings
	Defaults DefaultConfig `json:"defaults"`
}

// RateLimitRule defines a single rate limiting rule
type RateLimitRule struct {
	// Rule identification
	Name        string `json:"name"`
	Description string `json:"description"`
	
	// Rule scope - determines which requests this rule applies to
	Scope string `json:"scope"` // "user", "ip", "global", "user_provider", "user_model", etc.
	
	// Filters - if specified, rule only applies to matching requests
	Provider      string `json:"provider,omitempty"`       // "openai", "anthropic", "gemini"
	Model         string `json:"model,omitempty"`          // specific model name
	Endpoint      string `json:"endpoint,omitempty"`       // specific endpoint pattern
	StreamingOnly bool   `json:"streaming_only,omitempty"` // only apply to streaming requests
	
	// Rate limiting parameters
	Type         string        `json:"type"`          // "requests", "tokens", "requests_per_minute", "tokens_per_minute"
	Limit        int64         `json:"limit"`         // maximum number of tokens/requests
	Window       time.Duration `json:"window"`        // time window for the limit
	RefillRate   int64         `json:"refill_rate"`   // tokens to add per refill period
	RefillPeriod time.Duration `json:"refill_period"` // how often to refill tokens
	
	// Priority for rule ordering (higher priority rules are checked first)
	Priority int `json:"priority"`
	
	// Whether this rule is enabled
	Enabled bool `json:"enabled"`
}

// StorageConfig defines storage configuration for rate limiting data
type StorageConfig struct {
	Type   string                 `json:"type"`   // "memory", "redis", "database"
	Config map[string]interface{} `json:"config"` // storage-specific configuration
}

// DefaultConfig defines default rate limiting settings
type DefaultConfig struct {
	// Default limits for different scopes
	UserRequestsPerMinute int64 `json:"user_requests_per_minute"`
	UserTokensPerMinute   int64 `json:"user_tokens_per_minute"`
	UserTokensPerHour     int64 `json:"user_tokens_per_hour"`
	UserTokensPerDay      int64 `json:"user_tokens_per_day"`
	
	IPRequestsPerMinute   int64 `json:"ip_requests_per_minute"`
	IPTokensPerMinute     int64 `json:"ip_tokens_per_minute"`
	
	GlobalRequestsPerMinute int64 `json:"global_requests_per_minute"`
	GlobalTokensPerMinute   int64 `json:"global_tokens_per_minute"`
	
	// Model-specific defaults
	ModelLimits map[string]ModelLimit `json:"model_limits"`
}

// ModelLimit defines rate limits for specific models
type ModelLimit struct {
	RequestsPerMinute int64 `json:"requests_per_minute"`
	TokensPerMinute   int64 `json:"tokens_per_minute"`
	TokensPerHour     int64 `json:"tokens_per_hour"`
	TokensPerDay      int64 `json:"tokens_per_day"`
}

// NewDefaultConfig creates a default configuration
func NewDefaultConfig() *Config {
	return &Config{
		Enabled: true,
		Rules: []*RateLimitRule{
			// User-level limits
			{
				Name:         "user_requests_per_minute",
				Description:  "Limit users to 60 requests per minute",
				Scope:        "user",
				Type:         "requests",
				Limit:        60,
				Window:       time.Minute,
				RefillRate:   1,
				RefillPeriod: time.Second,
				Priority:     100,
				Enabled:      true,
			},
			{
				Name:         "user_tokens_per_minute",
				Description:  "Limit users to 10,000 tokens per minute",
				Scope:        "user",
				Type:         "tokens",
				Limit:        10000,
				Window:       time.Minute,
				RefillRate:   167, // ~10k tokens per minute
				RefillPeriod: time.Second,
				Priority:     90,
				Enabled:      true,
			},
			{
				Name:         "user_tokens_per_hour",
				Description:  "Limit users to 100,000 tokens per hour",
				Scope:        "user",
				Type:         "tokens",
				Limit:        100000,
				Window:       time.Hour,
				RefillRate:   1667, // ~100k tokens per hour
				RefillPeriod: time.Minute,
				Priority:     80,
				Enabled:      true,
			},
			
			// IP-level limits (for unauthenticated requests)
			{
				Name:         "ip_requests_per_minute",
				Description:  "Limit IP addresses to 30 requests per minute",
				Scope:        "ip",
				Type:         "requests",
				Limit:        30,
				Window:       time.Minute,
				RefillRate:   1,
				RefillPeriod: 2 * time.Second,
				Priority:     70,
				Enabled:      true,
			},
			
			// Model-specific limits
			{
				Name:         "gpt4_tokens_per_minute",
				Description:  "Limit GPT-4 usage to 5,000 tokens per minute per user",
				Scope:        "user_model",
				Model:        "gpt-4",
				Type:         "tokens",
				Limit:        5000,
				Window:       time.Minute,
				RefillRate:   84, // ~5k tokens per minute
				RefillPeriod: time.Second,
				Priority:     110,
				Enabled:      true,
			},
			{
				Name:         "claude_opus_tokens_per_minute",
				Description:  "Limit Claude Opus usage to 3,000 tokens per minute per user",
				Scope:        "user_model",
				Model:        "claude-3-opus",
				Type:         "tokens",
				Limit:        3000,
				Window:       time.Minute,
				RefillRate:   50, // ~3k tokens per minute
				RefillPeriod: time.Second,
				Priority:     110,
				Enabled:      true,
			},
			
			// Provider-specific limits
			{
				Name:         "openai_requests_per_minute",
				Description:  "Limit OpenAI requests to 1000 per minute globally",
				Scope:        "global_provider",
				Provider:     "openai",
				Type:         "requests",
				Limit:        1000,
				Window:       time.Minute,
				RefillRate:   17, // ~1k requests per minute
				RefillPeriod: time.Second,
				Priority:     50,
				Enabled:      false, // Disabled by default for global limits
			},
			
			// Streaming-specific limits
			{
				Name:         "streaming_requests_per_minute",
				Description:  "Limit streaming requests to 30 per minute per user",
				Scope:        "user",
				Type:         "requests",
				StreamingOnly: true,
				Limit:        30,
				Window:       time.Minute,
				RefillRate:   1,
				RefillPeriod: 2 * time.Second,
				Priority:     120,
				Enabled:      true,
			},
		},
		Storage: StorageConfig{
			Type:   "memory",
			Config: make(map[string]interface{}),
		},
		Defaults: DefaultConfig{
			UserRequestsPerMinute: 60,
			UserTokensPerMinute:   10000,
			UserTokensPerHour:     100000,
			UserTokensPerDay:      500000,
			IPRequestsPerMinute:   30,
			IPTokensPerMinute:     5000,
			GlobalRequestsPerMinute: 10000,
			GlobalTokensPerMinute:   1000000,
			ModelLimits: map[string]ModelLimit{
				"gpt-4": {
					RequestsPerMinute: 20,
					TokensPerMinute:   5000,
					TokensPerHour:     50000,
					TokensPerDay:      200000,
				},
				"gpt-4-turbo": {
					RequestsPerMinute: 30,
					TokensPerMinute:   8000,
					TokensPerHour:     80000,
					TokensPerDay:      300000,
				},
				"gpt-3.5-turbo": {
					RequestsPerMinute: 100,
					TokensPerMinute:   15000,
					TokensPerHour:     150000,
					TokensPerDay:      500000,
				},
				"claude-3-opus": {
					RequestsPerMinute: 15,
					TokensPerMinute:   3000,
					TokensPerHour:     30000,
					TokensPerDay:      150000,
				},
				"claude-3-sonnet": {
					RequestsPerMinute: 50,
					TokensPerMinute:   10000,
					TokensPerHour:     100000,
					TokensPerDay:      400000,
				},
				"claude-3-haiku": {
					RequestsPerMinute: 100,
					TokensPerMinute:   20000,
					TokensPerHour:     200000,
					TokensPerDay:      800000,
				},
				"gemini-pro": {
					RequestsPerMinute: 60,
					TokensPerMinute:   12000,
					TokensPerHour:     120000,
					TokensPerDay:      500000,
				},
			},
		},
	}
}

// LoadConfig loads configuration from a JSON file
func LoadConfig(filename string) (*Config, error) {
	// Try to load from file first
	if _, err := os.Stat(filename); err == nil {
		data, err := os.ReadFile(filename)
		if err != nil {
			return nil, fmt.Errorf("failed to read config file: %w", err)
		}
		
		var config Config
		if err := json.Unmarshal(data, &config); err != nil {
			return nil, fmt.Errorf("failed to parse config file: %w", err)
		}
		
		return &config, nil
	}
	
	// If file doesn't exist, return default config
	return NewDefaultConfig(), nil
}

// SaveConfig saves configuration to a JSON file
func (c *Config) SaveConfig(filename string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	
	if err := os.WriteFile(filename, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}
	
	return nil
}

// ValidateConfig validates the configuration
func (c *Config) ValidateConfig() error {
	if !c.Enabled {
		return nil // No validation needed if disabled
	}
	
	// Validate rules
	for i, rule := range c.Rules {
		if err := rule.Validate(); err != nil {
			return fmt.Errorf("invalid rule %d (%s): %w", i, rule.Name, err)
		}
	}
	
	return nil
}

// Validate validates a rate limit rule
func (r *RateLimitRule) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("rule name is required")
	}
	
	if r.Scope == "" {
		return fmt.Errorf("rule scope is required")
	}
	
	validScopes := []string{"user", "ip", "global", "user_provider", "user_model", "user_endpoint", "ip_provider", "global_provider", "global_model"}
	validScope := false
	for _, scope := range validScopes {
		if r.Scope == scope {
			validScope = true
			break
		}
	}
	if !validScope {
		return fmt.Errorf("invalid scope: %s", r.Scope)
	}
	
	if r.Type == "" {
		return fmt.Errorf("rule type is required")
	}
	
	validTypes := []string{"requests", "tokens", "requests_per_minute", "tokens_per_minute"}
	validType := false
	for _, rtype := range validTypes {
		if r.Type == rtype {
			validType = true
			break
		}
	}
	if !validType {
		return fmt.Errorf("invalid type: %s", r.Type)
	}
	
	if r.Limit <= 0 {
		return fmt.Errorf("limit must be positive")
	}
	
	if r.Window <= 0 {
		return fmt.Errorf("window must be positive")
	}
	
	if r.RefillRate <= 0 {
		return fmt.Errorf("refill rate must be positive")
	}
	
	if r.RefillPeriod <= 0 {
		return fmt.Errorf("refill period must be positive")
	}
	
	return nil
}

// GetRulesForScope returns all rules that apply to a given scope
func (c *Config) GetRulesForScope(scope string) []*RateLimitRule {
	var rules []*RateLimitRule
	
	for _, rule := range c.Rules {
		if rule.Enabled && rule.Scope == scope {
			rules = append(rules, rule)
		}
	}
	
	return rules
}

// GetRuleByName returns a rule by name
func (c *Config) GetRuleByName(name string) *RateLimitRule {
	for _, rule := range c.Rules {
		if rule.Name == name {
			return rule
		}
	}
	return nil
}

// AddRule adds a new rate limit rule
func (c *Config) AddRule(rule *RateLimitRule) error {
	if err := rule.Validate(); err != nil {
		return fmt.Errorf("invalid rule: %w", err)
	}
	
	// Check for duplicate names
	for _, existing := range c.Rules {
		if existing.Name == rule.Name {
			return fmt.Errorf("rule with name %s already exists", rule.Name)
		}
	}
	
	c.Rules = append(c.Rules, rule)
	return nil
}

// RemoveRule removes a rate limit rule by name
func (c *Config) RemoveRule(name string) bool {
	for i, rule := range c.Rules {
		if rule.Name == name {
			c.Rules = append(c.Rules[:i], c.Rules[i+1:]...)
			return true
		}
	}
	return false
}

// UpdateRule updates an existing rate limit rule
func (c *Config) UpdateRule(name string, updatedRule *RateLimitRule) error {
	if err := updatedRule.Validate(); err != nil {
		return fmt.Errorf("invalid rule: %w", err)
	}
	
	for i, rule := range c.Rules {
		if rule.Name == name {
			// Keep the same name
			updatedRule.Name = name
			c.Rules[i] = updatedRule
			return nil
		}
	}
	
	return fmt.Errorf("rule with name %s not found", name)
}

// SortRulesByPriority sorts rules by priority (higher priority first)
func (c *Config) SortRulesByPriority() {
	// Simple bubble sort by priority (descending)
	n := len(c.Rules)
	for i := 0; i < n-1; i++ {
		for j := 0; j < n-i-1; j++ {
			if c.Rules[j].Priority < c.Rules[j+1].Priority {
				c.Rules[j], c.Rules[j+1] = c.Rules[j+1], c.Rules[j]
			}
		}
	}
} 
