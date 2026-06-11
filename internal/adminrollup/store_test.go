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

func TestSaveTodayAndArchiveDaily(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()

	data := map[string]interface{}{"spend_today_usd": 1.23, "requests_today": int64(5)}
	require.NoError(t, store.SaveToday(ctx, MetricCost, "2026-06-10", data))
	require.NoError(t, store.ArchiveDaily(ctx, MetricCost, "2026-06-10", data))

	history, err := store.LoadHistory(ctx, MetricCost)
	require.NoError(t, err)
	require.Len(t, history, 1)
	require.Equal(t, "2026-06-10", history[0].Day)
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
