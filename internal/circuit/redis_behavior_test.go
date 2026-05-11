package circuit

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
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
		got, err := luaRecordFailure.Run(ctx, store.rdb,
			[]string{store.failuresKey("openai"), store.stateKey("openai"), store.halfOpenKey("openai")},
			now,
			cutoff,
			cfg.FailureThreshold,
			cfg.CooldownSeconds,
			cfg.WindowSeconds,
			cfg.CooldownSeconds*halfOpenMarkerTTLMultiplier,
			fmt.Sprintf("same-score-member-%d", i),
		).Text()
		if err != nil {
			t.Fatalf("luaRecordFailure call %d: %v", i+1, err)
		}
		if got != want.String() {
			t.Fatalf("call %d: want state %s, got %s", i+1, want, got)
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

func TestRedisStore_StateTransitionsFromOpenToHalfOpenThenClosedAfterIdle(t *testing.T) {
	cfg := Config{
		FailureThreshold: 1,
		WindowSeconds:    60,
		CooldownSeconds:  5,
	}.Defaults()
	store, mr := newRedisStoreForBehaviorTest(t, cfg)
	ctx := context.Background()

	state, err := store.RecordTerminalFailure(ctx, "gemini")
	if err != nil {
		t.Fatalf("RecordTerminalFailure: %v", err)
	}
	if state != StateOpen {
		t.Fatalf("want StateOpen after threshold, got %s", state)
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
