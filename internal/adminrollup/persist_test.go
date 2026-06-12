package adminrollup

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestQueueTodayLegacyFlush(t *testing.T) {
	store := testStore(t)
	p := NewPersister(store, MetricCost)
	day := time.Now().UTC().Format("2006-01-02")

	p.QueueToday(day, map[string]interface{}{
		"spend_today_usd": 3.14,
		"requests_today":  int64(2),
	})
	p.FlushNow()

	ctx := context.Background()
	history, err := store.LoadHistory(ctx, MetricCost)
	require.NoError(t, err)

	var found bool
	for _, row := range history {
		if row.Day == day {
			found = true
			require.InDelta(t, 3.14, row.Data["spend_today_usd"], 0.001)
			break
		}
	}
	require.True(t, found, "legacy SaveToday should persist JSON today blob readable by LoadHistory")
}

func TestArchiveImmediatelyFlushesPendingToday(t *testing.T) {
	store := testStore(t)
	p := NewPersister(store, MetricPII)
	day := time.Now().UTC().AddDate(0, 0, -1).Format("2006-01-02")

	p.QueueToday(day, map[string]interface{}{"requests_scanned": int64(9)})
	p.ArchiveImmediately(day, map[string]interface{}{"requests_scanned": int64(9)})

	ctx := context.Background()
	history, err := store.LoadHistory(ctx, MetricPII)
	require.NoError(t, err)
	var archived bool
	for _, row := range history {
		if row.Day == day {
			archived = true
			require.InDelta(t, 9, row.Data["requests_scanned"], 0.001)
		}
	}
	require.True(t, archived)
}
