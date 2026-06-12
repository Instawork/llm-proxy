package adminrollup

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplyDeltaMultiWriterSums(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	day := "2026-06-12"

	p1 := NewPersister(store, MetricCost)
	p2 := NewPersister(store, MetricCost)

	p1.QueueDelta(day, Delta{
		Totals: map[string]float64{
			"spend_usd": 1.0,
			"requests":  3,
		},
		Dimensions: map[string]map[string]float64{
			"by_provider": {"openai|spend_usd": 1.0, "openai|requests": 3},
		},
	})
	p2.QueueDelta(day, Delta{
		Totals: map[string]float64{
			"spend_usd": 2.5,
			"requests":  4,
		},
		Dimensions: map[string]map[string]float64{
			"by_provider": {"openai|spend_usd": 2.5, "openai|requests": 4},
		},
	})
	p1.FlushNow()
	p2.FlushNow()

	snap := map[string]interface{}{"available": true}
	store.MergeToday(ctx, MetricCost, day, snap, TopNCaps{ByKey: 100})

	require.InDelta(t, 3.5, snap["spend_today_usd"].(float64), 0.001)
	require.Equal(t, int64(7), snap["requests_today"])
	byProv, ok := snap["by_provider"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, byProv, 1)
	require.InDelta(t, 3.5, byProv[0]["spend_usd"].(float64), 0.001)
	require.InDelta(t, 7, byProv[0]["requests"].(float64), 0.001)
}

func TestPersisterMergesPendingDeltas(t *testing.T) {
	store := testStore(t)
	p := NewPersister(store, MetricUsage)
	day := "2026-06-12"

	p.QueueDelta(day, Delta{Totals: map[string]float64{"requests": 2, "tokens": 100}})
	p.QueueDelta(day, Delta{Totals: map[string]float64{"requests": 3, "tokens": 50}})
	p.FlushNow()

	ctx := context.Background()
	totals, err := store.loadHash(ctx, totalsKey(MetricUsage, day))
	require.NoError(t, err)
	require.InDelta(t, 5, totals["requests"], 0.001)
	require.InDelta(t, 150, totals["tokens"], 0.001)
}

func TestDeltaEmpty(t *testing.T) {
	require.True(t, (Delta{}).empty())
	require.True(t, Delta{Totals: map[string]float64{}, Dimensions: map[string]map[string]float64{}}.empty())
	require.False(t, Delta{Totals: map[string]float64{"x": 1}}.empty())
	require.False(t, Delta{Dimensions: map[string]map[string]float64{"d": {"m": 1}}}.empty())
}

func TestApplyDeltaEmptyNoOp(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	require.NoError(t, store.ApplyDelta(ctx, MetricCost, "2026-06-12", Delta{}))
	totals, err := store.loadHash(ctx, totalsKey(MetricCost, "2026-06-12"))
	require.NoError(t, err)
	require.Empty(t, totals)
}
