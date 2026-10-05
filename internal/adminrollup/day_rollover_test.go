package adminrollup

import (
	"context"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// gatedBackend wraps the memory backend, records the order of write ops,
// and parks applyDelta until release is closed (or ctx expires) so a test
// can hold the "Redis is slow" state for as long as it likes.
type gatedBackend struct {
	backend
	release chan struct{}

	mu  sync.Mutex
	ops []string
}

func newGatedBackend() *gatedBackend {
	return &gatedBackend{backend: newMemoryBackend(), release: make(chan struct{})}
}

func (g *gatedBackend) record(op string) {
	g.mu.Lock()
	g.ops = append(g.ops, op)
	g.mu.Unlock()
}

func (g *gatedBackend) snapshotOps() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string(nil), g.ops...)
}

func (g *gatedBackend) applyDelta(ctx context.Context, metric, day string, d Delta, ttl time.Duration) error {
	g.record("applyDelta:start")
	select {
	case <-g.release:
	case <-ctx.Done():
		g.record("applyDelta:ctx-done")
		return ctx.Err()
	}
	g.record("applyDelta:done")
	return g.backend.applyDelta(ctx, metric, day, d, ttl)
}

func (g *gatedBackend) trySetNX(ctx context.Context, key, value string, ttl time.Duration) (bool, error) {
	g.record("trySetNX")
	return g.backend.trySetNX(ctx, key, value, ttl)
}

func gatedStore(t *testing.T) (*Store, *gatedBackend) {
	t.Helper()
	be := newGatedBackend()
	store := &Store{be: be, logger: slog.Default(), retentionDays: 7, historyDays: 3}
	t.Cleanup(func() { _ = store.Close() })
	return store, be
}

func waitForOp(t *testing.T, be *gatedBackend, want string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ops := be.snapshotOps()
		if slices.Contains(ops, want) {
			return ops
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("op %q never observed; ops=%v", want, be.snapshotOps())
	return nil
}

// A UTC day rollover must not block the caller (which holds a recorder's
// mutex) on the store: FinishDayRollover returns immediately even while the
// flush is parked, and the archive election is still ordered after the flush
// completes.
func TestFinishDayRollover_ReturnsImmediatelyAndFlushesBeforeArchive(t *testing.T) {
	store, be := gatedStore(t)
	persister := NewPersister(store, MetricCost)

	var b RecorderBinding
	b.BindRollup(store, persister)

	const oldDay = "2026-06-10"
	b.QueueDelta(oldDay, Delta{Totals: map[string]float64{"requests_today": 1}})

	start := time.Now()
	b.FinishDayRollover(MetricCost, oldDay, TopNCaps{})
	require.Less(t, time.Since(start), 200*time.Millisecond,
		"FinishDayRollover must hand the store I/O to a goroutine")

	ops := waitForOp(t, be, "applyDelta:start")
	require.NotContains(t, ops, "trySetNX", "archive election must wait for the flush")

	// Keep the flush parked for a while longer: still no archive.
	time.Sleep(50 * time.Millisecond)
	require.NotContains(t, be.snapshotOps(), "trySetNX")

	close(be.release)
	ops = waitForOp(t, be, "trySetNX")

	var flushDone, elected int
	for i, op := range ops {
		switch op {
		case "applyDelta:done":
			flushDone = i
		case "trySetNX":
			elected = i
		}
	}
	require.Greater(t, elected, flushDone, "flush must complete before the archiver election; ops=%v", ops)
}

func TestFinishDayRollover_UnboundIsNoOp(t *testing.T) {
	var b RecorderBinding
	b.FinishDayRollover(MetricCost, "2026-06-10", TopNCaps{})
}
