package main

import (
	"path/filepath"
	"testing"

	"github.com/Instawork/llm-proxy/internal/config"
)

// TestCircuitConfigFromYAML_ExpandsRedisURLEnvVar nails down the env-var
// expansion contract for the circuit-breaker Redis URL: the raw YAML value
// `${REDIS_URL}` must be replaced by whatever the process env holds at
// boot, so the ECS-injected secret reaches the Redis client.
func TestCircuitConfigFromYAML_ExpandsRedisURLEnvVar(t *testing.T) {
	const want = "redis://:pw@cache.example.com:6379/5"
	t.Setenv("REDIS_URL", want)

	cbYAML := config.CircuitBreakerConfig{
		Enabled: true,
		Mode:    "log",
		Backend: "redis",
		Redis: &config.RedisConfig{
			URL:   "${REDIS_URL}",
			DB:    5,
			DBSet: true,
		},
	}

	cfg := circuitConfigFromYAML(cbYAML, false)

	if cfg.RedisURL != want {
		t.Fatalf("RedisURL want %q, got %q (os.ExpandEnv didn't fire)", want, cfg.RedisURL)
	}
	if cfg.RedisDB != 5 {
		t.Fatalf("RedisDB overlay lost: want 5, got %d", cfg.RedisDB)
	}
	if !cfg.RedisDBSet {
		t.Fatal("RedisDBSet overlay lost")
	}
}

// TestCircuitConfigFromYAML_ExpandsShellStyleVar verifies the `$VAR` form
// (no braces) is also honoured — os.ExpandEnv accepts both.
func TestCircuitConfigFromYAML_ExpandsShellStyleVar(t *testing.T) {
	t.Setenv("MY_REDIS", "redis://cache.example.com:6379/1")

	cfg := circuitConfigFromYAML(config.CircuitBreakerConfig{
		Enabled: true,
		Backend: "redis",
		Redis:   &config.RedisConfig{URL: "$MY_REDIS"},
	}, false)

	if cfg.RedisURL != "redis://cache.example.com:6379/1" {
		t.Fatalf("shell-style var not expanded; got %q", cfg.RedisURL)
	}
}

// TestCircuitConfigFromYAML_UnsetVarBecomesEmpty documents the fail-safe
// behaviour: an unset env var collapses to "" (os.ExpandEnv semantics),
// which then triggers `NewRedisStore`'s "address or url is required"
// error, which `initializeCircuitStore` catches and falls back to the
// memory store.  This path is what keeps a misconfigured sidecar from
// refusing to boot.
func TestCircuitConfigFromYAML_UnsetVarBecomesEmpty(t *testing.T) {
	// Guarantee the env var isn't set by an earlier test or the CI runner.
	t.Setenv("DEFINITELY_NOT_SET_ANYWHERE_12345", "")

	cfg := circuitConfigFromYAML(config.CircuitBreakerConfig{
		Enabled: true,
		Backend: "redis",
		Redis:   &config.RedisConfig{URL: "${DEFINITELY_NOT_SET_ANYWHERE_12345}"},
	}, false)

	if cfg.RedisURL != "" {
		t.Fatalf("unset env var should expand to empty string; got %q", cfg.RedisURL)
	}
}

// TestCircuitConfigFromYAML_LiteralURLPassesThrough confirms that a YAML
// value without any `$` tokens is returned unchanged — no false positives
// from the expansion pass.
func TestCircuitConfigFromYAML_LiteralURLPassesThrough(t *testing.T) {
	const literal = "redis://cache.example.com:6379/5"

	cfg := circuitConfigFromYAML(config.CircuitBreakerConfig{
		Enabled: true,
		Backend: "redis",
		Redis:   &config.RedisConfig{URL: literal},
	}, false)

	if cfg.RedisURL != literal {
		t.Fatalf("literal URL mutated by ExpandEnv: want %q, got %q", literal, cfg.RedisURL)
	}
}

// TestCircuitConfigFromYAML_ProductionYAMLExpandsEndToEnd is the full
// integration slice: load the real production.yml (merged over base.yml),
// set REDIS_URL in the environment, feed the parsed CircuitBreakerConfig
// through circuitConfigFromYAML, and confirm the expanded URL reaches
// circuit.Config.  If this test ever fails you have a real bug in the
// path from ECS → binary → Redis.
func TestCircuitConfigFromYAML_ProductionYAMLExpandsEndToEnd(t *testing.T) {
	const want = "redis://:prodpw@finch-cache.internal:6379/6"
	t.Setenv("REDIS_URL", want)

	configsDir, err := filepath.Abs(filepath.Join("..", "..", "configs"))
	if err != nil {
		t.Fatalf("resolve configs dir: %v", err)
	}
	merged, err := config.LoadAndMergeConfigs([]string{
		filepath.Join(configsDir, "base.yml"),
		filepath.Join(configsDir, "production.yml"),
	})
	if err != nil {
		t.Fatalf("load prod configs: %v", err)
	}

	cfg := circuitConfigFromYAML(merged.Features.CircuitBreaker, false)

	if cfg.RedisURL != want {
		t.Fatalf("production.yml REDIS_URL expansion broken: want %q, got %q", want, cfg.RedisURL)
	}
	if cfg.Mode != "log" {
		t.Fatalf("production.yml cb.mode should be 'log' until explicitly flipped, got %q", cfg.Mode)
	}
	if cfg.Backend != "redis" {
		t.Fatalf("production.yml cb.backend should be 'redis', got %q", cfg.Backend)
	}
	if cfg.RedisDB != 5 {
		t.Fatalf("production.yml cb.redis.db should pin 5 (isolated from Finch's DB), got %d", cfg.RedisDB)
	}
	if !cfg.RedisDBSet {
		t.Fatal("production.yml cb.redis.db should be recorded as explicitly set")
	}
}
