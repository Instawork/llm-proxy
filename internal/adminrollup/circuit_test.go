package adminrollup

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/circuit"
	"github.com/stretchr/testify/require"
)

func snap(provs map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{"providers": provs}
}

func TestCircuitDayPeakTakesElementwiseMax(t *testing.T) {
	p := newCircuitDayPeak()

	// Window gauge bounces 3 -> 7 -> 2 for openai; peak must stick at 7.
	p.merge(snap(map[string]interface{}{
		"openai": map[string]interface{}{"failures": 3, "state": "closed"},
	}))
	p.merge(snap(map[string]interface{}{
		"openai": map[string]interface{}{"failures": 7, "state": "open", "rollup_open": true},
	}))
	p.merge(snap(map[string]interface{}{
		"openai": map[string]interface{}{"failures": 2, "state": "closed"},
	}))

	out := p.payload()
	if got := out["total_failures"].(int); got != 7 {
		t.Fatalf("total_failures = %d, want 7 (peak, not sum)", got)
	}
	prov := out["providers"].(map[string]interface{})["openai"].(map[string]interface{})
	if got := prov["failures"].(int); got != 7 {
		t.Fatalf("openai peak failures = %d, want 7", got)
	}
	// rollup_open is sticky once seen true within the day.
	if got := prov["rollup_open"].(bool); !got {
		t.Fatal("rollup_open should remain true after being observed open")
	}
	// state tracks the last observed value.
	if got := prov["state"].(string); got != "closed" {
		t.Fatalf("state = %q, want last-observed 'closed'", got)
	}
}

func TestCircuitDayPeakMultipleProviders(t *testing.T) {
	p := newCircuitDayPeak()
	p.merge(snap(map[string]interface{}{
		"openai":    map[string]interface{}{"failures": 4},
		"anthropic": map[string]interface{}{"failures": 1},
	}))
	p.merge(snap(map[string]interface{}{
		"anthropic": map[string]interface{}{"failures": 6},
	}))

	out := p.payload()
	if got := out["total_failures"].(int); got != 10 {
		t.Fatalf("total_failures = %d, want 10 (4 + 6 peaks)", got)
	}
}

func TestCircuitDayPeakHandlesFloatFromRedis(t *testing.T) {
	p := newCircuitDayPeak()
	// Values round-tripped through JSON/Redis arrive as float64.
	p.merge(snap(map[string]interface{}{
		"gemini": map[string]interface{}{"failures": float64(5)},
	}))
	prov := p.payload()["providers"].(map[string]interface{})["gemini"].(map[string]interface{})
	if got := prov["failures"].(int); got != 5 {
		t.Fatalf("failures from float64 = %d, want 5", got)
	}
}

func TestCircuitDayPeakIgnoresMalformedSnapshots(t *testing.T) {
	p := newCircuitDayPeak()
	p.merge(map[string]interface{}{"providers": "not-a-map"})
	p.merge(map[string]interface{}{}) // no providers key
	p.merge(snap(map[string]interface{}{"openai": "not-a-map"}))

	out := p.payload()
	if got := out["total_failures"].(int); got != 0 {
		t.Fatalf("total_failures = %d, want 0 for malformed input", got)
	}
}

func TestSnapIntCoercions(t *testing.T) {
	cases := []struct {
		in   interface{}
		want int
	}{
		{int(3), 3},
		{int64(4), 4},
		{float64(5.9), 5},
		{"nope", 0},
		{nil, 0},
	}
	for _, c := range cases {
		if got := snapInt(c.in); got != c.want {
			t.Fatalf("snapInt(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestSnapshotCircuitNilStore(t *testing.T) {
	out := SnapshotCircuit(context.Background(), nil, []string{"openai"}, 3, 300)
	if got := out["total_failures"].(int); got != 0 {
		t.Fatalf("nil store total_failures = %d, want 0", got)
	}
	if provs := out["providers"].(map[string]interface{}); len(provs) != 0 {
		t.Fatalf("nil store providers = %+v, want empty", provs)
	}
}

func TestSnapshotCircuitMemoryStore(t *testing.T) {
	cfg := circuit.Config{
		Enabled:          true,
		Backend:          "memory",
		FailureThreshold: 2,
		WindowSeconds:    60,
		CooldownSeconds:  30,
	}.Defaults()
	store := circuit.NewMemoryStore(cfg)
	ctx := context.Background()
	_, _, err := store.RecordTerminalFailure(ctx, "openai")
	require.NoError(t, err)
	_, _, err = store.RecordTerminalFailure(ctx, "openai")
	require.NoError(t, err)

	out := SnapshotCircuit(ctx, store, []string{"openai", "anthropic"}, 2, 300)
	require.Equal(t, 2, out["total_failures"])
	provs := out["providers"].(map[string]interface{})
	openai := provs["openai"].(map[string]interface{})
	require.Equal(t, 2, openai["failures"])
	require.Equal(t, "open", openai["state"])
	require.Contains(t, openai, "rollup_open")
}

func TestSnapshotCircuitSkipsGetStatsErrors(t *testing.T) {
	store := &fakeCircuitStore{statsErr: errors.New("redis down")}
	out := SnapshotCircuit(context.Background(), store, []string{"openai"}, 0, 0)
	require.Equal(t, 0, out["total_failures"])
	require.Empty(t, out["providers"].(map[string]interface{}))
}

func TestArchiverHolderIDNonEmpty(t *testing.T) {
	require.NotEmpty(t, archiverHolderID())
}

func TestTryElectArchiverRedisBackend(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	day := time.Now().UTC().Format("2006-01-02")
	require.Equal(t, BackendRedis, store.Backend())
	require.True(t, store.TryElectArchiver(ctx, MetricCircuit, day, "writer-a"))
	require.False(t, store.TryElectArchiver(ctx, MetricCircuit, day, "writer-b"))
}

func TestRunCircuitArchiverExitsOnStop(t *testing.T) {
	store := testStore(t)
	persister := NewPersister(store, MetricCircuit)
	cb := circuit.NewMemoryStore(circuit.Config{Enabled: true, Backend: "memory"}.Defaults())

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		RunCircuitArchiver(store, persister, cb, []string{"openai"}, 0, 0, stop)
		close(done)
	}()
	close(stop)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunCircuitArchiver did not exit after stop closed")
	}
}

func TestRunCircuitArchiverNilDepsReturnImmediately(t *testing.T) {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		RunCircuitArchiver(nil, nil, nil, nil, 0, 0, stop)
		close(done)
	}()
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("nil-deps archiver did not return")
	}
}

type fakeCircuitStore struct {
	statsErr error
}

func (f *fakeCircuitStore) GetState(context.Context, string) (circuit.State, error) {
	return circuit.StateClosed, nil
}
func (f *fakeCircuitStore) RecordTerminalFailure(context.Context, string) (circuit.State, bool, error) {
	return circuit.StateClosed, false, nil
}
func (f *fakeCircuitStore) RecordSuccess(context.Context, string) error { return nil }
func (f *fakeCircuitStore) RecordProbeFailed(context.Context, string) error {
	return nil
}
func (f *fakeCircuitStore) GetStats(context.Context, string) (*circuit.ProviderStats, error) {
	return nil, f.statsErr
}

var _ circuit.Store = (*fakeCircuitStore)(nil)

func TestCircuitArchiverElectsSingleWriter(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	day := time.Now().UTC().Format("2006-01-02")

	var wg sync.WaitGroup
	winners := make(chan bool, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			winners <- store.TryElectArchiver(ctx, MetricCircuit, day, fmt.Sprintf("instance-%d", id))
		}(i)
	}
	wg.Wait()
	close(winners)

	trueCount := 0
	for w := range winners {
		if w {
			trueCount++
		}
	}
	require.Equal(t, 1, trueCount)
}
