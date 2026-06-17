package adminusers

import (
	"context"
	"testing"

	"github.com/Instawork/llm-proxy/internal/testhelpers/dynamodbfake"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testUserStore(t *testing.T) *Store {
	t.Helper()
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())
	store, err := NewStore(StoreConfig{
		TableName:       "test-admin-users",
		Region:          "us-west-2",
		AutoCreateTable: true,
	})
	require.NoError(t, err)
	return store
}

func TestEnsureUserCreatesViewer(t *testing.T) {
	store := testUserStore(t)
	ctx := context.Background()

	user, created, err := store.EnsureUser(ctx, "Alice@Example.com", "Alice", "pic")
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, "alice@example.com", user.Email)
	assert.Equal(t, RoleViewer, user.Role)

	_, createdAgain, err := store.EnsureUser(ctx, "alice@example.com", "Alice Updated", "pic2")
	require.NoError(t, err)
	assert.False(t, createdAgain)

	got, err := store.GetUser(ctx, "alice@example.com")
	require.NoError(t, err)
	assert.Equal(t, "Alice Updated", got.Name)
	assert.Equal(t, RoleViewer, got.Role)
}

func TestCreateUserAndSetRole(t *testing.T) {
	store := testUserStore(t)
	ctx := context.Background()

	_, err := store.CreateUser(ctx, "editor@example.com", RoleEditor)
	require.NoError(t, err)

	require.NoError(t, store.SetRole(ctx, "editor@example.com", RoleAdmin))

	got, err := store.GetUser(ctx, "editor@example.com")
	require.NoError(t, err)
	assert.Equal(t, RoleAdmin, got.Role)
}

func TestListUsersAndCountAdmins(t *testing.T) {
	store := testUserStore(t)
	ctx := context.Background()

	_, err := store.CreateUser(ctx, "a@example.com", RoleAdmin)
	require.NoError(t, err)
	_, err = store.CreateUser(ctx, "b@example.com", RoleViewer)
	require.NoError(t, err)

	users, err := store.ListUsers(ctx)
	require.NoError(t, err)
	assert.Len(t, users, 2)

	count, err := store.CountAdmins(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

func TestRecordShareAwarenessIdempotent(t *testing.T) {
	store := testUserStore(t)
	ctx := context.Background()

	_, _, err := store.EnsureUser(ctx, "viewer@example.com", "", "")
	require.NoError(t, err)

	require.NoError(t, store.RecordShareAwareness(ctx, "viewer@example.com", "share-uuid"))
	require.NoError(t, store.RecordShareAwareness(ctx, "viewer@example.com", "share-uuid"))
}

func TestDeleteUser(t *testing.T) {
	store := testUserStore(t)
	ctx := context.Background()

	_, err := store.CreateUser(ctx, "gone@example.com", RoleViewer)
	require.NoError(t, err)
	require.NoError(t, store.DeleteUser(ctx, "gone@example.com"))

	_, err = store.GetUser(ctx, "gone@example.com")
	require.Error(t, err)
}
