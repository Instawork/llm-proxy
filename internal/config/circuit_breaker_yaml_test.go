package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCircuitBreaker_EnvConfigsLoad is a guardrail that ensures every
// environment YAML in `configs/` still parses and that production + staging
// stay in observe-only mode until someone explicitly flips them.
//
// This catches two classes of mistake:
//  1. Typos in the new `mode:` / `redis:` keys that would silently fall
//     back to defaults.
//  2. A future "make it live" change that sets mode=enforce in prod/staging
//     without an accompanying test update — forcing the author to
//     acknowledge the behavioural flip here.
func TestCircuitBreaker_EnvConfigsLoad(t *testing.T) {
	// Tests run from the package directory; configs live two levels up.
	configsDir, err := filepath.Abs(filepath.Join("..", "..", "configs"))
	if err != nil {
		t.Fatalf("resolve configs dir: %v", err)
	}
	if _, err := os.Stat(configsDir); err != nil {
		t.Skipf("configs dir not found (%s) — skipping", configsDir)
	}

	tests := []struct {
		file        string
		wantEnabled bool
		wantMode    string // "" skips the mode assertion
		wantBackend string // "" skips the backend assertion
	}{
		// Production and staging must stay observe-only until an
		// operator intentionally flips them and updates this test.
		{"production.yml", true, "log", "redis"},
		{"staging.yml", true, "log", "redis"},
		// Dev is free to enforce + use memory; just assert it parses.
		{"dev.yml", true, "", ""},
		// Base is the shared root; no circuit-breaker assertions.
		{"base.yml", false, "", ""},
	}

	basePath := filepath.Join(configsDir, "base.yml")

	for _, tc := range tests {
		tc := tc
		t.Run(tc.file, func(t *testing.T) {
			envPath := filepath.Join(configsDir, tc.file)
			// Env configs are delta-merged atop base.yml at runtime;
			// validating the merged result is what actually matters.
			paths := []string{basePath}
			if tc.file != "base.yml" {
				paths = append(paths, envPath)
			}
			cfg, err := LoadAndMergeConfigs(paths)
			if err != nil {
				t.Fatalf("load %s: %v", tc.file, err)
			}
			cb := cfg.Features.CircuitBreaker
			if cb.Enabled != tc.wantEnabled {
				t.Errorf("%s: cb.enabled want %v, got %v", tc.file, tc.wantEnabled, cb.Enabled)
			}
			if tc.wantMode != "" && cb.Mode != tc.wantMode {
				t.Errorf("%s: cb.mode want %q, got %q (if you intentionally flipped the rollout mode, update this test)",
					tc.file, tc.wantMode, cb.Mode)
			}
			if tc.wantBackend != "" && cb.Backend != tc.wantBackend {
				t.Errorf("%s: cb.backend want %q, got %q", tc.file, tc.wantBackend, cb.Backend)
			}
		})
	}
}

// TestCircuitBreaker_ProdAndStaging_RedisPinnedToDedicatedDB verifies that
// the shared Finch Redis cluster can't accidentally collide with the
// circuit-breaker namespace: we pin to a dedicated DB so a future operator
// mistake in ML_CACHE_URL can't start overwriting app cache keys.
func TestCircuitBreaker_ProdAndStaging_RedisPinnedToDedicatedDB(t *testing.T) {
	configsDir, err := filepath.Abs(filepath.Join("..", "..", "configs"))
	if err != nil {
		t.Fatalf("resolve configs dir: %v", err)
	}
	if _, err := os.Stat(configsDir); err != nil {
		t.Skipf("configs dir not found (%s) — skipping", configsDir)
	}

	basePath := filepath.Join(configsDir, "base.yml")
	for _, file := range []string{"production.yml", "staging.yml"} {
		cfg, err := LoadAndMergeConfigs([]string{basePath, filepath.Join(configsDir, file)})
		if err != nil {
			t.Fatalf("load %s: %v", file, err)
		}
		cb := cfg.Features.CircuitBreaker
		if cb.Redis == nil {
			t.Fatalf("%s: cb.redis must be populated for shared-cluster rollout", file)
		}
		if cb.Redis.DB == 0 {
			t.Errorf("%s: cb.redis.db must be explicitly non-zero so we don't collide with Finch (got 0)", file)
		}
		if cb.Redis.URL == "" {
			t.Errorf("%s: cb.redis.url must be set (expected `${REDIS_URL}` from ECS secret)", file)
		}
	}
}
