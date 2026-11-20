package config

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func validIntegrationsConfig() IntegrationsConfig {
	return IntegrationsConfig{
		Enabled:           true,
		RequestsPerMinute: 60,
		Callers: []IntegrationCaller{{
			ID:             "init",
			TokenSHA256Env: "LLM_PROXY_INTEGRATION_INIT_TOKEN_SHA256",
			Scopes:         []string{IntegrationScopeKeyRequestsCreate, IntegrationScopeKeyRequestsRead},
		}},
	}
}

func TestValidateIntegrationsConfig(t *testing.T) {
	require.NoError(t, validateIntegrationsConfig(validIntegrationsConfig()))
	require.NoError(t, validateIntegrationsConfig(IntegrationsConfig{}), "disabled with no callers is valid")

	cases := []struct {
		name   string
		mutate func(*IntegrationsConfig)
		want   string
	}{
		{"bad caller id", func(c *IntegrationsConfig) { c.Callers[0].ID = "Init_Bot" }, "must match"},
		{"empty caller id", func(c *IntegrationsConfig) { c.Callers[0].ID = "" }, "must match"},
		{"duplicate caller id", func(c *IntegrationsConfig) { c.Callers = append(c.Callers, c.Callers[0]) }, "duplicate id"},
		{"empty env name", func(c *IntegrationsConfig) { c.Callers[0].TokenSHA256Env = "" }, "token_sha256_env"},
		{"env value instead of name", func(c *IntegrationsConfig) { c.Callers[0].TokenSHA256Env = "${TOKEN}" }, "token_sha256_env"},
		{"unknown scope", func(c *IntegrationsConfig) { c.Callers[0].Scopes = []string{"keys:delete"} }, "unknown scope"},
		{"negative rate", func(c *IntegrationsConfig) { c.RequestsPerMinute = -1 }, "requests_per_minute"},
		{"negative max", func(c *IntegrationsConfig) { c.MaxMonthlyCostLimitCents = -1 }, "cannot be negative"},
		{"disabled still checks callers", func(c *IntegrationsConfig) { c.Enabled = false; c.Callers[0].ID = "BAD" }, "must match"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validIntegrationsConfig()
			tc.mutate(&cfg)
			err := validateIntegrationsConfig(cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestCallersPerEnvironment(t *testing.T) {
	configsDir, err := filepath.Abs(filepath.Join("..", "..", "configs"))
	require.NoError(t, err)
	load := func(env string) IntegrationsConfig {
		t.Helper()
		t.Setenv("ENVIRONMENT", env)
		cfg, err := LoadAndMergeConfigs([]string{
			filepath.Join(configsDir, "base.yml"),
			filepath.Join(configsDir, env+".yml"),
		})
		require.NoError(t, err)
		return cfg.Features.AdminDashboard.Integrations
	}

	for _, env := range []string{"production", "staging"} {
		t.Run(env, func(t *testing.T) {
			ic := load(env)
			assert.Equal(t, 60, ic.RequestsPerMinute, "inherited from base.yml")
			assert.Equal(t, int64(10000), ic.MaxDailyCostLimitCents)
			assert.Equal(t, int64(100000), ic.MaxMonthlyCostLimitCents)
			assert.Equal(t, "${LLM_PROXY_INTEGRATION_TICKET_URL_HOSTS}", ic.TicketURLHosts)
			require.NotEmpty(t, ic.Callers)
			assert.Equal(t, "init", ic.Callers[0].ID)
			assert.Equal(t, "LLM_PROXY_INTEGRATION_INIT_TOKEN_SHA256", ic.Callers[0].TokenSHA256Env)
		})
	}

	staging := load("staging")
	assert.False(t, staging.Enabled, "staging integrations stay off until Init is confirmed")
	assert.False(t, staging.Callers[0].Disabled)
	assert.Len(t, staging.Callers, 1, "finch is production-only")

	prodCfg := load("production")
	assert.True(t, prodCfg.Enabled, "on in production for finch")
	prod := prodCfg.Callers
	require.Len(t, prod, 2)
	assert.True(t, prod[0].Disabled, "init stays off until Init is confirmed")
	finch := prod[1]
	assert.Equal(t, "finch", finch.ID)
	assert.Equal(t, "LLM_PROXY_INTEGRATION_FINCH_TOKEN_SHA256", finch.TokenSHA256Env)
	assert.ElementsMatch(t, []string{IntegrationScopeAgentLimitsRead, IntegrationScopeAgentLimitsWrite, IntegrationScopeAgentSpendRead}, finch.Scopes)
	assert.False(t, finch.Disabled)

	dev := load("dev")
	assert.True(t, dev.Enabled)
	require.Len(t, dev.Callers, 1)
	assert.Equal(t, "dev", dev.Callers[0].ID)
	assert.Equal(t, "LLM_PROXY_INTEGRATION_DEV_TOKEN_SHA256", dev.Callers[0].TokenSHA256Env)

	for _, env := range []string{"sidecar", "fuzz"} {
		ic := load(env)
		assert.False(t, ic.Enabled, "%s inherits enabled: false", env)
		assert.Empty(t, ic.Callers, env)
	}
	assert.False(t, strings.Contains(load("production").TicketURLHosts, "init.inc"), "vendor host is injected, not committed")
}
