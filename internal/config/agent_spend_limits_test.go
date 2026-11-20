package config

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validAgentSpendLimits() AgentSpendLimitsConfig {
	return AgentSpendLimitsConfig{
		Enabled:                 true,
		Mode:                    AgentSpendModeObserve,
		UserPrefix:              "finch-agent-",
		DefaultSoftMonthlyCents: 10000,
		DefaultHardMonthlyCents: 20000,
		MaxHardMonthlyCents:     500000,
		LimitCacheTTLSeconds:    60,
	}
}

func agentSpendTestConfig(a AgentSpendLimitsConfig) *YAMLConfig {
	c := &YAMLConfig{Providers: map[string]ProviderConfig{}}
	c.Features.APIKeyManagement = APIKeyManagementConfig{Enabled: true, TableName: "t", Region: "us-west-2"}
	c.Features.AgentSpendLimits = a
	return c
}

func TestValidate_AgentSpendLimits(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*AgentSpendLimitsConfig)
		wantErr string
	}{
		{"valid observe", func(*AgentSpendLimitsConfig) {}, ""},
		{"valid enforce", func(a *AgentSpendLimitsConfig) { a.Mode = AgentSpendModeEnforce }, ""},
		{"soft equals hard", func(a *AgentSpendLimitsConfig) { a.DefaultSoftMonthlyCents = 20000 }, ""},
		{"all zero defaults", func(a *AgentSpendLimitsConfig) { a.DefaultSoftMonthlyCents, a.DefaultHardMonthlyCents = 0, 0 }, ""},
		{"hard at max", func(a *AgentSpendLimitsConfig) { a.DefaultHardMonthlyCents = a.MaxHardMonthlyCents }, ""},
		{"ttl zero uses default", func(a *AgentSpendLimitsConfig) { a.LimitCacheTTLSeconds = 0 }, ""},
		{"empty mode", func(a *AgentSpendLimitsConfig) { a.Mode = "" }, "mode"},
		{"unknown mode", func(a *AgentSpendLimitsConfig) { a.Mode = "block" }, "mode"},
		{"empty prefix", func(a *AgentSpendLimitsConfig) { a.UserPrefix = "" }, "user_prefix is required"},
		{"prefix with slash", func(a *AgentSpendLimitsConfig) { a.UserPrefix = "finch/agent-" }, "user_prefix"},
		{"prefix with pipe", func(a *AgentSpendLimitsConfig) { a.UserPrefix = "finch|" }, "user_prefix"},
		{"prefix with space", func(a *AgentSpendLimitsConfig) { a.UserPrefix = "finch agent-" }, "user_prefix"},
		{"negative soft", func(a *AgentSpendLimitsConfig) { a.DefaultSoftMonthlyCents = -1 }, "default_soft_monthly_cents cannot be negative"},
		{"hard below soft", func(a *AgentSpendLimitsConfig) { a.DefaultHardMonthlyCents = 9999 }, "must be at least"},
		{"zero max", func(a *AgentSpendLimitsConfig) { a.MaxHardMonthlyCents = 0 }, "max_hard_monthly_cents must be positive"},
		{"hard over max", func(a *AgentSpendLimitsConfig) { a.MaxHardMonthlyCents = 19999 }, "cannot exceed"},
		{"negative ttl", func(a *AgentSpendLimitsConfig) { a.LimitCacheTTLSeconds = -1 }, "limit_cache_ttl_seconds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := validAgentSpendLimits()
			tc.mutate(&a)
			err := agentSpendTestConfig(a).Validate()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, "agent_spend_limits")
			require.ErrorContains(t, err, tc.wantErr)
		})
	}
}

func TestValidate_AgentSpendLimitsRequiresAPIKeyManagement(t *testing.T) {
	c := agentSpendTestConfig(validAgentSpendLimits())
	c.Features.APIKeyManagement = APIKeyManagementConfig{}
	require.ErrorContains(t, c.Validate(), "api_key_management must be enabled")
}

func TestValidate_AgentSpendLimitsDisabledSkipsChecks(t *testing.T) {
	c := agentSpendTestConfig(AgentSpendLimitsConfig{Mode: "bogus", DefaultSoftMonthlyCents: -5})
	require.NoError(t, c.Validate())
}

func TestAgentSpendLimitsConfig_Helpers(t *testing.T) {
	a := validAgentSpendLimits()
	assert.Equal(t, 60*time.Second, a.LimitCacheTTL())
	a.LimitCacheTTLSeconds = 0
	assert.Equal(t, time.Duration(DefaultAgentLimitCacheTTLSeconds)*time.Second, a.LimitCacheTTL())

	assert.False(t, a.Enforcing())
	a.Mode = AgentSpendModeEnforce
	assert.True(t, a.Enforcing())
	a.Enabled = false
	assert.False(t, a.Enforcing())

	assert.True(t, a.Governs("finch-agent-123"))
	assert.False(t, a.Governs("finch-agent-"))
	assert.False(t, a.Governs("finch-brain-1"))
	assert.False(t, a.Governs("Finch-Agent-1"))
	a.UserPrefix = ""
	assert.False(t, a.Governs("finch-agent-1"))
}

// TestRealEnvConfigs_AgentSpendLimitsDefaults checks that every environment
// loads the base.yml defaults (which needs the raw-YAML overlay merge) and
// that they pass validation once enabled.
func TestRealEnvConfigs_AgentSpendLimitsDefaults(t *testing.T) {
	configsDir, err := filepath.Abs(filepath.Join("..", "..", "configs"))
	require.NoError(t, err)
	cases := []struct {
		name    string
		env     string
		overlay []string
	}{
		{"dev", "dev", []string{"dev.yml"}},
		{"staging", "staging", []string{"staging.yml"}},
		{"staging+sidecar", "staging", []string{"staging.yml", "sidecar.yml"}},
		{"production", "production", []string{"production.yml"}},
		{"production+sidecar", "production", []string{"production.yml", "sidecar.yml"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENVIRONMENT", tc.env)
			paths := []string{filepath.Join(configsDir, "base.yml")}
			for _, o := range tc.overlay {
				paths = append(paths, filepath.Join(configsDir, o))
			}
			cfg, err := LoadAndMergeConfigs(paths)
			require.NoError(t, err)

			a := cfg.Features.AgentSpendLimits
			assert.False(t, a.Enabled)
			assert.Equal(t, AgentSpendModeObserve, a.Mode)
			assert.Equal(t, "finch-agent-", a.UserPrefix)
			assert.Equal(t, int64(10000), a.DefaultSoftMonthlyCents)
			assert.Equal(t, int64(20000), a.DefaultHardMonthlyCents)
			assert.Equal(t, int64(500000), a.MaxHardMonthlyCents)
			assert.Equal(t, 60, a.LimitCacheTTLSeconds)
			assert.False(t, a.FailClosedOnReadError)

			a.Enabled = true
			c := agentSpendTestConfig(a)
			require.NoError(t, c.Validate())
		})
	}
}
