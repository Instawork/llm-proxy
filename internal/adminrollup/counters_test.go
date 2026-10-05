package adminrollup

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTopN_OrdersByCountThenNameAndTruncates(t *testing.T) {
	m := map[string]int64{"b": 5, "a": 5, "c": 9, "d": 1}
	assert.Equal(t, []NameCount{{"c", 9}, {"a", 5}, {"b", 5}, {"d", 1}}, TopN(m, 0))
	assert.Equal(t, []NameCount{{"c", 9}, {"a", 5}}, TopN(m, 2))
	assert.Equal(t, []NameCount{}, TopN(nil, 3))
}

func TestIntMapDelta_OnlyChangedKeys(t *testing.T) {
	cur := map[string]int64{"a": 5, "b": 2, "c": 7}
	prev := map[string]int64{"a": 5, "b": 1, "z": 4}
	assert.Equal(t, map[string]float64{"b": 1, "c": 7}, IntMapDelta(cur, prev))
	assert.Empty(t, IntMapDelta(nil, prev))
}

func TestCopyIntMap_IsIndependent(t *testing.T) {
	src := map[string]int64{"a": 1}
	cp := CopyIntMap(src)
	cp["a"] = 2
	cp["b"] = 3
	assert.Equal(t, map[string]int64{"a": 1}, src)
	assert.NotNil(t, CopyIntMap(nil))
}

func TestDayKey_UsesUTC(t *testing.T) {
	loc := time.FixedZone("west", -10*3600)
	// 2026-03-02 23:30 in UTC-10 is already 2026-03-03 in UTC.
	assert.Equal(t, "2026-03-03", DayKey(time.Date(2026, 3, 2, 23, 30, 0, 0, loc)))
}

func TestRollDay_SwapsKeyAndRunsRolloverOnlyOnChange(t *testing.T) {
	store, be := gatedStore(t)
	var b RecorderBinding
	b.BindRollup(store, NewPersister(store, MetricPII))

	day := "2026-01-01"
	same := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	require.False(t, b.RollDay(&day, same, MetricPII, TopNCaps{}))
	assert.Equal(t, "2026-01-01", day)
	assert.Empty(t, be.snapshotOps(), "no rollover work when the day is unchanged")

	next := time.Date(2026, 1, 2, 0, 0, 1, 0, time.UTC)
	require.True(t, b.RollDay(&day, next, MetricPII, TopNCaps{}))
	assert.Equal(t, "2026-01-02", day)
	// FinishDayRollover's goroutine attempts the elected archive for the old day.
	waitForOp(t, be, "trySetNX")
}

func TestRollDay_UnboundStillAdvancesKey(t *testing.T) {
	var b RecorderBinding
	day := ""
	require.True(t, b.RollDay(&day, time.Date(2026, 5, 6, 0, 0, 0, 0, time.UTC), MetricCost, TopNCaps{}))
	assert.Equal(t, "2026-05-06", day)
}
