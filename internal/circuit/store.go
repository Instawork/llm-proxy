package circuit

import "context"

// Store abstracts the shared state backend for the circuit breaker.  Both the
// in-memory and Redis implementations satisfy this interface.
//
// The `key` argument names a single state-machine slot.  Historically this
// was the provider name (e.g. "gemini"); since per-model keying landed it
// is typically `<provider>:<model>` (e.g. "gemini:gemini-2.5-pro-preview"),
// falling back to plain `<provider>` when the model cannot be extracted.
// The Store treats it as opaque — only the Transport interprets the
// composition.
type Store interface {
	// GetState returns the current circuit state for the given key.
	GetState(ctx context.Context, key string) (State, error)

	// RecordTerminalFailure records one terminal degraded failure for key.
	// It returns the NEW state after recording, which may have just become
	// Open if the failure pushed the count past the threshold, plus a
	// boolean openedNow indicating whether THIS call is the one that
	// transitioned the breaker Closed → Open.  Callers use openedNow to
	// drive at-most-once side effects (e.g. recording a per-provider
	// rollup event) without needing a separate compare-and-swap.
	RecordTerminalFailure(ctx context.Context, key string) (newState State, openedNow bool, err error)

	// RecordSuccess is called when a half-open probe succeeds.  It transitions
	// the circuit to Closed and resets all failure counters.
	RecordSuccess(ctx context.Context, key string) error

	// RecordProbeFailed is called when a half-open probe fails.  It
	// transitions the circuit back to Open for another full cooldown period.
	RecordProbeFailed(ctx context.Context, key string) error

	// ForceOpen transitions key straight to Open for one cooldown period,
	// bypassing the failure-count threshold.  It is for authoritative,
	// non-transient failures where a SINGLE event proves the breaker should
	// trip immediately — e.g. OpenAI 429 insufficient_quota, a billing cap
	// that makes every request fail until an operator tops up.  Opening the
	// bare-provider key this way fast-fails the whole provider, and recovery
	// then rides the normal Open → HalfOpen → single-probe → Closed lifecycle:
	// after cooldownSeconds exactly one probe re-tests the upstream; success
	// closes it, failure re-opens for another cooldown.  Idempotent / safe to
	// call when already Open (it refreshes the cooldown).
	ForceOpen(ctx context.Context, key string, cooldownSeconds int) error

	// GetStats returns health statistics for the key (used in /health).
	GetStats(ctx context.Context, key string) (*ProviderStats, error)
}

// RollupRecorder is an optional capability that Stores can implement to
// support the provider-level rollup signal.  It is kept separate from the
// core Store interface so adding the rollup never breaks an external
// implementer of Store: callers that need rollup type-assert to
// RollupRecorder and silently skip the feature when the assertion fails.
//
// Both the bundled MemoryStore and RedisStore implement this interface.
type RollupRecorder interface {
	// RecordKeyOpenedForRollup appends a single `key just opened` event to
	// the rollup sliding window for `provider`.  Implementations dedupe by
	// (timestamp, key) where possible so duplicate events from concurrent
	// callers cannot artificially inflate the rollup counter.
	RecordKeyOpenedForRollup(ctx context.Context, provider string, openedKey string, windowSeconds int) error

	// RollupOpen reports whether the per-provider rollup window currently
	// holds at least `threshold` distinct key-open events.  The transport
	// consults this in addition to the per-key state so a wholesale
	// provider outage (multiple model breakers tripping concurrently)
	// fast-fails every model for the provider, not just the keys that
	// happened to trip.
	//
	RollupOpen(ctx context.Context, provider string, threshold, windowSeconds int) (open bool, count int, err error)

	// ClearRollupKey removes openedKey from the rollup window for
	// `provider`.  Called by the transport when a per-key half-open
	// probe succeeds, so the rollup signal drops back to Closed in step
	// with the per-key recovery instead of waiting for the rollup's own
	// time window to expire.  Without this, a cooldown shorter than the
	// rollup window would leave rollup-open even after every per-key
	// breaker has recovered.
	ClearRollupKey(ctx context.Context, provider string, openedKey string) error

	// RolledUpKeys returns the set of currently-tracked open keys for
	// `provider` inside the rollup window.  Used by /health to expose
	// "which models are degraded right now" without callers having to
	// enumerate every (provider, model) pair themselves.  Order is not
	// guaranteed.  Implementations prune aged-out entries before
	// returning so the answer reflects the live window.
	RolledUpKeys(ctx context.Context, provider string, windowSeconds int) ([]string, error)
}

// Factory returns the appropriate Store implementation based on the Backend
// field of cfg.  "redis" → RedisStore, anything else → MemoryStore.
func Factory(cfg Config) (Store, error) {
	if cfg.Backend == "redis" {
		return NewRedisStore(cfg)
	}
	return NewMemoryStore(cfg), nil
}
