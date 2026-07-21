package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestUpstreamConfig_Unmarshal(t *testing.T) {
	t.Parallel()

	raw := `
enabled: true
features:
  cost_tracking:
    enabled: false
  upstream:
    response_header_timeout_seconds: 300
providers:
  openai:
    enabled: true
  gemini:
    enabled: true
    response_header_timeout_seconds: 600
  anthropic:
    enabled: true
    response_header_timeout_seconds: 0
`
	var cfg YAMLConfig
	require.NoError(t, yaml.Unmarshal([]byte(raw), &cfg))
	require.Equal(t, 300, cfg.Features.Upstream.ResponseHeaderTimeoutSeconds)
	require.Equal(t, 0, cfg.Providers["openai"].ResponseHeaderTimeoutSeconds)
	require.Equal(t, 600, cfg.Providers["gemini"].ResponseHeaderTimeoutSeconds)
	require.Equal(t, 0, cfg.Providers["anthropic"].ResponseHeaderTimeoutSeconds)

	require.Equal(t, 5*time.Minute, cfg.DefaultResponseHeaderTimeout())
	require.Equal(t, 5*time.Minute, cfg.ResponseHeaderTimeoutFor("openai"))
	require.Equal(t, 10*time.Minute, cfg.ResponseHeaderTimeoutFor("gemini"))
	require.Equal(t, 5*time.Minute, cfg.ResponseHeaderTimeoutFor("anthropic"))
}

func TestLoadYAMLConfig_UpstreamTimeouts(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "upstream.yml")
	require.NoError(t, os.WriteFile(path, []byte(`
enabled: true
features:
  cost_tracking:
    enabled: false
  upstream:
    response_header_timeout_seconds: 180
providers:
  openai:
    enabled: true
  bedrock:
    enabled: true
    response_header_timeout_seconds: 420
`), 0o644))

	cfg, err := LoadYAMLConfig(path)
	require.NoError(t, err)
	require.Equal(t, 180, cfg.Features.Upstream.ResponseHeaderTimeoutSeconds)
	require.Equal(t, 3*time.Minute, cfg.DefaultResponseHeaderTimeout())
	require.Equal(t, 3*time.Minute, cfg.ResponseHeaderTimeoutFor("openai"))
	require.Equal(t, 7*time.Minute, cfg.ResponseHeaderTimeoutFor("bedrock"))
	require.Equal(t, 3*time.Minute, cfg.ResponseHeaderTimeoutFor("gemini"))
}

func TestLoadAndMergeConfigs_UpstreamTimeoutOverlay(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	base := filepath.Join(dir, "base.yml")
	overlay := filepath.Join(dir, "overlay.yml")
	require.NoError(t, os.WriteFile(base, []byte(`
enabled: true
features:
  cost_tracking:
    enabled: false
  upstream:
    response_header_timeout_seconds: 300
providers:
  openai:
    enabled: true
  gemini:
    enabled: true
`), 0o644))
	require.NoError(t, os.WriteFile(overlay, []byte(`
features:
  upstream:
    response_header_timeout_seconds: 240
providers:
  gemini:
    response_header_timeout_seconds: 480
`), 0o644))

	cfg, err := LoadAndMergeConfigs([]string{base, overlay})
	require.NoError(t, err)
	require.Equal(t, 240, cfg.Features.Upstream.ResponseHeaderTimeoutSeconds)
	require.Equal(t, 4*time.Minute, cfg.DefaultResponseHeaderTimeout())
	require.Equal(t, 4*time.Minute, cfg.ResponseHeaderTimeoutFor("openai"))
	require.Equal(t, 8*time.Minute, cfg.ResponseHeaderTimeoutFor("gemini"))
}

func TestBaseYAML_UpstreamTimeoutDefault(t *testing.T) {
	// Repo-root relative: tests run with package dir as cwd for `go test ./internal/config`.
	cfg, err := LoadYAMLConfig(filepath.Join("..", "..", "configs", "base.yml"))
	require.NoError(t, err)
	require.Equal(t, 300, cfg.Features.Upstream.ResponseHeaderTimeoutSeconds)
	require.Equal(t, 5*time.Minute, cfg.DefaultResponseHeaderTimeout())
	for _, name := range []string{"openai", "anthropic", "gemini", "bedrock", "bedrock-mantle"} {
		require.Equal(t, 5*time.Minute, cfg.ResponseHeaderTimeoutFor(name), name)
	}
}

func TestResponseHeaderTimeoutFor_Table(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		cfg      *YAMLConfig
		provider string
		want     time.Duration
	}{
		{
			name:     "nil config",
			cfg:      nil,
			provider: "openai",
			want:     5 * time.Minute,
		},
		{
			name:     "empty config",
			cfg:      &YAMLConfig{},
			provider: "openai",
			want:     5 * time.Minute,
		},
		{
			name: "global only",
			cfg: &YAMLConfig{
				Features: FeaturesConfig{
					Upstream: UpstreamConfig{ResponseHeaderTimeoutSeconds: 90},
				},
			},
			provider: "openai",
			want:     90 * time.Second,
		},
		{
			name: "provider zero inherits global",
			cfg: &YAMLConfig{
				Features: FeaturesConfig{
					Upstream: UpstreamConfig{ResponseHeaderTimeoutSeconds: 120},
				},
				Providers: map[string]ProviderConfig{
					"openai": {ResponseHeaderTimeoutSeconds: 0},
				},
			},
			provider: "openai",
			want:     2 * time.Minute,
		},
		{
			name: "provider override beats global",
			cfg: &YAMLConfig{
				Features: FeaturesConfig{
					Upstream: UpstreamConfig{ResponseHeaderTimeoutSeconds: 120},
				},
				Providers: map[string]ProviderConfig{
					"openai": {ResponseHeaderTimeoutSeconds: 45},
				},
			},
			provider: "openai",
			want:     45 * time.Second,
		},
		{
			name: "unknown provider inherits global",
			cfg: &YAMLConfig{
				Features: FeaturesConfig{
					Upstream: UpstreamConfig{ResponseHeaderTimeoutSeconds: 150},
				},
				Providers: map[string]ProviderConfig{
					"openai": {ResponseHeaderTimeoutSeconds: 600},
				},
			},
			provider: "not-a-provider",
			want:     150 * time.Second,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.cfg.ResponseHeaderTimeoutFor(tc.provider); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestValidateUpstreamConfig_ErrorMessages(t *testing.T) {
	t.Parallel()

	t.Run("negative global", func(t *testing.T) {
		t.Parallel()
		cfg := GetDefaultYAMLConfig()
		cfg.Features.Upstream.ResponseHeaderTimeoutSeconds = -1
		err := cfg.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "response_header_timeout_seconds cannot be negative")
	})

	t.Run("negative provider names provider", func(t *testing.T) {
		t.Parallel()
		cfg := GetDefaultYAMLConfig()
		cfg.Providers["gemini"] = ProviderConfig{
			Enabled:                      true,
			ResponseHeaderTimeoutSeconds: -2,
		}
		err := cfg.Validate()
		require.Error(t, err)
		require.True(t,
			strings.Contains(err.Error(), "providers.gemini") &&
				strings.Contains(err.Error(), "response_header_timeout_seconds cannot be negative"),
			"got %v", err)
	})

	t.Run("zero values ok", func(t *testing.T) {
		t.Parallel()
		cfg := GetDefaultYAMLConfig()
		cfg.Features.Upstream.ResponseHeaderTimeoutSeconds = 0
		cfg.Providers["openai"] = ProviderConfig{
			Enabled:                      true,
			ResponseHeaderTimeoutSeconds: 0,
		}
		require.NoError(t, cfg.validateUpstreamConfig())
		require.Equal(t, 5*time.Minute, cfg.DefaultResponseHeaderTimeout())
	})
}
