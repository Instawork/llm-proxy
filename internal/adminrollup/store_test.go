package adminrollup

import (
	"context"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	store, err := NewStore(Config{
		Enabled: true,
		Redis: &config.RedisConfig{
			Address: mr.Addr(),
			DB:      6,
			DBSet:   true,
		},
		RetentionDays: 7,
		HistoryDays:   3,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestStoreKeySpendUSD(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	day := "2026-06-17"

	if v, err := store.KeySpendUSD(ctx, MetricCost, day, "iw:abc"); err != nil || v != 0 {
		t.Fatalf("missing key spend = %v err=%v", v, err)
	}

	// Two instances independently fold deltas for the same key; HINCRBYFLOAT
	// accumulates them into a single fleet-wide value.
	d := Delta{
		Totals: map[string]float64{"spend_usd": 1.25},
		Dimensions: map[string]map[string]float64{
			"by_key": {dimMemberField("iw:abc", "spend_usd"): 1.25},
		},
	}
	require.NoError(t, store.ApplyDelta(ctx, MetricCost, day, d))
	require.NoError(t, store.ApplyDelta(ctx, MetricCost, day, d))

	v, err := store.KeySpendUSD(ctx, MetricCost, day, "iw:abc")
	require.NoError(t, err)
	if v != 2.50 {
		t.Fatalf("fleet key spend = %v want 2.50", v)
	}
}

func TestReserveKeySpend(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	day := "2026-06-17"
	const key = "iw:reserve"
	const limitCents = 100 // $1.00 cap

	// Record $0.40 of actual spend.
	require.NoError(t, store.ApplyDelta(ctx, MetricCost, day, Delta{
		Totals:     map[string]float64{"spend_usd": 0.40},
		Dimensions: map[string]map[string]float64{"by_key": {dimMemberField(key, "spend_usd"): 0.40}},
	}))

	// Recorded $0.40 is under the $1.00 cap, so the boundary request is
	// allowed and reserves its estimate ($0.65 → combined $1.05).
	allowed, err := store.ReserveKeySpend(ctx, MetricCost, day, key, 0.65, limitCents)
	require.NoError(t, err)
	require.True(t, allowed, "boundary request under cap should be allowed and reserved")

	// Combined recorded+reserved is now $1.05 ≥ $1.00, so the NEXT request is
	// blocked: the in-flight reservation prevents concurrent overshoot.
	allowed, err = store.ReserveKeySpend(ctx, MetricCost, day, key, 0.10, limitCents)
	require.NoError(t, err)
	require.False(t, allowed, "request after cap reached (incl. reservations) must be blocked")

	reserved, err := store.ReservedKeySpendUSD(ctx, MetricCost, day, key)
	require.NoError(t, err)
	require.InDelta(t, 0.65, reserved, 1e-9, "only the allowed reservation should be held")

	// Release the reservation; it must floor at 0 even if over-released.
	require.NoError(t, store.AddKeyReservation(ctx, MetricCost, day, key, -10.0))
	reserved, err = store.ReservedKeySpendUSD(ctx, MetricCost, day, key)
	require.NoError(t, err)
	require.Equal(t, 0.0, reserved, "over-release must floor at 0, never go negative")
}

func TestSaveTodayAndArchiveDaily(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	// Use a day inside the HistoryDays window (relative to now); a hardcoded
	// date eventually falls outside the window and LoadHistory drops it.
	day := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	data := map[string]interface{}{"spend_today_usd": 1.23, "requests_today": int64(5)}
	require.NoError(t, store.SaveToday(ctx, MetricCost, day, data))
	require.NoError(t, store.ArchiveDaily(ctx, MetricCost, day, data))

	history, err := store.LoadHistory(ctx, MetricCost)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, day, history[0].Day)
	require.InDelta(t, 1.23, history[0].Data["spend_today_usd"].(float64), 0.001)
}

func TestLoadHistoryMergesToday(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	today := time.Now().UTC().Format("2006-01-02")
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")

	require.NoError(t, store.ArchiveDaily(ctx, MetricPII, yesterday, map[string]interface{}{
		"requests_with_pii": int64(3),
		"by_entity":         []map[string]interface{}{{"name": "EMAIL_ADDRESS", "count": int64(2)}},
	}))
	require.NoError(t, store.SaveToday(ctx, MetricPII, today, map[string]interface{}{
		"requests_with_pii": int64(1),
	}))

	history, err := store.LoadHistory(ctx, MetricPII)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(history), 2)
}

func TestNewStoreDisabled(t *testing.T) {
	store, err := NewStore(Config{Enabled: false})
	require.NoError(t, err)
	require.Nil(t, store)
}

func TestMemoryBackendRoundTrip(t *testing.T) {
	store, err := NewStore(Config{
		Enabled:       true,
		Backend:       BackendMemory,
		RetentionDays: 7,
		HistoryDays:   3,
	})
	require.NoError(t, err)
	require.NotNil(t, store)
	require.Equal(t, BackendMemory, store.Backend())
	t.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	require.NoError(t, store.ArchiveDaily(ctx, MetricCost, "2026-06-10", map[string]interface{}{
		"spend_today_usd": 2.5,
	}))
	require.NoError(t, store.SaveToday(ctx, MetricCost, time.Now().UTC().Format("2006-01-02"), map[string]interface{}{
		"spend_today_usd": 1.0,
	}))

	history, err := store.LoadHistory(ctx, MetricCost)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(history), 1)
}

func TestInferBackendFromConfig(t *testing.T) {
	mem, err := NewStore(Config{Enabled: true})
	require.NoError(t, err)
	require.Equal(t, BackendMemory, mem.Backend())
	t.Cleanup(func() { _ = mem.Close() })
}
