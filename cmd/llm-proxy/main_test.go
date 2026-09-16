package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/providers"
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
// path from deploy environment → binary → Redis.
func TestCircuitConfigFromYAML_ProductionYAMLExpandsEndToEnd(t *testing.T) {
	const want = "redis://:prodpw@cache.internal:6379/6"
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
	if cfg.Mode != "enforce" {
		t.Fatalf("production.yml cb.mode should be 'enforce' (circuit breaker actively enforcing in prod), got %q", cfg.Mode)
	}
	if cfg.Backend != "redis" {
		t.Fatalf("production.yml cb.backend should be 'redis', got %q", cfg.Backend)
	}
	if cfg.RedisDB != 5 {
		t.Fatalf("production.yml cb.redis.db should pin 5 (isolated from any co-tenant DB), got %d", cfg.RedisDB)
	}
	if !cfg.RedisDBSet {
		t.Fatal("production.yml cb.redis.db should be recorded as explicitly set")
	}
}

// TestProductionYAML_HasEstimationBlock guards against the production bug
// where rate_limiting had no estimation child: every field defaulted to
// zero, MaxSampleBytes=0 made EstimateRequestTokens treat any non-empty
// request body as too large to sample, and the model name was never
// extracted — silently disabling the cluster-wide cost-limit reservation
// added in #43 for any key with a cost limit configured, in addition to
// breaking rate-limit token estimation.
func TestProductionYAML_HasEstimationBlock(t *testing.T) {
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

	est := merged.Features.RateLimiting.Estimation
	if est.MaxSampleBytes <= 0 {
		t.Fatalf("production.yml rate_limiting.estimation.max_sample_bytes must be > 0, got %d", est.MaxSampleBytes)
	}
	if est.BytesPerToken <= 0 {
		t.Fatalf("production.yml rate_limiting.estimation.bytes_per_token must be > 0, got %d", est.BytesPerToken)
	}
	if est.CharsPerToken <= 0 {
		t.Fatalf("production.yml rate_limiting.estimation.chars_per_token must be > 0, got %d", est.CharsPerToken)
	}
}

// TestProductionYAML_PIIRedactSetsExplicitMaxBodyBytes guards against the
// production bug where pii_redact had no max_body_bytes, silently falling
// back to the 1 MiB middleware default. With fail_mode "closed", any body
// over that limit 503s instead of skipping redaction, and a caller that
// retries an oversized payload keeps hitting 503 on every attempt — not
// just once, and with no attached alert (see the Datadog monitor fix in
// the paired infrastructure PR).
func TestProductionYAML_PIIRedactSetsExplicitMaxBodyBytes(t *testing.T) {
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

	const oneMiB = 1024 * 1024
	got := merged.Features.PIIRedact.MaxBodyBytes
	if got <= oneMiB {
		t.Fatalf("production.yml pii_redact.max_body_bytes must be set above the 1 MiB default, got %d", got)
	}
}

// TestDeployedYAML_NoDevCORSOrigin guards against dev_cors_origin leaking
// into a deploy from base.yml: it enables credentialed CORS for the Vite
// origin on the admin API and puts the admin URL helpers into dev mode.
func TestDeployedYAML_NoDevCORSOrigin(t *testing.T) {
	configsDir, err := filepath.Abs(filepath.Join("..", "..", "configs"))
	if err != nil {
		t.Fatalf("resolve configs dir: %v", err)
	}
	for _, env := range []string{"production", "staging"} {
		merged, err := config.LoadAndMergeConfigs([]string{
			filepath.Join(configsDir, "base.yml"),
			filepath.Join(configsDir, env+".yml"),
		})
		if err != nil {
			t.Fatalf("load %s configs: %v", env, err)
		}
		if got := merged.Features.AdminDashboard.DevCORSOrigin; got != "" {
			t.Fatalf("%s.yml must not carry dev_cors_origin, got %q", env, got)
		}
	}
	merged, err := config.LoadAndMergeConfigs([]string{
		filepath.Join(configsDir, "base.yml"),
		filepath.Join(configsDir, "dev.yml"),
	})
	if err != nil {
		t.Fatalf("load dev configs: %v", err)
	}
	if merged.Features.AdminDashboard.DevCORSOrigin == "" {
		t.Fatal("dev.yml must keep dev_cors_origin for the Vite dev server")
	}
}

// TestSidecarProfile_WritesRollupsWithoutDashboard locks in the sidecar
// contract: the dashboard HTTP server is OFF, but rollup writing stays ON so
// sidecars publish usage/cost/etc to the shared Redis the standalone dashboard
// reads. Regression guard for the base+production+sidecar deep-merge plus the
// decoupling of rollups from admin_dashboard.enabled.
func TestSidecarProfile_WritesRollupsWithoutDashboard(t *testing.T) {
	t.Setenv("REDIS_URL", "redis://:pw@cache.internal:6379/6")

	configsDir, err := filepath.Abs(filepath.Join("..", "..", "configs"))
	if err != nil {
		t.Fatalf("resolve configs dir: %v", err)
	}
	merged, err := config.LoadAndMergeConfigs([]string{
		filepath.Join(configsDir, "base.yml"),
		filepath.Join(configsDir, "production.yml"),
		filepath.Join(configsDir, "sidecar.yml"),
	})
	if err != nil {
		t.Fatalf("load sidecar configs: %v", err)
	}

	admin := merged.Features.AdminDashboard
	if admin.Enabled {
		t.Fatal("sidecar: admin_dashboard.enabled must be false (no HTTP dashboard server)")
	}
	if !admin.Rollups.Enabled {
		t.Fatal("sidecar: admin_dashboard.rollups.enabled must stay true (inherited) so sidecars write rollups")
	}
	if rc := adminrollup.ConfigFromYAML(admin); !rc.Enabled {
		t.Fatal("sidecar: rollup store must be enabled even though the dashboard server is off")
	}
	if admin.Rollups.Redis == nil || admin.Rollups.Redis.DB != 6 {
		t.Fatalf("sidecar: rollups must target the shared Redis db 6, got %+v", admin.Rollups.Redis)
	}
}

type stubAPIKeyStore struct{}

func (stubAPIKeyStore) ValidateAndGetActualKey(context.Context, string) (string, string, error) {
	return "", "", nil
}

// TestAPIKeyStoreFailureIsFatal pins the fail-closed rule: with key
// management enabled, a nil store outside local dev must abort startup
// instead of silently serving provider routes without key validation.
func TestAPIKeyStoreFailureIsFatal(t *testing.T) {
	cfgWith := func(enabled bool) *config.YAMLConfig {
		yc := config.GetDefaultYAMLConfig()
		yc.Features.APIKeyManagement.Enabled = enabled
		return yc
	}

	tests := []struct {
		name     string
		enabled  bool
		store    providers.APIKeyStore
		localDev bool
		want     bool
	}{
		{name: "enabled, nil store, prod", enabled: true, store: nil, localDev: false, want: true},
		{name: "enabled, nil store, local dev", enabled: true, store: nil, localDev: true, want: false},
		{name: "disabled, nil store, prod", enabled: false, store: nil, localDev: false, want: false},
		{name: "enabled, store present, prod", enabled: true, store: stubAPIKeyStore{}, localDev: false, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := apiKeyStoreFailureIsFatal(cfgWith(tt.enabled), tt.store, tt.localDev); got != tt.want {
				t.Fatalf("apiKeyStoreFailureIsFatal() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsExplicitLocalDev(t *testing.T) {
	tests := []struct {
		env, allowDefault string
		want              bool
	}{
		{env: "", allowDefault: "", want: true},
		{env: "dev", allowDefault: "", want: true},
		{env: "local", allowDefault: "", want: true},
		{env: "production", allowDefault: "", want: false},
		{env: "staging", allowDefault: "", want: false},
		{env: "production", allowDefault: "1", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.env+"/allow="+tt.allowDefault, func(t *testing.T) {
			t.Setenv("ENVIRONMENT", tt.env)
			t.Setenv("LLM_PROXY_ALLOW_DEFAULT_CONFIG", tt.allowDefault)
			if got := isExplicitLocalDev(); got != tt.want {
				t.Fatalf("isExplicitLocalDev() = %v, want %v", got, tt.want)
			}
		})
	}
}
