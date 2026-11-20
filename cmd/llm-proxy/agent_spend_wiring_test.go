package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/coststats"
)

func withAgentSpendGlobals(t *testing.T) {
	t.Helper()
	prevRecorder, prevStore, prevResolver := globalCostStatsRecorder, globalAPIKeyStore, globalAgentLimitResolver
	t.Cleanup(func() {
		globalCostStatsRecorder, globalAPIKeyStore, globalAgentLimitResolver = prevRecorder, prevStore, prevResolver
	})
	globalAgentLimitResolver = nil
	globalAPIKeyStore = nil
}

func agentSpendYAML(enabled bool) *config.YAMLConfig {
	yc := config.GetDefaultYAMLConfig()
	yc.Features.AgentSpendLimits = config.AgentSpendLimitsConfig{
		Enabled:                 enabled,
		Mode:                    config.AgentSpendModeObserve,
		UserPrefix:              "finch-agent-",
		DefaultSoftMonthlyCents: 10000,
		DefaultHardMonthlyCents: 20000,
		MaxHardMonthlyCents:     500000,
	}
	return yc
}

func TestInitAgentSpendLimits_DisabledInstallsNothing(t *testing.T) {
	withAgentSpendGlobals(t)
	globalCostStatsRecorder = coststats.NewRecorder()
	initAgentSpendLimits(agentSpendYAML(false))
	assert.Nil(t, globalAgentLimitResolver)
	assert.False(t, globalCostStatsRecorder.GovernsMonthlyUser("finch-agent-1"))
}

func TestInitAgentSpendLimits_SetsRecorderPrefix(t *testing.T) {
	withAgentSpendGlobals(t)
	globalCostStatsRecorder = coststats.NewRecorder()
	initAgentSpendLimits(agentSpendYAML(true))
	require.NotNil(t, globalAgentLimitResolver)
	assert.True(t, globalCostStatsRecorder.GovernsMonthlyUser("finch-agent-1"))
	assert.False(t, globalCostStatsRecorder.GovernsMonthlyUser("support-bot"))

	// No key store: every lookup errors, so the middleware fails open and
	// reports limit_lookup_error instead of silently skipping enforcement.
	_, err := globalAgentLimitResolver.Resolve(context.Background(), "finch-agent-1")
	require.ErrorIs(t, err, errAgentLimitStoreUnavailable)
}

// The admin integration PUT invalidates the resolver it is given, so it
// must be the very instance the middleware reads.
func TestInitAgentSpendLimits_AdminAndMiddlewareShareResolver(t *testing.T) {
	withAgentSpendGlobals(t)
	globalCostStatsRecorder = coststats.NewRecorder()
	yc := agentSpendYAML(true)
	initAgentSpendLimits(yc)
	require.NotNil(t, globalAgentLimitResolver)

	limits, ok := agentSpendLimitOptions(yc).Limits.(*apikeys.AgentLimitResolver)
	require.True(t, ok)
	assert.Same(t, globalAgentLimitResolver, limits)
	assert.Same(t, globalAgentLimitResolver, adminDeps(yc).AgentLimitResolver)
}

func TestInitAgentSpendLimits_NeedsCostTracking(t *testing.T) {
	withAgentSpendGlobals(t)
	globalCostStatsRecorder = nil
	initAgentSpendLimits(agentSpendYAML(true))
	assert.Nil(t, globalAgentLimitResolver)
}

func TestAgentSpendLimitOptions_UsesCostDatadogTransport(t *testing.T) {
	yc := agentSpendYAML(true)
	assert.Nil(t, costTrackingDatadogConfig(yc))
	dd := &config.DatadogTransportConfig{Host: "localhost", Port: "8125", Namespace: "llm"}
	yc.Features.CostTracking.Transports = []config.TransportConfig{
		{Type: "file", File: &config.FileTransportConfig{Path: "x"}},
		{Type: "datadog", Datadog: dd},
	}
	assert.Same(t, dd, costTrackingDatadogConfig(yc))
}

var _ apikeys.AgentLimitGetter = unavailableAgentLimits{}
