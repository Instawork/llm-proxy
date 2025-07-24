package config

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestYAMLConfigWithUnderscores(t *testing.T) {
	// Test loading the actual config.yml file
	configPath := "../config.yml"
	
	// Check if file exists (adjust path if needed)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// Try alternative path
		configPath = "config.yml"
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			t.Fatalf("Config file not found. Tried paths: ../config.yml and config.yml")
		}
	}
	
	// Load the YAML configuration
	config, err := LoadYAMLConfig(configPath)
	if err != nil {
		t.Fatalf("Failed to load YAML config: %v", err)
	}
	
	// Test that configuration is loaded
	if config == nil {
		t.Fatal("Config is nil")
	}
	
	// Test that config is enabled
	if !config.Enabled {
		t.Error("Expected config to be enabled")
	}
	
	// Test OpenAI provider default limits with underscores
	openaiProvider, exists := config.Providers["openai"]
	if !exists {
		t.Fatal("OpenAI provider not found")
	}
	
	// Test that numeric values with underscores are parsed correctly
	testCases := []struct {
		name     string
		actual   int64
		expected int64
	}{
		{"OpenAI TPM", openaiProvider.DefaultLimits.TokensPerMinute, 450_000},
		{"OpenAI RPM", openaiProvider.DefaultLimits.RequestsPerMinute, 5_000},
		{"OpenAI TPH", openaiProvider.DefaultLimits.TokensPerHour, 27_000_000},
		{"OpenAI TPD", openaiProvider.DefaultLimits.TokensPerDay, 648_000_000},
		{"OpenAI RPD", openaiProvider.DefaultLimits.RequestsPerDay, 300_000},
		{"OpenAI Burst Tokens", openaiProvider.DefaultLimits.BurstTokens, 45_000},
	}
	
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.actual != tc.expected {
				t.Errorf("Expected %s to be %d, got %d", tc.name, tc.expected, tc.actual)
			}
		})
	}
	
	// Test specific model limits with underscores
	gpt41Model, exists := openaiProvider.Models["gpt-4.1"]
	if !exists {
		t.Fatal("GPT-4.1 model not found")
	}
	
	modelTestCases := []struct {
		name     string
		actual   int64
		expected int64
	}{
		{"GPT-4.1 TPM", gpt41Model.Limits.TokensPerMinute, 450_000},
		{"GPT-4.1 RPM", gpt41Model.Limits.RequestsPerMinute, 5_000},
		{"GPT-4.1 TPD", gpt41Model.Limits.TokensPerDay, 10_800_000},
		{"GPT-4.1 RPD", gpt41Model.Limits.RequestsPerDay, 7_200_000},
		{"GPT-4.1 Burst Tokens", gpt41Model.Limits.BurstTokens, 45_000},
	}
	
	for _, tc := range modelTestCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.actual != tc.expected {
				t.Errorf("Expected %s to be %d, got %d", tc.name, tc.expected, tc.actual)
			}
		})
	}
	
	// Test Anthropic provider with underscores
	anthropicProvider, exists := config.Providers["anthropic"]
	if !exists {
		t.Fatal("Anthropic provider not found")
	}
	
	anthropicTestCases := []struct {
		name     string
		actual   int64
		expected int64
	}{
		{"Anthropic TPM", anthropicProvider.DefaultLimits.TokensPerMinute, 80_000},
		{"Anthropic RPM", anthropicProvider.DefaultLimits.RequestsPerMinute, 1_000},
		{"Anthropic TPH", anthropicProvider.DefaultLimits.TokensPerHour, 4_800_000},
		{"Anthropic TPD", anthropicProvider.DefaultLimits.TokensPerDay, 115_200_000},
		{"Anthropic RPD", anthropicProvider.DefaultLimits.RequestsPerDay, 1_440_000},
		{"Anthropic Burst Tokens", anthropicProvider.DefaultLimits.BurstTokens, 8_000},
	}
	
	for _, tc := range anthropicTestCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.actual != tc.expected {
				t.Errorf("Expected %s to be %d, got %d", tc.name, tc.expected, tc.actual)
			}
		})
	}
	
	// Test Gemini provider with very large numbers
	geminiProvider, exists := config.Providers["gemini"]
	if !exists {
		t.Fatal("Gemini provider not found")
	}
	
	geminiTestCases := []struct {
		name     string
		actual   int64
		expected int64
	}{
		{"Gemini TPM", geminiProvider.DefaultLimits.TokensPerMinute, 5_000_000},
		{"Gemini TPH", geminiProvider.DefaultLimits.TokensPerHour, 300_000_000},
		{"Gemini TPD", geminiProvider.DefaultLimits.TokensPerDay, 7_200_000_000},
		{"Gemini Burst Tokens", geminiProvider.DefaultLimits.BurstTokens, 500_000},
	}
	
	for _, tc := range geminiTestCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.actual != tc.expected {
				t.Errorf("Expected %s to be %d, got %d", tc.name, tc.expected, tc.actual)
			}
		})
	}
	
	// Test default limits with underscores
	defaultTestCases := []struct {
		name     string
		actual   int64
		expected int64
	}{
		{"User TPM", config.Defaults.User.TokensPerMinute, 100_000},
		{"User TPH", config.Defaults.User.TokensPerHour, 6_000_000},
		{"User TPD", config.Defaults.User.TokensPerDay, 144_000_000},
		{"User RPM", config.Defaults.User.RequestsPerMinute, 1_000},
		{"User RPH", config.Defaults.User.RequestsPerHour, 60_000},
		{"User RPD", config.Defaults.User.RequestsPerDay, 1_440_000},
		{"IP TPM", config.Defaults.IP.TokensPerMinute, 0},
		{"IP TPH", config.Defaults.IP.TokensPerHour, 0},
		{"IP RPH", config.Defaults.IP.RequestsPerHour, 0},
		{"Global TPM", config.Defaults.Global.TokensPerMinute, 10_000_000},
		{"Global TPH", config.Defaults.Global.TokensPerHour, 600_000_000},
		{"Global RPM", config.Defaults.Global.RequestsPerMinute, 50_000},
		{"Global RPH", config.Defaults.Global.RequestsPerHour, 3_000_000},
	}
	
	for _, tc := range defaultTestCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.actual != tc.expected {
				t.Errorf("Expected %s to be %d, got %d", tc.name, tc.expected, tc.actual)
			}
		})
	}
}

func TestModelAliases(t *testing.T) {
	configPath := "../config.yml"
	
	// Check if file exists (adjust path if needed)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		configPath = "config.yml"
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			t.Fatalf("Config file not found")
		}
	}
	
	config, err := LoadYAMLConfig(configPath)
	if err != nil {
		t.Fatalf("Failed to load YAML config: %v", err)
	}
	
	// Test that aliases are loaded correctly
	openaiProvider, exists := config.Providers["openai"]
	if !exists {
		t.Fatal("OpenAI provider not found")
	}
	
	// Test GPT-4.1 aliases
	gpt41Model, exists := openaiProvider.Models["gpt-4.1"]
	if !exists {
		t.Fatal("GPT-4.1 model not found")
	}
	
	expectedAliases := []string{"gpt-4.1-2025-04-14"}
	if len(gpt41Model.Aliases) != len(expectedAliases) {
		t.Errorf("Expected %d aliases, got %d", len(expectedAliases), len(gpt41Model.Aliases))
	}
	
	// Check each alias exists
	for _, expectedAlias := range expectedAliases {
		found := false
		for _, actualAlias := range gpt41Model.Aliases {
			if actualAlias == expectedAlias {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Expected alias '%s' not found", expectedAlias)
		}
	}
	
	// Test Anthropic aliases
	anthropicProvider, exists := config.Providers["anthropic"]
	if !exists {
		t.Fatal("Anthropic provider not found")
	}
	
	claudeModel, exists := anthropicProvider.Models["claude-3-5-sonnet"]
	if !exists {
		t.Fatal("Claude 3.5 Sonnet model not found")
	}
	
	expectedClaudeAliases := []string{
		"claude-3-5-sonnet-latest",
		"claude-3.5-sonnet",
		"claude-sonnet-3.5",
		"claude-3-5-sonnet-20241022",
		"claude-3-5-sonnet-20240628",
	}
	
	if len(claudeModel.Aliases) != len(expectedClaudeAliases) {
		t.Errorf("Expected %d Claude aliases, got %d", len(expectedClaudeAliases), len(claudeModel.Aliases))
	}
}

func TestConfigValidation(t *testing.T) {
	configPath := "../config.yml"
	
	// Check if file exists (adjust path if needed)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		configPath = "config.yml"
	}
	
	config, err := LoadYAMLConfig(configPath)
	if err != nil {
		t.Fatalf("Failed to load YAML config: %v", err)
	}
	
	// Test configuration validation
	err = config.Validate()
	if err != nil {
		t.Errorf("Configuration validation failed: %v", err)
	}
	
	// Test that all enabled providers have valid configurations
	for providerName, provider := range config.Providers {
		if provider.Enabled {
			// Check that provider has some default limits
			if provider.DefaultLimits.TokensPerMinute <= 0 && provider.DefaultLimits.RequestsPerMinute <= 0 {
				t.Errorf("Provider %s has no default limits", providerName)
			}
			
			// Check that enabled models have valid limits
			for modelName, model := range provider.Models {
				if model.Enabled {
					if model.Limits.TokensPerMinute <= 0 && model.Limits.RequestsPerMinute <= 0 {
						t.Errorf("Model %s for provider %s has no rate limits", modelName, providerName)
					}
				}
			}
		}
	}
}

func TestUnderscoreNumberParsing(t *testing.T) {
	// Create a temporary YAML file with underscored numbers to test parsing
	testYAML := `
enabled: true
test_values:
  small_number: 1_000
  medium_number: 100_000
  large_number: 10_000_000
  very_large_number: 7_200_000_000
  decimal_number: 2_500.50
`
	
	// Create a temporary file
	tmpFile, err := os.CreateTemp("", "test_config_*.yml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())
	
	// Write test YAML content
	if _, err := tmpFile.Write([]byte(testYAML)); err != nil {
		t.Fatalf("Failed to write to temp file: %v", err)
	}
	tmpFile.Close()
	
	// Define a struct to unmarshal the test YAML
	type TestConfig struct {
		Enabled    bool `yaml:"enabled"`
		TestValues struct {
			SmallNumber     int64   `yaml:"small_number"`
			MediumNumber    int64   `yaml:"medium_number"`
			LargeNumber     int64   `yaml:"large_number"`
			VeryLargeNumber int64   `yaml:"very_large_number"`
			DecimalNumber   float64 `yaml:"decimal_number"`
		} `yaml:"test_values"`
	}
	
	// Read and parse the file
	data, err := os.ReadFile(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to read temp file: %v", err)
	}
	
	var testConfig TestConfig
	if err := yaml.Unmarshal(data, &testConfig); err != nil {
		t.Fatalf("Failed to unmarshal YAML: %v", err)
	}
	
	// Test that underscored numbers are parsed correctly
	testCases := []struct {
		name     string
		actual   int64
		expected int64
	}{
		{"Small Number", testConfig.TestValues.SmallNumber, 1_000},
		{"Medium Number", testConfig.TestValues.MediumNumber, 100_000},
		{"Large Number", testConfig.TestValues.LargeNumber, 10_000_000},
		{"Very Large Number", testConfig.TestValues.VeryLargeNumber, 7_200_000_000},
	}
	
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.actual != tc.expected {
				t.Errorf("Expected %s to be %d, got %d", tc.name, tc.expected, tc.actual)
			}
		})
	}
	
	// Test decimal number with underscores
	expectedDecimal := 2500.50
	if testConfig.TestValues.DecimalNumber != expectedDecimal {
		t.Errorf("Expected decimal number to be %f, got %f", expectedDecimal, testConfig.TestValues.DecimalNumber)
	}
	
	// Test that config is enabled
	if !testConfig.Enabled {
		t.Error("Expected config to be enabled")
	}
}

func TestRealWorldNumbers(t *testing.T) {
	// Test some real-world rate limiting numbers that should be readable with underscores
	examples := []struct {
		description string
		yamlValue   string
		expected    int64
	}{
		{"OpenAI TPM Tier 2", "450_000", 450000},
		{"Anthropic ITPM Tier 2", "80_000", 80000},
		{"Gemini TPM Tier 2", "5_000_000", 5000000},
		{"Daily requests", "1_440_000", 1440000},
		{"Very large limit", "7_200_000_000", 7200000000},
	}
	
	for _, example := range examples {
		t.Run(example.description, func(t *testing.T) {
			// Create a simple YAML to test this specific number
			testYAML := "test_value: " + example.yamlValue
			
			var result struct {
				TestValue int64 `yaml:"test_value"`
			}
			
			if err := yaml.Unmarshal([]byte(testYAML), &result); err != nil {
				t.Fatalf("Failed to parse YAML for %s: %v", example.description, err)
			}
			
			if result.TestValue != example.expected {
				t.Errorf("For %s, expected %d, got %d", example.description, example.expected, result.TestValue)
			}
		})
	}
} 

func TestGetModelPricing(t *testing.T) {
	// Create a temporary YAML file with tiered and alias pricing
	testYAML := `
providers:
  gemini:
    enabled: true
    models:
      "gemini-2.5-pro":
        enabled: true
        limits:
            tokens_per_minute: 1000
        pricing:
          - threshold: 200000
            input: 1.25
            output: 10.00
          - threshold: 0
            input: 2.50
            output: 15.00
  openai:
    enabled: true
    models:
      "gpt-4o":
        enabled: true
        aliases: ["gpt-4o-alias"]
        limits:
            tokens_per_minute: 1000
        pricing:
          input: 2.50
          output: 10.00
          overrides:
            "gpt-4o-alias":
              input: 5.00
              output: 15.00
`
	tmpFile, err := os.CreateTemp("", "test_pricing_*.yml")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write([]byte(testYAML)); err != nil {
		t.Fatalf("Failed to write to temp file: %v", err)
	}
	tmpFile.Close()

	config, err := LoadYAMLConfig(tmpFile.Name())
	if err != nil {
		t.Fatalf("Failed to load YAML config: %v", err)
	}

	// Test Tiered Pricing
	t.Run("TieredPricing", func(t *testing.T) {
		// Test case 1: Below threshold
		pricing, err := config.GetModelPricing("gemini", "gemini-2.5-pro", 100000)
		if err != nil {
			t.Fatalf("GetModelPricing failed: %v", err)
		}
		if pricing.Input != 1.25 || pricing.Output != 10.00 {
			t.Errorf("Expected pricing for 100k tokens to be 1.25/10.00, got %.2f/%.2f", pricing.Input, pricing.Output)
		}

		// Test case 2: Above threshold
		pricing, err = config.GetModelPricing("gemini", "gemini-2.5-pro", 300000)
		if err != nil {
			t.Fatalf("GetModelPricing failed: %v", err)
		}
		if pricing.Input != 2.50 || pricing.Output != 15.00 {
			t.Errorf("Expected pricing for 300k tokens to be 2.50/15.00, got %.2f/%.2f", pricing.Input, pricing.Output)
		}
	})

	// Test Alias Pricing
	t.Run("AliasPricing", func(t *testing.T) {
		// Test case 1: Alias with override
		pricing, err := config.GetModelPricing("openai", "gpt-4o-alias", 0)
		if err != nil {
			t.Fatalf("GetModelPricing failed: %v", err)
		}
		if pricing.Input != 5.00 || pricing.Output != 15.00 {
			t.Errorf("Expected pricing for alias to be 5.00/15.00, got %.2f/%.2f", pricing.Input, pricing.Output)
		}

		// Test case 2: Canonical model
		pricing, err = config.GetModelPricing("openai", "gpt-4o", 0)
		if err != nil {
			t.Fatalf("GetModelPricing failed: %v", err)
		}
		if pricing.Input != 2.50 || pricing.Output != 10.00 {
			t.Errorf("Expected pricing for canonical model to be 2.50/10.00, got %.2f/%.2f", pricing.Input, pricing.Output)
		}
	})
} 
