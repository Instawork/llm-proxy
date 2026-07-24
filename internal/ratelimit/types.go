package ratelimit

import (
	"context"
	"fmt"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
)

// ScopeKeys identifies the dimensions used for applying limits.
// Empty fields are ignored.
type ScopeKeys struct {
	Provider string
	Model    string
	APIKey   string
	UserID   string
}

// ReservationResult contains the outcome of a reservation attempt.
type ReservationResult struct {
	Allowed           bool
	ReservationID     string
	RetryAfterSeconds int
	Reason            string
	Details           *LimitDetails
}

// LimitDetails describes which limit triggered and its characteristics
type LimitDetails struct {
	// ScopeKey is the specific scoped key that triggered the limit, e.g., "global",
	// "provider:openai", "model:gpt-4o", "key:abc...", or "user:123".
	ScopeKey string
	// Metric is either "requests" or "tokens".
	Metric string
	// Window indicates the time window of the exceeded limit: "minute" or "day".
	Window string
	// Limit is the configured maximum for the window (0 if unlimited/unknown).
	Limit int
	// Remaining is the best-effort estimate of remaining capacity at the time of denial.
	Remaining int
}

// CounterSnapshot holds live counter values for one scoped rate-limit key.
type CounterSnapshot struct {
	Requests int `json:"requests"`
	Tokens   int `json:"tokens"`
}

// LimitsSnapshot captures configured limits plus optional live counter state.
type LimitsSnapshot struct {
	Enabled   bool                      `json:"enabled"`
	Backend   string                    `json:"backend"`
	Limits    config.LimitsConfig       `json:"limits"`
	Overrides config.RateLimitOverrides `json:"overrides,omitempty"`
	Minute    *WindowSnapshot           `json:"minute,omitempty"`
	Day       *WindowSnapshot           `json:"day,omitempty"`
}

// WindowSnapshot is one rolling window's counters keyed by scope string.
type WindowSnapshot struct {
	WindowStart string                     `json:"window_start"`
	Counters    map[string]CounterSnapshot `json:"counters"`
}

// Snapshotter exposes live rate-limit counter state for observability dashboards.
type Snapshotter interface {
	Snapshot(now time.Time) LimitsSnapshot
}

// RateLimiter defines the minimum functionality for enforcing limits.
type RateLimiter interface {
	// CheckAndReserve attempts to atomically count 1 request and estTokens across
	// all applicable limits for the provided scope. If any limit would be exceeded,
	// the call returns Allowed=false and does not mutate counters.
	CheckAndReserve(ctx context.Context, id string, scope ScopeKeys, estTokens int, now time.Time) (ReservationResult, error)

	// Adjust reconciles a prior reservation by applying the token delta
	// (actual-estimated) across the same scope. Negative deltas credit back.
	//
	// reservedAt MUST be the time the reservation was made (the `now` passed
	// to CheckAndReserve). LLM responses routinely take longer than a minute,
	// so the reconcile often lands in a later window than the reservation;
	// applying the delta to the current window would then debit/credit other
	// requests' counters instead of this reservation's. Implementations skip
	// any window (minute/day) that has rotated since reservedAt — the
	// reservation expired with that window and there is nothing to reconcile.
	Adjust(ctx context.Context, id string, scope ScopeKeys, tokenDelta int, reservedAt, now time.Time) error

	// Cancel releases the effects of a prior reservation entirely (e.g., upstream
	// error). estTokens MUST be the same value passed to CheckAndReserve for this
	// reservation — the previous signature accepted only (id, scope, now) and
	// silently left the reserved tokens in place, causing under-credit when an
	// upstream error happened after reservation. Callers that genuinely don't
	// know the est tokens can pass 0; counters will only decrement the request.
	//
	// reservedAt has the same window-attribution semantics as in Adjust: a
	// cancel that arrives after the reservation's window rotated is a no-op
	// for that window rather than erasing other requests' reservations.
	Cancel(ctx context.Context, id string, scope ScopeKeys, estTokens int, reservedAt, now time.Time) error
}

// PerKeyOverrideFunc resolves dynamic per-key rate-limit overrides at request
// time (e.g. from the DynamoDB API-key record), keyed by the iw: API key.
// It returns ok=false when the key has no override, so static config /
// global defaults apply. Implementations must be safe for concurrent use and
// fast (cached) since they run on the hot path.
type PerKeyOverrideFunc func(keyID string) (config.LimitsConfig, bool)

// PerKeyOverridable is implemented by limiters that accept a dynamic per-key
// override source layered on top of static YAML overrides.
type PerKeyOverridable interface {
	SetPerKeyOverride(fn PerKeyOverrideFunc)
}

// Factory creates a RateLimiter based on configuration.
func Factory(cfg *config.YAMLConfig) (RateLimiter, error) {
	if cfg == nil || !cfg.Features.RateLimiting.Enabled {
		return nil, nil
	}
	backend := cfg.Features.RateLimiting.Backend
	if backend == "" || backend == "memory" {
		return NewMemoryLimiter(cfg), nil
	}
	if backend == "redis" {
		// Redis implementation added separately
		return NewRedisLimiter(cfg)
	}
	// An unknown backend with rate limiting enabled is a misconfiguration: a
	// typo like "reddis" previously returned (nil, nil) and silently disabled
	// enforcement. Fail fast at startup instead.
	return nil, fmt.Errorf("unknown rate limiting backend %q (expected \"memory\" or \"redis\")", backend)
}
