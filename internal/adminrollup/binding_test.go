package adminrollup

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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

	// Archive a completed day directly (day-rollover path). Use a day inside
	// the HistoryDays window (relative to now); a hardcoded date eventually
	// falls outside the window and MergeHistory drops it.
	day := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")
	b.ArchiveDay(day, map[string]interface{}{
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
	require.Equal(t, day, rows[0]["day"])
}

func TestRecorderBindingQueueDeltaFlush(t *testing.T) {
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

	// QueueDelta is debounced; FlushRollup forces the pending write out now.
	b.QueueDelta("2026-06-11", Delta{
		Totals: map[string]float64{"requests": 3, "tokens": 300},
	})
	b.FlushRollup()

	require.True(t, mr.DB(6).Exists(totalsKey(MetricUsage, "2026-06-11")),
		"FlushRollup should have persisted hash-backed today totals")
}

func TestRecorderBindingMergeTodayAndArchiveFromAggregates(t *testing.T) {
	store := testStore(t)
	persister := NewPersister(store, MetricUsage)
	var b RecorderBinding
	b.BindRollup(store, persister)

	day := time.Now().UTC().Format("2006-01-02")
	b.QueueDelta(day, Delta{Totals: map[string]float64{"requests": 4, "tokens": 40}})
	b.FlushRollup()

	snap := map[string]interface{}{"available": true}
	b.MergeToday(MetricUsage, day, snap, TopNCaps{ByKey: 100})
	require.Equal(t, int64(4), snap["requests_today"])
	require.Equal(t, int64(40), snap["tokens_today"])

	b.ArchiveDayFromAggregates(MetricUsage, day, TopNCaps{ByKey: 100})
	history, err := store.LoadHistory(context.Background(), MetricUsage)
	require.NoError(t, err)
	var archived bool
	for _, row := range history {
		if row.Day == day {
			archived = true
			require.InDelta(t, 4, row.Data["requests_today"], 0.001)
		}
	}
	require.True(t, archived)
}

func TestRecorderBindingMergeRecentEvents(t *testing.T) {
	store := testStore(t)
	var b RecorderBinding
	b.BindRollup(store, NewPersister(store, MetricPII))

	ctx := context.Background()
	require.NoError(t, store.AppendRecentEvent(ctx, MetricPII, map[string]any{
		"time":     1,
		"provider": "openai",
		"outcome":  "ok",
	}, 50))

	snap := map[string]interface{}{}
	b.MergeRecentEvents(MetricPII, "recent", 50, snap, func(raw []json.RawMessage) any {
		out := make([]map[string]any, 0, len(raw))
		for _, payload := range raw {
			var row map[string]any
			if json.Unmarshal(payload, &row) == nil {
				out = append(out, row)
			}
		}
		return out
	})

	require.Equal(t, store.Backend(), snap["recent_backend"])
	rows, ok := snap["recent"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, rows, 1)
	require.Equal(t, "openai", rows[0]["provider"])
}
