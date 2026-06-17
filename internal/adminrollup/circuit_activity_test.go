package adminrollup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMergeTodayCircuitActivity(t *testing.T) {
	store := testStore(t)
	ctx := t.Context()
	day := time.Now().UTC().Format("2006-01-02")

	err := store.ApplyDelta(ctx, MetricCircuitActivity, day, Delta{
		Totals: map[string]float64{
			"checks_total": 5,
			"blocked_open": 2,
		},
		Dimensions: map[string]map[string]float64{
			"by_key": {
				"gemini:gemini-2.5-flash-lite": 2,
			},
		},
	})
	require.NoError(t, err)

	snap := map[string]interface{}{
		"checks_total": int64(1),
		"blocked_open": int64(0),
	}
	store.MergeToday(ctx, MetricCircuitActivity, day, snap, TopNCaps{})

	assert.Equal(t, int64(5), snap["checks_total"])
	assert.Equal(t, int64(2), snap["blocked_open"])
	byKey, ok := snap["by_key"].(map[string]int64)
	require.True(t, ok)
	assert.Equal(t, int64(2), byKey["gemini:gemini-2.5-flash-lite"])
}
