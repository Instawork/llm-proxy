package ratelimit

import (
	"context"
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
	Adjust(ctx context.Context, id string, scope ScopeKeys, tokenDelta int, now time.Time) error

	// Cancel releases the effects of a prior reservation entirely (e.g., upstream
	// error). estTokens MUST be the same value passed to CheckAndReserve for this
	// reservation — the previous signature accepted only (id, scope, now) and
	// silently left the reserved tokens in place, causing under-credit when an
	// upstream error happened after reservation. Callers that genuinely don't
	// know the est tokens can pass 0; counters will only decrement the request.
	Cancel(ctx context.Context, id string, scope ScopeKeys, estTokens int, now time.Time) error
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
	return nil, nil
}
