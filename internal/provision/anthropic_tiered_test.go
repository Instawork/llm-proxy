package provision

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAnthropicTiered_Provision(t *testing.T) {
	t.Parallel()

	p, err := NewAnthropicTiered(TierMetered, map[string]string{
		TierMetered:      "sk-ant-api03-metered",
		TierElevated:     "sk-ant-api03-elevated",
		TierUnrestricted: "sk-ant-api03-unrestricted",
	})
	require.NoError(t, err)

	res, err := p.Provision(context.Background(), ProvisionRequest{Name: "test"})
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-api03-metered", res.ActualKey)
	assert.Equal(t, TierMetered, res.UpstreamID)
	assert.Equal(t, UpstreamKindAnthropicTiered, res.UpstreamKind)

	res, err = p.Provision(context.Background(), ProvisionRequest{Name: "test", Tier: TierElevated})
	require.NoError(t, err)
	assert.Equal(t, "sk-ant-api03-elevated", res.ActualKey)
}

func TestAnthropicTiered_InvalidTier(t *testing.T) {
	t.Parallel()

	p, err := NewAnthropicTiered(TierMetered, map[string]string{TierMetered: "sk-ant-api03-metered"})
	require.NoError(t, err)

	_, err = p.Provision(context.Background(), ProvisionRequest{Name: "test", Tier: "unknown"})
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidTier))
}

func TestAnthropicTiered_TierStatus(t *testing.T) {
	t.Parallel()

	p, err := NewAnthropicTiered(TierMetered, map[string]string{
		TierMetered:  "sk-ant-api03-metered",
		TierElevated: "sk-ant-api03-elevated",
	})
	require.NoError(t, err)

	defaultTier, tiers := p.TierStatus()
	assert.Equal(t, TierMetered, defaultTier)
	assert.ElementsMatch(t, []string{TierMetered, TierElevated}, tiers)
}

func TestNewAnthropicTiered_Validation(t *testing.T) {
	t.Parallel()

	_, err := NewAnthropicTiered(TierMetered, map[string]string{})
	require.Error(t, err)

	_, err = NewAnthropicTiered("missing", map[string]string{TierMetered: "sk-ant-api03-metered"})
	require.Error(t, err)
}
