package cost

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFuzzyMatching(t *testing.T) {
	// Create a cost tracker with some test pricing data
	ct := NewCostTracker()

	// Add some test pricing data
	ct.SetPricingForModel("openai", "gpt-4", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 0.03, Output: 0.06},
		},
	})
	ct.SetPricingForModel("openai", "gpt-3.5-turbo", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 0.0015, Output: 0.002},
		},
	})
	ct.SetPricingForModel("anthropic", "claude-3-opus", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 0.015, Output: 0.075},
		},
	})

	tests := []struct {
		name           string
		provider       string
		model          string
		expectedMatch  string
		shouldEstimate bool
		expectError    bool
	}{
		{
			name:           "exact match should not be estimate",
			provider:       "openai",
			model:          "gpt-4",
			expectedMatch:  "gpt-4",
			shouldEstimate: false,
			expectError:    false,
		},
		{
			name:           "close match should be estimate",
			provider:       "openai",
			model:          "gpt4", // Close to gpt-4
			expectedMatch:  "gpt-4",
			shouldEstimate: true,
			expectError:    false,
		},
		{
			name:           "another close match",
			provider:       "openai",
			model:          "gpt-3.5-turbo-16k", // Close to gpt-3.5-turbo
			expectedMatch:  "gpt-3.5-turbo",
			shouldEstimate: true,
			expectError:    false,
		},
		{
			name:           "very different model should fail",
			provider:       "openai",
			model:          "completely-different-model",
			expectedMatch:  "",
			shouldEstimate: false,
			expectError:    true,
		},
		{
			name:           "non-existent provider should fail",
			provider:       "nonexistent",
			model:          "gpt-4",
			expectedMatch:  "",
			shouldEstimate: false,
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pricing, matchedModel, isEstimate, err := ct.GetPricingForModelWithFuzzyMatch(tt.provider, tt.model, 1000)

			if tt.expectError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expectedMatch, matchedModel)
			assert.Equal(t, tt.shouldEstimate, isEstimate)
			assert.NotNil(t, pricing)
		})
	}
}

func TestFuzzyMatchingThreshold(t *testing.T) {
	ct := NewCostTracker()

	// Add test pricing data
	ct.SetPricingForModel("test", "exact-match", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 0.01, Output: 0.02},
		},
	})

	// Test that very different strings don't match
	_, _, _, err := ct.GetPricingForModelWithFuzzyMatch("test", "completely-different", 1000)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no close match found")
}

func TestCalculateCostWithFuzzyMatch(t *testing.T) {
	ct := NewCostTracker()

	// Add test pricing data
	ct.SetPricingForModel("openai", "gpt-4", &ModelPricing{
		Tiers: []PricingTier{
			{Threshold: 0, Input: 0.03, Output: 0.06},
		},
	})

	// Test exact match
	inputCost, outputCost, totalCost, matchedModel, isEstimate, err := ct.CalculateCostWithFuzzyMatch("openai", "gpt-4", 1000, 500)
	assert.NoError(t, err)
	assert.Equal(t, "gpt-4", matchedModel)
	assert.False(t, isEstimate)
	assert.Greater(t, totalCost, 0.0)

	// Test fuzzy match
	inputCost2, outputCost2, totalCost2, matchedModel2, isEstimate2, err := ct.CalculateCostWithFuzzyMatch("openai", "gpt4", 1000, 500)
	assert.NoError(t, err)
	assert.Equal(t, "gpt-4", matchedModel2)
	assert.True(t, isEstimate2)
	assert.Greater(t, totalCost2, 0.0)

	// The fuzzy match should produce the same costs as the exact match since it's using the same pricing
	assert.Equal(t, inputCost, inputCost2)
	assert.Equal(t, outputCost, outputCost2)
	assert.Equal(t, totalCost, totalCost2)
}
