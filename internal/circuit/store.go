package circuit

import "context"

// Store abstracts the shared state backend for the circuit breaker.  Both the
// in-memory and Redis implementations satisfy this interface.
type Store interface {
	// GetState returns the current circuit state for the given provider.
	GetState(ctx context.Context, provider string) (State, error)

	// RecordTerminalFailure records one terminal degraded failure for provider.
	// It returns the NEW state after recording, which may have just become Open
	// if the failure pushed the count past the threshold.
	RecordTerminalFailure(ctx context.Context, provider string) (State, error)

	// RecordSuccess is called when a half-open probe succeeds.  It transitions
	// the circuit to Closed and resets all failure counters.
	RecordSuccess(ctx context.Context, provider string) error

	// RecordProbeFailed is called when a half-open probe fails.  It
	// transitions the circuit back to Open for another full cooldown period.
	RecordProbeFailed(ctx context.Context, provider string) error

	// GetStats returns health statistics for the provider (used in /health).
	GetStats(ctx context.Context, provider string) (*ProviderStats, error)
}

// Factory returns the appropriate Store implementation based on the Backend
// field of cfg.  "redis" → RedisStore, anything else → MemoryStore.
func Factory(cfg Config) (Store, error) {
	if cfg.Backend == "redis" {
		return NewRedisStore(cfg)
	}
	return NewMemoryStore(cfg), nil
}
