package modelstatusstats

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecorder_SnapshotCounts(t *testing.T) {
	rec := NewRecorder()
	rec.RecordRetired("openai", "o1-mini")
	rec.RecordRetired("openai", "o1-mini")
	rec.RecordDeprecated("anthropic", "claude-2")
	rec.RecordUnknown("gemini", "typo-model")

	snap := rec.Snapshot()
	require.Equal(t, true, snap["available"])
	assert.Equal(t, int64(2), snap["retired_total"])
	assert.Equal(t, int64(1), snap["deprecated_total"])
	assert.Equal(t, int64(1), snap["unknown_total"])

	byRetired, ok := snap["by_retired"].([]kv)
	require.True(t, ok)
	require.Len(t, byRetired, 1)
	assert.Equal(t, "openai:o1-mini", byRetired[0].Name)
	assert.Equal(t, int64(2), byRetired[0].Count)
}

func TestRecorder_NilSnapshot(t *testing.T) {
	var rec *Recorder
	snap := rec.Snapshot()
	assert.Equal(t, false, snap["available"])
}

func TestRecorderSnapshotStaleDayWithoutTraffic(t *testing.T) {
	rec := NewRecorder()
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02")
	today := time.Now().UTC().Format("2006-01-02")
	rec.dayKey = yesterday
	rec.retiredTotal = 99

	snap := rec.Snapshot()
	assert.Equal(t, today, snap["day"])
	assert.Equal(t, int64(0), snap["retired_total"])
}
