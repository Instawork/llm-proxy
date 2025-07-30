package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

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
}

// FeaturesConfig represents feature toggle configuration
type FeaturesConfig struct {
	CostTracking CostTrackingConfig `yaml:"cost_tracking"`
}

// CostTrackingConfig represents cost tracking feature configuration
type CostTrackingConfig struct {
	Enabled   bool            `yaml:"enabled"`
	Transport TransportConfig `yaml:"transport"`
}

// TransportConfig represents cost tracking transport configuration
type TransportConfig struct {
	Type     string                   `yaml:"type"` // "file" or "dynamodb"
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

// ProviderConfig represents configuration for a specific provider
type ProviderConfig struct {
	Enabled bool                   `yaml:"enabled"`
	Models  map[string]ModelConfig `yaml:"models"`
}

// ModelConfig represents configuration for a specific model
type ModelConfig struct {
	Enabled bool     `yaml:"enabled"`
	Aliases []string `yaml:"aliases,omitempty"` // Alternative model names
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
	Tiers     []PricingTier      `yaml:"tiers,omitempty"`
	Overrides map[string]Pricing `yaml:"overrides,omitempty"` // Pricing for specific model aliases
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

	// Parse the pricing structures.
	if err := config.ParsePricing(); err != nil {
		return nil, fmt.Errorf("failed to parse pricing structures: %w", err)
	}

	return &config, nil
}

// loadYAMLConfigWithoutValidation loads configuration from a YAML file without validation
// This is used for environment-specific configs that may only contain partial overrides
func loadYAMLConfigWithoutValidation(filename string) (*YAMLConfig, error) {
	// Check if file exists
	if _, err := os.Stat(filename); os.IsNotExist(err) {
		return &YAMLConfig{}, nil // Return empty config if file doesn't exist
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

	// Note: We intentionally skip validation and pricing parsing here
	// since this is just for environment overrides
	return &config, nil
}

// LoadEnvironmentConfig loads base configuration and overlays environment-specific configuration
// based on the ENVIRONMENT variable (defaults to "dev")
func LoadEnvironmentConfig() (*YAMLConfig, error) {
	configDir := "configs"

	// Load base configuration
	baseConfig, err := LoadYAMLConfig(filepath.Join(configDir, "base.yml"))
	if err != nil {
		return nil, fmt.Errorf("failed to load base configuration: %w", err)
	}

	// Get environment from environment variable, default to "dev"
	env := os.Getenv("ENVIRONMENT")
	if env == "" {
		env = "dev"
	}
	slog.Info("Loading environment configuration", "environment", env)

	// Load environment-specific configuration (skip validation since it's just overrides)
	envConfigPath := filepath.Join(configDir, fmt.Sprintf("%s.yml", env))
	envConfig, err := loadYAMLConfigWithoutValidation(envConfigPath)
	if err != nil {
		// If environment config doesn't exist, just use base config
		if os.IsNotExist(err) {
			return baseConfig, nil
		}
		return nil, fmt.Errorf("failed to load environment configuration for %s: %w", env, err)
	}

	// Merge environment config into base config
	mergedConfig, err := mergeConfigs(baseConfig, envConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to merge configurations: %w", err)
	}

	return mergedConfig, nil
}

// mergeConfigs merges the environment config into the base config
// Environment config values override base config values
func mergeConfigs(base, env *YAMLConfig) (*YAMLConfig, error) {
	// Convert both configs to YAML bytes
	baseBytes, err := yaml.Marshal(base)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal base config: %w", err)
	}

	envBytes, err := yaml.Marshal(env)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal environment config: %w", err)
	}

	// Parse both as generic maps for merging
	var baseMap map[string]interface{}
	if err := yaml.Unmarshal(baseBytes, &baseMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal base config to map: %w", err)
	}

	var envMap map[string]interface{}
	if err := yaml.Unmarshal(envBytes, &envMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal environment config to map: %w", err)
	}

	// Deep merge environment config into base config
	mergedMap := deepMerge(baseMap, envMap)

	// Convert merged map back to YAML bytes
	mergedBytes, err := yaml.Marshal(mergedMap)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal merged config: %w", err)
	}

	// Parse back into YAMLConfig struct
	var mergedConfig YAMLConfig
	if err := yaml.Unmarshal(mergedBytes, &mergedConfig); err != nil {
		return nil, fmt.Errorf("failed to unmarshal merged config: %w", err)
	}

	// Validate and parse pricing
	if err := mergedConfig.Validate(); err != nil {
		return nil, fmt.Errorf("invalid merged configuration: %w", err)
	}

	if err := mergedConfig.ParsePricing(); err != nil {
		return nil, fmt.Errorf("failed to parse pricing in merged configuration: %w", err)
	}

	return &mergedConfig, nil
}

// deepMerge recursively merges map b into map a
// Values in b override values in a
func deepMerge(a, b map[string]interface{}) map[string]interface{} {
	result := make(map[string]interface{})

	// Copy all values from a
	for k, v := range a {
		result[k] = v
	}

	// Merge values from b
	for k, v := range b {
		if existingValue, exists := result[k]; exists {
			// If both values are maps, merge them recursively
			if existingMap, ok := existingValue.(map[string]interface{}); ok {
				if newMap, ok := v.(map[string]interface{}); ok {
					result[k] = deepMerge(existingMap, newMap)
					continue
				}
			}
		}
		// Otherwise, override with the new value
		result[k] = v
	}

	return result
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
			} else if in, ok := tierMap["input"].(int); ok {
				tier.Input = float64(in)
			}
			if out, ok := tierMap["output"].(float64); ok {
				tier.Output = out
			} else if out, ok := tierMap["output"].(int); ok {
				tier.Output = float64(out)
			}
			mp.Tiers = append(mp.Tiers, tier)
		}
	case map[string]interface{}:
		// It's a simple price or has overrides.
		if _, ok := v["input"]; ok {
			// Simple pricing.
			tier := PricingTier{Threshold: 0}
			if in, ok := v["input"].(float64); ok {
				tier.Input = in
			} else if in, ok := v["input"].(int); ok {
				tier.Input = float64(in)
			}
			if out, ok := v["output"].(float64); ok {
				tier.Output = out
			} else if out, ok := v["output"].(int); ok {
				tier.Output = float64(out)
			}
			mp.Tiers = []PricingTier{tier}
		}

		if overrides, ok := v["overrides"].(map[string]interface{}); ok {
			mp.Overrides = make(map[string]Pricing)
			for alias, overrideData := range overrides {
				overrideMap := overrideData.(map[string]interface{})
				pricing := Pricing{}
				if in, ok := overrideMap["input"].(float64); ok {
					pricing.Input = in
				} else if in, ok := overrideMap["input"].(int); ok {
					pricing.Input = float64(in)
				}
				if out, ok := overrideMap["output"].(float64); ok {
					pricing.Output = out
				} else if out, ok := overrideMap["output"].(int); ok {
					pricing.Output = float64(out)
				}
				mp.Overrides[alias] = pricing
			}
		}
	default:
		return nil, fmt.Errorf("unsupported pricing format: %T", pricingData)
	}

	return mp, nil
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

// GetDefaultYAMLConfig returns a default configuration
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
		},
		Providers: map[string]ProviderConfig{
			"openai": {
				Enabled: true,
				Models:  make(map[string]ModelConfig),
			},
			"anthropic": {
				Enabled: true,
				Models:  make(map[string]ModelConfig),
			},
			"gemini": {
				Enabled: true,
				Models:  make(map[string]ModelConfig),
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

	// Log provider and model details
	for providerName, provider := range c.Providers {
		if !provider.Enabled {
			logger.Info("Provider disabled", "provider", strings.ToUpper(providerName))
			continue
		}

		logger.Info("Provider enabled", "provider", strings.ToUpper(providerName))

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

				// Log pricing information for enabled models
				if model.Enabled && model.Pricing != nil {
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
				} else if model.Enabled {
					logger.Info("Pricing not configured", "provider", providerName, "model", modelName)
				}
			}
		} else {
			logger.Info("No models specifically configured", "provider", providerName)
		}
	}
}
