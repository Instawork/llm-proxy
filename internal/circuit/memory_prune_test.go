package circuit

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryStore_PruneIdle_OnlyReclaimsIdleClosedEntries(t *testing.T) {
	cfg := defaultConfig()
	cfg.FailureThreshold = 2
	s := NewMemoryStore(cfg)
	ctx := context.Background()

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	now := base
	s.SetClockForTesting(func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	})
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}

	// idle: touched once, closed, no failures.
	_, err := s.GetState(ctx, "openai:idle")
	require.NoError(t, err)
	// failing: closed but has a recent failure in the window.
	_, _, err = s.RecordTerminalFailure(ctx, "openai:failing")
	require.NoError(t, err)
	// open: tripped.
	for i := 0; i < cfg.FailureThreshold; i++ {
		_, _, err = s.RecordTerminalFailure(ctx, "openai:open")
		require.NoError(t, err)
	}
	st, err := s.GetState(ctx, "openai:open")
	require.NoError(t, err)
	require.Equal(t, StateOpen, st)

	// Nothing is older than the idle TTL yet.
	assert.Equal(t, 0, s.PruneIdle())
	assert.Len(t, s.entries, 3)

	advance(memoryEntryIdleTTL + time.Second)

	// "fresh" was touched after the cutoff and must survive.
	_, err = s.GetState(ctx, "openai:fresh")
	require.NoError(t, err)

	deleted := s.PruneIdle()
	assert.Equal(t, 1, deleted)

	s.mu.Lock()
	_, idleExists := s.entries["openai:idle"]
	_, failingExists := s.entries["openai:failing"]
	_, openExists := s.entries["openai:open"]
	_, freshExists := s.entries["openai:fresh"]
	s.mu.Unlock()
	assert.False(t, idleExists, "idle closed entry should be reclaimed")
	assert.True(t, failingExists, "entry with failures in window must survive")
	assert.True(t, openExists, "open entry must survive")
	assert.True(t, freshExists, "recently touched entry must survive")

	// A pruned key simply gets re-interned as a fresh Closed entry.
	st, err = s.GetState(ctx, "openai:idle")
	require.NoError(t, err)
	assert.Equal(t, StateClosed, st)
}

func TestMemoryStore_RunIdlePruner_SweepsOnTickAndStops(t *testing.T) {
	s := NewMemoryStore(defaultConfig())
	ctx := context.Background()

	var mu sync.Mutex
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s.SetClockForTesting(func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	})

	_, err := s.GetState(ctx, "gemini:idle")
	require.NoError(t, err)

	mu.Lock()
	now = now.Add(memoryEntryIdleTTL + time.Second)
	mu.Unlock()

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.RunIdlePruner(5*time.Millisecond, stop)
	}()

	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return len(s.entries) == 0
	}, 2*time.Second, time.Millisecond, "pruner tick should reclaim the idle entry")

	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunIdlePruner did not return after stop was closed")
	}
}

func TestMemoryStore_RunIdlePruner_NonPositiveIntervalUsesDefault(t *testing.T) {
	s := NewMemoryStore(defaultConfig())
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.RunIdlePruner(0, stop)
	}()
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunIdlePruner with default interval did not stop")
	}
}
