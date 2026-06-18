package provision

import (
	"testing"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRuntimeFromYAML_AnthropicTierExpandEnv(t *testing.T) {
	t.Setenv("LLM_PROXY_ANTHROPIC_KEY_METERED", "sk-ant-api03-metered")
	t.Setenv("LLM_PROXY_ANTHROPIC_KEY_ELEVATED", "sk-ant-api03-elevated")
	t.Setenv("LLM_PROXY_ANTHROPIC_KEY_UNRESTRICTED", "sk-ant-api03-unrestricted")

	rt := RuntimeFromYAML(config.KeyProvisioningConfig{
		Enabled: true,
		Anthropic: config.AnthropicProvisioningConfig{
			Enabled:     true,
			DefaultTier: TierMetered,
			Tiers: map[string]string{
				TierMetered:      "${LLM_PROXY_ANTHROPIC_KEY_METERED}",
				TierElevated:     "${LLM_PROXY_ANTHROPIC_KEY_ELEVATED}",
				TierUnrestricted: "${LLM_PROXY_ANTHROPIC_KEY_UNRESTRICTED}",
			},
		},
	})

	assert.True(t, rt.AnthropicTiersFromYAML)
	assert.Equal(t, "sk-ant-api03-metered", rt.AnthropicTierKeys[TierMetered])
	assert.Equal(t, "sk-ant-api03-elevated", rt.AnthropicTierKeys[TierElevated])
	assert.Equal(t, "sk-ant-api03-unrestricted", rt.AnthropicTierKeys[TierUnrestricted])

	mgr, err := NewManagerFromRuntime(rt, nil)
	require.NoError(t, err)
	require.True(t, mgr.Enabled())

	p, ok := mgr.ForProvider("anthropic")
	require.True(t, ok)

	res, err := p.Provision(t.Context(), ProvisionRequest{Name: "test", Tier: TierElevated})
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-api03-elevated", res.ActualKey)
	assert.Equal(t, UpstreamKindAnthropicTiered, res.UpstreamKind)
}

func TestRuntimeFromYAML_OpenAIProjectExpandEnv(t *testing.T) {
	t.Setenv("LLM_PROXY_OPENAI_PROJECT_ID", "proj-from-env")

	rt := RuntimeFromYAML(config.KeyProvisioningConfig{
		Enabled: true,
		OpenAI: config.OpenAIProvisioningConfig{
			Enabled:   true,
			ProjectID: "${LLM_PROXY_OPENAI_PROJECT_ID}",
		},
	})

	assert.Equal(t, "proj-from-env", rt.OpenAIProjectID)
}

func TestNewManagerFromRuntime_DevFake(t *testing.T) {
	rt := RuntimeFromYAML(config.KeyProvisioningConfig{
		Enabled: true,
		DevFake: true,
	})

	mgr, err := NewManagerFromRuntime(rt, nil)
	require.NoError(t, err)
	require.True(t, mgr.Enabled())

	for _, provider := range []string{"openai", "anthropic", "gemini"} {
		p, ok := mgr.ForProvider(provider)
		require.True(t, ok, provider)
		res, provErr := p.Provision(t.Context(), ProvisionRequest{Name: "dev-test"})
		require.NoError(t, provErr, provider)
		assert.NotEmpty(t, res.ActualKey, provider)
	}

	anthropic, ok := mgr.ForProvider("anthropic")
	require.True(t, ok)
	tiered, ok := anthropic.(interface {
		TierStatus() (defaultTier string, tiers []string)
	})
	require.True(t, ok)
	defaultTier, tiers := tiered.TierStatus()
	assert.Equal(t, TierMetered, defaultTier)
	assert.ElementsMatch(t, []string{TierMetered, TierElevated, TierUnrestricted}, tiers)
}
