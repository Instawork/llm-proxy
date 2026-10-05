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

	r := NewRedisRecorder(client, nil)
	t.Cleanup(r.Close)
	return r, mr
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
	r, mr := newRedisRecorderForTest(t)
	r.RecordCheck()
	r.RecordCheck()

	snap := r.Snapshot()
	assert.Equal(t, int64(2), snap["checks_total"])

	// Checks are coalesced in-process and written on the flusher tick or on
	// an explicit flush — not one Redis round-trip per RecordCheck.
	assert.Equal(t, int64(2), r.pendingChecks.Load())
	r.FlushRedisChecks()
	assert.Equal(t, int64(0), r.pendingChecks.Load())
	assert.Equal(t, "2", mr.HGet(redisCountersKey, "checks_total"))
	assert.NotEmpty(t, mr.HGet(redisCountersKey, "started_at"))

	// A second flush with nothing pending must not touch Redis.
	r.FlushRedisChecks()
	assert.Equal(t, "2", mr.HGet(redisCountersKey, "checks_total"))
}

func TestRedisRecorder_FlushRedisChecks_RetainsDeltaOnError(t *testing.T) {
	r, mr := newRedisRecorderForTest(t)
	r.RecordCheck()
	r.RecordCheck()
	r.RecordCheck()

	mr.SetError("ERR simulated outage")
	r.FlushRedisChecks()
	assert.Equal(t, int64(3), r.pendingChecks.Load(), "failed flush must keep the delta for retry")

	mr.SetError("")
	r.FlushRedisChecks()
	assert.Equal(t, int64(0), r.pendingChecks.Load())
	assert.Equal(t, "3", mr.HGet(redisCountersKey, "checks_total"))
}

func TestRedisRecorder_CloseFlushesPendingAndIsIdempotent(t *testing.T) {
	r, mr := newRedisRecorderForTest(t)
	r.RecordCheck()
	require.NotNil(t, r.flusherStop, "first check should start the flusher")

	r.Close()
	assert.Equal(t, "1", mr.HGet(redisCountersKey, "checks_total"))

	// Close is idempotent and the flusher goroutine has exited.
	r.Close()
	select {
	case <-r.flusherDone:
	default:
		t.Fatal("flusher goroutine still running after Close")
	}

	// A check recorded after Close is counted locally but never starts a
	// new flusher goroutine.
	r.RecordCheck()
	assert.Equal(t, int64(2), r.Snapshot()["checks_total"])
	assert.Equal(t, int64(1), r.pendingChecks.Load())
}

// Close during a Redis outage must still return promptly and must not
// zero the pending delta: the final flush and its retry both put the count
// back, so the loss is bounded to what is logged, never silently discarded.
func TestRedisRecorder_CloseDuringOutageRetainsPendingDelta(t *testing.T) {
	r, mr := newRedisRecorderForTest(t)
	r.RecordCheck()
	r.RecordCheck()
	mr.SetError("ERR simulated outage")

	done := make(chan struct{})
	go func() { r.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return during a Redis outage")
	}
	assert.Equal(t, int64(2), r.pendingChecks.Load(), "failed final flush must not discard the delta")
	assert.Equal(t, "", mr.HGet(redisCountersKey, "checks_total"))
}

func TestRedisRecorder_FlusherTickShipsChecks(t *testing.T) {
	r, mr := newRedisRecorderForTest(t)
	r.RecordCheck()
	require.Eventually(t, func() bool {
		return mr.HGet(redisCountersKey, "checks_total") == "1"
	}, 3*checkFlushInterval, 20*time.Millisecond)
}

func TestRecorder_CloseWithoutRedisIsNoOp(t *testing.T) {
	var nilRecorder *Recorder
	nilRecorder.Close()

	r := NewRecorder()
	r.RecordCheck()
	r.Close()
	r.Close()
	assert.Equal(t, int64(0), r.pendingChecks.Load())
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
