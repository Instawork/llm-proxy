package config

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
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
	CostTracking     CostTrackingConfig     `yaml:"cost_tracking"`
	APIKeyManagement APIKeyManagementConfig `yaml:"api_key_management"`
	RateLimiting     RateLimitingConfig     `yaml:"rate_limiting"`
	CircuitBreaker   CircuitBreakerConfig   `yaml:"circuit_breaker"`
}

// CircuitBreakerConfig configures the proxy-side circuit breaker and retry
// policies for provider degradation detection.
type CircuitBreakerConfig struct {
	// Enabled gates the feature entirely.
	Enabled bool `yaml:"enabled"`

	// Mode selects the operational mode.  "log" (default) is observe-only:
	// the transport does one round trip, classifies the response, records
	// failures in the store for observability, emits counterfactual log
	// lines, and returns the real upstream response — no retries, no
	// fast-fail on open circuit, no synthetic 503s.  "enforce" runs the
	// full retry + fast-fail + synthetic-response behaviour.
	//
	// The safe default is "log" so that partially rolled-out or
	// misconfigured deployments never alter user-facing traffic.
	Mode string `yaml:"mode"`

	// Backend selects the state store: "memory" (default, single-process) or
	// "redis" (recommended for production with multiple proxy instances).
	Backend string `yaml:"backend"`

	// FailureThreshold is the number of terminal failures within WindowSeconds
	// that trips the circuit.  Default: 5.
	FailureThreshold int `yaml:"failure_threshold"`

	// WindowSeconds is the sliding-window TTL for failure counters.  Default: 120.
	WindowSeconds int `yaml:"window_seconds"`

	// CooldownSeconds is how long the circuit stays Open before probing.
	// Default: 300.
	CooldownSeconds int `yaml:"cooldown_seconds"`

	// MaxTransientRetries is the retry limit for degraded-class failures.
	// Default: 2.
	MaxTransientRetries int `yaml:"max_transient_retries"`

	// MaxRateLimitRetries is the retry limit for rate-limit failures.
	// Default: 2.
	MaxRateLimitRetries int `yaml:"max_rate_limit_retries"`

	// RetryContributionMode controls whether retried failures count toward the
	// circuit-breaker threshold.  Values: "off", "log" (default), "on".
	RetryContributionMode string `yaml:"retry_contribution_mode"`

	// GlobalRateLimitEscalationWindow is the number of seconds of sustained
	// global rate-limit failures that must elapse before escalating to
	// provider_degraded.  Default: 60.
	GlobalRateLimitEscalationWindow int `yaml:"global_rate_limit_escalation_window"`

	// Redis connection settings when Backend is "redis".
	Redis *RedisConfig `yaml:"redis,omitempty"`

	// TestModeEnabled allows the X-LLM-Proxy-Test-Mode header to be honoured.
	// Should be false in production.
	TestModeEnabled bool `yaml:"test_mode_enabled"`

	// DegradedSignal overrides the substring embedded in synthesised
	// degraded error bodies so downstream clients can detect proxy-
	// originated degradation.  Leave empty to use the default
	// (see circuit.DefaultDegradedSignal).  Change it only if your clients
	// already key off a different, project-specific tag.
	DegradedSignal string `yaml:"degraded_signal,omitempty"`
}

// CostTrackingConfig represents cost tracking feature configuration
type CostTrackingConfig struct {
	Enabled       bool              `yaml:"enabled"`
	Async         bool              `yaml:"async,omitempty"`          // Enable async tracking with workers (default: false - sync)
	Workers       int               `yaml:"workers,omitempty"`        // Number of worker goroutines for async tracking (default: 5)
	QueueSize     int               `yaml:"queue_size,omitempty"`     // Size of the async tracking queue (default: 1000)
	FlushInterval int               `yaml:"flush_interval,omitempty"` // Interval in seconds to flush pending records (default: 15)
	Transports    []TransportConfig `yaml:"transports,omitempty"`     // Multiple transport configs
}

// TransportConfig represents cost tracking transport configuration
type TransportConfig struct {
	Type     string                   `yaml:"type"` // "file", "dynamodb", or "datadog"
	File     *FileTransportConfig     `yaml:"file,omitempty"`
	DynamoDB *DynamoDBTransportConfig `yaml:"dynamodb,omitempty"`
	Datadog  *DatadogTransportConfig  `yaml:"datadog,omitempty"`
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

// DatadogTransportConfig represents Datadog transport configuration
type DatadogTransportConfig struct {
	Host       string   `yaml:"host"`        // DogStatsD host (default: localhost)
	Port       string   `yaml:"port"`        // DogStatsD port (default: 8125)
	Namespace  string   `yaml:"namespace"`   // Namespace to prefix metrics (default: "llm_proxy")
	Tags       []string `yaml:"tags"`        // Global tags to apply to all metrics
	SampleRate float64  `yaml:"sample_rate"` // Global sample rate (default: 1.0)
}

// APIKeyManagementConfig represents API key management configuration
type APIKeyManagementConfig struct {
	Enabled   bool   `yaml:"enabled"`
	TableName string `yaml:"table_name"`
	Region    string `yaml:"region"`
}

// RateLimitingConfig represents rate limiting feature configuration
type RateLimitingConfig struct {
	Enabled    bool               `yaml:"enabled"`
	Backend    string             `yaml:"backend"` // "memory" or "redis"
	Limits     LimitsConfig       `yaml:"limits"`
	Overrides  RateLimitOverrides `yaml:"overrides,omitempty"`
	Estimation EstimationConfig   `yaml:"estimation,omitempty"`
	Redis      *RedisConfig       `yaml:"redis,omitempty"`
}

// LimitsConfig contains the per-window limits. Zero or negative means unlimited.
type LimitsConfig struct {
	RequestsPerMinute int `yaml:"requests_per_minute"`
	TokensPerMinute   int `yaml:"tokens_per_minute"`
	RequestsPerDay    int `yaml:"requests_per_day"`
	TokensPerDay      int `yaml:"tokens_per_day"`
}

// RateLimitOverrides allow per-entity limit overrides
type RateLimitOverrides struct {
	PerKey   map[string]LimitsConfig `yaml:"per_key,omitempty"`
	PerUser  map[string]LimitsConfig `yaml:"per_user,omitempty"`
	PerModel map[string]LimitsConfig `yaml:"per_model,omitempty"`
}

// EstimationConfig controls request token estimation behavior
type EstimationConfig struct {
	MaxSampleBytes        int            `yaml:"max_sample_bytes"`
	BytesPerToken         int            `yaml:"bytes_per_token"`
	CharsPerToken         int            `yaml:"chars_per_token"`
	ProviderCharsPerToken map[string]int `yaml:"provider_chars_per_token,omitempty"`
}

// RedisConfig contains Redis backend settings
type RedisConfig struct {
	// URL is a full Redis connection string (e.g. `redis://:pw@host:6379/3`
	// or `rediss://...` for TLS).  Takes priority over Address/Password
	// when set.  YAML unmarshalling does not expand `${VAR}` / `$VAR`
	// tokens; callers may expand them later during wiring/initialization
	// so secrets (e.g. Finch's ML_CACHE_URL SSM parameter) can be passed
	// in via container environment without baking credentials into YAML.
	URL      string `yaml:"url"`
	Address  string `yaml:"address"`
	Password string `yaml:"password"`
	// DB pins which Redis database the circuit breaker uses.  When the
	// URL already encodes a DB (via `/N`) this field overrides it — set
	// this explicitly when sharing a Redis instance with another tenant
	// that owns a different DB.  DBSet tracks whether YAML explicitly
	// provided db, allowing an explicit db: 0 to override a URL DB.
	DB    int  `yaml:"db"`
	DBSet bool `yaml:"-"`
}

type redisConfigYAML struct {
	URL      string `yaml:"url,omitempty"`
	Address  string `yaml:"address,omitempty"`
	Password string `yaml:"password,omitempty"`
	DB       *int   `yaml:"db,omitempty"`
}

// UnmarshalYAML records whether db was explicitly present so db: 0 can be
// distinguished from an omitted DB value.
func (c *RedisConfig) UnmarshalYAML(value *yaml.Node) error {
	var raw redisConfigYAML
	if err := value.Decode(&raw); err != nil {
		return err
	}

	c.URL = raw.URL
	c.Address = raw.Address
	c.Password = raw.Password
	if raw.DB != nil {
		c.DB = *raw.DB
		c.DBSet = true
	} else {
		c.DB = 0
		c.DBSet = false
	}
	return nil
}

// MarshalYAML preserves the distinction between omitted db and explicit db: 0
// across config merge round-trips.
func (c RedisConfig) MarshalYAML() (interface{}, error) {
	out := redisConfigYAML{
		URL:      c.URL,
		Address:  c.Address,
		Password: c.Password,
	}
	if c.DBSet {
		db := c.DB
		out.DB = &db
	}
	return out, nil
}

// MarshalJSON redacts secret-bearing fields on RedisConfig so the full
// configuration can be safely dumped by the --version / /health paths
// (or any other JSON diagnostic output) without leaking credentials.
// Password is replaced with a fixed sentinel when present, and any
// userinfo in URL is stripped by re-marshalling the parsed URL.  The
// output schema is stable: every field the YAML form uses is still
// present so diagnostic tooling does not have to special-case
// redacted payloads.
//
// Note: this only affects JSON marshalling.  The YAML-tagged struct
// can still be serialised unchanged for round-trip writes (see
// SaveConfigurationToFile) — in that code path the config is written
// back to an operator-owned file where the original credentials must
// be preserved.
func (c RedisConfig) MarshalJSON() ([]byte, error) {
	const redacted = "***REDACTED***"
	// Use an alias type to avoid an infinite MarshalJSON recursion on
	// the embedded struct literal.  The alias has no methods.
	type redisConfigJSON struct {
		URL      string `json:"url,omitempty"`
		Address  string `json:"address,omitempty"`
		Password string `json:"password,omitempty"`
		DB       int    `json:"db"`
	}
	out := redisConfigJSON{
		URL:     redactedRedisURL(c.URL),
		Address: c.Address,
		DB:      c.DB,
	}
	if c.Password != "" {
		out.Password = redacted
	}
	return json.Marshal(out)
}

// redactedRedisURL returns u with any embedded userinfo (`user:pw@`)
// replaced by `***:***@`.  An unparseable URL is fully redacted to
// "***" since we cannot safely strip credentials we cannot locate.
func redactedRedisURL(u string) string {
	if u == "" {
		return ""
	}
	parsed, err := url.Parse(u)
	if err != nil || parsed == nil {
		return "***"
	}
	if parsed.User != nil {
		parsed.User = url.UserPassword("***", "***")
	}
	return parsed.String()
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
	Default   Pricing            `yaml:"default,omitempty"`
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

// LoadAndMergeConfigs loads multiple configuration files and merges them in order
func LoadAndMergeConfigs(filePaths []string) (*YAMLConfig, error) {
	if len(filePaths) == 0 {
		return nil, fmt.Errorf("no configuration files provided")
	}

	// Load the first config as the base
	baseConfig, err := LoadYAMLConfig(filePaths[0])
	if err != nil {
		return nil, fmt.Errorf("failed to load base config from %s: %w", filePaths[0], err)
	}

	// If only one file, return it
	if len(filePaths) == 1 {
		return baseConfig, nil
	}

	// Merge additional configs in order
	mergedConfig := baseConfig
	for i := 1; i < len(filePaths); i++ {
		// Load the overlay config without validation (like environment configs)
		overlayConfig, err := loadYAMLConfigWithoutValidation(filePaths[i])
		if err != nil {
			return nil, fmt.Errorf("failed to load overlay config from %s: %w", filePaths[i], err)
		}

		// Merge the overlay into the current merged config
		mergedConfig, err = mergeConfigs(mergedConfig, overlayConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to merge config from %s: %w", filePaths[i], err)
		}
	}

	return mergedConfig, nil
}

// SaveYAMLConfig saves configuration to a YAML file
func (c *YAMLConfig) SaveYAMLConfig(filename string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(filename, data, 0o644); err != nil {
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

	// Validate rate limiting configuration if enabled
	if c.Features.RateLimiting.Enabled {
		if err := c.validateRateLimitingConfig(); err != nil {
			return fmt.Errorf("invalid rate limiting configuration: %w", err)
		}
	}

	// Validate circuit breaker configuration if enabled
	if c.Features.CircuitBreaker.Enabled {
		if err := c.validateCircuitBreakerConfig(); err != nil {
			return fmt.Errorf("invalid CircuitBreakerConfig: %w", err)
		}
	}

	return nil
}

// validateTransportConfig validates the transport configuration
func (c *YAMLConfig) validateTransportConfig() error {
	transports := c.GetAllTransports()
	if len(transports) == 0 {
		return fmt.Errorf("at least one transport configuration is required")
	}

	for i, transport := range transports {
		if err := c.validateSingleTransport(transport); err != nil {
			return fmt.Errorf("transport %d: %w", i, err)
		}
	}

	return nil
}

// validateSingleTransport validates a single transport configuration
func (c *YAMLConfig) validateSingleTransport(transport TransportConfig) error {
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
	case "datadog":
		if transport.Datadog == nil {
			return fmt.Errorf("datadog transport configuration is required when type is 'datadog'")
		}
		// Host and Port have defaults, so no validation needed
	case "":
		return fmt.Errorf("transport type is required")
	default:
		return fmt.Errorf("unsupported transport type: %s (supported: file, dynamodb, datadog)", transport.Type)
	}

	return nil
}

// GetAllTransports returns all configured transports
func (c *YAMLConfig) GetAllTransports() []TransportConfig {
	if !c.Features.CostTracking.Enabled {
		return nil
	}

	return c.Features.CostTracking.Transports
}

// validateRateLimitingConfig validates the rate limiting configuration
func (c *YAMLConfig) validateRateLimitingConfig() error {
	rl := c.Features.RateLimiting
	switch rl.Backend {
	case "", "memory":
		// default to memory
	case "redis":
		if rl.Redis == nil || (rl.Redis.URL == "" && rl.Redis.Address == "") {
			return fmt.Errorf("redis backend selected but redis.url and redis.address are empty")
		}
	default:
		return fmt.Errorf("unsupported backend: %s (supported: memory, redis)", rl.Backend)
	}

	if rl.Estimation.BytesPerToken < 0 {
		return fmt.Errorf("estimation.bytes_per_token cannot be negative")
	}
	if rl.Estimation.CharsPerToken < 0 {
		return fmt.Errorf("estimation.chars_per_token cannot be negative")
	}
	if rl.Estimation.MaxSampleBytes < -1 {
		return fmt.Errorf("estimation.max_sample_bytes cannot be less than -1")
	}
	// Validate per-provider char overrides if present
	if rl.Estimation.ProviderCharsPerToken != nil {
		for prov, v := range rl.Estimation.ProviderCharsPerToken {
			if v < 0 {
				return fmt.Errorf("estimation.provider_chars_per_token[%s] cannot be negative", prov)
			}
		}
	}
	return nil
}

// validateCircuitBreakerConfig validates the circuit_breaker feature config
// early so --validate-config catches rollout typos before runtime.
func (c *YAMLConfig) validateCircuitBreakerConfig() error {
	cb := c.Features.CircuitBreaker

	switch cb.Mode {
	case "", "log", "enforce":
	default:
		return fmt.Errorf("mode must be one of log, enforce (got %q)", cb.Mode)
	}

	switch cb.Backend {
	case "", "memory":
		// default to memory
	case "redis":
		if cb.Redis == nil {
			return fmt.Errorf("backend redis requires redis configuration")
		}
		if cb.Redis.URL == "" && cb.Redis.Address == "" {
			return fmt.Errorf("backend redis requires redis.url or redis.address")
		}
	default:
		return fmt.Errorf("backend must be one of memory, redis (got %q)", cb.Backend)
	}

	if cb.FailureThreshold < 0 {
		return fmt.Errorf("failure_threshold cannot be negative")
	}
	if cb.FailureThreshold == 0 {
		return fmt.Errorf("failure_threshold must be greater than 0")
	}
	if cb.WindowSeconds < 0 {
		return fmt.Errorf("window_seconds cannot be negative")
	}
	if cb.WindowSeconds == 0 {
		return fmt.Errorf("window_seconds must be greater than 0")
	}
	if cb.CooldownSeconds < 0 {
		return fmt.Errorf("cooldown_seconds cannot be negative")
	}
	if cb.CooldownSeconds == 0 {
		return fmt.Errorf("cooldown_seconds must be greater than 0")
	}
	if cb.MaxTransientRetries < 0 {
		return fmt.Errorf("max_transient_retries cannot be negative")
	}
	if cb.MaxRateLimitRetries < 0 {
		return fmt.Errorf("max_rate_limit_retries cannot be negative")
	}
	if cb.GlobalRateLimitEscalationWindow < 0 {
		return fmt.Errorf("global_rate_limit_escalation_window cannot be negative")
	}

	switch cb.RetryContributionMode {
	case "", "off", "log", "on":
	default:
		return fmt.Errorf("retry_contribution_mode must be one of off, log, on (got %q)", cb.RetryContributionMode)
	}

	return nil
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
		// It's a simple price, has overrides, or already processed tiers.

		// Check if it has already-processed tiers (from config merging)
		if tiers, ok := v["tiers"].([]interface{}); ok {
			for _, tierData := range tiers {
				tierMap, ok := tierData.(map[string]interface{})
				if !ok {
					return nil, fmt.Errorf("invalid pricing tier format in tiers array")
				}
				tier := PricingTier{}
				if th, ok := tierMap["threshold"].(int); ok {
					tier.Threshold = th
				} else if th, ok := tierMap["threshold"].(float64); ok {
					tier.Threshold = int(th)
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
		} else if _, ok := v["input"]; ok {
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
			CircuitBreaker: CircuitBreakerConfig{
				Enabled:                         false,
				Mode:                            "log",
				Backend:                         "memory",
				FailureThreshold:                5,
				WindowSeconds:                   120,
				CooldownSeconds:                 300,
				MaxTransientRetries:             2,
				MaxRateLimitRetries:             2,
				RetryContributionMode:           "log",
				GlobalRateLimitEscalationWindow: 60,
				TestModeEnabled:                 false,
			},
			CostTracking: CostTrackingConfig{
				Enabled: true,
				Transports: []TransportConfig{
					{
						Type: "file",
						File: &FileTransportConfig{
							Path: "./cost_tracking.json",
						},
					},
				},
			},
			RateLimiting: RateLimitingConfig{
				Enabled: false,
				Backend: "memory",
				Limits: LimitsConfig{
					RequestsPerMinute: 0,
					TokensPerMinute:   0,
					RequestsPerDay:    0,
					TokensPerDay:      0,
				},
				Estimation: EstimationConfig{
					MaxSampleBytes: 20000,
					BytesPerToken:  4,
					CharsPerToken:  4,
					ProviderCharsPerToken: map[string]int{
						"openai":    5,
						"anthropic": 3,
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
