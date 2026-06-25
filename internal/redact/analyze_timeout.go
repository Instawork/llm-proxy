package redact

import (
	"context"
	"time"
)

// analyze100KiB is the body-size chunk used when scaling /analyze deadlines.
const analyze100KiB = 100 * 1024

// defaultAnalyzeTimeoutMax caps scaled deadlines when YAML leaves timeout_ms_max
// at zero but timeout_ms_per_100kb is set.
const defaultAnalyzeTimeoutMax = 30 * time.Second

type analyzeTimeoutCtxKey struct{}

// AnalyzeTimeoutConfig holds the base, per-100KiB increment, and cap for
// Presidio /analyze deadlines on a single provider request body.
type AnalyzeTimeoutConfig struct {
	Base      time.Duration
	Per100KiB time.Duration
	Max       time.Duration
}

// ComputeAnalyzeTimeout returns the /analyze deadline for a request body of
// bodyBytes. When Per100KiB is zero, only Base applies (with the package
// defaultAnalyzeTimeout fallback when Base is zero).
func ComputeAnalyzeTimeout(bodyBytes int, cfg AnalyzeTimeoutConfig) time.Duration {
	timeout := cfg.Base
	if timeout <= 0 {
		timeout = defaultAnalyzeTimeout
	}
	if cfg.Per100KiB > 0 && bodyBytes > 0 {
		chunks := bodyBytes / analyze100KiB
		timeout += time.Duration(chunks) * cfg.Per100KiB
	}
	max := cfg.Max
	if max <= 0 && cfg.Per100KiB > 0 {
		max = defaultAnalyzeTimeoutMax
	}
	if max > 0 && timeout > max {
		timeout = max
	}
	return timeout
}

// WithAnalyzeTimeout stashes a per-request /analyze deadline on ctx for the
// redactor to read. Middleware sets this from ComputeAnalyzeTimeout.
func WithAnalyzeTimeout(ctx context.Context, d time.Duration) context.Context {
	if d <= 0 {
		return ctx
	}
	return context.WithValue(ctx, analyzeTimeoutCtxKey{}, d)
}

// AnalyzeTimeoutFromContext returns the deadline stashed by WithAnalyzeTimeout,
// or fallback when none was set.
func AnalyzeTimeoutFromContext(ctx context.Context, fallback time.Duration) time.Duration {
	if v, ok := ctx.Value(analyzeTimeoutCtxKey{}).(time.Duration); ok && v > 0 {
		return v
	}
	if fallback > 0 {
		return fallback
	}
	return defaultAnalyzeTimeout
}
