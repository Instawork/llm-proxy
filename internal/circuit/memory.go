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
	lastTouchedAt time.Time // used by PruneIdle to GC long-idle Closed entries
}

// memoryEntryIdleTTL governs how long a per-key entry can sit Closed with
// zero failures before PruneIdle deletes it. Set generously so a key that
// just trips and recovers does not vanish from observability immediately;
// long enough that a /chat completions traffic pattern with thousands of
// unique models can recycle entries instead of accumulating indefinitely.
const memoryEntryIdleTTL = 30 * time.Minute

// rollupEntry holds the per-provider rollup sliding window of `key
// currently degraded` events.  Each entry is unique by `key`: re-arming
// the same key (e.g. on a failed half-open probe) updates the
// timestamp in place rather than appending a duplicate, so the entry
// count is always equal to the number of distinct per-key breakers
// that are currently tracked as degraded inside the window.
//
// Why dedup-by-key (vs. one event per Closed→Open edge):
//
//   - Without dedup, a single flappy key that opens, recovers, opens
//     again, recovers, etc., contributes N events per N flaps and can
//     single-handedly trip the rollup.  That is not the wholesale-
//     degradation signal we want.
//   - With dedup, the rollup count is "how many distinct keys are
//     degraded right now", which is the operationally meaningful signal.
//
// Probe failures call RecordKeyOpenedForRollup again to refresh the
// timestamp, so a long-burn outage (same N keys continuously down for
// hours) keeps tripping the rollup instead of silently aging out.  Only
// a successful probe (which calls ClearRollupKey) drops a key from the
// window.
type rollupOpenEvent struct {
	at  time.Time
	key string
}

type rollupEntry struct {
	mu     sync.Mutex
	events []rollupOpenEvent
}

// MemoryStore is a single-process, mutex-backed circuit breaker store.
// It is the default backend for local development.
//
// Error/fail-open policy:
//   - All operations are O(1) in-memory and never return non-nil errors.
//   - Context cancellation is intentionally ignored: every operation
//     completes synchronously without blocking, so honouring ctx would
//     race against critical-section completion and risk leaving the
//     store in an inconsistent state (e.g. a half-applied state
//     transition).
//   - This matches RedisStore's fail-open posture: callers can treat an
//     err return as "circuit stays Closed", and the proxy never fails a
//     real request because of a circuit-store hiccup.
type MemoryStore struct {
	cfg        Config
	mu         sync.Mutex
	entries    map[string]*memoryEntry
	rollupMu   sync.Mutex
	rollupKeys map[string]*rollupEntry // keyed by provider name

	// nowFn is the clock used by every time-sensitive operation. Tests can
	// override this with SetClockForTesting to fast-forward without
	// sleeping; production code leaves it nil and falls back to time.Now.
	clockMu sync.RWMutex
	nowFn   func() time.Time
}

// NewMemoryStore constructs a MemoryStore with cfg defaults applied.
func NewMemoryStore(cfg Config) *MemoryStore {
	return &MemoryStore{
		cfg:        cfg.Defaults(),
		entries:    make(map[string]*memoryEntry),
		rollupKeys: make(map[string]*rollupEntry),
	}
}

// SetClockForTesting overrides the wall clock used by MemoryStore. Passing
// nil restores time.Now. Intended for unit tests that need deterministic
// time-based assertions; not safe to call concurrently with store reads in
// production code.
func (s *MemoryStore) SetClockForTesting(nowFn func() time.Time) {
	s.clockMu.Lock()
	defer s.clockMu.Unlock()
	s.nowFn = nowFn
}

func (s *MemoryStore) now() time.Time {
	s.clockMu.RLock()
	fn := s.nowFn
	s.clockMu.RUnlock()
	if fn != nil {
		return fn()
	}
	return time.Now()
}

func (s *MemoryStore) entry(key string) *memoryEntry {
	now := s.now()
	s.mu.Lock()
	e, ok := s.entries[key]
	if !ok {
		e = &memoryEntry{lastTouchedAt: now}
		s.entries[key] = e
	} else {
		e.lastTouchedAt = now
	}
	s.mu.Unlock()
	return e
}

// PruneIdle deletes per-key entries that are in StateClosed with no
// failures and have not been touched in the configured idle window.
// Returns the number of entries deleted. Without periodic pruning the
// store grows unbounded over the process lifetime: per-(provider, model)
// keys are interned forever, so a workload with high model cardinality
// (e.g. one-off fine-tunes) leaks memory indefinitely.
//
// Callers may run this on a timer; the store does not start its own
// goroutine in order to keep ownership/cancellation explicit.
func (s *MemoryStore) PruneIdle() int {
	cutoff := s.now().Add(-memoryEntryIdleTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	deleted := 0
	for k, e := range s.entries {
		e.mu.Lock()
		idle := !e.lastTouchedAt.IsZero() && e.lastTouchedAt.Before(cutoff) &&
			e.state == StateClosed && len(e.failures) == 0 && !e.probeInFlight
		e.mu.Unlock()
		if idle {
			delete(s.entries, k)
			deleted++
		}
	}
	return deleted
}

func (s *MemoryStore) rollupEntryFor(provider string) *rollupEntry {
	s.rollupMu.Lock()
	r, ok := s.rollupKeys[provider]
	if !ok {
		r = &rollupEntry{}
		s.rollupKeys[provider] = r
	}
	s.rollupMu.Unlock()
	return r
}

// GetState returns the current circuit state, advancing from Open → HalfOpen
// when the cooldown period has elapsed.
func (s *MemoryStore) GetState(_ context.Context, key string) (State, error) {
	e := s.entry(key)
	e.mu.Lock()
	defer e.mu.Unlock()
	return s.stateUnlocked(e), nil
}

// stateUnlocked evaluates the current state, transitioning Open → HalfOpen when
// the cooldown expires.  Must be called with e.mu held.
func (s *MemoryStore) stateUnlocked(e *memoryEntry) State {
	if e.state == StateOpen && !e.cooldownUntil.IsZero() && s.now().After(e.cooldownUntil) {
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
func (s *MemoryStore) RecordTerminalFailure(_ context.Context, key string) (State, bool, error) {
	e := s.entry(key)
	e.mu.Lock()
	defer e.mu.Unlock()

	now := s.now()

	// Append and prune the sliding window.
	e.failures = append(e.failures, now)
	s.pruneFailuresLocked(e, now)

	// Open the circuit if we've crossed the threshold.  We track whether
	// this specific call caused the Closed → Open edge so the caller can
	// drive at-most-once side effects (e.g. recording a per-provider
	// rollup event) without an extra round trip.
	openedNow := false
	if len(e.failures) >= s.cfg.FailureThreshold && e.state == StateClosed {
		e.state = StateOpen
		e.cooldownUntil = now.Add(time.Duration(s.cfg.CooldownSeconds) * time.Second)
		e.probeInFlight = false
		openedNow = true
	}

	return s.stateUnlocked(e), openedNow, nil
}

// RecordSuccess closes the circuit after a successful half-open probe.
func (s *MemoryStore) RecordSuccess(_ context.Context, key string) error {
	e := s.entry(key)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state = StateClosed
	e.failures = e.failures[:0]
	e.cooldownUntil = time.Time{}
	e.probeInFlight = false
	e.probeStartAt = time.Time{}
	return nil
}

// ForceOpen transitions key straight to Open for one cooldown period,
// bypassing the failure-count threshold.  Recovery then rides the normal
// Open → HalfOpen → probe lifecycle (stateUnlocked promotes to HalfOpen once
// the cooldown elapses).  See Store.ForceOpen for the rationale.
func (s *MemoryStore) ForceOpen(_ context.Context, key string, cooldownSeconds int) error {
	if cooldownSeconds <= 0 {
		cooldownSeconds = s.cfg.CooldownSeconds
	}
	e := s.entry(key)
	e.mu.Lock()
	defer e.mu.Unlock()
	now := s.now()
	e.state = StateOpen
	e.cooldownUntil = now.Add(time.Duration(cooldownSeconds) * time.Second)
	e.probeInFlight = false
	e.probeStartAt = time.Time{}
	// Record a failure in the sliding window so observability (GetStats
	// Failures, the dashboard "Failures by provider" / trend charts, and
	// daily-history rollups) reflects the forced-open event. Without this the
	// breaker shows state=open with failures=0 and the graphs stay empty.
	e.failures = append(e.failures, now)
	s.pruneFailuresLocked(e, now)
	return nil
}

// RecordProbeFailed re-opens the circuit after a failed half-open probe.
func (s *MemoryStore) RecordProbeFailed(_ context.Context, key string) error {
	e := s.entry(key)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state = StateOpen
	e.cooldownUntil = s.now().Add(time.Duration(s.cfg.CooldownSeconds) * time.Second)
	e.probeInFlight = false
	e.probeStartAt = time.Time{}
	return nil
}

// ReleaseProbe releases the probe slot without changing the circuit state.
// Used when the probe did not produce a signal we should credit — e.g.
// the caller's request context was cancelled or its deadline expired, so
// the "success or failure" of the upstream call does not reflect on the
// provider's health.  Safe to call multiple times.
func (s *MemoryStore) ReleaseProbe(_ context.Context, key string) error {
	e := s.entry(key)
	e.mu.Lock()
	defer e.mu.Unlock()
	e.probeInFlight = false
	e.probeStartAt = time.Time{}
	return nil
}

// GetStats returns a snapshot of the key's circuit stats.
func (s *MemoryStore) GetStats(_ context.Context, key string) (*ProviderStats, error) {
	e := s.entry(key)
	e.mu.Lock()
	defer e.mu.Unlock()

	s.pruneFailuresLocked(e, s.now())
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

// GetProviderStats aggregates failures and worst-case state across every
// per-model key for provider (see ProviderStatsFor).
func (s *MemoryStore) GetProviderStats(_ context.Context, provider string) (*ProviderStats, error) {
	now := s.now()
	stats := &ProviderStats{State: StateClosed}

	s.mu.Lock()
	defer s.mu.Unlock()

	for key, e := range s.entries {
		if !providerKeyMatches(key, provider) {
			continue
		}
		e.mu.Lock()
		s.pruneFailuresLocked(e, now)
		stats.Failures += len(e.failures)
		state := s.stateUnlocked(e)
		stats.State = worseState(stats.State, state)
		if state == StateOpen && !e.cooldownUntil.IsZero() {
			t := e.cooldownUntil
			if stats.CooldownUntil == nil || t.Before(*stats.CooldownUntil) {
				stats.CooldownUntil = &t
			}
		}
		e.mu.Unlock()
	}
	return stats, nil
}

// pruneRollupLocked removes rollup events older than the window cutoff.
// Caller must hold r.mu.
//
// A non-positive windowSeconds is treated as "no rollup tracking": all
// recorded events are dropped. Previously this branch silently early-returned
// without pruning, so a misconfigured WindowSeconds=0 deployment would
// accumulate rollup events forever (per-provider memory leak).
func (s *MemoryStore) pruneRollupLocked(r *rollupEntry, now time.Time, windowSeconds int) {
	if windowSeconds <= 0 {
		r.events = r.events[:0]
		return
	}
	cutoff := now.Add(-time.Duration(windowSeconds) * time.Second)
	pruned := r.events[:0]
	for _, ev := range r.events {
		if ev.at.After(cutoff) {
			pruned = append(pruned, ev)
		}
	}
	if len(pruned) > maxFailureWindowEntries {
		pruned = pruned[len(pruned)-maxFailureWindowEntries:]
	}
	r.events = pruned
}

// RecordKeyOpenedForRollup adds (or refreshes the timestamp on) the
// given openedKey inside the rollup sliding window for provider.
// Membership is unique by key — this method is idempotent on (provider,
// openedKey) within a single window — so callers can safely re-record
// the same key on every probe failure to keep a long-burn outage in
// the window without inflating the count.
func (s *MemoryStore) RecordKeyOpenedForRollup(_ context.Context, provider string, openedKey string, windowSeconds int) error {
	r := s.rollupEntryFor(provider)
	r.mu.Lock()
	defer r.mu.Unlock()
	now := s.now()
	found := false
	for i := range r.events {
		if r.events[i].key == openedKey {
			r.events[i].at = now
			found = true
			break
		}
	}
	if !found {
		r.events = append(r.events, rollupOpenEvent{at: now, key: openedKey})
	}
	s.pruneRollupLocked(r, now, windowSeconds)
	return nil
}

// RollupOpen reports whether the rollup sliding window for the provider
// currently holds at least `threshold` events.  Threshold == 0 disables
// the rollup (always returns false), so callers can safely call it
// regardless of whether the operator has opted in.
func (s *MemoryStore) RollupOpen(_ context.Context, provider string, threshold, windowSeconds int) (bool, int, error) {
	if threshold <= 0 {
		return false, 0, nil
	}
	r := s.rollupEntryFor(provider)
	r.mu.Lock()
	defer r.mu.Unlock()
	s.pruneRollupLocked(r, s.now(), windowSeconds)
	count := len(r.events)
	return count >= threshold, count, nil
}

// ClearRollupKey removes every event whose key matches openedKey from
// the rollup window for provider.  Used to drop a per-key breaker out
// of the rollup signal as soon as it recovers via half-open probe,
// so the rollup tracks "currently-degraded models" rather than
// "models that tripped in the last N seconds".
func (s *MemoryStore) ClearRollupKey(_ context.Context, provider string, openedKey string) error {
	r := s.rollupEntryFor(provider)
	r.mu.Lock()
	defer r.mu.Unlock()
	pruned := r.events[:0]
	for _, ev := range r.events {
		if ev.key != openedKey {
			pruned = append(pruned, ev)
		}
	}
	r.events = pruned
	return nil
}

// RolledUpKeys returns the set of keys currently inside the rollup
// window for provider.  Aged-out events are pruned before the snapshot
// is returned so callers see the live state.  Order is not guaranteed.
func (s *MemoryStore) RolledUpKeys(_ context.Context, provider string, windowSeconds int) ([]string, error) {
	r := s.rollupEntryFor(provider)
	r.mu.Lock()
	defer r.mu.Unlock()
	s.pruneRollupLocked(r, s.now(), windowSeconds)
	keys := make([]string, 0, len(r.events))
	for _, ev := range r.events {
		keys = append(keys, ev.key)
	}
	return keys, nil
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
func (s *MemoryStore) TryStartProbe(_ context.Context, key string) bool {
	e := s.entry(key)
	e.mu.Lock()
	defer e.mu.Unlock()
	if s.stateUnlocked(e) != StateHalfOpen {
		return false
	}
	now := s.now()
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
