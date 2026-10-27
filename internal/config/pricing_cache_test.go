package config

import (
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

	t.Run("omitted", func(t *testing.T) {
		mp, err := parseModelPricing(map[string]any{"input": 1.0, "output": 2.0})
		require.NoError(t, err)
		assert.Zero(t, mp.Tiers[0].CacheRead)
		assert.Zero(t, mp.Tiers[0].CacheWrite)
	})
}
