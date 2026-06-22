package adminrollup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestModelStatusMergeToday(t *testing.T) {
	store, err := NewStore(Config{Enabled: true, Backend: "memory"})
	require.NoError(t, err)
	require.NotNil(t, store)

	ctx := context.Background()
	day := "2026-06-22"
	err = store.ApplyDelta(ctx, MetricModelStatus, day, Delta{
		Totals: map[string]float64{
			"retired_total":    2,
			"deprecated_total": 1,
			"unknown_total":    3,
		},
		Dimensions: map[string]map[string]float64{
			"by_retired":    {"openai:o1-mini": 2},
			"by_deprecated": {"anthropic:claude-2": 1},
			"by_unknown":    {"gemini:typo": 3},
		},
	})
	require.NoError(t, err)

	snap := map[string]interface{}{"available": true}
	store.MergeToday(ctx, MetricModelStatus, day, snap, TopNCaps{})

	assert.Equal(t, int64(2), snap["retired_total"])
	assert.Equal(t, int64(1), snap["deprecated_total"])
	assert.Equal(t, int64(3), snap["unknown_total"])
}
