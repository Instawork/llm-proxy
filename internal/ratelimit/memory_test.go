package ratelimit

import (
	"context"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
)

func baseCfg() *config.YAMLConfig {
	c := config.GetDefaultYAMLConfig()
	c.Features.RateLimiting.Enabled = true
	c.Features.RateLimiting.Backend = "memory"
	c.Features.RateLimiting.Limits = config.LimitsConfig{
		RequestsPerMinute: 2,
		TokensPerMinute:   100,
		RequestsPerDay:    5,
		TokensPerDay:      500,
	}
	return c
}

func TestMemoryLimiterRequestsPerMinute(t *testing.T) {
	cfg := baseCfg()
	lim := NewMemoryLimiter(cfg)

	scope := ScopeKeys{Provider: "openai", Model: "gpt-4o", UserID: "u1"}
	now := time.Now()
	// First two allowed
	if res, _ := lim.CheckAndReserve(context.Background(), "1", scope, 10, now); !res.Allowed {
		t.Fatalf("first should be allowed")
	}
	if res, _ := lim.CheckAndReserve(context.Background(), "2", scope, 10, now); !res.Allowed {
		t.Fatalf("second should be allowed")
	}
	// Third should be blocked
	if res, _ := lim.CheckAndReserve(context.Background(), "3", scope, 10, now); res.Allowed {
		t.Fatalf("third should be blocked by minute limit")
	}
}

func TestMemoryLimiterTokensPerMinute(t *testing.T) {
	cfg := baseCfg()
	lim := NewMemoryLimiter(cfg)
	scope := ScopeKeys{Provider: "openai", Model: "gpt-4o", UserID: "u2"}
	now := time.Now()

	if res, _ := lim.CheckAndReserve(context.Background(), "1", scope, 60, now); !res.Allowed {
		t.Fatalf("first should be allowed")
	}
	if res, _ := lim.CheckAndReserve(context.Background(), "2", scope, 50, now); res.Allowed {
		t.Fatalf("second should be blocked by token per minute limit")
	}
}

func TestMemoryLimiterDailyWindow(t *testing.T) {
	cfg := baseCfg()
	cfg.Features.RateLimiting.Limits.RequestsPerMinute = 1000
	cfg.Features.RateLimiting.Limits.TokensPerMinute = 100000
	lim := NewMemoryLimiter(cfg)
	scope := ScopeKeys{Provider: "openai", Model: "gpt-4o", UserID: "u3"}
	now := time.Now()

	for i := 0; i < 5; i++ {
		if res, _ := lim.CheckAndReserve(context.Background(), "x", scope, 10, now); !res.Allowed {
			t.Fatalf("request %d should be allowed", i)
		}
	}
	if res, _ := lim.CheckAndReserve(context.Background(), "y", scope, 10, now); res.Allowed {
		t.Fatalf("sixth should be blocked by day limit")
	}
}

func TestMemoryLimiterOverrides(t *testing.T) {
	cfg := baseCfg()
	cfg.Features.RateLimiting.Overrides.PerUser = map[string]config.LimitsConfig{
		"special": {RequestsPerMinute: 1},
	}
	lim := NewMemoryLimiter(cfg)
	scope := ScopeKeys{UserID: "special"}
	now := time.Now()

	if res, _ := lim.CheckAndReserve(context.Background(), "1", scope, 1, now); !res.Allowed {
		t.Fatalf("first should be allowed for override")
	}
	if res, _ := lim.CheckAndReserve(context.Background(), "2", scope, 1, now); res.Allowed {
		t.Fatalf("second should be blocked by override")
	}
}

func TestMemoryLimiterUserTokensPerMinute(t *testing.T) {
	cfg := baseCfg()
	cfg.Features.RateLimiting.Limits = config.LimitsConfig{}
	cfg.Features.RateLimiting.Overrides.PerUser = map[string]config.LimitsConfig{
		"tu": {TokensPerMinute: 50},
	}
	lim := NewMemoryLimiter(cfg)
	scope := ScopeKeys{UserID: "tu"}
	now := time.Now()

	if res, _ := lim.CheckAndReserve(context.Background(), "1", scope, 40, now); !res.Allowed {
		t.Fatalf("first should be allowed")
	}
	if res, _ := lim.CheckAndReserve(context.Background(), "2", scope, 20, now); res.Allowed {
		t.Fatalf("second should be blocked by user token override")
	}
}

func TestMemoryLimiterUserRequestsPerDay(t *testing.T) {
	cfg := baseCfg()
	cfg.Features.RateLimiting.Limits = config.LimitsConfig{}
	cfg.Features.RateLimiting.Overrides.PerUser = map[string]config.LimitsConfig{
		"ud": {RequestsPerDay: 2},
	}
	lim := NewMemoryLimiter(cfg)
	scope := ScopeKeys{UserID: "ud"}
	now := time.Now()

	if res, _ := lim.CheckAndReserve(context.Background(), "1", scope, 1, now); !res.Allowed {
		t.Fatalf("first should be allowed")
	}
	if res, _ := lim.CheckAndReserve(context.Background(), "2", scope, 1, now); !res.Allowed {
		t.Fatalf("second should be allowed")
	}
	if res, _ := lim.CheckAndReserve(context.Background(), "3", scope, 1, now); res.Allowed {
		t.Fatalf("third should be blocked by user daily request override")
	}
}

func TestMemoryLimiterDailyWindowReset(t *testing.T) {
	cfg := baseCfg()
	// Make minute windows effectively unlimited so only day limits apply
	cfg.Features.RateLimiting.Limits.RequestsPerMinute = 100000
	cfg.Features.RateLimiting.Limits.TokensPerMinute = 10000000
	cfg.Features.RateLimiting.Limits.RequestsPerDay = 2
	cfg.Features.RateLimiting.Limits.TokensPerDay = 100

	lim := NewMemoryLimiter(cfg)
	scope := ScopeKeys{UserID: "u-reset"}

	// Choose a stable reference time
	day1 := time.Date(2025, 1, 2, 10, 0, 0, 0, time.UTC)

	// Consume daily request limit
	if res, _ := lim.CheckAndReserve(context.Background(), "r1", scope, 10, day1); !res.Allowed {
		t.Fatalf("first request should be allowed on day1")
	}
	if res, _ := lim.CheckAndReserve(context.Background(), "r2", scope, 10, day1); !res.Allowed {
		t.Fatalf("second request should be allowed on day1")
	}
	if res, _ := lim.CheckAndReserve(context.Background(), "r3", scope, 10, day1); res.Allowed {
		t.Fatalf("third request should be blocked on day1 by daily request limit")
	}

	// Advance to next day and verify counters reset
	day2 := day1.Add(25 * time.Hour)
	if res, _ := lim.CheckAndReserve(context.Background(), "r4", scope, 10, day2); !res.Allowed {
		t.Fatalf("request should be allowed on day2 after daily reset")
	}

	// Test tokens-per-day reset as well: consume up to 100 then block, then allow next day
	// Finish consuming tokens on day2
	if res, _ := lim.CheckAndReserve(context.Background(), "t1", scope, 90, day2); !res.Allowed {
		t.Fatalf("token reservation should be allowed to reach daily token limit on day2")
	}
	if res, _ := lim.CheckAndReserve(context.Background(), "t2", scope, 1, day2); res.Allowed {
		t.Fatalf("token reservation should be blocked after exceeding daily token limit on day2")
	}

	day3 := day2.Add(25 * time.Hour)
	if res, _ := lim.CheckAndReserve(context.Background(), "t3", scope, 50, day3); !res.Allowed {
		t.Fatalf("token reservation should be allowed on day3 after daily reset")
	}
}

func TestMemoryLimiterPerAPIKeyMinuteAndDay(t *testing.T) {
	cfg := baseCfg()
	// Unlimited globals; constrain per-key. Use separate keys to isolate minute vs day windows
	cfg.Features.RateLimiting.Limits = config.LimitsConfig{}
	cfg.Features.RateLimiting.Overrides.PerKey = map[string]config.LimitsConfig{
		// k1: test minute limits
		"k1": {RequestsPerMinute: 1, TokensPerMinute: 30},
		// k2: test day requests only
		"k2": {RequestsPerDay: 2},
		// k3: test day tokens only (requests unlimited)
		"k3": {TokensPerDay: 50},
	}
	lim := NewMemoryLimiter(cfg)
	now := time.Now()

	// Minute requests (key k1)
	if res, _ := lim.CheckAndReserve(context.Background(), "m1", ScopeKeys{APIKey: "k1"}, 1, now); !res.Allowed {
		t.Fatalf("first request per minute for key should be allowed")
	}
	if res, _ := lim.CheckAndReserve(context.Background(), "m2", ScopeKeys{APIKey: "k1"}, 1, now); res.Allowed {
		t.Fatalf("second request per minute for key should be blocked")
	}

	// Minute tokens (key k1, rotate minute)
	now2 := now.Add(61 * time.Second)
	if res, _ := lim.CheckAndReserve(context.Background(), "t1", ScopeKeys{APIKey: "k1"}, 25, now2); !res.Allowed {
		t.Fatalf("first token reservation per minute should be allowed")
	}
	if res, _ := lim.CheckAndReserve(context.Background(), "t2", ScopeKeys{APIKey: "k1"}, 10, now2); res.Allowed {
		t.Fatalf("second token reservation per minute should be blocked")
	}

	// Day requests (key k2) across different minutes within same day
	day := now.Truncate(24 * time.Hour).Add(24 * time.Hour)
	if res, _ := lim.CheckAndReserve(context.Background(), "d1", ScopeKeys{APIKey: "k2"}, 1, day); !res.Allowed {
		t.Fatalf("first day request per key should be allowed")
	}
	if res, _ := lim.CheckAndReserve(context.Background(), "d2", ScopeKeys{APIKey: "k2"}, 1, day.Add(2*time.Minute)); !res.Allowed {
		t.Fatalf("second day request per key should be allowed")
	}
	if res, _ := lim.CheckAndReserve(context.Background(), "d3", ScopeKeys{APIKey: "k2"}, 1, day.Add(4*time.Minute)); res.Allowed {
		t.Fatalf("third day request per key should be blocked")
	}

	// Day tokens (key k3) across different minutes within same day
	if res, _ := lim.CheckAndReserve(context.Background(), "dt1", ScopeKeys{APIKey: "k3"}, 45, day.Add(6*time.Minute)); !res.Allowed {
		t.Fatalf("first day token reservation per key should be allowed")
	}
	if res, _ := lim.CheckAndReserve(context.Background(), "dt2", ScopeKeys{APIKey: "k3"}, 10, day.Add(8*time.Minute)); res.Allowed {
		t.Fatalf("second day token reservation per key should be blocked")
	}
}
