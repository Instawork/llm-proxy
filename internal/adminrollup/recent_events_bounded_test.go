package adminrollup

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parkedRecentBackend parks every appendRecentEvent until release is closed
// (or ctx expires) and counts how many writes are in flight at once.
type parkedRecentBackend struct {
	backend
	release chan struct{}

	mu       sync.Mutex
	inFlight int
	peak     int
	started  int
}

func newParkedRecentBackend() *parkedRecentBackend {
	return &parkedRecentBackend{backend: newMemoryBackend(), release: make(chan struct{})}
}

func (p *parkedRecentBackend) appendRecentEvent(ctx context.Context, key string, payload []byte, maxLen int, ttl time.Duration) error {
	p.mu.Lock()
	p.started++
	p.inFlight++
	if p.inFlight > p.peak {
		p.peak = p.inFlight
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.inFlight--
		p.mu.Unlock()
	}()
	select {
	case <-p.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return p.backend.appendRecentEvent(ctx, key, payload, maxLen, ttl)
}

func (p *parkedRecentBackend) stats() (started, inFlight, peak int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.started, p.inFlight, p.peak
}

func drainRecentEventSem(t *testing.T) {
	t.Helper()
	require.Eventually(t, func() bool { return len(recentEventSem) == 0 },
		2*time.Second, 5*time.Millisecond, "recent-event semaphore did not drain")
}

func TestAppendRecentEvent_BoundsInFlightWritesAndDropsOverflow(t *testing.T) {
	drainRecentEventSem(t)
	be := newParkedRecentBackend()
	store := &Store{be: be, logger: slog.Default(), retentionDays: 7, historyDays: 3}
	t.Cleanup(func() { _ = store.Close() })

	var b RecorderBinding
	b.BindRollup(store, nil)

	droppedBefore := RecentEventsDropped()
	const burst = maxConcurrentRecentEventWrites * 4
	start := time.Now()
	for i := 0; i < burst; i++ {
		b.AppendRecentEvent(MetricPII, map[string]int{"i": i}, 50)
	}
	// Every call must return immediately even though the backend is parked.
	assert.Less(t, time.Since(start), 500*time.Millisecond)

	require.Eventually(t, func() bool {
		started, _, _ := be.stats()
		return started == maxConcurrentRecentEventWrites
	}, 2*time.Second, 5*time.Millisecond)

	started, inFlight, peak := be.stats()
	assert.Equal(t, maxConcurrentRecentEventWrites, started, "only cap-many goroutines may reach the backend")
	assert.Equal(t, maxConcurrentRecentEventWrites, inFlight)
	assert.Equal(t, maxConcurrentRecentEventWrites, peak)
	assert.Equal(t, int64(burst-maxConcurrentRecentEventWrites), RecentEventsDropped()-droppedBefore)

	close(be.release)
	drainRecentEventSem(t)

	// Once the backlog clears, writes flow again and land in the list.
	b.AppendRecentEvent(MetricPII, map[string]string{"after": "drain"}, 50)
	require.Eventually(t, func() bool {
		raw, err := store.LoadRecentEventPayloads(context.Background(), MetricPII, 50)
		if err != nil {
			return false
		}
		for _, r := range raw {
			var m map[string]string
			if json.Unmarshal(r, &m) == nil && m["after"] == "drain" {
				return true
			}
		}
		return false
	}, 2*time.Second, 5*time.Millisecond)
	_, _, peak = be.stats()
	assert.LessOrEqual(t, peak, maxConcurrentRecentEventWrites)
}

func TestAppendRecentEvent_ReleasesSlotWhenWriteTimesOut(t *testing.T) {
	drainRecentEventSem(t)
	be := newParkedRecentBackend() // never released: every write hits recentEventWriteTimeout
	store := &Store{be: be, logger: slog.Default(), retentionDays: 7, historyDays: 3}
	t.Cleanup(func() { _ = store.Close() })

	var b RecorderBinding
	b.BindRollup(store, nil)
	b.AppendRecentEvent(MetricIDGate, map[string]int{"x": 1}, 10)
	require.Eventually(t, func() bool { return len(recentEventSem) == 1 }, time.Second, time.Millisecond)

	// The slot is returned once the bounded write gives up.
	require.Eventually(t, func() bool { return len(recentEventSem) == 0 },
		recentEventWriteTimeout*4, 5*time.Millisecond)
}

func TestAppendRecentEvent_UnboundIsNoOp(t *testing.T) {
	drainRecentEventSem(t)
	var b RecorderBinding
	before := RecentEventsDropped()
	b.AppendRecentEvent(MetricPII, map[string]int{"x": 1}, 10)
	assert.Equal(t, 0, len(recentEventSem))
	assert.Equal(t, before, RecentEventsDropped())
}
