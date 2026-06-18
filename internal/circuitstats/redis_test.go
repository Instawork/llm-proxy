package circuitstats

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRedisRecorderForTest(t *testing.T) (*Recorder, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)

	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	return NewRedisRecorder(client, nil), mr
}

func TestRedisRecorder_ProbeLifecycle(t *testing.T) {
	r, _ := newRedisRecorderForTest(t)

	r.RecordProbe("openai", "openai")
	r.RecordProbeClosed("openai", "openai", 200)
	r.RecordProbe("openai", "openai:gpt-4o")
	r.RecordProbeReopened("openai", "openai:gpt-4o", 429, "http_429_quota", "")

	// Allow async check increments to settle if any were fired.
	time.Sleep(10 * time.Millisecond)

	snap := r.Snapshot()
	require.Equal(t, true, snap["available"])
	assert.Equal(t, "redis", snap["backend"])
	assert.Equal(t, int64(2), snap["probes_started"])
	assert.Equal(t, int64(1), snap["probes_succeeded"])
	assert.Equal(t, int64(1), snap["probes_failed"])

	events, ok := snap["recent_events"].([]activityEvent)
	require.True(t, ok)
	require.Len(t, events, 4)
	assert.Equal(t, EventProbeReopened, events[0].Kind)
	assert.Equal(t, EventProbe, events[1].Kind)
	assert.Equal(t, EventProbeClosed, events[2].Kind)
}

func TestRedisRecorder_RecordCheck(t *testing.T) {
	r, _ := newRedisRecorderForTest(t)
	r.RecordCheck()
	r.RecordCheck()
	time.Sleep(20 * time.Millisecond)

	snap := r.Snapshot()
	assert.Equal(t, int64(2), snap["checks_total"])
}

func TestRedisRecorder_SharedRecentEventsAcrossRecorders(t *testing.T) {
	r1, mr := newRedisRecorderForTest(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	r2 := NewRedisRecorder(client, nil)

	r1.RecordProbe("gemini", "gemini")
	r2.RecordProbeClosed("gemini", "gemini", 200)

	snap := r2.Snapshot()
	assert.Equal(t, int64(1), snap["probes_succeeded"])

	events := snap["recent_events"].([]activityEvent)
	require.Len(t, events, 2)
	assert.Equal(t, EventProbeClosed, events[0].Kind)
	assert.Equal(t, EventProbe, events[1].Kind)
}
