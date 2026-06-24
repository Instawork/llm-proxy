package circuitstats

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecorder_ProbeLifecycle(t *testing.T) {
	r := NewRecorder()
	r.RecordProbe("openai", "openai")
	r.RecordProbeClosed("openai", "openai", 200)
	r.RecordProbe("openai", "openai:gpt-4o")
	r.RecordProbeReopened("openai", "openai:gpt-4o", 429, "http_429_quota", "insufficient_quota: quota exceeded")

	snap := r.Snapshot()
	require.Equal(t, true, snap["available"])
	assert.Equal(t, int64(2), snap["probes_started"])
	assert.Equal(t, int64(1), snap["probes_succeeded"])
	assert.Equal(t, int64(1), snap["probes_failed"])

	events, ok := snap["recent_events"].([]activityEvent)
	require.True(t, ok)
	require.Len(t, events, 4)
	assert.Equal(t, "insufficient_quota: quota exceeded", events[0].UpstreamError)
	assert.Equal(t, EventProbeReopened, events[0].Kind)
	assert.Equal(t, "open", events[0].NewState)
	assert.Equal(t, EventProbe, events[1].Kind)
	assert.Equal(t, "half_open", events[1].NewState)
	assert.Equal(t, EventProbeClosed, events[2].Kind)
	assert.Equal(t, "closed", events[2].NewState)
}

func TestRecorder_FastFailAndOpened(t *testing.T) {
	r := NewRecorder()
	r.RecordCheck()
	r.RecordCheck()
	r.RecordFastFail("gemini", "gemini:gemini-2.5-flash-lite")
	r.RecordFastFail("gemini", "gemini:gemini-2.5-flash-lite")
	r.RecordFastFail("openai", "openai")
	r.RecordOpened("openai", "openai", "insufficient_quota", "openai_insufficient_quota", "insufficient_quota: quota exceeded", 429)

	snap := r.Snapshot()
	assert.Equal(t, int64(2), snap["checks_total"])
	assert.Equal(t, int64(3), snap["blocked_open"])
	assert.Equal(t, int64(1), snap["circuits_opened"])

	byKey, ok := snap["by_key"].(map[string]int64)
	require.True(t, ok)
	assert.Equal(t, int64(2), byKey["gemini:gemini-2.5-flash-lite"])
	assert.Equal(t, int64(1), byKey["openai"])
}

func TestRecorder_SnapshotNil(t *testing.T) {
	var r *Recorder
	snap := r.Snapshot()
	assert.Equal(t, false, snap["available"])
}

func TestRecorderSnapshotStaleDayWithoutTraffic(t *testing.T) {
	r := NewRecorder()
	yesterday := time.Now().UTC().Add(-24 * time.Hour).Format("2006-01-02")
	today := time.Now().UTC().Format("2006-01-02")
	r.dayKey = yesterday
	r.checksTotal = 99
	r.blockedOpen = 42

	snap := r.Snapshot()
	assert.Equal(t, today, snap["day"])
	assert.Equal(t, int64(0), snap["checks_total"])
	assert.Equal(t, int64(0), snap["blocked_open"])
}
