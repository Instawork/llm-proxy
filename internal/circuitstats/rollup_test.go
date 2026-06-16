package circuitstats

import (
	"testing"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRollupStore(t *testing.T) *adminrollup.Store {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	store, err := adminrollup.NewStore(adminrollup.Config{
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

func TestRecorder_SnapshotMergeHistory(t *testing.T) {
	store := newRollupStore(t)
	persister := adminrollup.NewPersister(store, adminrollup.MetricCircuitActivity)

	r := NewRecorder()
	r.BindRollup(store, persister)
	r.RecordCheck()
	r.FlushRollup()

	snap := r.Snapshot()
	require.Equal(t, true, snap["daily_history_available"])
	rows, ok := snap["daily_history"].([]map[string]interface{})
	require.True(t, ok)
	require.NotEmpty(t, rows)
}

func TestRecorder_RollupAggregatesFleetChecks(t *testing.T) {
	store := newRollupStore(t)
	persister := adminrollup.NewPersister(store, adminrollup.MetricCircuitActivity)

	r1 := NewRecorder()
	r1.BindRollup(store, persister)
	r2 := NewRecorder()
	r2.BindRollup(store, persister)

	r1.RecordCheck()
	r2.RecordCheck()
	r2.RecordCheck()
	r1.FlushRollup()
	r2.FlushRollup()

	snap := r1.Snapshot()
	assert.Equal(t, int64(3), snap["checks_total"])
}
