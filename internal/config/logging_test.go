package config

import (
	"bytes"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLogConfiguration_Enabled(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	cfg := GetDefaultYAMLConfig()
	cfg.LogConfiguration(logger)
	assert.Contains(t, buf.String(), "Configuration Summary")
}

func TestLogConfiguration_Disabled(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	cfg := GetDefaultYAMLConfig()
	cfg.Enabled = false
	cfg.LogConfiguration(logger)
	assert.Contains(t, buf.String(), "Configuration is disabled")
}

func TestLogConfiguration_DisabledProviderAndModelStatuses(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	cfg := &YAMLConfig{
		Enabled: true,
		Providers: map[string]ProviderConfig{
			"disabled-prov": {Enabled: false},
			"openai": {
				Enabled: true,
				Models: map[string]ModelConfig{
					"gpt-4o": {
						Enabled: true,
						Aliases: []string{"g4", "gpt4"},
						Pricing: &ModelPricing{
							Tiers:     []PricingTier{{Threshold: 0, Input: 1, Output: 2}},
							Overrides: map[string]Pricing{"g4": {Input: 0.5, Output: 1.0}},
						},
					},
					"tiered": {
						Enabled: true,
						Pricing: &ModelPricing{
							Tiers: []PricingTier{
								{Threshold: 1000, Input: 1, Output: 2},
								{Threshold: 0, Input: 10, Output: 20},
							},
						},
					},
					"unparseable": {
						Enabled: true,
						Pricing: 12345,
					},
					"no-pricing-enabled":  {Enabled: true},
					"no-pricing-disabled": {Enabled: false},
				},
			},
			"empty-models": {Enabled: true},
		},
	}

	cfg.LogConfiguration(logger)
	out := buf.String()
	assert.Contains(t, out, "Provider disabled")
	assert.Contains(t, out, "Provider enabled")
	assert.Contains(t, out, "Model status")
	assert.Contains(t, out, "Tiered pricing")
	assert.Contains(t, out, "Pricing override")
	assert.Contains(t, out, "Could not parse pricing info")
	assert.Contains(t, out, "Pricing not configured")
	assert.Contains(t, out, "No models specifically configured")
}
