package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newRedisLimiterForTest(t *testing.T, cfg *config.YAMLConfig) (RateLimiter, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	cfg.Features.RateLimiting.Enabled = true
	cfg.Features.RateLimiting.Backend = "redis"
	cfg.Features.RateLimiting.Redis = &config.RedisConfig{Address: mr.Addr()}
	lim, err := NewRedisLimiter(cfg)
	if err != nil {
		t.Fatalf("NewRedisLimiter: %v", err)
	}
	return lim, mr
}

func TestRedisLimiterRequestsPerMinute(t *testing.T) {
	cfg := baseCfg()
	lim, _ := newRedisLimiterForTest(t, cfg)

	scope := ScopeKeys{Provider: "openai", Model: "gpt-4o", UserID: "u1"}
	now := time.Now()

	if res, _ := lim.CheckAndReserve(context.Background(), "1", scope, 10, now); !res.Allowed {
		t.Fatalf("first should be allowed")
	}
	if res, _ := lim.CheckAndReserve(context.Background(), "2", scope, 10, now); !res.Allowed {
		t.Fatalf("second should be allowed")
	}
	if res, _ := lim.CheckAndReserve(context.Background(), "3", scope, 10, now); res.Allowed {
		t.Fatalf("third should be blocked by minute limit")
	}
}

func TestRedisLimiterTokensPerMinute(t *testing.T) {
	cfg := baseCfg()
	lim, _ := newRedisLimiterForTest(t, cfg)

	scope := ScopeKeys{Provider: "openai", Model: "gpt-4o", UserID: "u2"}
	now := time.Now()

	if res, _ := lim.CheckAndReserve(context.Background(), "1", scope, 60, now); !res.Allowed {
		t.Fatalf("first should be allowed")
	}
	if res, _ := lim.CheckAndReserve(context.Background(), "2", scope, 50, now); res.Allowed {
		t.Fatalf("second should be blocked by token limit")
	}
}

// TestRedisLimiterCancelAfterMinuteRotationSkipsNewWindow guards the
// window-attribution fix: request A reserves in minute window W1, the key
// expires at the boundary, request B reserves in W2, and A's late 5xx cancel
// must not drain B's counters. The day window is still current, so A's day
// reservation is released.
func TestRedisLimiterCancelAfterMinuteRotationSkipsNewWindow(t *testing.T) {
	cfg := baseCfg()
	lim, mr := newRedisLimiterForTest(t, cfg)

	scope := ScopeKeys{UserID: "u-rotate"}
	t0 := time.Date(2026, 7, 24, 12, 0, 50, 0, time.UTC)

	resA, _ := lim.CheckAndReserve(context.Background(), "A", scope, 40, t0)
	require.True(t, resA.Allowed)

	// Cross the minute boundary: the W1 minute key (TTL 10s) expires.
	mr.FastForward(15 * time.Second)
	t1 := t0.Add(15 * time.Second)

	resB, _ := lim.CheckAndReserve(context.Background(), "B", scope, 40, t1)
	require.True(t, resB.Allowed)

	require.NoError(t, lim.Cancel(context.Background(), "A", scope, 40, t0, t1))

	// B's 40 minute tokens must still be counted: a further 70-token reserve
	// exceeds TPM=100 only if B's reservation survived the cancel.
	probe, _ := lim.CheckAndReserve(context.Background(), "probe", scope, 70, t1)
	assert.False(t, probe.Allowed, "B's minute reservation must survive A's late cancel")

	// The day window was still current, so A's day tokens were released:
	// B's 40 remain (A's 40 + B's 40 - A's cancel).
	dayTok := mr.HGet("rl:day:user:u-rotate", "tok")
	assert.Equal(t, "40", dayTok, "day window should hold only B's tokens after A's cancel")
}

func TestRedisLimiterAdjust(t *testing.T) {
	cfg := baseCfg()
	lim, _ := newRedisLimiterForTest(t, cfg)

	scope := ScopeKeys{Provider: "openai", Model: "gpt-4o", UserID: "u3"}
	now := time.Now()

	// Reserve 50 tokens
	res, _ := lim.CheckAndReserve(context.Background(), "1", scope, 50, now)
	if !res.Allowed {
		t.Fatalf("should be allowed")
	}

	// Adjust to refund 30 tokens
	err := lim.Adjust(context.Background(), "1", scope, -30, now, now)
	if err != nil {
		t.Fatalf("Adjust: %v", err)
	}

	// Now we should have 80 tokens left for the minute
	res2, _ := lim.CheckAndReserve(context.Background(), "2", scope, 70, now)
	if !res2.Allowed {
		t.Fatalf("should be allowed after refund")
	}
}

func TestRedisLimiterCancel(t *testing.T) {
	cfg := baseCfg()
	lim, _ := newRedisLimiterForTest(t, cfg)

	scope := ScopeKeys{Provider: "openai", Model: "gpt-4o", UserID: "u4"}
	now := time.Now()

	// Use up the 2 requests per minute
	lim.CheckAndReserve(context.Background(), "1", scope, 10, now)
	lim.CheckAndReserve(context.Background(), "2", scope, 10, now)

	// Third should be blocked
	res, _ := lim.CheckAndReserve(context.Background(), "3", scope, 10, now)
	if res.Allowed {
		t.Fatalf("third should be blocked")
	}

	// Cancel one request
	err := lim.Cancel(context.Background(), "1", scope, 10, now, now)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	// Now we should have 1 request left
	res2, _ := lim.CheckAndReserve(context.Background(), "4", scope, 10, now)
	if !res2.Allowed {
		t.Fatalf("should be allowed after cancel")
	}
}

func TestRedisLimiter_PerModelOverride_Minute(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg := config.GetDefaultYAMLConfig()
	cfg.Features.RateLimiting.Enabled = true
	cfg.Features.RateLimiting.Backend = "redis"
	cfg.Features.RateLimiting.Redis = &config.RedisConfig{Address: mr.Addr()}
	cfg.Features.RateLimiting.Limits = config.LimitsConfig{}
	cfg.Features.RateLimiting.Overrides.PerModel = map[string]config.LimitsConfig{
		"gpt-4o": {RequestsPerMinute: 1, TokensPerMinute: 50},
	}

	lim, err := NewRedisLimiter(cfg)
	require.NoError(t, err)

	scope := ScopeKeys{Model: "gpt-4o"}
	now := time.Now()

	res, _ := lim.CheckAndReserve(context.Background(), "1", scope, 1, now)
	require.True(t, res.Allowed)

	res, _ = lim.CheckAndReserve(context.Background(), "2", scope, 1, now)
	assert.False(t, res.Allowed)
}

func TestRedisLimiter_PerModelOverride_Day(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg := config.GetDefaultYAMLConfig()
	cfg.Features.RateLimiting.Enabled = true
	cfg.Features.RateLimiting.Backend = "redis"
	cfg.Features.RateLimiting.Redis = &config.RedisConfig{Address: mr.Addr()}
	cfg.Features.RateLimiting.Limits = config.LimitsConfig{
		RequestsPerMinute: 1000,
		TokensPerMinute:   1000000,
	}
	cfg.Features.RateLimiting.Overrides.PerModel = map[string]config.LimitsConfig{
		"gpt-4o": {RequestsPerDay: 2, TokensPerDay: 500},
	}

	lim, err := NewRedisLimiter(cfg)
	require.NoError(t, err)

	scope := ScopeKeys{Model: "gpt-4o"}
	now := time.Now()

	r1, _ := lim.CheckAndReserve(context.Background(), "1", scope, 1, now)
	require.True(t, r1.Allowed)
	r2, _ := lim.CheckAndReserve(context.Background(), "2", scope, 1, now)
	require.True(t, r2.Allowed)
	r3, _ := lim.CheckAndReserve(context.Background(), "3", scope, 1, now)
	assert.False(t, r3.Allowed)
}

func TestRedisLimiter_PerUserOverride(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg := baseCfg()
	cfg.Features.RateLimiting.Limits = config.LimitsConfig{}
	cfg.Features.RateLimiting.Backend = "redis"
	cfg.Features.RateLimiting.Redis = &config.RedisConfig{Address: mr.Addr()}
	cfg.Features.RateLimiting.Overrides.PerUser = map[string]config.LimitsConfig{
		"alice": {RequestsPerMinute: 1, TokensPerDay: 50},
	}

	lim, err := NewRedisLimiter(cfg)
	require.NoError(t, err)

	scope := ScopeKeys{UserID: "alice"}
	now := time.Now()
	res, _ := lim.CheckAndReserve(context.Background(), "1", scope, 1, now)
	require.True(t, res.Allowed)
	res, _ = lim.CheckAndReserve(context.Background(), "2", scope, 1, now)
	assert.False(t, res.Allowed)
}

func TestRedisLimiter_DynamicPerKeyOverride(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg := baseCfg()
	cfg.Features.RateLimiting.Limits = config.LimitsConfig{RequestsPerMinute: 1000}
	cfg.Features.RateLimiting.Backend = "redis"
	cfg.Features.RateLimiting.Redis = &config.RedisConfig{Address: mr.Addr()}

	lim, err := NewRedisLimiter(cfg)
	require.NoError(t, err)
	ov, ok := lim.(PerKeyOverridable)
	require.True(t, ok)
	ov.SetPerKeyOverride(func(keyID string) (config.LimitsConfig, bool) {
		if keyID == "iw:dynamic" {
			return config.LimitsConfig{RequestsPerMinute: 1}, true
		}
		return config.LimitsConfig{}, false
	})

	scope := ScopeKeys{APIKey: "iw:dynamic"}
	now := time.Now()
	res, err := lim.CheckAndReserve(context.Background(), "1", scope, 1, now)
	require.NoError(t, err)
	require.True(t, res.Allowed)
	res, err = lim.CheckAndReserve(context.Background(), "2", scope, 1, now)
	require.NoError(t, err)
	assert.False(t, res.Allowed)
}

func TestRedisLimiter_PerKeyTPMBlocksFirstRequestWhenEstimateExceedsLimit(t *testing.T) {
	mr := miniredis.RunT(t)
	cfg := baseCfg()
	cfg.Features.RateLimiting.Limits = config.LimitsConfig{RequestsPerMinute: 1000, TokensPerMinute: 0}
	cfg.Features.RateLimiting.Backend = "redis"
	cfg.Features.RateLimiting.Redis = &config.RedisConfig{Address: mr.Addr()}

	lim, err := NewRedisLimiter(cfg)
	require.NoError(t, err)
	ov, ok := lim.(PerKeyOverridable)
	require.True(t, ok)
	ov.SetPerKeyOverride(func(keyID string) (config.LimitsConfig, bool) {
		if keyID == "iw:tpm-cap" {
			return config.LimitsConfig{TokensPerMinute: 50}, true
		}
		return config.LimitsConfig{}, false
	})

	scope := ScopeKeys{APIKey: "iw:tpm-cap"}
	now := time.Now()
	res, err := lim.CheckAndReserve(context.Background(), "1", scope, 4800, now)
	require.NoError(t, err)
	assert.False(t, res.Allowed, "first request should be blocked when est tokens exceed per-key TPM")
}

func TestRedisLimiter_SnapshotReportsLiveCounters(t *testing.T) {
	cfg := baseCfg()
	lim, _ := newRedisLimiterForTest(t, cfg)

	snapshotter, ok := lim.(Snapshotter)
	require.True(t, ok, "redis limiter must implement Snapshotter")

	scope := ScopeKeys{Provider: "openai", Model: "gpt-4o", UserID: "snapuser"}
	now := time.Now()
	res, err := lim.CheckAndReserve(context.Background(), "1", scope, 17, now)
	require.NoError(t, err)
	require.True(t, res.Allowed)

	snap := snapshotter.Snapshot(now)
	assert.Equal(t, "redis", snap.Backend)
	require.NotNil(t, snap.Minute)

	got := snap.Minute.Counters["user:snapuser"]
	assert.Equal(t, 1, got.Requests)
	assert.Equal(t, 17, got.Tokens)

	global := snap.Minute.Counters["global"]
	assert.Equal(t, 1, global.Requests)
}

func TestNewRedisLimiter_NilRedisConfigErrors(t *testing.T) {
	cfg := config.GetDefaultYAMLConfig()
	cfg.Features.RateLimiting.Enabled = true
	cfg.Features.RateLimiting.Backend = "redis"
	cfg.Features.RateLimiting.Redis = nil
	_, err := NewRedisLimiter(cfg)
	assert.Error(t, err)
}
