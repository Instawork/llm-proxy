package cost

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func startedAsyncTracker(t *testing.T, workers, queueSize int) *CostTracker {
	t.Helper()
	ct := NewCostTracker(&mockTransport{})
	ct.ConfigureAsync(workers, queueSize, 60)
	require.NoError(t, ct.StartAsyncWorkers())
	t.Cleanup(ct.StopAsyncWorkers)
	return ct
}

func TestRunInBackground_SyncModeAndNilTrackerRunInline(t *testing.T) {
	var ran atomic.Int32
	job := func(ctx context.Context) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("background job must receive a bounded context")
		}
		ran.Add(1)
	}

	ct := NewCostTracker(&mockTransport{})
	ct.SetSyncMode()
	ct.RunInBackground(job)
	require.Equal(t, int32(1), ran.Load(), "sync mode runs the job before returning")

	var nilCT *CostTracker
	nilCT.RunInBackground(job)
	require.Equal(t, int32(2), ran.Load(), "nil tracker runs the job inline")

	ct.RunInBackground(nil) // must not panic
}

func TestRunInBackground_AsyncRunsOffCallerGoroutine(t *testing.T) {
	ct := startedAsyncTracker(t, 2, 16)

	release := make(chan struct{})
	done := make(chan struct{})
	start := time.Now()
	ct.RunInBackground(func(ctx context.Context) {
		<-release
		close(done)
	})
	require.Less(t, time.Since(start), 500*time.Millisecond, "caller must not wait for the job")

	select {
	case <-done:
		t.Fatal("job finished before it was released: it ran inline")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("job never ran on the worker pool")
	}
}

func TestRunInBackground_FullQueueFallsBackToInline(t *testing.T) {
	ct := startedAsyncTracker(t, 1, 1)

	// Occupy the single worker and the single queue slot.
	park := make(chan struct{})
	defer close(park)
	workerBusy := make(chan struct{})
	ct.RunInBackground(func(ctx context.Context) {
		close(workerBusy)
		<-park
	})
	<-workerBusy
	ct.RunInBackground(func(ctx context.Context) { <-park })

	var inline atomic.Bool
	ct.RunInBackground(func(ctx context.Context) { inline.Store(true) })
	require.True(t, inline.Load(), "with the queue full the job must run inline, not be dropped")
}

func TestStopAsyncWorkers_DrainsQueuedBackgroundJobs(t *testing.T) {
	ct := NewCostTracker(&mockTransport{})
	ct.ConfigureAsync(1, 64, 60)
	require.NoError(t, ct.StartAsyncWorkers())

	// Park the worker so later jobs stay queued until shutdown drains them.
	park := make(chan struct{})
	workerBusy := make(chan struct{})
	ct.RunInBackground(func(ctx context.Context) {
		close(workerBusy)
		<-park
	})
	<-workerBusy

	var mu sync.Mutex
	var ran int
	for range 10 {
		ct.RunInBackground(func(ctx context.Context) {
			mu.Lock()
			ran++
			mu.Unlock()
		})
	}
	mu.Lock()
	require.Equal(t, 0, ran, "jobs should be queued behind the parked worker")
	mu.Unlock()

	close(park)
	ct.StopAsyncWorkers()

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, 10, ran, "shutdown must drain queued background jobs")
}
