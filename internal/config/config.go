package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/ratelimit"
	"gopkg.in/yaml.v3"
)

// formatNumber formats a number with commas for better readability
func formatNumber(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	
	// Convert to string and add commas
	str := fmt.Sprintf("%d", n)
	result := ""
	
	for i, char := range str {
		if i > 0 && (len(str)-i)%3 == 0 {
			result += ","
		}
		result += string(char)
	}
	
	return result
}

// YAMLConfig represents the main YAML configuration structure
type YAMLConfig struct {
	// Global settings
	Enabled bool `yaml:"enabled"`
	
	// Features configuration
	Features FeaturesConfig `yaml:"features"`
	
	// Providers configuration
	Providers map[string]ProviderConfig `yaml:"providers"`
	
	// Global default limits
	Defaults DefaultLimits `yaml:"defaults"`
}

// FeaturesConfig represents feature toggle configuration
type FeaturesConfig struct {
	CostTracking  CostTrackingConfig `yaml:"cost_tracking"`
	RateLimiting  FeatureConfig      `yaml:"rate_limiting"`
}

// CostTrackingConfig represents cost tracking feature configuration
type CostTrackingConfig struct {
	Enabled   bool            `yaml:"enabled"`
	Transport TransportConfig `yaml:"transport"`
}

// TransportConfig represents cost tracking transport configuration
type TransportConfig struct {
	Type     string          `yaml:"type"` // "file" or "dynamodb"
	File     *FileTransportConfig     `yaml:"file,omitempty"`
	DynamoDB *DynamoDBTransportConfig `yaml:"dynamodb,omitempty"`
}

// FileTransportConfig represents file-based transport configuration
type FileTransportConfig struct {
	Path string `yaml:"path"`
}

// DynamoDBTransportConfig represents DynamoDB transport configuration
type DynamoDBTransportConfig struct {
	TableName string `yaml:"table_name"`
	Region    string `yaml:"region"`
}

// FeatureConfig represents a single feature's configuration
type FeatureConfig struct {
	Enabled bool `yaml:"enabled"`
}

// ProviderConfig represents configuration for a specific provider
type ProviderConfig struct {
	Enabled bool                  `yaml:"enabled"`
	Models  map[string]ModelConfig `yaml:"models"`
	
	// Provider-wide limits (fallback if model-specific limits not defined)
	DefaultLimits ModelLimits `yaml:"default_limits"`
}

// ModelConfig represents configuration for a specific model
type ModelConfig struct {
	Enabled bool        `yaml:"enabled"`
	Aliases []string    `yaml:"aliases,omitempty"` // Alternative model names that share the same limits
	Limits  ModelLimits `yaml:"limits"`
	// Pricing can be a single price, or a list of tiers.
	Pricing interface{} `yaml:"pricing,omitempty"`
}

// Pricing represents a simple input/output cost structure.
type Pricing struct {
	Input  float64 `yaml:"input"`  // Cost per 1M input tokens in USD
	Output float64 `yaml:"output"` // Cost per 1M output tokens in USD
}

// PricingTier represents a pricing tier with a token threshold.
type PricingTier struct {
	Threshold int     `yaml:"threshold"` // The token threshold for this tier
	Input     float64 `yaml:"input"`     // Cost per 1M input tokens in USD
	Output    float64 `yaml:"output"`    // Cost per 1M output tokens in USD
}

// ModelPricing represents pricing information for a model, with optional overrides for aliases.
type ModelPricing struct {
	Tiers     []PricingTier        `yaml:"tiers,omitempty"`
	Overrides map[string]Pricing `yaml:"overrides,omitempty"` // Pricing for specific model aliases
}

// ModelLimits represents rate limiting configuration for a model
type ModelLimits struct {
	// Token-based limits
	TokensPerMinute int64 `yaml:"tokens_per_minute"`  // TPM
	TokensPerHour   int64 `yaml:"tokens_per_hour"`    // TPH (calculated from TPM if not specified)
	TokensPerDay    int64 `yaml:"tokens_per_day"`     // TPD
	
	// Request-based limits
	RequestsPerMinute int64 `yaml:"requests_per_minute"` // RPM
	RequestsPerHour   int64 `yaml:"requests_per_hour"`   // RPH (calculated from RPM if not specified)
	RequestsPerDay    int64 `yaml:"requests_per_day"`    // RPD (calculated from RPM if not specified)
	
	// Burst limits (for token bucket configuration)
	BurstTokens   int64 `yaml:"burst_tokens,omitempty"`   // Max tokens in burst
	BurstRequests int64 `yaml:"burst_requests,omitempty"` // Max requests in burst
}

// DefaultLimits represents global default rate limits
type DefaultLimits struct {
	// User-level defaults
	User UserLimits `yaml:"user"`
	
	// IP-level defaults (for unauthenticated requests)
	IP IPLimits `yaml:"ip"`
	
	// Global limits
	Global GlobalLimits `yaml:"global"`
}

// UserLimits represents default limits for authenticated users
type UserLimits struct {
	TokensPerMinute   int64 `yaml:"tokens_per_minute"`
	TokensPerHour     int64 `yaml:"tokens_per_hour"`
	TokensPerDay      int64 `yaml:"tokens_per_day"`
	RequestsPerMinute int64 `yaml:"requests_per_minute"`
	RequestsPerHour   int64 `yaml:"requests_per_hour"`
	RequestsPerDay    int64 `yaml:"requests_per_day"`
}

// IPLimits represents default limits for IP-based rate limiting
type IPLimits struct {
	TokensPerMinute   int64 `yaml:"tokens_per_minute"`
	TokensPerHour     int64 `yaml:"tokens_per_hour"`
	RequestsPerMinute int64 `yaml:"requests_per_minute"`
	RequestsPerHour   int64 `yaml:"requests_per_hour"`
}

// GlobalLimits represents system-wide limits
type GlobalLimits struct {
	TokensPerMinute   int64 `yaml:"tokens_per_minute"`
	TokensPerHour     int64 `yaml:"tokens_per_hour"`
	RequestsPerMinute int64 `yaml:"requests_per_minute"`
	RequestsPerHour   int64 `yaml:"requests_per_hour"`
}

// LoadYAMLConfig loads configuration from a YAML file
func LoadYAMLConfig(filename string) (*YAMLConfig, error) {
	// Check if file exists
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return GetDefaultYAMLConfig(), nil
	}
	
	// Read the file
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", filename, err)
	}
	
	// Parse YAML
	var config YAMLConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse YAML config: %w", err)
	}
	
	// Validate and fill in calculated fields
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	// Before filling calculated fields, parse the pricing structures.
	if err := config.ParsePricing(); err != nil {
		return nil, fmt.Errorf("failed to parse pricing structures: %w", err)
	}

	config.FillCalculatedFields()

	return &config, nil
}

// SaveYAMLConfig saves configuration to a YAML file
func (c *YAMLConfig) SaveYAMLConfig(filename string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}
	
	if err := os.WriteFile(filename, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}
	
	return nil
}

// Validate validates the configuration
func (c *YAMLConfig) Validate() error {
	if c.Providers == nil {
		return fmt.Errorf("providers configuration is required")
	}
	
	// Validate transport configuration if cost tracking is enabled
	if c.Features.CostTracking.Enabled {
		if err := c.validateTransportConfig(); err != nil {
			return fmt.Errorf("invalid transport configuration: %w", err)
		}
	}
	
	for providerName, provider := range c.Providers {
		if provider.Enabled {
			if provider.Models == nil {
				continue // Skip validation if no models defined
			}
			
			for modelName, model := range provider.Models {
				if model.Enabled {
					// Validate that at least one limit is specified
					limits := model.Limits
					if limits.TokensPerMinute <= 0 && limits.RequestsPerMinute <= 0 {
						return fmt.Errorf("provider %s model %s must have at least one rate limit specified", providerName, modelName)
					}
				}
			}
		}
	}
	
	return nil
}

// validateTransportConfig validates the transport configuration
func (c *YAMLConfig) validateTransportConfig() error {
	transport := c.Features.CostTracking.Transport
	
	switch transport.Type {
	case "file":
		if transport.File == nil {
			return fmt.Errorf("file transport configuration is required when type is 'file'")
		}
		if transport.File.Path == "" {
			return fmt.Errorf("file path is required for file transport")
		}
	case "dynamodb":
		if transport.DynamoDB == nil {
			return fmt.Errorf("dynamodb transport configuration is required when type is 'dynamodb'")
		}
		if transport.DynamoDB.TableName == "" {
			return fmt.Errorf("table_name is required for dynamodb transport")
		}
		if transport.DynamoDB.Region == "" {
			return fmt.Errorf("region is required for dynamodb transport")
		}
	case "":
		return fmt.Errorf("transport type is required")
	default:
		return fmt.Errorf("unsupported transport type: %s (supported: file, dynamodb)", transport.Type)
	}
	
	return nil
}

// GetTransportConfig returns the transport configuration
func (c *YAMLConfig) GetTransportConfig() (*TransportConfig, error) {
	if !c.Features.CostTracking.Enabled {
		return nil, fmt.Errorf("cost tracking is disabled")
	}
	
	return &c.Features.CostTracking.Transport, nil
}

// ParsePricing iterates through all models and parses the flexible `Pricing` field
// into a structured `ModelPricing` object.
func (c *YAMLConfig) ParsePricing() error {
	for providerName, provider := range c.Providers {
		for modelName, model := range provider.Models {
			if model.Pricing == nil {
				continue
			}

			parsedPricing, err := parseModelPricing(model.Pricing)
			if err != nil {
				return fmt.Errorf("error parsing pricing for %s/%s: %w", providerName, modelName, err)
			}
			model.Pricing = parsedPricing
			provider.Models[modelName] = model
		}
	}
	return nil
}

// parseModelPricing handles the logic of parsing the `interface{}` pricing field.
func parseModelPricing(pricingData interface{}) (*ModelPricing, error) {
	mp := &ModelPricing{}

	switch v := pricingData.(type) {
	case []interface{}:
		// It's a list of tiers.
		for _, tierData := range v {
			tierMap, ok := tierData.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("invalid pricing tier format")
			}
			tier := PricingTier{}
			if th, ok := tierMap["threshold"].(int); ok {
				tier.Threshold = th
			}
			if in, ok := tierMap["input"].(float64); ok {
				tier.Input = in
			}
			if out, ok := tierMap["output"].(float64); ok {
				tier.Output = out
			}
			mp.Tiers = append(mp.Tiers, tier)
		}
	case map[string]interface{}:
		// It's a simple price or has overrides.
		if _, ok := v["input"]; ok {
			// Simple pricing.
			mp.Tiers = []PricingTier{
				{
					Threshold: 0,
					Input:     v["input"].(float64),
					Output:    v["output"].(float64),
				},
			}
		}

		if overrides, ok := v["overrides"].(map[string]interface{}); ok {
			mp.Overrides = make(map[string]Pricing)
			for alias, overrideData := range overrides {
				overrideMap := overrideData.(map[string]interface{})
				mp.Overrides[alias] = Pricing{
					Input:  overrideMap["input"].(float64),
					Output: overrideMap["output"].(float64),
				}
			}
		}
	default:
		return nil, fmt.Errorf("unsupported pricing format: %T", pricingData)
	}

	return mp, nil
}


// FillCalculatedFields fills in calculated fields based on primary limits
func (c *YAMLConfig) FillCalculatedFields() {
	for providerName, provider := range c.Providers {
		// Fill provider default limits
		provider.DefaultLimits.FillCalculated()
		
		// Fill model-specific limits
		for modelName, model := range provider.Models {
			model.Limits.FillCalculated()
			provider.Models[modelName] = model
		}
		
		c.Providers[providerName] = provider
	}
	
	// Fill default limits
	c.Defaults.FillCalculated()
}

// FillCalculated fills calculated fields for ModelLimits
func (ml *ModelLimits) FillCalculated() {
	// Calculate hourly limits from per-minute if not specified
	if ml.TokensPerHour <= 0 && ml.TokensPerMinute > 0 {
		ml.TokensPerHour = ml.TokensPerMinute * 60
	}
	if ml.RequestsPerHour <= 0 && ml.RequestsPerMinute > 0 {
		ml.RequestsPerHour = ml.RequestsPerMinute * 60
	}
	
	// Calculate daily limits from per-minute if not specified  
	if ml.RequestsPerDay <= 0 && ml.RequestsPerMinute > 0 {
		ml.RequestsPerDay = ml.RequestsPerMinute * 60 * 24
	}
	
	// Set reasonable burst limits if not specified
	if ml.BurstTokens <= 0 && ml.TokensPerMinute > 0 {
		// Allow 10% of per-minute limit as burst
		ml.BurstTokens = ml.TokensPerMinute / 10
		if ml.BurstTokens < 100 {
			ml.BurstTokens = 100 // Minimum burst
		}
	}
	
	if ml.BurstRequests <= 0 && ml.RequestsPerMinute > 0 {
		// Allow 10% of per-minute limit as burst
		ml.BurstRequests = ml.RequestsPerMinute / 10
		if ml.BurstRequests < 5 {
			ml.BurstRequests = 5 // Minimum burst
		}
	}
}

// FillCalculated fills calculated fields for DefaultLimits
func (dl *DefaultLimits) FillCalculated() {
	// User limits
	if dl.User.TokensPerHour <= 0 && dl.User.TokensPerMinute > 0 {
		dl.User.TokensPerHour = dl.User.TokensPerMinute * 60
	}
	if dl.User.TokensPerDay <= 0 && dl.User.TokensPerMinute > 0 {
		dl.User.TokensPerDay = dl.User.TokensPerMinute * 60 * 24
	}
	if dl.User.RequestsPerHour <= 0 && dl.User.RequestsPerMinute > 0 {
		dl.User.RequestsPerHour = dl.User.RequestsPerMinute * 60
	}
	if dl.User.RequestsPerDay <= 0 && dl.User.RequestsPerMinute > 0 {
		dl.User.RequestsPerDay = dl.User.RequestsPerMinute * 60 * 24
	}
	
	// IP limits
	if dl.IP.TokensPerHour <= 0 && dl.IP.TokensPerMinute > 0 {
		dl.IP.TokensPerHour = dl.IP.TokensPerMinute * 60
	}
	if dl.IP.RequestsPerHour <= 0 && dl.IP.RequestsPerMinute > 0 {
		dl.IP.RequestsPerHour = dl.IP.RequestsPerMinute * 60
	}
	
	// Global limits
	if dl.Global.TokensPerHour <= 0 && dl.Global.TokensPerMinute > 0 {
		dl.Global.TokensPerHour = dl.Global.TokensPerMinute * 60
	}
	if dl.Global.RequestsPerHour <= 0 && dl.Global.RequestsPerMinute > 0 {
		dl.Global.RequestsPerHour = dl.Global.RequestsPerMinute * 60
	}
}

// GetModelLimits returns the rate limits for a specific provider and model
func (c *YAMLConfig) GetModelLimits(provider, model string) (*ModelLimits, error) {
	providerConfig, exists := c.Providers[provider]
	if !exists {
		return nil, fmt.Errorf("provider %s not found", provider)
	}
	
	if !providerConfig.Enabled {
		return nil, fmt.Errorf("provider %s is disabled", provider)
	}
	
	// Check if model exists in provider config
	if modelConfig, exists := providerConfig.Models[model]; exists {
		if !modelConfig.Enabled {
			return nil, fmt.Errorf("model %s for provider %s is disabled", model, provider)
		}
		return &modelConfig.Limits, nil
	}
	
	// Fall back to provider default limits
	if providerConfig.DefaultLimits.TokensPerMinute > 0 || providerConfig.DefaultLimits.RequestsPerMinute > 0 {
		return &providerConfig.DefaultLimits, nil
	}
	
	return nil, fmt.Errorf("no rate limits configured for provider %s model %s", provider, model)
}

// GetModelPricing returns the pricing information for a specific provider and model
func (c *YAMLConfig) GetModelPricing(provider, model string, inputTokens int) (*Pricing, error) {
	providerConfig, exists := c.Providers[provider]
	if !exists {
		return nil, fmt.Errorf("provider %s not found", provider)
	}

	if !providerConfig.Enabled {
		return nil, fmt.Errorf("provider %s is disabled", provider)
	}

	// findModelConfig checks for a direct match or an alias and returns the
	// canonical model's configuration and its name.
	findModelConfig := func(modelName string) (*ModelConfig, string) {
		// Check for a direct match first.
		if mc, ok := providerConfig.Models[modelName]; ok {
			return &mc, modelName
		}
		// Check if the model is an alias.
		for canonicalName, mc := range providerConfig.Models {
			for _, alias := range mc.Aliases {
				if alias == modelName {
					return &mc, canonicalName
				}
			}
		}
		return nil, ""
	}

	modelConfig, canonicalName := findModelConfig(model)
	if modelConfig == nil || !modelConfig.Enabled {
		return nil, fmt.Errorf("model %s for provider %s is not configured or disabled", model, provider)
	}

	modelPricing, ok := modelConfig.Pricing.(*ModelPricing)
	if !ok || modelPricing == nil {
		return nil, fmt.Errorf("no pricing configured for provider %s model %s", provider, canonicalName)
	}

	// Check for a pricing override for the specific alias that was requested.
	if price, ok := modelPricing.Overrides[model]; ok {
		return &price, nil
	}

	// Handle tiered pricing.
	if len(modelPricing.Tiers) > 0 {
		// Sort tiers by threshold descending to find the correct tier.
		// A simple bubble sort is fine for the small number of tiers we expect.
		for i := 0; i < len(modelPricing.Tiers); i++ {
			for j := i + 1; j < len(modelPricing.Tiers); j++ {
				if modelPricing.Tiers[i].Threshold < modelPricing.Tiers[j].Threshold {
					modelPricing.Tiers[i], modelPricing.Tiers[j] = modelPricing.Tiers[j], modelPricing.Tiers[i]
				}
			}
		}

		for _, tier := range modelPricing.Tiers {
			if tier.Threshold == 0 || inputTokens <= tier.Threshold {
				return &Pricing{Input: tier.Input, Output: tier.Output}, nil
			}
		}
	}

	return nil, fmt.Errorf("no applicable pricing tier found for provider %s model %s with %d tokens", provider, canonicalName, inputTokens)
}

// ConvertToRateLimitRules converts YAML configuration to rate limiting rules
func (c *YAMLConfig) ConvertToRateLimitRules() []*ratelimit.RateLimitRule {
	var rules []*ratelimit.RateLimitRule
	priority := 200 // Start with high priority for model-specific rules
	
	// Generate model-specific rules
	for providerName, provider := range c.Providers {
		if !provider.Enabled {
			continue
		}
		
		for modelName, model := range provider.Models {
			if !model.Enabled {
				continue
			}
			
			limits := model.Limits
			
			// Create alias list for description (but only create rules for canonical model)
			aliasDescription := ""
			if len(model.Aliases) > 0 {
				aliasDescription = fmt.Sprintf(" (aliases: %s)", strings.Join(model.Aliases, ", "))
			}
			
			// Generate rules only for the canonical model name
			// Aliases will be resolved to this canonical model by the rate limiter
			// Token per minute rule for this canonical model
			if limits.TokensPerMinute > 0 {
				rules = append(rules, &ratelimit.RateLimitRule{
					Name:         fmt.Sprintf("%s_%s_tokens_per_minute", providerName, modelName),
					Description:  fmt.Sprintf("Limit %s %s to %d tokens per minute per user%s", providerName, modelName, limits.TokensPerMinute, aliasDescription),
					Scope:        "user_model",
					Provider:     providerName,
					Model:        modelName, // Use canonical model name only
					Type:         "tokens",
					Limit:        limits.TokensPerMinute,
					Window:       time.Minute,
					RefillRate:   limits.TokensPerMinute / 60, // Refill per second
					RefillPeriod: time.Second,
					Priority:     priority,
					Enabled:      true,
				})
			}
			
			// Requests per minute rule for this canonical model
			if limits.RequestsPerMinute > 0 {
				rules = append(rules, &ratelimit.RateLimitRule{
					Name:         fmt.Sprintf("%s_%s_requests_per_minute", providerName, modelName),
					Description:  fmt.Sprintf("Limit %s %s to %d requests per minute per user%s", providerName, modelName, limits.RequestsPerMinute, aliasDescription),
					Scope:        "user_model",
					Provider:     providerName,
					Model:        modelName, // Use canonical model name only
					Type:         "requests",
					Limit:        limits.RequestsPerMinute,
					Window:       time.Minute,
					RefillRate:   limits.RequestsPerMinute / 60, // Refill per second
					RefillPeriod: time.Second,
					Priority:     priority + 1,
					Enabled:      true,
				})
			}
			
			// Daily token limit if specified
			if limits.TokensPerDay > 0 {
				rules = append(rules, &ratelimit.RateLimitRule{
					Name:         fmt.Sprintf("%s_%s_tokens_per_day", providerName, modelName),
					Description:  fmt.Sprintf("Limit %s %s to %d tokens per day per user%s", providerName, modelName, limits.TokensPerDay, aliasDescription),
					Scope:        "user_model",
					Provider:     providerName,
					Model:        modelName, // Use canonical model name only
					Type:         "tokens",
					Limit:        limits.TokensPerDay,
					Window:       24 * time.Hour,
					RefillRate:   limits.TokensPerDay / (24 * 60), // Refill per minute
					RefillPeriod: time.Minute,
					Priority:     priority - 1,
					Enabled:      true,
				})
			}
			
			priority -= 5 // Lower priority for next model group
		}
		
		// Provider-wide default rules
		if provider.DefaultLimits.TokensPerMinute > 0 {
			rules = append(rules, &ratelimit.RateLimitRule{
				Name:         fmt.Sprintf("%s_default_tokens_per_minute", providerName),
				Description:  fmt.Sprintf("Default limit for %s provider: %d tokens per minute per user", providerName, provider.DefaultLimits.TokensPerMinute),
				Scope:        "user_provider",
				Provider:     providerName,
				Type:         "tokens",
				Limit:        provider.DefaultLimits.TokensPerMinute,
				Window:       time.Minute,
				RefillRate:   provider.DefaultLimits.TokensPerMinute / 60,
				RefillPeriod: time.Second,
				Priority:     100, // Lower priority than model-specific
				Enabled:      true,
			})
		}
		
		if provider.DefaultLimits.RequestsPerMinute > 0 {
			rules = append(rules, &ratelimit.RateLimitRule{
				Name:         fmt.Sprintf("%s_default_requests_per_minute", providerName),
				Description:  fmt.Sprintf("Default limit for %s provider: %d requests per minute per user", providerName, provider.DefaultLimits.RequestsPerMinute),
				Scope:        "user_provider",
				Provider:     providerName,
				Type:         "requests",
				Limit:        provider.DefaultLimits.RequestsPerMinute,
				Window:       time.Minute,
				RefillRate:   provider.DefaultLimits.RequestsPerMinute / 60,
				RefillPeriod: time.Second,
				Priority:     99,
				Enabled:      true,
			})
		}
	}
	
	// Add global default rules
	if c.Defaults.User.TokensPerMinute > 0 {
		rules = append(rules, &ratelimit.RateLimitRule{
			Name:         "user_global_tokens_per_minute",
			Description:  fmt.Sprintf("Global user limit: %d tokens per minute", c.Defaults.User.TokensPerMinute),
			Scope:        "user",
			Type:         "tokens",
			Limit:        c.Defaults.User.TokensPerMinute,
			Window:       time.Minute,
			RefillRate:   c.Defaults.User.TokensPerMinute / 60,
			RefillPeriod: time.Second,
			Priority:     50,
			Enabled:      true,
		})
	}
	
	if c.Defaults.User.RequestsPerMinute > 0 {
		rules = append(rules, &ratelimit.RateLimitRule{
			Name:         "user_global_requests_per_minute",
			Description:  fmt.Sprintf("Global user limit: %d requests per minute", c.Defaults.User.RequestsPerMinute),
			Scope:        "user",
			Type:         "requests",
			Limit:        c.Defaults.User.RequestsPerMinute,
			Window:       time.Minute,
			RefillRate:   c.Defaults.User.RequestsPerMinute / 60,
			RefillPeriod: time.Second,
			Priority:     49,
			Enabled:      true,
		})
	}
	
	if c.Defaults.IP.TokensPerMinute > 0 {
		rules = append(rules, &ratelimit.RateLimitRule{
			Name:         "ip_global_tokens_per_minute",
			Description:  fmt.Sprintf("Global IP limit: %d tokens per minute", c.Defaults.IP.TokensPerMinute),
			Scope:        "ip",
			Type:         "tokens",
			Limit:        c.Defaults.IP.TokensPerMinute,
			Window:       time.Minute,
			RefillRate:   c.Defaults.IP.TokensPerMinute / 60,
			RefillPeriod: time.Second,
			Priority:     40,
			Enabled:      true,
		})
	}
	
	if c.Defaults.IP.RequestsPerMinute > 0 {
		rules = append(rules, &ratelimit.RateLimitRule{
			Name:         "ip_global_requests_per_minute",
			Description:  fmt.Sprintf("Global IP limit: %d requests per minute", c.Defaults.IP.RequestsPerMinute),
			Scope:        "ip",
			Type:         "requests",
			Limit:        c.Defaults.IP.RequestsPerMinute,
			Window:       time.Minute,
			RefillRate:   c.Defaults.IP.RequestsPerMinute / 60,
			RefillPeriod: time.Second,
			Priority:     39,
			Enabled:      true,
		})
	}
	
	return rules
}

// getDefaultYAMLConfig returns a default configuration
func GetDefaultYAMLConfig() *YAMLConfig {
	return &YAMLConfig{
		Enabled: true,
		Features: FeaturesConfig{
			CostTracking: CostTrackingConfig{
				Enabled: true,
				Transport: TransportConfig{
					Type: "file",
					File: &FileTransportConfig{
						Path: "./cost_tracking.json",
					},
				},
			},
			RateLimiting: FeatureConfig{
				Enabled: true,
			},
		},
		Providers: map[string]ProviderConfig{
			"openai": {
				Enabled: true,
				Models:  make(map[string]ModelConfig),
				DefaultLimits: ModelLimits{
					TokensPerMinute:   10000,
					RequestsPerMinute: 100,
				},
			},
			"anthropic": {
				Enabled: true,
				Models:  make(map[string]ModelConfig),
				DefaultLimits: ModelLimits{
					TokensPerMinute:   5000,
					RequestsPerMinute: 50,
				},
			},
			"gemini": {
				Enabled: true,
				Models:  make(map[string]ModelConfig),
				DefaultLimits: ModelLimits{
					TokensPerMinute:   8000,
					RequestsPerMinute: 60,
				},
			},
		},
		Defaults: DefaultLimits{
			User: UserLimits{
				TokensPerMinute:   10000,
				RequestsPerMinute: 60,
			},
			IP: IPLimits{
				TokensPerMinute:   5000,
				RequestsPerMinute: 30,
			},
			Global: GlobalLimits{
				TokensPerMinute:   1000000,
				RequestsPerMinute: 10000,
			},
		},
	}
}

// LogConfiguration logs the configuration in a clean, per-model format
func (c *YAMLConfig) LogConfiguration(logger *slog.Logger) {
	if !c.Enabled {
		logger.Warn("Configuration is disabled")
		return
	}
	
	logger.Info("📋 Configuration Summary:", "enabled", c.Enabled, "providers_configured", len(c.Providers))
	
	// Check if rate limiting is enabled
	rateLimitingEnabled := c.Features.RateLimiting.Enabled
	
	// Log provider and model details
	for providerName, provider := range c.Providers {
		if !provider.Enabled {
			logger.Info("Provider disabled", "provider", strings.ToUpper(providerName))
			continue
		}
		
		logger.Info("Provider enabled", "provider", strings.ToUpper(providerName))
		
		// Log provider default limits if they exist and rate limiting is enabled
		if rateLimitingEnabled && (provider.DefaultLimits.TokensPerMinute > 0 || provider.DefaultLimits.RequestsPerMinute > 0) {
			logger.Info("Default limits", "provider", providerName, "tokens_per_minute", formatNumber(provider.DefaultLimits.TokensPerMinute), "requests_per_minute", formatNumber(provider.DefaultLimits.RequestsPerMinute))
		} else if !rateLimitingEnabled && (provider.DefaultLimits.TokensPerMinute > 0 || provider.DefaultLimits.RequestsPerMinute > 0) {
			logger.Info("Rate limiting disabled for provider", "provider", providerName)
		}
		
		// Log model-specific configurations
		if len(provider.Models) > 0 {
			logger.Info("Models configured", "provider", providerName, "count", len(provider.Models))
			for modelName, model := range provider.Models {
				status := "ENABLED"
				if !model.Enabled {
					status = "DISABLED"
				}
				
				logger.Info("Model status", "provider", providerName, "model", modelName, "status", status)
				
				// Log aliases if they exist
				if len(model.Aliases) > 0 {
					logger.Info("Model aliases", "provider", providerName, "model", modelName, "aliases", strings.Join(model.Aliases, ", "))
				}
				
				// Log rate limits only if rate limiting is enabled and model is enabled
				if model.Enabled {
					if rateLimitingEnabled {
						logger.Info("Rate limits",
							"provider", providerName,
							"model", modelName,
							"tokens_per_minute", formatNumber(model.Limits.TokensPerMinute),
							"tokens_per_day", formatNumber(model.Limits.TokensPerDay),
							"requests_per_minute", formatNumber(model.Limits.RequestsPerMinute),
							"requests_per_day", formatNumber(model.Limits.RequestsPerDay),
							"burst_tokens", formatNumber(model.Limits.BurstTokens),
							"burst_requests", formatNumber(model.Limits.BurstRequests),
						)
					} else {
						logger.Info("Rate limiting disabled for model", "provider", providerName, "model", modelName)
					}
					
					// Log pricing information
					if model.Pricing != nil {
						modelPricing, ok := model.Pricing.(*ModelPricing)
						if !ok {
							logger.Warn("Could not parse pricing info", "provider", providerName, "model", modelName)
							continue
						}
						if len(modelPricing.Tiers) > 1 {
							for _, tier := range modelPricing.Tiers {
								if tier.Threshold > 0 {
									logger.Info("Tiered pricing", "provider", providerName, "model", modelName, "threshold", tier.Threshold, "input_cost", tier.Input, "output_cost", tier.Output)
								} else {
									logger.Info("Fallback pricing", "provider", providerName, "model", modelName, "input_cost", tier.Input, "output_cost", tier.Output)
								}
							}
						} else if len(modelPricing.Tiers) == 1 {
							logger.Info("Simple pricing", "provider", providerName, "model", modelName, "input_cost", modelPricing.Tiers[0].Input, "output_cost", modelPricing.Tiers[0].Output)
						}

						if len(modelPricing.Overrides) > 0 {
							for alias, price := range modelPricing.Overrides {
								logger.Info("Pricing override", "provider", providerName, "model", modelName, "alias", alias, "input_cost", price.Input, "output_cost", price.Output)
							}
						}
					} else {
						logger.Info("Pricing not configured", "provider", providerName, "model", modelName)
					}
				}
			}
		} else {
			logger.Info("No models specifically configured, using provider defaults", "provider", providerName)
		}
	}
	
	// Log default limits only if rate limiting is enabled
	if rateLimitingEnabled {
		logger.Info("Global Default Limits")
		if c.Defaults.User.TokensPerMinute > 0 || c.Defaults.User.RequestsPerMinute > 0 {
			logger.Info("Per User limits", "tokens_per_minute", formatNumber(c.Defaults.User.TokensPerMinute), "tokens_per_day", formatNumber(c.Defaults.User.TokensPerDay), "requests_per_minute", formatNumber(c.Defaults.User.RequestsPerMinute), "requests_per_day", formatNumber(c.Defaults.User.RequestsPerDay))
		}
		
		if c.Defaults.Global.TokensPerMinute > 0 || c.Defaults.Global.RequestsPerMinute > 0 {
			logger.Info("Global limits", "tokens_per_minute", formatNumber(c.Defaults.Global.TokensPerMinute), "requests_per_minute", formatNumber(c.Defaults.Global.RequestsPerMinute))
		}
	} else {
		logger.Info("Global Rate Limiting: DISABLED")
	}
} 
