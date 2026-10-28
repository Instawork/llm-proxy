package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseModelPricing_CachePrices(t *testing.T) {
	t.Run("flat map", func(t *testing.T) {
		mp, err := parseModelPricing(map[string]any{
			"input": 4.0, "output": 20, "cache_read": 0.2, "cache_write": 5,
		})
		require.NoError(t, err)
		require.Len(t, mp.Tiers, 1)
		assert.Equal(t, 0.2, mp.Tiers[0].CacheRead)
		assert.Equal(t, 5.0, mp.Tiers[0].CacheWrite)
	})

	t.Run("tier list", func(t *testing.T) {
		mp, err := parseModelPricing([]any{
			map[string]any{"threshold": 100_000, "input": 0.1, "output": 0.5, "cache_read": 0.01, "cache_write": 0.125},
			map[string]any{"threshold": 0, "input": 0.5, "output": 2.5, "cache_read": 0.05, "cache_write": 0.625},
		})
		require.NoError(t, err)
		require.Len(t, mp.Tiers, 2)
		assert.Equal(t, 0.01, mp.Tiers[0].CacheRead)
		assert.Equal(t, 0.625, mp.Tiers[1].CacheWrite)
	})

	t.Run("overrides", func(t *testing.T) {
		mp, err := parseModelPricing(map[string]any{
			"input": 3.3, "output": 16.5, "cache_read": 0.33, "cache_write": 4.125,
			"overrides": map[string]any{
				"global.m": map[string]any{"input": 3.0, "output": 15, "cache_read": 0.3, "cache_write": 3.75},
			},
		})
		require.NoError(t, err)
		got := mp.Overrides["global.m"]
		assert.Equal(t, Pricing{Input: 3, Output: 15, CacheRead: 0.3, CacheWrite: 3.75}, got)
	})

	t.Run("omitted", func(t *testing.T) {
		mp, err := parseModelPricing(map[string]any{"input": 1.0, "output": 2.0})
		require.NoError(t, err)
		assert.Zero(t, mp.Tiers[0].CacheRead)
		assert.Zero(t, mp.Tiers[0].CacheWrite)
	})
}

func TestParseModelPricing_RejectsNonNumericPrices(t *testing.T) {
	cases := map[string]any{
		"flat cache_read string":  map[string]any{"input": 3.0, "output": 15, "cache_read": "0.3"},
		"flat input string":       map[string]any{"input": "3", "output": 15},
		"tier list output bool":   []any{map[string]any{"threshold": 0, "input": 1.0, "output": true}},
		"tiers map cache_write":   map[string]any{"tiers": []any{map[string]any{"threshold": 0, "input": 1.0, "output": 2.0, "cache_write": "3.75"}}},
		"tier threshold string":   []any{map[string]any{"threshold": "200000", "input": 1.0, "output": 2.0}},
		"override cache_read map": map[string]any{"input": 3.0, "output": 15, "overrides": map[string]any{"global.m": map[string]any{"input": 3.0, "output": 15, "cache_read": map[string]any{}}}},
	}
	for name, pricing := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := parseModelPricing(pricing)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "must be a number")
		})
	}
}

func TestParsePricing_NonNumericPriceFailsConfigLoad(t *testing.T) {
	cfg := &YAMLConfig{Providers: map[string]ProviderConfig{
		"anthropic": {Models: map[string]ModelConfig{
			"claude-x": {Pricing: map[string]any{"input": 3.0, "output": 15, "cache_read": "0.3", "cache_write": 3.75}},
		}},
	}}
	err := cfg.ParsePricing()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "anthropic/claude-x")
	assert.Contains(t, err.Error(), "cache_read must be a number, got string")
}

func TestParseModelPricing_AcceptsIntegerAndFloatPrices(t *testing.T) {
	mp, err := parseModelPricing([]any{
		map[string]any{"threshold": 200000.0, "input": 2, "output": 12.0, "cache_read": int64(1), "cache_write": 2.5},
		map[string]any{"threshold": 0, "input": 4.0, "output": 18},
	})
	require.NoError(t, err)
	require.Len(t, mp.Tiers, 2)
	assert.Equal(t, PricingTier{Threshold: 200000, Input: 2, Output: 12, CacheRead: 1, CacheWrite: 2.5}, mp.Tiers[0])
	assert.Equal(t, PricingTier{Threshold: 0, Input: 4, Output: 18}, mp.Tiers[1])
}

func TestBaseConfig_ClaudeEntriesHaveCachePrices(t *testing.T) {
	cfg, err := LoadAndMergeConfigs([]string{"../../configs/base.yml"})
	require.NoError(t, err)

	checked := 0
	for _, provider := range []string{"anthropic", "bedrock", "bedrock-mantle"} {
		for name, model := range cfg.Providers[provider].Models {
			if !strings.Contains(name, "claude") {
				continue
			}
			mp, ok := model.Pricing.(*ModelPricing)
			require.True(t, ok, "%s/%s pricing not parsed", provider, name)
			for i, tier := range mp.Tiers {
				checked++
				assert.Positive(t, tier.CacheRead, "%s/%s tier[%d] cache_read", provider, name, i)
				assert.Positive(t, tier.CacheWrite, "%s/%s tier[%d] cache_write", provider, name, i)
			}
		}
	}
	assert.NotZero(t, checked)
}
