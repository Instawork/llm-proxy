package apikeys

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListAgentLimits_ReturnsOnlyOverrides(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()

	empty, err := store.ListAgentLimits(ctx)
	require.NoError(t, err)
	assert.Empty(t, empty)

	_, err = store.CreateKey(ctx, "openai", "real-sk", "a key", 0, nil, nil)
	require.NoError(t, err)
	_, err = store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "req@example.com",
		Provider:       "openai",
		Name:           "svc",
		Description:    "needed",
	})
	require.NoError(t, err)
	_, err = store.PutAgentLimit(ctx, "finch-agent-1", AgentLimitUpdate{
		HardMonthlyCents: SetField[int64](1000),
		UpdatedBy:        "a@example.com",
	})
	require.NoError(t, err)
	_, err = store.PutAgentLimit(ctx, "finch-agent-2", AgentLimitUpdate{
		Exempt:    SetField(true),
		UpdatedBy: "b@example.com",
	})
	require.NoError(t, err)

	got, err := store.ListAgentLimits(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.NotNil(t, got["finch-agent-1"].HardMonthlyCents)
	assert.Equal(t, int64(1000), *got["finch-agent-1"].HardMonthlyCents)
	assert.Nil(t, got["finch-agent-1"].Exempt)
	require.NotNil(t, got["finch-agent-2"].Exempt)
	assert.True(t, *got["finch-agent-2"].Exempt)
	assert.Equal(t, "finch-agent-2", got["finch-agent-2"].Label())
}
