package config

import (
	"math"
	"os"
	"path/filepath"
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

func TestParseModelPricing_RejectsNullNonFiniteAndNegativePrices(t *testing.T) {
	cases := []struct {
		name    string
		pricing any
		want    string
	}{
		{"flat input null", map[string]any{"input": nil, "output": 2.0}, "input must be a number, got null"},
		{"flat cache_read null", map[string]any{"input": 1.0, "output": 2.0, "cache_read": nil}, "cache_read must be a number, got null"},
		{"flat input nan", map[string]any{"input": math.NaN(), "output": 2.0}, "input must be a finite number"},
		{"flat output +inf", map[string]any{"input": 1.0, "output": math.Inf(1)}, "output must be a finite number"},
		{"tier cache_write -inf", []any{map[string]any{"threshold": 0, "input": 1.0, "output": 2.0, "cache_write": math.Inf(-1)}}, "cache_write must be a finite number"},
		{"flat input negative", map[string]any{"input": -1.0, "output": 2.0}, "input must not be negative"},
		{"tier threshold negative", []any{map[string]any{"threshold": -5, "input": 1.0, "output": 2.0}}, "threshold must not be negative"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parseModelPricing(tc.pricing)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestLoadAndMergeConfigs_RejectsInvalidYAMLPrices(t *testing.T) {
	cases := []struct {
		name    string
		pricing string
		want    string
	}{
		{"quoted string", `{input: "3", output: 15}`, "input must be a number, got string"},
		{"present null", `{input: null, output: 15}`, "input must be a number, got null"},
		{"yaml nan", `{input: .nan, output: 15}`, "input must be a finite number"},
		{"yaml inf", `{input: 3, output: .inf}`, "output must be a finite number"},
		{"negative", `{input: 3, output: -15}`, "output must not be negative"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cfg.yml")
			body := "providers:\n  anthropic:\n    enabled: true\n    models:\n      claude-x:\n        pricing: " + tc.pricing + "\n"
			require.NoError(t, os.WriteFile(path, []byte(body), 0o600))

			_, err := LoadAndMergeConfigs([]string{path})
			require.Error(t, err)
			assert.Contains(t, err.Error(), "anthropic/claude-x")
			assert.Contains(t, err.Error(), tc.want)
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

func TestBaseConfig_OpenAIAndBedrockGPTEntriesHaveCachePrices(t *testing.T) {
	cfg, err := LoadAndMergeConfigs([]string{"../../configs/base.yml"})
	require.NoError(t, err)

	// Native OpenAI models backfilled in base.yml.
	t.Run("native openai backfilled models", func(t *testing.T) {
		mp, ok := cfg.Providers["openai"].Models["gpt-6.1-terra"].Pricing.(*ModelPricing)
		require.True(t, ok)
		require.Len(t, mp.Tiers, 2)
		assert.Equal(t, PricingTier{Threshold: 272_000, Input: 1.00, Output: 5.00, CacheRead: 0.05, CacheWrite: 1.25}, mp.Tiers[0])
		assert.Equal(t, PricingTier{Threshold: 0, Input: 2.00, Output: 7.50, CacheRead: 0.10, CacheWrite: 2.50}, mp.Tiers[1])

		cyber, ok := cfg.Providers["openai"].Models["gpt-5.5-cyber"].Pricing.(*ModelPricing)
		require.True(t, ok)
		require.Len(t, cyber.Tiers, 1)
		assert.Equal(t, PricingTier{Threshold: 0, Input: 12.50, Output: 75.00, CacheRead: 1.25}, cyber.Tiers[0])

		search, ok := cfg.Providers["openai"].Models["gpt-5-search-api"].Pricing.(*ModelPricing)
		require.True(t, ok)
		require.Len(t, search.Tiers, 1)
		assert.Equal(t, PricingTier{Threshold: 0, Input: 1.25, Output: 10.00, CacheRead: 0.125}, search.Tiers[0])
	})

	// Bedrock openai.gpt-* models: every tier has cache_read and cache_write,
	// and Geo (us.*) is exactly 1.1x Global (global.*).
	t.Run("bedrock openai.gpt-* models", func(t *testing.T) {
		checked := 0
		models := cfg.Providers["bedrock"].Models
		for name, model := range models {
			if !strings.Contains(name, "openai.gpt-") {
				continue
			}
			mp, ok := model.Pricing.(*ModelPricing)
			require.True(t, ok, "bedrock/%s pricing not parsed", name)
			for i, tier := range mp.Tiers {
				checked++
				assert.Positive(t, tier.CacheRead, "bedrock/%s tier[%d] cache_read", name, i)
				assert.Positive(t, tier.CacheWrite, "bedrock/%s tier[%d] cache_write", name, i)
			}
			if suffix, isUS := strings.CutPrefix(name, "us."); isUS {
				globalModel, exists := models["global."+suffix]
				require.True(t, exists, "missing global pair for bedrock/%s", name)
				globalMP, ok := globalModel.Pricing.(*ModelPricing)
				require.True(t, ok)
				require.Len(t, mp.Tiers, len(globalMP.Tiers))
				for i := range mp.Tiers {
					assert.InDelta(t, globalMP.Tiers[i].Input*1.1, mp.Tiers[i].Input, 1e-9, "%s tier[%d] input", name, i)
					assert.InDelta(t, globalMP.Tiers[i].Output*1.1, mp.Tiers[i].Output, 1e-9, "%s tier[%d] output", name, i)
					assert.InDelta(t, globalMP.Tiers[i].CacheRead*1.1, mp.Tiers[i].CacheRead, 1e-9, "%s tier[%d] cache_read", name, i)
					assert.InDelta(t, globalMP.Tiers[i].CacheWrite*1.1, mp.Tiers[i].CacheWrite, 1e-9, "%s tier[%d] cache_write", name, i)
				}
			}
		}
		assert.NotZero(t, checked)
	})

	// Bedrock Mantle openai.gpt-* models: every tier has cache_read; all except
	// gpt-5.4 and gpt-5.5 (no published write price on AWS cards) also have cache_write.
	t.Run("bedrock-mantle openai.gpt-* models", func(t *testing.T) {
		checked := 0
		noWrite := map[string]bool{"openai.gpt-5.4": true, "openai.gpt-5.5": true}
		for name, model := range cfg.Providers["bedrock-mantle"].Models {
			if !strings.HasPrefix(name, "openai.gpt-") {
				continue
			}
			mp, ok := model.Pricing.(*ModelPricing)
			require.True(t, ok, "bedrock-mantle/%s pricing not parsed", name)
			for i, tier := range mp.Tiers {
				checked++
				assert.Positive(t, tier.CacheRead, "bedrock-mantle/%s tier[%d] cache_read", name, i)
				if noWrite[name] {
					assert.Zero(t, tier.CacheWrite, "bedrock-mantle/%s tier[%d] cache_write", name, i)
				} else {
					assert.Positive(t, tier.CacheWrite, "bedrock-mantle/%s tier[%d] cache_write", name, i)
				}
			}
		}
		assert.NotZero(t, checked)
	})
}
