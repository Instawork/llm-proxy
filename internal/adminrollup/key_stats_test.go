package adminrollup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestKeyCostDayStats(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	day := time.Now().UTC().Format("2006-01-02")
	const key = "iw:abc…deadbeef"

	require.NoError(t, store.ApplyDelta(ctx, MetricCost, day, Delta{
		Totals: map[string]float64{"spend_usd": 0.55},
		Dimensions: map[string]map[string]float64{
			"by_key": {
				dimMemberField(key, "spend_usd"):        0.55,
				dimMemberField(key, "input_spend_usd"):  0.50,
				dimMemberField(key, "output_spend_usd"): 0.05,
				dimMemberField(key, "requests"):         3,
				dimMemberField(key, "input_tokens"):     1000,
				dimMemberField(key, "output_tokens"):    50,
			},
		},
	}))

	stats, ok, err := store.KeyCostDayStats(ctx, day, key)
	require.NoError(t, err)
	require.True(t, ok)
	require.InDelta(t, 0.55, stats.SpendUSD, 1e-9)
	require.InDelta(t, 0.50, stats.InputSpendUSD, 1e-9)
	require.InDelta(t, 0.05, stats.OutputSpendUSD, 1e-9)
	require.Equal(t, int64(3), stats.Requests)
	require.Equal(t, int64(1000), stats.InputTokens)
	require.Equal(t, int64(50), stats.OutputTokens)
}

func TestKeyPIIDayCount(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	day := time.Now().UTC().Format("2006-01-02")
	const key = "iw:abc…deadbeef"

	require.NoError(t, store.ApplyDelta(ctx, MetricPII, day, Delta{
		Totals: map[string]float64{"requests_scanned": 2},
		Dimensions: map[string]map[string]float64{
			"by_key": {key: 2},
		},
	}))

	count, ok, err := store.KeyPIIDayCount(ctx, day, key)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, int64(2), count)
}

func TestKeyCostDailySeriesToday(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	day := time.Now().UTC().Format("2006-01-02")
	const key = "iw:abc…deadbeef"

	require.NoError(t, store.ApplyDelta(ctx, MetricCost, day, Delta{
		Totals: map[string]float64{"spend_usd": 1.0},
		Dimensions: map[string]map[string]float64{
			"by_key": {dimMemberField(key, "spend_usd"): 1.0},
		},
	}))

	series, ok, err := store.KeyCostDailySeries(ctx, key, 3)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, series, 3)
	require.InDelta(t, 1.0, series[len(series)-1].Value, 1e-9)
}

func TestKeyCostDailySeriesArchivedDay(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	const key = "iw:abc…deadbeef"

	require.NoError(t, store.ArchiveDaily(ctx, MetricCost, yesterday, map[string]interface{}{
		"by_key": []interface{}{
			map[string]interface{}{"key_id": key, "spend_usd": 2.5},
		},
	}))

	series, ok, err := store.KeyCostDailySeries(ctx, key, 2)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, series, 2)
	require.Equal(t, yesterday, series[0].Day)
	require.InDelta(t, 2.5, series[0].Value, 1e-9)
}

func TestKeyPIIDailySeries(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	day := time.Now().UTC().Format("2006-01-02")
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	const key = "iw:abc…deadbeef"

	require.NoError(t, store.ApplyDelta(ctx, MetricPII, day, Delta{
		Totals: map[string]float64{"requests_scanned": 1},
		Dimensions: map[string]map[string]float64{
			"by_key": {key: 1},
		},
	}))
	require.NoError(t, store.ArchiveDaily(ctx, MetricPII, yesterday, map[string]interface{}{
		"top_keys": []interface{}{
			map[string]interface{}{"name": key, "count": int64(3)},
		},
	}))

	series, ok, err := store.KeyPIIDailySeries(ctx, key, 2)
	require.NoError(t, err)
	require.True(t, ok)
	require.Len(t, series, 2)
	require.Equal(t, yesterday, series[0].Day)
	require.InDelta(t, 3, series[0].Value, 1e-9)
	require.InDelta(t, 1, series[len(series)-1].Value, 1e-9)
}
