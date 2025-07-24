package config

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestYAMLConfigLoading(t *testing.T) {
	// Test loading the actual config.yml file
	configPath := "../../configs/config.yml"
	
	// Check if file exists (try different paths)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		configPath = "../configs/config.yml"
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			configPath = "configs/config.yml"
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				t.Skip("Config file not found, skipping test")
			}
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
	
	// Test that providers exist
	if len(config.Providers) == 0 {
		t.Error("Expected at least one provider")
	}
	
	// Test OpenAI provider exists
	openaiProvider, exists := config.Providers["openai"]
	if !exists {
		t.Fatal("OpenAI provider not found")
	}
	
	if !openaiProvider.Enabled {
		t.Error("Expected OpenAI provider to be enabled")
	}
}

func TestModelAliases(t *testing.T) {
	configPath := "../../configs/config.yml"
	
	// Check if file exists (try different paths)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		configPath = "../configs/config.yml"
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			configPath = "configs/config.yml"
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				t.Skip("Config file not found, skipping test")
			}
		}
	}
	
	config, err := LoadYAMLConfig(configPath)
	if err != nil {
		t.Fatalf("Failed to load YAML config: %v", err)
	}
	
	// Test that aliases are loaded correctly for OpenAI
	openaiProvider, exists := config.Providers["openai"]
	if !exists {
		t.Fatal("OpenAI provider not found")
	}
	
	// Check that models with aliases exist and have them loaded
	for modelName, model := range openaiProvider.Models {
		if len(model.Aliases) > 0 {
			t.Logf("Model %s has aliases: %v", modelName, model.Aliases)
			// Verify aliases are strings
			for _, alias := range model.Aliases {
				if alias == "" {
					t.Errorf("Empty alias found for model %s", modelName)
				}
			}
		}
	}
	
	// Test Anthropic aliases if provider exists
	if anthropicProvider, exists := config.Providers["anthropic"]; exists {
		for modelName, model := range anthropicProvider.Models {
			if len(model.Aliases) > 0 {
				t.Logf("Anthropic model %s has aliases: %v", modelName, model.Aliases)
			}
		}
	}
}

func TestBasicConfigValidation(t *testing.T) {
	configPath := "../../configs/config.yml"
	
	// Check if file exists (try different paths)
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		configPath = "../configs/config.yml"
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			configPath = "configs/config.yml"
			if _, err := os.Stat(configPath); os.IsNotExist(err) {
				t.Skip("Config file not found, skipping test")
			}
		}
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
	
	// Test that all enabled providers have models
	for providerName, provider := range config.Providers {
		if provider.Enabled {
			t.Logf("Provider %s is enabled with %d models", providerName, len(provider.Models))
			
			// Check that enabled models are properly configured
			for modelName, model := range provider.Models {
				if model.Enabled {
					t.Logf("Model %s for provider %s is enabled", modelName, providerName)
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
	// Test some real-world numbers that should be readable with underscores
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
enabled: true
features:
  cost_tracking:
    enabled: true
    transport:
      type: "file"
      file:
        path: "./test_cost_tracking.json"
providers:
  gemini:
    enabled: true
    models:
      "gemini-2.5-pro":
        enabled: true
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

func TestDefaultConfig(t *testing.T) {
	// Test that GetDefaultYAMLConfig returns a valid configuration
	config := GetDefaultYAMLConfig()
	
	if config == nil {
		t.Fatal("Default config is nil")
	}
	
	if !config.Enabled {
		t.Error("Expected default config to be enabled")
	}
	
	// Test that default providers exist
	expectedProviders := []string{"openai", "anthropic", "gemini"}
	for _, providerName := range expectedProviders {
		provider, exists := config.Providers[providerName]
		if !exists {
			t.Errorf("Expected provider %s not found in default config", providerName)
		} else if !provider.Enabled {
			t.Errorf("Expected provider %s to be enabled in default config", providerName)
		}
	}
	
	// Test cost tracking is enabled by default
	if !config.Features.CostTracking.Enabled {
		t.Error("Expected cost tracking to be enabled in default config")
	}
} 
