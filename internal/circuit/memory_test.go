package circuit

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func defaultConfig() Config {
	return Config{
		Enabled:          true,
		Backend:          "memory",
		FailureThreshold: 3,
		WindowSeconds:    10,
		CooldownSeconds:  5,
	}.Defaults()
}

func TestMemoryStore_InitialStateClosed(t *testing.T) {
	s := NewMemoryStore(defaultConfig())
	state, err := s.GetState(context.Background(), "openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != StateClosed {
		t.Fatalf("want StateClosed, got %s", state)
	}
}

func TestMemoryStore_ForceOpenRecordsFailureForObservability(t *testing.T) {
	s := NewMemoryStore(defaultConfig())
	ctx := context.Background()

	require.NoError(t, s.ForceOpen(ctx, "openai", 0))

	stats, err := s.GetStats(ctx, "openai")
	require.NoError(t, err)
	require.Equal(t, StateOpen, stats.State)
	// Regression: ForceOpen must credit a failure so the dashboard charts
	// (which plot GetStats.Failures / total_failures) reflect the forced-open
	// event instead of showing state=open with an empty graph.
	require.Equal(t, 1, stats.Failures)
}

func TestMemoryStore_CircuitOpensAtThreshold(t *testing.T) {
	cfg := defaultConfig()
	cfg.FailureThreshold = 3
	s := NewMemoryStore(cfg)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		st, openedNow, err := s.RecordTerminalFailure(ctx, "openai")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if st != StateClosed {
			t.Fatalf("expected StateClosed after %d failures, got %s", i+1, st)
		}
		if openedNow {
			t.Fatalf("expected openedNow=false while still under threshold (i=%d)", i)
		}
	}

	st, openedNow, err := s.RecordTerminalFailure(ctx, "openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if st != StateOpen {
		t.Fatalf("expected StateOpen after 3 failures, got %s", st)
	}
	if !openedNow {
		t.Fatalf("expected openedNow=true on the call that crosses the threshold")
	}
}

func TestMemoryStore_RecordSuccess_ClosesCircuit(t *testing.T) {
	cfg := defaultConfig()
	cfg.FailureThreshold = 1
	s := NewMemoryStore(cfg)
	ctx := context.Background()

	s.RecordTerminalFailure(ctx, "anthropic") //nolint:errcheck
	s.RecordSuccess(ctx, "anthropic")         //nolint:errcheck

	state, _ := s.GetState(ctx, "anthropic")
	if state != StateClosed {
		t.Fatalf("want StateClosed after success, got %s", state)
	}
}

func TestMemoryStore_HalfOpenAfterCooldown(t *testing.T) {
	cfg := Config{
		Enabled:          true,
		FailureThreshold: 1,
		WindowSeconds:    60,
		CooldownSeconds:  0, // will be set to 1ns via manipulation
	}.Defaults()
	s := NewMemoryStore(cfg)
	ctx := context.Background()

	// Trip the circuit.
	s.RecordTerminalFailure(ctx, "gemini") //nolint:errcheck

	// Manually set the cooldown to the past so half-open transition is tested.
	e := s.entry("gemini")
	e.mu.Lock()
	e.cooldownUntil = time.Now().Add(-1 * time.Second) // expired
	e.mu.Unlock()

	state, _ := s.GetState(ctx, "gemini")
	if state != StateHalfOpen {
		t.Fatalf("want StateHalfOpen after cooldown expiry, got %s", state)
	}
}

func TestMemoryStore_ProbeFailed_ReOpensCircuit(t *testing.T) {
	cfg := Config{
		Enabled:          true,
		FailureThreshold: 1,
		WindowSeconds:    60,
		CooldownSeconds:  10,
	}.Defaults()
	s := NewMemoryStore(cfg)
	ctx := context.Background()

	s.RecordTerminalFailure(ctx, "openai") //nolint:errcheck
	// Manually expire cooldown.
	e := s.entry("openai")
	e.mu.Lock()
	e.cooldownUntil = time.Now().Add(-1 * time.Second)
	e.mu.Unlock()

	// Probe fails → should re-open.
	s.RecordProbeFailed(ctx, "openai") //nolint:errcheck
	state, _ := s.GetState(ctx, "openai")
	if state != StateOpen {
		t.Fatalf("want StateOpen after probe failure, got %s", state)
	}
}

func TestMemoryStore_SlidingWindowExpiry(t *testing.T) {
	cfg := Config{
		Enabled:          true,
		FailureThreshold: 3,
		WindowSeconds:    60,
	}.Defaults()
	s := NewMemoryStore(cfg)
	ctx := context.Background()

	// Record 2 failures at the current time.
	s.RecordTerminalFailure(ctx, "openai") //nolint:errcheck
	s.RecordTerminalFailure(ctx, "openai") //nolint:errcheck

	// Rewind the recorded failure timestamps so they fall outside the
	// sliding window — this is equivalent to waiting out the window but
	// does not introduce a wall-clock sleep, which was previously the
	// slowest test in the package and occasionally flaked on busy CI.
	e := s.entry("openai")
	e.mu.Lock()
	rewound := time.Now().Add(-2 * time.Duration(cfg.WindowSeconds) * time.Second)
	for i := range e.failures {
		e.failures[i] = rewound
	}
	e.mu.Unlock()

	// One more failure — the previous two are now outside the window so
	// the count should be exactly 1 and the circuit should stay closed.
	st, _, _ := s.RecordTerminalFailure(ctx, "openai")
	if st != StateClosed {
		t.Fatalf("want StateClosed after window expiry, got %s", st)
	}
	stats, _ := s.GetStats(ctx, "openai")
	if stats.Failures != 1 {
		t.Fatalf("want 1 failure after sliding-window prune, got %d", stats.Failures)
	}
}

func TestMemoryStore_GetStats(t *testing.T) {
	cfg := defaultConfig()
	s := NewMemoryStore(cfg)
	ctx := context.Background()

	s.RecordTerminalFailure(ctx, "openai") //nolint:errcheck
	stats, err := s.GetStats(ctx, "openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.State != StateClosed {
		t.Fatalf("want StateClosed, got %s", stats.State)
	}
	if stats.Failures != 1 {
		t.Fatalf("want 1 failure, got %d", stats.Failures)
	}
}

func TestMemoryStore_GetProviderStats_AggregatesPerModelKeys(t *testing.T) {
	cfg := defaultConfig()
	cfg.WindowSeconds = 60
	s := NewMemoryStore(cfg)
	ctx := context.Background()

	s.RecordTerminalFailure(ctx, "gemini:gemini-2.5-flash")       //nolint:errcheck
	s.RecordTerminalFailure(ctx, "gemini:gemini-2.5-flash")       //nolint:errcheck
	s.RecordTerminalFailure(ctx, "gemini:gemini-1-flash-preview") //nolint:errcheck

	bare, err := s.GetStats(ctx, "gemini")
	require.NoError(t, err)
	require.Equal(t, 0, bare.Failures)

	agg, err := s.GetProviderStats(ctx, "gemini")
	require.NoError(t, err)
	require.Equal(t, 3, agg.Failures)
}

// Parity with the RedisStore regression: an Open breaker with an empty
// failure window (re-opened via a failed probe) must still report Open.
func TestMemoryStore_GetProviderStats_OpenWithEmptyFailureWindow(t *testing.T) {
	cfg := defaultConfig()
	cfg.WindowSeconds = 60
	cfg.CooldownSeconds = 300
	s := NewMemoryStore(cfg)
	ctx := context.Background()

	require.NoError(t, s.RecordProbeFailed(ctx, "openai"))

	bare, err := s.GetStats(ctx, "openai")
	require.NoError(t, err)
	require.Equal(t, 0, bare.Failures)

	agg, err := s.GetProviderStats(ctx, "openai")
	require.NoError(t, err)
	require.Equal(t, StateOpen, agg.State)
	require.Equal(t, 0, agg.Failures)
}

func TestMemoryStore_GetStatsPrunesExpiredFailures(t *testing.T) {
	cfg := defaultConfig()
	cfg.WindowSeconds = 1
	s := NewMemoryStore(cfg)
	ctx := context.Background()

	s.RecordTerminalFailure(ctx, "openai") //nolint:errcheck
	e := s.entry("openai")
	e.mu.Lock()
	e.failures[0] = time.Now().Add(-2 * time.Second)
	e.mu.Unlock()

	stats, err := s.GetStats(ctx, "openai")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if stats.Failures != 0 {
		t.Fatalf("want expired failures pruned from stats, got %d", stats.Failures)
	}
}

func TestMemoryStore_TryStartProbe(t *testing.T) {
	cfg := Config{
		Enabled:          true,
		Backend:          "memory",
		FailureThreshold: 1,
		WindowSeconds:    10,
		CooldownSeconds:  1,
	}.Defaults()
	s := NewMemoryStore(cfg)
	ctx := context.Background()

	if s.TryStartProbe(ctx, "openai") {
		t.Fatal("TryStartProbe should return false before half-open state")
	}
	s.RecordTerminalFailure(ctx, "openai") //nolint:errcheck
	time.Sleep(1100 * time.Millisecond)
	if state, _ := s.GetState(ctx, "openai"); state != StateHalfOpen {
		t.Fatalf("circuit should be half-open after cooldown, got %s", state)
	}
	// First attempt should succeed.
	if !s.TryStartProbe(ctx, "openai") {
		t.Fatal("first TryStartProbe should return true")
	}
	// Second attempt while probe is in flight should fail.
	if s.TryStartProbe(ctx, "openai") {
		t.Fatal("second TryStartProbe should return false")
	}
}

func TestMemoryStore_MultipleProviders_Independent(t *testing.T) {
	cfg := Config{Enabled: true, FailureThreshold: 1, WindowSeconds: 60, CooldownSeconds: 300}.Defaults()
	s := NewMemoryStore(cfg)
	ctx := context.Background()

	s.RecordTerminalFailure(ctx, "anthropic") //nolint:errcheck

	openAIState, _ := s.GetState(ctx, "openai")
	if openAIState != StateClosed {
		t.Fatalf("openai should be unaffected by anthropic failure, got %s", openAIState)
	}

	anthropicState, _ := s.GetState(ctx, "anthropic")
	if anthropicState != StateOpen {
		t.Fatalf("anthropic should be Open, got %s", anthropicState)
	}
}

func TestMemoryStore_ReleaseProbe_ResetsFlag(t *testing.T) {
	cfg := Config{
		Enabled: true, Backend: "memory",
		FailureThreshold: 1, WindowSeconds: 60, CooldownSeconds: 1,
	}
	s := NewMemoryStore(cfg)
	ctx := context.Background()

	_, _, err := s.RecordTerminalFailure(ctx, "foo")
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		st, _ := s.GetState(ctx, "foo")
		return st == StateHalfOpen
	}, 3*time.Second, 50*time.Millisecond)

	require.True(t, s.TryStartProbe(ctx, "foo"))
	require.False(t, s.TryStartProbe(ctx, "foo"))

	require.NoError(t, s.ReleaseProbe(ctx, "foo"))
	require.True(t, s.TryStartProbe(ctx, "foo"))
}
