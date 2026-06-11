package adminrollup

import (
	"testing"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

func TestRecorderBindingUnboundIsNoOp(t *testing.T) {
	var b RecorderBinding
	// None of these should panic or do anything when unbound.
	b.QueueToday("2026-06-11", map[string]interface{}{"x": 1})
	b.ArchiveDay("2026-06-11", map[string]interface{}{"x": 1})
	b.FlushRollup()

	snap := map[string]interface{}{"available": true}
	b.MergeHistory(MetricCost, snap)
	if _, ok := snap["daily_history"]; ok {
		t.Fatal("unbound MergeHistory must not mutate the snapshot")
	}
}

func TestRecorderBindingNilReceiverSafe(t *testing.T) {
	var b *RecorderBinding
	b.BindRollup(nil, nil) // must not panic on nil receiver
}

func TestRecorderBindingArchiveAndMergeHistory(t *testing.T) {
	store := testStore(t)
	persister := NewPersister(store, MetricCost)

	var b RecorderBinding
	b.BindRollup(store, persister)

	// Archive a completed day directly (day-rollover path).
	b.ArchiveDay("2026-06-10", map[string]interface{}{
		"spend_today_usd": 2.50,
		"requests_today":  int64(7),
	})

	// MergeHistory should now fold the archived day into a live snapshot.
	snap := map[string]interface{}{"available": true}
	b.MergeHistory(MetricCost, snap)

	require.Equal(t, true, snap["daily_history_available"])
	rows, ok := snap["daily_history"].([]map[string]interface{})
	require.True(t, ok, "daily_history should be rows")
	require.Len(t, rows, 1)
	require.Equal(t, "2026-06-10", rows[0]["day"])
}

func TestRecorderBindingQueueTodayFlush(t *testing.T) {
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	store, err := NewStore(Config{
		Enabled:       true,
		Redis:         &config.RedisConfig{Address: mr.Addr(), DB: 6, DBSet: true},
		RetentionDays: 7,
		HistoryDays:   3,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })

	persister := NewPersister(store, MetricUsage)
	var b RecorderBinding
	b.BindRollup(store, persister)

	// QueueToday is debounced; FlushRollup forces the pending write out now.
	b.QueueToday("2026-06-11", map[string]interface{}{
		"requests_today": int64(3),
		"tokens_today":   int64(300),
	})
	b.FlushRollup()

	// The debounced "today" key should now exist in Redis.
	require.True(t, mr.DB(6).Exists(todayKey(MetricUsage, "2026-06-11")),
		"FlushRollup should have persisted the pending today key")
}
