package coststats

import (
	"context"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
)

const testUserPrefix = "finch-agent-"

func redisBoundRecorder(t *testing.T) (*Recorder, *adminrollup.Store, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	require.NoError(t, err)
	t.Cleanup(mr.Close)
	store, err := adminrollup.NewStore(adminrollup.Config{
		Enabled: true,
		Redis:   &config.RedisConfig{Address: mr.Addr(), DB: 6, DBSet: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	r := NewRecorder()
	r.BindRollup(store, adminrollup.NewPersister(store, adminrollup.MetricCost))
	return r, store, mr
}

func memoryBoundRecorder(t *testing.T) (*Recorder, *adminrollup.Store) {
	t.Helper()
	store, err := adminrollup.NewStore(adminrollup.Config{Enabled: true, Backend: adminrollup.BackendMemory})
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	r := NewRecorder()
	r.BindRollup(store, adminrollup.NewPersister(store, adminrollup.MetricCost))
	return r, store
}

func forEachBoundRecorder(t *testing.T, fn func(t *testing.T, r *Recorder, store *adminrollup.Store)) {
	t.Helper()
	t.Run("memory", func(t *testing.T) {
		r, store := memoryBoundRecorder(t)
		fn(t, r, store)
	})
	t.Run("redis", func(t *testing.T) {
		r, store, _ := redisBoundRecorder(t)
		fn(t, r, store)
	})
}

func currentMonth() string { return time.Now().UTC().Format("2006-01") }

// With no prefix configured (the default until the agent spend limits
// config lands) nothing is written and keyless requests queue no job.
func TestMonthlyUserSpend_DefaultPrefixWritesNothing(t *testing.T) {
	forEachBoundRecorder(t, func(t *testing.T, r *Recorder, store *adminrollup.Store) {
		var queued int
		r.SetBackgroundRunner(func(job func(ctx context.Context)) {
			queued++
			job(context.Background())
		})

		r.RecordRequest("anthropic", "", "finch-agent-1", "claude", 0.25, 0.15, 0.10, 100, 50)

		require.Equal(t, 0, queued)
		require.False(t, r.GovernsMonthlyUser("finch-agent-1"))
		all, err := store.UserMonthlySpendAll(context.Background(), adminrollup.MetricCost, currentMonth())
		require.NoError(t, err)
		require.Empty(t, all)
	})
}

func TestMonthlyUserSpend_OnlyPrefixedLabelsWritten(t *testing.T) {
	forEachBoundRecorder(t, func(t *testing.T, r *Recorder, store *adminrollup.Store) {
		r.SetMonthlyUserPrefix(testUserPrefix)
		var queued []func(ctx context.Context)
		r.SetBackgroundRunner(func(job func(ctx context.Context)) { queued = append(queued, job) })

		for _, user := range []string{
			"finch-agent-1",
			"finch-agent-1",
			"finch-agent-2",
			"web_search",
			"ip:127.0.0.1",
			"finch-agent-",
			"finch-agent-3|x",
			"",
		} {
			r.RecordRequest("anthropic", "", user, "claude", 0.25, 0.15, 0.10, 100, 50)
		}
		r.RecordRequest("anthropic", "", "finch-agent-9", "claude", 0, 0, 0, 100, 50)

		require.Len(t, queued, 3, "only prefixed labels with non-zero spend queue a job")
		all, err := store.UserMonthlySpendAll(context.Background(), adminrollup.MetricCost, currentMonth())
		require.NoError(t, err)
		require.Empty(t, all, "the write is deferred to the background runner")

		for _, job := range queued {
			job(context.Background())
		}
		all, err = store.UserMonthlySpendAll(context.Background(), adminrollup.MetricCost, currentMonth())
		require.NoError(t, err)
		require.Len(t, all, 2)
		require.InDelta(t, 0.5, all["finch-agent-1"], 1e-9)
		require.InDelta(t, 0.25, all["finch-agent-2"], 1e-9)
	})
}

// A request carrying both an iw- key and a governed label takes one worker
// pool turn for both monthly writes.
func TestMonthlyUserSpend_KeyAndUserShareOneJob(t *testing.T) {
	r, store, _ := redisBoundRecorder(t)
	r.SetMonthlyUserPrefix(testUserPrefix)
	var queued []func(ctx context.Context)
	r.SetBackgroundRunner(func(job func(ctx context.Context)) { queued = append(queued, job) })

	const key = "iw:abcdefgh999"
	r.RecordRequest("openai", key, "finch-agent-1", "gpt-4o-mini", 0.25, 0.15, 0.10, 100, 50)
	require.Len(t, queued, 1)
	queued[0](context.Background())

	require.InDelta(t, 0.25, r.KeyMonthlySpendUSD(context.Background(), key), 1e-9)
	v, err := store.UserMonthlySpendUSD(context.Background(), adminrollup.MetricCost, currentMonth(), "finch-agent-1")
	require.NoError(t, err)
	require.InDelta(t, 0.25, v, 1e-9)
}

func TestMonthlyUserSpend_InlineWithoutRunner(t *testing.T) {
	r, store := memoryBoundRecorder(t)
	r.SetMonthlyUserPrefix(testUserPrefix)
	r.RecordRequest("anthropic", "", "finch-agent-1", "claude", 0.25, 0.15, 0.10, 100, 50)

	v, err := store.UserMonthlySpendUSD(context.Background(), adminrollup.MetricCost, currentMonth(), "finch-agent-1")
	require.NoError(t, err)
	require.InDelta(t, 0.25, v, 1e-9)
}

func TestMonthlyUserSpend_UnboundQueuesNothing(t *testing.T) {
	r := NewRecorder()
	r.SetMonthlyUserPrefix(testUserPrefix)
	var queued int
	r.SetBackgroundRunner(func(job func(ctx context.Context)) { queued++ })
	r.RecordRequest("anthropic", "", "finch-agent-1", "claude", 0.25, 0.15, 0.10, 100, 50)
	require.Equal(t, 0, queued)
}

// The month is pinned when the request is recorded, so a job that runs
// after the month turns still lands in the month the spend happened.
func TestMonthlyUserSpend_WriteUsesPinnedMonth(t *testing.T) {
	r, store := memoryBoundRecorder(t)
	ctx := context.Background()
	r.applyMonthlySpend(ctx, "2026-10", adminrollup.Delta{}, "finch-agent-1", 1.5)
	r.applyMonthlySpend(ctx, "2026-11", adminrollup.Delta{}, "finch-agent-1", 0.5)

	oct, err := store.UserMonthlySpendUSD(ctx, adminrollup.MetricCost, "2026-10", "finch-agent-1")
	require.NoError(t, err)
	require.InDelta(t, 1.5, oct, 1e-9)
	nov, err := store.UserMonthlySpendUSD(ctx, adminrollup.MetricCost, "2026-11", "finch-agent-1")
	require.NoError(t, err)
	require.InDelta(t, 0.5, nov, 1e-9)
}

func TestRecorderUserMonthlyReservation(t *testing.T) {
	forEachBoundRecorder(t, func(t *testing.T, r *Recorder, store *adminrollup.Store) {
		ctx := context.Background()
		const month = "2026-11"
		const id = "finch-agent-1"
		require.NoError(t, store.ApplyMonthlyUserSpend(ctx, adminrollup.MetricCost, month, id, 0.40))

		spend, degraded := r.UserMonthlySpendUSDDetailed(ctx, month, id)
		require.False(t, degraded)
		require.InDelta(t, 0.40, spend, 1e-9)

		allowed, active, err := r.ReserveUserMonthlySpend(ctx, month, id, 0.65, 100)
		require.NoError(t, err)
		require.True(t, active)
		require.True(t, allowed)

		allowed, active, err = r.ReserveUserMonthlySpend(ctx, month, id, 0.10, 100)
		require.NoError(t, err)
		require.True(t, active)
		require.False(t, allowed)

		require.NoError(t, r.AdjustUserMonthlyReservation(ctx, month, id, -100))
		reserved, err := store.ReservedUserMonthlySpendUSD(ctx, adminrollup.MetricCost, month, id)
		require.NoError(t, err)
		require.Equal(t, 0.0, reserved)

		allowed, active, err = r.ReserveUserMonthlySpend(ctx, month, id, 0.10, 0)
		require.NoError(t, err)
		require.False(t, active, "limit <= 0 means unlimited and reserves nothing")
		require.True(t, allowed)
	})
}

func TestRecorderUserMonthlyReservation_Unbound(t *testing.T) {
	r := NewRecorder()
	ctx := context.Background()
	allowed, active, err := r.ReserveUserMonthlySpend(ctx, "2026-11", "finch-agent-1", 0.1, 100)
	require.NoError(t, err)
	require.False(t, active)
	require.True(t, allowed)
	require.NoError(t, r.AdjustUserMonthlyReservation(ctx, "2026-11", "finch-agent-1", -1))
	spend, degraded := r.UserMonthlySpendUSDDetailed(ctx, "2026-11", "finch-agent-1")
	require.False(t, degraded)
	require.Equal(t, 0.0, spend)
}

func TestRecorderUserMonthlyReservation_StoreError(t *testing.T) {
	r, _, mr := redisBoundRecorder(t)
	mr.Close()
	ctx := context.Background()

	allowed, active, err := r.ReserveUserMonthlySpend(ctx, "2026-11", "finch-agent-1", 0.1, 100)
	require.Error(t, err)
	require.False(t, active)
	require.True(t, allowed)

	_, degraded := r.UserMonthlySpendUSDDetailed(ctx, "2026-11", "finch-agent-1")
	require.True(t, degraded)
	require.Error(t, r.AdjustUserMonthlyReservation(ctx, "2026-11", "finch-agent-1", -1))
}
