package circuitstats

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecorder_ProbeLifecycle(t *testing.T) {
	r := NewRecorder()
	r.RecordProbe("openai", "openai")
	r.RecordProbeClosed("openai", "openai", 200)
	r.RecordProbe("openai", "openai:gpt-4o")
	r.RecordProbeReopened("openai", "openai:gpt-4o", 429, "http_429_quota")

	snap := r.Snapshot()
	require.Equal(t, true, snap["available"])
	assert.Equal(t, int64(2), snap["probes_started"])
	assert.Equal(t, int64(1), snap["probes_succeeded"])
	assert.Equal(t, int64(1), snap["probes_failed"])

	events, ok := snap["recent_events"].([]activityEvent)
	require.True(t, ok)
	require.Len(t, events, 4)
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
	r.RecordFastFail("gemini", "gemini")
	r.RecordOpened("openai", "openai", "insufficient_quota")

	snap := r.Snapshot()
	assert.Equal(t, int64(2), snap["checks_total"])
	assert.Equal(t, int64(1), snap["blocked_open"])
	assert.Equal(t, int64(1), snap["circuits_opened"])
}

func TestRecorder_SnapshotNil(t *testing.T) {
	var r *Recorder
	snap := r.Snapshot()
	assert.Equal(t, false, snap["available"])
}
