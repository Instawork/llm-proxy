package provision

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFake_Provision(t *testing.T) {
	t.Parallel()

	openai, err := NewFake("openai").Provision(t.Context(), ProvisionRequest{Name: "test"})
	require.NoError(t, err)
	assert.Contains(t, openai.ActualKey, "sk-fake-dev-openai-")
	assert.Equal(t, UpstreamKindOpenAIServiceAccount, openai.UpstreamKind)

	gemini, err := NewFake("gemini").Provision(t.Context(), ProvisionRequest{Name: "test"})
	require.NoError(t, err)
	assert.Contains(t, gemini.ActualKey, "AIza-fake-dev-gemini-")
	assert.Equal(t, UpstreamKindGCPAPIKey, gemini.UpstreamKind)

	anthropic, err := NewFakeAnthropic().Provision(t.Context(), ProvisionRequest{Name: "test", Tier: TierElevated})
	require.NoError(t, err)
	assert.Contains(t, anthropic.ActualKey, "sk-ant-fake-dev-elevated-")
	assert.Equal(t, TierElevated, anthropic.UpstreamID)
}

func TestFake_ProvisionInvalidTier(t *testing.T) {
	t.Parallel()

	_, err := NewFakeAnthropic().Provision(t.Context(), ProvisionRequest{Name: "test", Tier: "bogus"})
	require.ErrorIs(t, err, ErrInvalidTier)
}
