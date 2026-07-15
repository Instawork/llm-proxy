package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeYAML(t *testing.T, dir, name string, body string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	full := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
	return full
}

func TestLoadEnvironmentConfig_DefaultsAndOverlay(t *testing.T) {
	tmpRoot := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	require.NoError(t, os.Chdir(tmpRoot))

	configDir := filepath.Join(tmpRoot, "configs")
	writeYAML(t, configDir, "base.yml", `
enabled: true
features:
  cost_tracking:
    enabled: false
providers:
  openai:
    enabled: true
    models:
      gpt-4o:
        enabled: true
`)
	writeYAML(t, configDir, "qa.yml", `
providers:
  openai:
    models:
      gpt-4o:
        enabled: true
        aliases: [g4]
`)

	t.Setenv("ENVIRONMENT", "qa")
	cfg, err := LoadEnvironmentConfig()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.True(t, cfg.Providers["openai"].Models["gpt-4o"].Enabled)
	require.Contains(t, cfg.Providers["openai"].Models["gpt-4o"].Aliases, "g4")
}

func TestLoadEnvironmentConfig_ConfigProfileOverlay(t *testing.T) {
	tmpRoot := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	require.NoError(t, os.Chdir(tmpRoot))

	configDir := filepath.Join(tmpRoot, "configs")
	writeYAML(t, configDir, "base.yml", `
enabled: true
features:
  pii_redact:
    enabled: false
  admin_dashboard:
    enabled: false
providers:
  openai:
    enabled: true
`)
	writeYAML(t, configDir, "production.yml", `
features:
  pii_redact:
    enabled: true
    analyzer_url: "http://localhost:3000"
  admin_dashboard:
    enabled: true
  circuit_breaker:
    enabled: true
    failure_threshold: 5
    window_seconds: 120
    cooldown_seconds: 300
`)
	writeYAML(t, configDir, "sidecar.yml", `
features:
  pii_redact:
    enabled: false
  admin_dashboard:
    enabled: false
`)

	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("LLM_PROXY_CONFIG_PROFILE", "sidecar")
	cfg, err := LoadEnvironmentConfig()
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.False(t, cfg.Features.PIIRedact.Enabled)
	assert.False(t, cfg.Features.AdminDashboard.Enabled)
	assert.True(t, cfg.Features.CircuitBreaker.Enabled)
}

func TestLoadEnvironmentConfig_MissingConfigProfileErrors(t *testing.T) {
	tmpRoot := t.TempDir()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	require.NoError(t, os.Chdir(tmpRoot))

	configDir := filepath.Join(tmpRoot, "configs")
	writeYAML(t, configDir, "base.yml", `
enabled: true
providers:
  openai:
    enabled: true
`)

	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("LLM_PROXY_CONFIG_PROFILE", "missing-profile")
	_, err = LoadEnvironmentConfig()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "missing-profile")
}

func TestLoadEnvironmentConfig_DefaultEnvIsDev(t *testing.T) {
	tmpRoot := t.TempDir()
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	require.NoError(t, os.Chdir(tmpRoot))

	configDir := filepath.Join(tmpRoot, "configs")
	writeYAML(t, configDir, "base.yml", `
enabled: true
providers:
  openai:
    enabled: true
`)
	t.Setenv("ENVIRONMENT", "")
	cfg, err := LoadEnvironmentConfig()
	require.NoError(t, err)
	require.NotNil(t, cfg)
}

func TestLoadEnvironmentConfig_BadBase_Errors(t *testing.T) {
	tmpRoot := t.TempDir()
	cwd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	require.NoError(t, os.Chdir(tmpRoot))

	configDir := filepath.Join(tmpRoot, "configs")
	writeYAML(t, configDir, "base.yml", `not: [ valid yaml`)
	_, err := LoadEnvironmentConfig()
	assert.Error(t, err)
}

func TestSaveYAMLConfig_RoundTrip(t *testing.T) {
	cfg := GetDefaultYAMLConfig()
	dir := t.TempDir()
	out := filepath.Join(dir, "out.yml")
	require.NoError(t, cfg.SaveYAMLConfig(out))

	st, err := os.Stat(out)
	require.NoError(t, err)
	assert.Greater(t, st.Size(), int64(0))

	loaded, err := LoadYAMLConfig(out)
	require.NoError(t, err)
	require.NotNil(t, loaded)
}

func TestSaveYAMLConfig_BadPath(t *testing.T) {
	cfg := GetDefaultYAMLConfig()
	err := cfg.SaveYAMLConfig(filepath.Join(t.TempDir(), "nope", "missing", "out.yml"))
	assert.Error(t, err)
}

func TestLoadYAMLConfig_MissingFile_ReturnsDefaults(t *testing.T) {
	cfg, err := LoadYAMLConfig(filepath.Join(t.TempDir(), "does-not-exist.yml"))
	require.NoError(t, err)
	require.NotNil(t, cfg)
}

func TestLoadYAMLConfig_BadYAML(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yml")
	require.NoError(t, os.WriteFile(bad, []byte("\t\t- not valid"), 0o644))
	_, err := LoadYAMLConfig(bad)
	assert.Error(t, err)
}

func TestLoadYAMLConfig_InvalidConfig_NoProviders(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.yml")
	require.NoError(t, os.WriteFile(p, []byte("enabled: true\n"), 0o644))
	_, err := LoadYAMLConfig(p)
	assert.Error(t, err)
}

func TestLoadAndMergeConfigs_MultipleFiles(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	overlay := filepath.Join(dir, "overlay.yml")

	require.NoError(t, os.WriteFile(base, []byte(`
enabled: true
providers:
  openai:
    enabled: true
    models:
      gpt-4o:
        enabled: true
`), 0o644))
	require.NoError(t, os.WriteFile(overlay, []byte(`
providers:
  openai:
    models:
      gpt-4o:
        aliases: [g4]
`), 0o644))

	merged, err := LoadAndMergeConfigs([]string{base, overlay})
	require.NoError(t, err)
	require.NotNil(t, merged)
	require.Contains(t, merged.Providers["openai"].Models["gpt-4o"].Aliases, "g4")
}

func TestLoadAndMergeConfigs_NoFilesErrors(t *testing.T) {
	_, err := LoadAndMergeConfigs(nil)
	assert.Error(t, err)
}

func TestLoadAndMergeConfigs_BadOverlayErrors(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	overlay := filepath.Join(dir, "bad.yml")
	require.NoError(t, os.WriteFile(base, []byte(`enabled: true
providers:
  openai:
    enabled: true
`), 0o644))
	require.NoError(t, os.WriteFile(overlay, []byte("\t- bad"), 0o644))
	_, err := LoadAndMergeConfigs([]string{base, overlay})
	assert.Error(t, err)
}

func TestLoadAndMergeConfigs_BadBaseErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yml")
	require.NoError(t, os.WriteFile(bad, []byte("\t- bad"), 0o644))
	_, err := LoadAndMergeConfigs([]string{bad})
	assert.Error(t, err)
}

func TestLoadAndMergeConfigs_OneFile(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	require.NoError(t, os.WriteFile(base, []byte(`enabled: true
providers:
  openai:
    enabled: true
`), 0o644))
	cfg, err := LoadAndMergeConfigs([]string{base})
	require.NoError(t, err)
	require.NotNil(t, cfg)
}

func TestGetAllTransports_FeatureDisabled(t *testing.T) {
	c := &YAMLConfig{}
	assert.Nil(t, c.GetAllTransports())
}

func TestGetAllTransports_FeatureEnabled(t *testing.T) {
	c := &YAMLConfig{Features: FeaturesConfig{CostTracking: CostTrackingConfig{
		Enabled:    true,
		Transports: []TransportConfig{{Type: "file", File: &FileTransportConfig{Path: "/tmp/x"}}},
	}}}
	assert.Len(t, c.GetAllTransports(), 1)
}

// TestRealEnvConfigs_FeatureKeysNotMisnestedUnderProviders guards against
// regressions where a `providers:` block in an environment YAML accidentally
// captures keys that belong under `features:` — specifically `circuit_breaker`
// and `rate_limiting`. Because YAMLConfig.Providers is a loose
// `map[string]ProviderConfig`, the YAML parser will silently accept those
// keys as phantom providers and reset the corresponding features to their
// zero values (disabled) on merge, with no validation error.
//
// The assertions are: after merging each env config onto base.yml,
//  1. the expected feature toggles in `features.*` are populated, AND
//  2. there are no phantom `Providers["circuit_breaker"]` or
//     `Providers["rate_limiting"]` entries.
func TestRealEnvConfigs_FeatureKeysNotMisnestedUnderProviders(t *testing.T) {
	configsDir, err := filepath.Abs(filepath.Join("..", "..", "configs"))
	require.NoError(t, err)
	if _, err := os.Stat(configsDir); err != nil {
		t.Skipf("configs dir not found (%s) — skipping", configsDir)
	}

	basePath := filepath.Join(configsDir, "base.yml")
	tests := []struct {
		file               string
		wantCBEnabled      bool
		wantRLEnabled      bool
		wantBedrockEnabled bool // expected providers.bedrock.enabled after merge
	}{
		{"dev.yml", true, true, true},
		{"staging.yml", true, false, false},
		{"production.yml", true, false, false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.file, func(t *testing.T) {
			envPath := filepath.Join(configsDir, tc.file)
			cfg, err := LoadAndMergeConfigs([]string{basePath, envPath})
			require.NoError(t, err, "load %s", tc.file)

			assert.Equal(t, tc.wantCBEnabled, cfg.Features.CircuitBreaker.Enabled,
				"%s: features.circuit_breaker.enabled — likely mis-nested under providers:", tc.file)
			assert.Equal(t, tc.wantRLEnabled, cfg.Features.RateLimiting.Enabled,
				"%s: features.rate_limiting.enabled — likely mis-nested under providers:", tc.file)

			if tc.file == "dev.yml" {
				assert.True(t, cfg.Features.PIIRedact.Enabled,
					"%s: features.pii_redact.enabled expected true for local dev", tc.file)
				assert.NotEmpty(t, cfg.Features.PIIRedact.AnalyzerURL,
					"%s: features.pii_redact.analyzer_url required when enabled", tc.file)
			}

			_, hasFakeCB := cfg.Providers["circuit_breaker"]
			assert.False(t, hasFakeCB,
				"%s: providers.circuit_breaker is a phantom provider — circuit_breaker belongs under features:", tc.file)
			_, hasFakeRL := cfg.Providers["rate_limiting"]
			assert.False(t, hasFakeRL,
				"%s: providers.rate_limiting is a phantom provider — rate_limiting belongs under features:", tc.file)

			if tc.wantBedrockEnabled {
				assert.True(t, cfg.Providers["bedrock"].Enabled,
					"%s: providers.bedrock.enabled expected true after merge", tc.file)
			}
		})
	}
}

// TestDevConfig_PerUserOverridesNotMisnested guards against the regression
// where features.rate_limiting.redis.per_user (which does not exist on the
// RedisConfig schema) silently swallowed dev-only user overrides. The
// correct anchor is features.rate_limiting.overrides.per_user. This test
// also asserts the wider class of bug: any per_user overrides defined in
// dev.yml are actually reachable on the merged RateLimitOverrides struct.
func TestDevConfig_PerUserOverridesNotMisnested(t *testing.T) {
	configsDir, err := filepath.Abs(filepath.Join("..", "..", "configs"))
	require.NoError(t, err)
	if _, err := os.Stat(configsDir); err != nil {
		t.Skipf("configs dir not found (%s) — skipping", configsDir)
	}

	basePath := filepath.Join(configsDir, "base.yml")
	devPath := filepath.Join(configsDir, "dev.yml")
	cfg, err := LoadAndMergeConfigs([]string{basePath, devPath})
	require.NoError(t, err)

	// dev.yml defines an `example-user` override at the correct anchor.
	override, ok := cfg.Features.RateLimiting.Overrides.PerUser["example-user"]
	assert.True(t, ok, "features.rate_limiting.overrides.per_user.example-user missing — likely mis-nested under redis:")
	if ok {
		assert.Equal(t, 5, override.RequestsPerMinute, "per_user override decoded with wrong limits")
		assert.Equal(t, 2000, override.TokensPerMinute)
	}
}

func TestRealEnvConfigs_DevAllowsBYOKeys(t *testing.T) {
	configsDir, err := filepath.Abs(filepath.Join("..", "..", "configs"))
	require.NoError(t, err)
	if _, err := os.Stat(configsDir); err != nil {
		t.Skipf("configs dir not found (%s) — skipping", configsDir)
	}

	basePath := filepath.Join(configsDir, "base.yml")
	devPath := filepath.Join(configsDir, "dev.yml")
	cfg, err := LoadAndMergeConfigs([]string{basePath, devPath})
	require.NoError(t, err)

	assert.True(t, cfg.Features.BYOKeys.Enabled,
		"dev llm-proxy must allow BYO provider keys")
}

func TestRealEnvConfigs_ProductionStandalonePIIEnabled(t *testing.T) {
	configsDir, err := filepath.Abs(filepath.Join("..", "..", "configs"))
	require.NoError(t, err)
	if _, err := os.Stat(configsDir); err != nil {
		t.Skipf("configs dir not found (%s) — skipping", configsDir)
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	require.NoError(t, os.Chdir(repoRoot))

	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("LLM_PROXY_CONFIG_PROFILE", "")
	cfg, err := LoadEnvironmentConfig()
	require.NoError(t, err)

	assert.True(t, cfg.Features.PIIRedact.Enabled,
		"standalone production llm-proxy must enable PII redaction")
	assert.True(t, cfg.Features.RedactAPI.Enabled,
		"standalone production must expose POST /redact")
	assert.False(t, cfg.Features.BYOKeys.Enabled,
		"standalone production must reject BYO provider keys")
}

func TestRealEnvConfigs_ProductionSidecarProfile(t *testing.T) {
	configsDir, err := filepath.Abs(filepath.Join("..", "..", "configs"))
	require.NoError(t, err)
	if _, err := os.Stat(configsDir); err != nil {
		t.Skipf("configs dir not found (%s) — skipping", configsDir)
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(cwd) })
	require.NoError(t, os.Chdir(repoRoot))

	t.Setenv("ENVIRONMENT", "production")
	t.Setenv("LLM_PROXY_CONFIG_PROFILE", "sidecar")
	cfg, err := LoadEnvironmentConfig()
	require.NoError(t, err)

	assert.True(t, cfg.Features.CircuitBreaker.Enabled,
		"production infra (circuit breaker) must survive the sidecar profile overlay")
	assert.False(t, cfg.Features.PIIRedact.Enabled,
		"sidecar profile must disable PII redaction")
	assert.False(t, cfg.Features.RedactAPI.Enabled,
		"sidecar profile must disable POST /redact")
	assert.False(t, cfg.Features.IDGate.Enabled,
		"sidecar profile must disable ID gate")
	assert.False(t, cfg.Features.AdminDashboard.Enabled,
		"sidecar profile must disable the admin dashboard")
	assert.Equal(t, "none", cfg.Features.History.Backend,
		"sidecar profile must disable row-history writes")
	assert.Equal(t, "sidecar", cfg.Features.History.Role,
		"sidecar profile must set history.role=sidecar")
	assert.True(t, cfg.Features.BYOKeys.Enabled,
		"sidecar profile must allow BYO provider keys")
	assert.True(t, cfg.Providers["bedrock-mantle"].Enabled,
		"production Mantle enablement must survive the sidecar profile overlay")
	assert.Equal(t, ProviderAuthTaskSigV4, cfg.Providers["bedrock-mantle"].Auth,
		"sidecar profile must set bedrock-mantle.auth=task_sigv4")
}
