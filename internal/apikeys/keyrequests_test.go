package apikeys

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testKeyRequestStore(t *testing.T) *Store {
	t.Helper()
	store, _ := newFakeStore(t)
	return store
}

func TestCreateKeyRequestPendingLock(t *testing.T) {
	store := testKeyRequestStore(t)
	ctx := context.Background()

	first, err := store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "viewer@example.com",
		Provider:       "openai",
		Description:    "first",
		DailyCostLimit: 1000,
	})
	require.NoError(t, err)
	require.NotEmpty(t, first.ID())

	_, err = store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "viewer@example.com",
		Provider:       "openai",
		Description:    "duplicate",
		DailyCostLimit: 1000,
	})
	assert.ErrorIs(t, err, ErrPendingKeyRequestExists)
}

func TestBeginCompleteKeyRequestApproval(t *testing.T) {
	store := testKeyRequestStore(t)
	ctx := context.Background()

	created, err := store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "editor@example.com",
		Provider:       "anthropic",
		Description:    "service key",
		DailyCostLimit: 2000,
	})
	require.NoError(t, err)

	claimed, err := store.BeginKeyRequestApproval(ctx, created.ID(), "admin@example.com")
	require.NoError(t, err)
	assert.Equal(t, KeyRequestStatusApproving, claimed.Status)

	_, err = store.BeginKeyRequestApproval(ctx, created.ID(), "other@example.com")
	assert.ErrorIs(t, err, ErrKeyRequestNotPending)

	completed, err := store.CompleteKeyRequestApproval(ctx, created.ID(), "sk-test-created-key")
	require.NoError(t, err)
	assert.Equal(t, KeyRequestStatusApproved, completed.Status)
	assert.Equal(t, "sk-test-created-key", completed.CreatedKey)

	_, err = store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "editor@example.com",
		Provider:       "anthropic",
		Description:    "after approval",
		DailyCostLimit: 2000,
	})
	require.NoError(t, err)
}

func TestRejectKeyRequestReleasesLock(t *testing.T) {
	store := testKeyRequestStore(t)
	ctx := context.Background()

	created, err := store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "viewer@example.com",
		Provider:       "gemini",
		Description:    "please reject",
	})
	require.NoError(t, err)

	rejected, err := store.RejectKeyRequest(ctx, created.ID(), "admin@example.com", "not needed")
	require.NoError(t, err)
	assert.Equal(t, KeyRequestStatusRejected, rejected.Status)

	_, err = store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "viewer@example.com",
		Provider:       "gemini",
		Description:    "retry after reject",
	})
	require.NoError(t, err)
}

func TestRollbackKeyRequestApproval(t *testing.T) {
	store := testKeyRequestStore(t)
	ctx := context.Background()

	created, err := store.CreateKeyRequest(ctx, CreateKeyRequestInput{
		RequesterEmail: "viewer@example.com",
		Provider:       "openai",
		Description:    "rollback test",
	})
	require.NoError(t, err)

	_, err = store.BeginKeyRequestApproval(ctx, created.ID(), "admin@example.com")
	require.NoError(t, err)

	require.NoError(t, store.RollbackKeyRequestApproval(ctx, created.ID()))

	got, err := store.GetKeyRequest(ctx, created.ID())
	require.NoError(t, err)
	assert.Equal(t, KeyRequestStatusPending, got.Status)
	assert.Empty(t, got.ReviewedBy)
}
