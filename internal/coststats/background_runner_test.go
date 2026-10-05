package coststats

import (
	"context"
	"testing"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/alicebob/miniredis/v2"
)

// With a background runner wired, RecordRequest must not touch Redis for the
// monthly key spend itself: the write is handed to the runner and lands only
// when the runner executes it. In-process stats stay synchronous.
func TestRecordRequest_MonthlyKeySpendGoesThroughBackgroundRunner(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()
	store, err := adminrollup.NewStore(adminrollup.Config{
		Enabled: true,
		Redis:   &config.RedisConfig{Address: mr.Addr(), DB: 6, DBSet: true},
	})
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()

	r := NewRecorder()
	r.BindRollup(store, adminrollup.NewPersister(store, adminrollup.MetricCost))

	var queued []func(ctx context.Context)
	r.SetBackgroundRunner(func(job func(ctx context.Context)) { queued = append(queued, job) })

	const key = "iw:abcdefgh999"
	r.RecordRequest("openai", key, "", "gpt-4o-mini", 0.25, 0.15, 0.10, 100, 50)

	if len(queued) != 1 {
		t.Fatalf("background runner received %d jobs, want 1", len(queued))
	}
	if got := r.KeyMonthlySpendUSD(context.Background(), key); got != 0 {
		t.Fatalf("monthly spend = %v before the background job ran, want 0 (write must be deferred)", got)
	}
	if got := r.Snapshot()["spend_today_usd"].(float64); got != 0.25 {
		t.Fatalf("in-process spend_today_usd = %v, want 0.25 (local stats stay synchronous)", got)
	}

	queued[0](context.Background())
	if got := r.KeyMonthlySpendUSD(context.Background(), key); got != 0.25 {
		t.Fatalf("monthly spend = %v after the background job ran, want 0.25", got)
	}

	// A request that touches no key queues nothing.
	queued = queued[:0]
	r.RecordRequest("openai", "", "", "gpt-4o-mini", 0.05, 0.03, 0.02, 10, 5)
	if len(queued) != 0 {
		t.Fatalf("keyless request queued %d jobs, want 0", len(queued))
	}

	// Without a runner the write is inline, as before.
	r.SetBackgroundRunner(nil)
	r.RecordRequest("openai", key, "", "gpt-4o-mini", 0.25, 0.15, 0.10, 100, 50)
	if got := r.KeyMonthlySpendUSD(context.Background(), key); got != 0.5 {
		t.Fatalf("monthly spend = %v with inline runner, want 0.5", got)
	}
}

// When no rollup store is bound there is nothing to write, so RecordRequest
// must not spend a worker-pool turn on a no-op job. (Under the cost tracker's
// 3-worker pool, a phantom job per request delays the real cost records
// behind it, which widens the read-only cost-limit overshoot window.)
func TestRecordRequest_UnboundRecorderQueuesNoBackgroundJob(t *testing.T) {
	r := NewRecorder()
	var queued int
	r.SetBackgroundRunner(func(job func(ctx context.Context)) { queued++ })

	r.RecordRequest("openai", "iw:abcdefgh999", "", "gpt-4o-mini", 0.25, 0.15, 0.10, 100, 50)

	if queued != 0 {
		t.Fatalf("unbound recorder queued %d background jobs, want 0", queued)
	}
	if got := r.Snapshot()["spend_today_usd"].(float64); got != 0.25 {
		t.Fatalf("in-process spend_today_usd = %v, want 0.25", got)
	}
}
