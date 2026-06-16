package circuit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRedisStoreForBehaviorTest(t *testing.T, cfg Config) (*RedisStore, *miniredis.Miniredis) {
	t.Helper()

	mr := miniredis.RunT(t)
	cfg.Enabled = true
	cfg.Backend = "redis"
	cfg.RedisAddress = mr.Addr()
	store, err := NewRedisStore(cfg)
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, mr
}

func TestRedisStore_RecordTerminalFailure_CountsSameScoreFailures(t *testing.T) {
	cfg := Config{
		FailureThreshold: 3,
		WindowSeconds:    60,
		CooldownSeconds:  300,
	}.Defaults()
	store, _ := newRedisStoreForBehaviorTest(t, cfg)
	ctx := context.Background()
	now := float64(time.Now().UnixNano()) / 1e9
	cutoff := now - float64(cfg.WindowSeconds)

	for i, want := range []State{StateClosed, StateClosed, StateOpen} {
		// luaRecordFailure now returns a 2-element table {state, openedNow},
		// so we read it via Slice() rather than Text().  openedNow is
		// covered by the per-API test below; here we only assert the
		// state transitions are stable across same-timestamp adds.
		got, err := luaRecordFailure.Run(
			ctx, store.rdb,
			[]string{store.failuresKey("openai"), store.stateKey("openai"), store.halfOpenKey("openai")},
			now,
			cutoff,
			cfg.FailureThreshold,
			cfg.CooldownSeconds,
			cfg.WindowSeconds,
			cfg.CooldownSeconds*halfOpenMarkerTTLMultiplier,
			fmt.Sprintf("same-score-member-%d", i),
		).Slice()
		if err != nil {
			t.Fatalf("luaRecordFailure call %d: %v", i+1, err)
		}
		if len(got) < 1 {
			t.Fatalf("luaRecordFailure call %d: want >=1 reply element, got %v", i+1, got)
		}
		state, _ := got[0].(string)
		if state != want.String() {
			t.Fatalf("call %d: want state %s, got %s", i+1, want, state)
		}
	}

	stats, err := store.GetStats(ctx, "openai")
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.Failures != 3 {
		t.Fatalf("same-score failures must remain distinct; want 3, got %d", stats.Failures)
	}
}

func TestRedisStore_GetProviderStats_AggregatesPerModelKeys(t *testing.T) {
	cfg := Config{
		FailureThreshold: 5,
		WindowSeconds:    60,
		CooldownSeconds:  300,
	}.Defaults()
	store, _ := newRedisStoreForBehaviorTest(t, cfg)
	ctx := context.Background()

	_, _, err := store.RecordTerminalFailure(ctx, "gemini:gemini-2.5-flash")
	require.NoError(t, err)
	_, _, err = store.RecordTerminalFailure(ctx, "gemini:gemini-2.5-flash")
	require.NoError(t, err)
	_, _, err = store.RecordTerminalFailure(ctx, "gemini:gemini-1-flash-preview")
	require.NoError(t, err)

	bare, err := store.GetStats(ctx, "gemini")
	require.NoError(t, err)
	require.Equal(t, 0, bare.Failures)

	agg, err := store.GetProviderStats(ctx, "gemini")
	require.NoError(t, err)
	require.Equal(t, 3, agg.Failures)
}

func TestRedisStore_StateTransitionsFromOpenToHalfOpenThenClosedAfterIdle(t *testing.T) {
	cfg := Config{
		FailureThreshold: 1,
		WindowSeconds:    60,
		CooldownSeconds:  5,
	}.Defaults()
	store, mr := newRedisStoreForBehaviorTest(t, cfg)
	ctx := context.Background()

	state, openedNow, err := store.RecordTerminalFailure(ctx, "gemini")
	if err != nil {
		t.Fatalf("RecordTerminalFailure: %v", err)
	}
	if state != StateOpen {
		t.Fatalf("want StateOpen after threshold, got %s", state)
	}
	if !openedNow {
		t.Fatalf("want openedNow=true on the call that crossed the threshold")
	}

	mr.FastForward(time.Duration(cfg.CooldownSeconds+1) * time.Second)
	state, err = store.GetState(ctx, "gemini")
	if err != nil {
		t.Fatalf("GetState after cooldown: %v", err)
	}
	if state != StateHalfOpen {
		t.Fatalf("want StateHalfOpen after cooldown expiry while marker lives, got %s", state)
	}

	mr.FastForward(time.Duration(cfg.CooldownSeconds*halfOpenMarkerTTLMultiplier) * time.Second)
	state, err = store.GetState(ctx, "gemini")
	if err != nil {
		t.Fatalf("GetState after half-open marker expiry: %v", err)
	}
	if state != StateClosed {
		t.Fatalf("want StateClosed after long idle marker expiry, got %s", state)
	}
}

// Bug 3 regression: while the breaker is HalfOpen (cooldown TTL has
// expired but the half-open marker is still alive), a
// RecordTerminalFailure call from a bypass request or a ModeLog
// observe-only path MUST NOT re-trip the breaker back to Open with a
// fresh full cooldown.  MemoryStore guards this with `e.state ==
// StateClosed`, but the Redis Lua script previously treated a missing
// state key as "closed" without consulting the half-open marker — so
// any failure recorded during the probe window would prematurely
// extend the cooldown and starve the actual probe.
//
// We exercise the bug by:
//  1. Tripping the breaker (state=Open, cooldown TTL ticking).
//  2. Fast-forwarding past the cooldown but NOT past the half-open
//     marker TTL → state should be HalfOpen.
//  3. Calling RecordTerminalFailure (simulating a bypass-observed
//     failure) and asserting:
//     - state remains HalfOpen (not re-tripped to Open)
//     - openedNow == false (so callers do not re-arm the rollup)
func TestRedisStore_RecordTerminalFailure_DoesNotReTripFromHalfOpen(t *testing.T) {
	cfg := Config{
		FailureThreshold: 1,
		WindowSeconds:    60,
		CooldownSeconds:  5,
	}.Defaults()
	store, mr := newRedisStoreForBehaviorTest(t, cfg)
	ctx := context.Background()

	state, openedNow, err := store.RecordTerminalFailure(ctx, "openai:gpt-4o")
	if err != nil {
		t.Fatalf("RecordTerminalFailure: %v", err)
	}
	if state != StateOpen {
		t.Fatalf("seed: want StateOpen after threshold, got %s", state)
	}
	if !openedNow {
		t.Fatalf("seed: openedNow must be true on the call that crossed the threshold")
	}

	mr.FastForward(time.Duration(cfg.CooldownSeconds+1) * time.Second)
	state, err = store.GetState(ctx, "openai:gpt-4o")
	if err != nil {
		t.Fatalf("GetState after cooldown: %v", err)
	}
	if state != StateHalfOpen {
		t.Fatalf("setup: want StateHalfOpen after cooldown TTL expired but marker alive, got %s", state)
	}

	state, openedNow, err = store.RecordTerminalFailure(ctx, "openai:gpt-4o")
	if err != nil {
		t.Fatalf("RecordTerminalFailure during half-open: %v", err)
	}
	if state != StateHalfOpen {
		t.Fatalf("RecordTerminalFailure must NOT re-trip the breaker out of HalfOpen; want StateHalfOpen, got %s", state)
	}
	if openedNow {
		t.Fatalf("openedNow must be false during HalfOpen — only Closed→Open transitions arm rollup re-records")
	}

	state, err = store.GetState(ctx, "openai:gpt-4o")
	if err != nil {
		t.Fatalf("GetState after observe-only failure: %v", err)
	}
	if state != StateHalfOpen {
		t.Fatalf("post-condition: state must remain HalfOpen, got %s", state)
	}
}

func TestRedisStore_Probes(t *testing.T) {
	cfg := Config{
		FailureThreshold: 1,
		WindowSeconds:    60,
		CooldownSeconds:  5,
	}.Defaults()
	store, mr := newRedisStoreForBehaviorTest(t, cfg)
	ctx := context.Background()

	// 1. Trip it
	store.RecordTerminalFailure(ctx, "openai")
	mr.FastForward(time.Duration(cfg.CooldownSeconds+1) * time.Second)

	// 2. TryStartProbe should succeed
	ok := store.TryStartProbe(ctx, "openai")
	if !ok {
		t.Fatalf("TryStartProbe: want true; got %v", ok)
	}

	// 3. TryStartProbe again should fail (lock held)
	ok = store.TryStartProbe(ctx, "openai")
	if ok {
		t.Fatalf("TryStartProbe 2: want false; got %v", ok)
	}

	// 4. KeepProbeAlive
	stop := store.KeepProbeAlive("openai")
	stop() // stop it immediately

	// 5. ReleaseProbe
	store.ReleaseProbe(ctx, "openai")

	// 6. TryStartProbe should succeed again
	ok = store.TryStartProbe(ctx, "openai")
	if !ok {
		t.Fatalf("TryStartProbe 3: want true; got %v", ok)
	}

	// 7. RecordProbeFailed
	err := store.RecordProbeFailed(ctx, "openai")
	if err != nil {
		t.Fatalf("RecordProbeFailed: %v", err)
	}

	// State should be Open again
	state, _ := store.GetState(ctx, "openai")
	if state != StateOpen {
		t.Fatalf("want StateOpen after RecordProbeFailed, got %s", state)
	}

	// Fast forward again to HalfOpen
	mr.FastForward(time.Duration(cfg.CooldownSeconds+1) * time.Second)
	ok = store.TryStartProbe(ctx, "openai")
	if !ok {
		t.Fatalf("TryStartProbe 4: want true")
	}

	// 8. RecordSuccess
	err = store.RecordSuccess(ctx, "openai")
	if err != nil {
		t.Fatalf("RecordSuccess: %v", err)
	}

	// State should be Closed
	state, _ = store.GetState(ctx, "openai")
	if state != StateClosed {
		t.Fatalf("want StateClosed after RecordSuccess, got %s", state)
	}
}

func TestRedisStore_Rollup(t *testing.T) {
	cfg := Config{
		FailureThreshold: 1,
		WindowSeconds:    60,
		CooldownSeconds:  5,
	}.Defaults()
	store, _ := newRedisStoreForBehaviorTest(t, cfg)
	ctx := context.Background()

	// 1. RecordKeyOpenedForRollup
	err := store.RecordKeyOpenedForRollup(ctx, "openai", "openai:gpt-4o", 60)
	if err != nil {
		t.Fatalf("RecordKeyOpenedForRollup: %v", err)
	}

	// 2. RollupOpen
	open, count, err := store.RollupOpen(ctx, "openai", 2, 60)
	if err != nil {
		t.Fatalf("RollupOpen: %v", err)
	}
	if open || count != 1 {
		t.Fatalf("want false, 1; got %v, %d", open, count)
	}

	// Add another
	if err := store.RecordKeyOpenedForRollup(ctx, "openai", "openai:o1-preview", 60); err != nil {
		t.Fatalf("RecordKeyOpenedForRollup (second key): %v", err)
	}
	open, count, err = store.RollupOpen(ctx, "openai", 2, 60)
	if err != nil {
		t.Fatalf("RollupOpen (second check): %v", err)
	}
	if !open || count != 2 {
		t.Fatalf("want true, 2; got %v, %d", open, count)
	}

	// 3. RolledUpKeys
	keys, err := store.RolledUpKeys(ctx, "openai", 60)
	if err != nil {
		t.Fatalf("RolledUpKeys: %v", err)
	}
	if len(keys) != 2 {
		t.Fatalf("want 2 keys, got %v", keys)
	}

	// 4. ClearRollupKey
	err = store.ClearRollupKey(ctx, "openai", "openai:gpt-4o")
	if err != nil {
		t.Fatalf("ClearRollupKey: %v", err)
	}

	open, count, _ = store.RollupOpen(ctx, "openai", 2, 60)
	if open || count != 1 {
		t.Fatalf("want false, 1 after clear; got %v, %d", open, count)
	}
}

// TestRedisStore_Rollup_DedupesByKey is the Redis parity test for memory's
// TestRollup_RecordKeyOpenedForRollup_DedupesByKey: a single flapping key
// repeatedly recorded must NOT trip the rollup, because the set-based
// rollup tracker keys distinct keys, not raw open-event counts.  Without
// this assertion the Redis SADD/ZADD implementation could regress to
// counter semantics and silently start tripping on a single-model outage.
func TestRedisStore_Rollup_DedupesByKey(t *testing.T) {
	cfg := Config{
		FailureThreshold: 1,
		WindowSeconds:    60,
		CooldownSeconds:  5,
	}.Defaults()
	store, _ := newRedisStoreForBehaviorTest(t, cfg)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := store.RecordKeyOpenedForRollup(ctx, "openai", "openai:m1", 60); err != nil {
			t.Fatalf("RecordKeyOpenedForRollup #%d: %v", i, err)
		}
	}

	open, count, err := store.RollupOpen(ctx, "openai", 3, 60)
	if err != nil {
		t.Fatalf("RollupOpen: %v", err)
	}
	if open {
		t.Fatalf("a single flapping key must not trip the Redis rollup; open=%v count=%d", open, count)
	}
	if count != 1 {
		t.Fatalf("dedup-by-key should yield count=1 after 5 records of the same key, got %d", count)
	}
}

func TestRedisStore_Ping_Ok(t *testing.T) {
	mr := miniredis.RunT(t)
	st, err := NewRedisStore(Config{
		Backend: "redis", FailureThreshold: 5, WindowSeconds: 60, CooldownSeconds: 60,
		RedisAddress: mr.Addr(),
	})
	require.NoError(t, err)
	defer st.Close()
	require.NoError(t, st.Ping(context.Background()))
}

func TestRedisStore_Close_NilSafe(t *testing.T) {
	var s *RedisStore
	assert.NoError(t, s.Close())

	zero := &RedisStore{}
	assert.NoError(t, zero.Close())
}
