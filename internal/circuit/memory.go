package circuit

import (
	"context"
	"sync"
	"time"
)

// maxFailureWindowEntries caps the number of per-provider failure timestamps
// the MemoryStore keeps in the sliding window.  Without a cap a sustained
// failure burst can cause the slice to grow O(failures-in-window), which on
// a long WindowSeconds setting is an unbounded-ish memory footprint per
// provider.  Once the cap is reached we drop the oldest entries; since the
// threshold check only cares about `len >= threshold`, this cap cannot
// cause a false negative (if we already hit the cap we already tripped the
// circuit several orders of magnitude ago).
const maxFailureWindowEntries = 4096

// probeWatchdogTTL bounds how long a probeInFlight=true flag can persist
// without progress before TryStartProbe considers it stale and reclaims
// the slot.  This defends against the degenerate case where a probe's
// goroutine exits (panic, ctx cancellation, deferred cleanup skipped)
// without clearing the flag, which would otherwise starve the circuit
// permanently in this process.  Chosen generously so a healthy probe
// (which typically completes in < 30 s) is never reclaimed prematurely.
const probeWatchdogTTL = 5 * time.Minute

// memoryEntry holds per-provider circuit state for the in-memory store.
type memoryEntry struct {
	mu            sync.Mutex
	state         State
	failures      []time.Time // sliding window: timestamps of terminal failures
	cooldownUntil time.Time
	probeInFlight bool      // prevents multiple concurrent probes in half-open
	probeStartAt  time.Time // watchdog timestamp; see probeWatchdogTTL
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

// pruneFailuresLocked removes failures outside the sliding window and applies
// the hard entry cap. Must be called with e.mu held.
func (s *MemoryStore) pruneFailuresLocked(e *memoryEntry, now time.Time) {
	window := time.Duration(s.cfg.WindowSeconds) * time.Second
	cutoff := now.Add(-window)

	pruned := e.failures[:0]
	for _, t := range e.failures {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	if len(pruned) > maxFailureWindowEntries {
		pruned = pruned[len(pruned)-maxFailureWindowEntries:]
	}
	e.failures = pruned
}

// RecordTerminalFailure adds a failure timestamp to the sliding window and
// opens the circuit if the threshold is exceeded.
func (s *MemoryStore) RecordTerminalFailure(_ context.Context, provider string) (State, error) {
	e := s.entry(provider)
	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()

	// Append and prune the sliding window.
	e.failures = append(e.failures, now)
	s.pruneFailuresLocked(e, now)

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
	e.probeStartAt = time.Time{}
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
	e.probeStartAt = time.Time{}
	return nil
}

// ReleaseProbe releases the probe slot without changing the circuit state.
// Used when the probe did not produce a signal we should credit — e.g.
// the caller's request context was cancelled or its deadline expired, so
// the "success or failure" of the upstream call does not reflect on the
// provider's health.  Safe to call multiple times.
func (s *MemoryStore) ReleaseProbe(_ context.Context, provider string) error {
	e := s.entry(provider)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.probeInFlight = false
	e.probeStartAt = time.Time{}
	return nil
}

// GetStats returns a snapshot of the provider's circuit stats.
func (s *MemoryStore) GetStats(_ context.Context, provider string) (*ProviderStats, error) {
	e := s.entry(provider)
	e.mu.Lock()
	defer e.mu.Unlock()

	s.pruneFailuresLocked(e, time.Now())
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
//
// The ctx argument is accepted for signature parity with RedisStore (the
// distributed backend); in-memory coordination is synchronous and does not
// consult it.
//
// A watchdog (see probeWatchdogTTL) forcibly reclaims a stale in-flight
// flag whose probeStartAt is older than the TTL.  Without this, a probe
// goroutine that exits before clearing probeInFlight (e.g. via a panic
// in the HTTP transport, an early context cancellation that skips the
// deferred cleanup, or a bug in caller wiring) would permanently starve
// future probes on this instance.
func (s *MemoryStore) TryStartProbe(_ context.Context, provider string) bool {
	e := s.entry(provider)
	e.mu.Lock()
	defer e.mu.Unlock()
	if s.stateUnlocked(e) != StateHalfOpen {
		return false
	}
	now := time.Now()
	if e.probeInFlight {
		if e.probeStartAt.IsZero() || now.Sub(e.probeStartAt) < probeWatchdogTTL {
			return false
		}
		// Stale flag — reclaim it.
	}
	e.probeInFlight = true
	e.probeStartAt = now
	return true
}
