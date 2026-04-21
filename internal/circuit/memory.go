package circuit

import (
	"context"
	"sync"
	"time"
)

// memoryEntry holds per-provider circuit state for the in-memory store.
type memoryEntry struct {
	mu            sync.Mutex
	state         State
	failures      []time.Time // sliding window: timestamps of terminal failures
	cooldownUntil time.Time
	probeInFlight bool // prevents multiple concurrent probes in half-open
}

// MemoryStore is a single-process, mutex-backed circuit breaker store.
// It is the default backend for local development.
type MemoryStore struct {
	cfg     Config
	mu      sync.Mutex
	entries map[string]*memoryEntry
}

// NewMemoryStore constructs a MemoryStore with cfg defaults applied.
func NewMemoryStore(cfg Config) *MemoryStore {
	return &MemoryStore{
		cfg:     cfg.Defaults(),
		entries: make(map[string]*memoryEntry),
	}
}

func (s *MemoryStore) entry(provider string) *memoryEntry {
	s.mu.Lock()
	e, ok := s.entries[provider]
	if !ok {
		e = &memoryEntry{}
		s.entries[provider] = e
	}
	s.mu.Unlock()
	return e
}

// GetState returns the current circuit state, advancing from Open → HalfOpen
// when the cooldown period has elapsed.
func (s *MemoryStore) GetState(_ context.Context, provider string) (State, error) {
	e := s.entry(provider)
	e.mu.Lock()
	defer e.mu.Unlock()
	return s.stateUnlocked(e), nil
}

// stateUnlocked evaluates the current state, transitioning Open → HalfOpen when
// the cooldown expires.  Must be called with e.mu held.
func (s *MemoryStore) stateUnlocked(e *memoryEntry) State {
	if e.state == StateOpen && !e.cooldownUntil.IsZero() && time.Now().After(e.cooldownUntil) {
		e.state = StateHalfOpen
		e.cooldownUntil = time.Time{}
	}
	return e.state
}

// RecordTerminalFailure adds a failure timestamp to the sliding window and
// opens the circuit if the threshold is exceeded.
func (s *MemoryStore) RecordTerminalFailure(_ context.Context, provider string) (State, error) {
	e := s.entry(provider)
	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()
	window := time.Duration(s.cfg.WindowSeconds) * time.Second
	cutoff := now.Add(-window)

	// Append and prune the sliding window.
	e.failures = append(e.failures, now)
	pruned := e.failures[:0]
	for _, t := range e.failures {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	e.failures = pruned

	// Open the circuit if we've crossed the threshold.
	if len(e.failures) >= s.cfg.FailureThreshold && e.state == StateClosed {
		e.state = StateOpen
		e.cooldownUntil = now.Add(time.Duration(s.cfg.CooldownSeconds) * time.Second)
		e.probeInFlight = false
	}

	return s.stateUnlocked(e), nil
}

// RecordSuccess closes the circuit after a successful half-open probe.
func (s *MemoryStore) RecordSuccess(_ context.Context, provider string) error {
	e := s.entry(provider)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state = StateClosed
	e.failures = e.failures[:0]
	e.cooldownUntil = time.Time{}
	e.probeInFlight = false
	return nil
}

// RecordProbeFailed re-opens the circuit after a failed half-open probe.
func (s *MemoryStore) RecordProbeFailed(_ context.Context, provider string) error {
	e := s.entry(provider)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state = StateOpen
	e.cooldownUntil = time.Now().Add(time.Duration(s.cfg.CooldownSeconds) * time.Second)
	e.probeInFlight = false
	return nil
}

// GetStats returns a snapshot of the provider's circuit stats.
func (s *MemoryStore) GetStats(_ context.Context, provider string) (*ProviderStats, error) {
	e := s.entry(provider)
	e.mu.Lock()
	defer e.mu.Unlock()

	state := s.stateUnlocked(e)
	stats := &ProviderStats{
		State:    state,
		Failures: len(e.failures),
	}
	if !e.cooldownUntil.IsZero() {
		t := e.cooldownUntil
		stats.CooldownUntil = &t
	}
	return stats, nil
}

// TryStartProbe attempts to acquire the probe slot in half-open state.
// Returns true if this goroutine should send the probe, false if another
// goroutine already claimed it (fast-fail the request instead).
func (s *MemoryStore) TryStartProbe(provider string) bool {
	e := s.entry(provider)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.probeInFlight {
		return false
	}
	e.probeInFlight = true
	return true
}
