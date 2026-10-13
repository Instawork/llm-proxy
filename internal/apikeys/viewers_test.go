package apikeys

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsKeyViewer(t *testing.T) {
	key := &APIKey{RequesterEmail: "Requester@Example.com", ViewerEmails: []string{"a@example.com"}}
	assert.True(t, IsKeyViewer(key, "a@example.com"))
	assert.True(t, IsKeyViewer(key, " A@Example.com "))
	assert.True(t, IsKeyViewer(key, "requester@example.com"))
	assert.False(t, IsKeyViewer(key, "b@example.com"))
	assert.False(t, IsKeyViewer(key, ""))
	assert.False(t, IsKeyViewer(nil, "a@example.com"))
	assert.False(t, IsKeyViewer(&APIKey{OwnerEmail: "o@example.com", ViewerEmails: []string{"a@example.com"}}, "a@example.com"))
}

func TestStore_AddAndRemoveKeyViewers(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()
	key, err := store.CreateKey(ctx, "openai", "sk-org", "org", 0, nil, nil)
	require.NoError(t, err)

	got, err := store.AddKeyViewers(ctx, key.PK, []string{"B@Example.com", "a@example.com", " b@example.com "})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a@example.com", "b@example.com"}, got.ViewerEmails)

	got, err = store.AddKeyViewers(ctx, key.PK, []string{"a@example.com", "c@example.com"})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"a@example.com", "b@example.com", "c@example.com"}, got.ViewerEmails)

	got, err = store.RemoveKeyViewer(ctx, key.PK, "A@example.com")
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"b@example.com", "c@example.com"}, got.ViewerEmails)

	got, err = store.RemoveKeyViewer(ctx, key.PK, "nobody@example.com")
	require.NoError(t, err)
	assert.Len(t, got.ViewerEmails, 2)

	_, err = store.RemoveKeyViewer(ctx, key.PK, "b@example.com")
	require.NoError(t, err)
	got, err = store.RemoveKeyViewer(ctx, key.PK, "c@example.com")
	require.NoError(t, err)
	assert.Empty(t, got.ViewerEmails)
}

func TestStore_AddKeyViewersLeavesOtherFieldsAlone(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()
	key, err := store.CreateKeyWithMeta(ctx, "openai", "sk-org", "org", 500, 0, nil, nil, KeyCreateMeta{RequesterEmail: "req@example.com"})
	require.NoError(t, err)

	got, err := store.AddKeyViewers(ctx, key.PK, []string{"a@example.com"})
	require.NoError(t, err)
	assert.Equal(t, "org", got.Description)
	assert.Equal(t, int64(500), got.DailyCostLimit)
	assert.Equal(t, "req@example.com", got.RequesterEmail)
	assert.True(t, got.Enabled)
}

func TestStore_ViewersRejectedOnPersonalKeys(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()
	key, err := store.CreatePersonalKey(ctx, "owner@example.com", "openai", "sk-own", "mine", 1000, KeyCreateMeta{})
	require.NoError(t, err)

	_, err = store.AddKeyViewers(ctx, key.PK, []string{"a@example.com"})
	require.ErrorIs(t, err, ErrViewersNotAllowed)
}

func TestStore_ViewersMissingKey(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()
	missing := KeyPrefix + "0000000000000000000000000000000000000000000000000000000000000000"

	_, err := store.AddKeyViewers(ctx, missing, []string{"a@example.com"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
	_, err = store.RemoveKeyViewer(ctx, missing, "a@example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_AddKeyViewersEnforcesCap(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()
	key, err := store.CreateKey(ctx, "openai", "sk-org", "org", 0, nil, nil)
	require.NoError(t, err)

	emails := make([]string, MaxKeyViewers)
	for i := range emails {
		emails[i] = fmt.Sprintf("user%d@example.com", i)
	}
	got, err := store.AddKeyViewers(ctx, key.PK, emails)
	require.NoError(t, err)
	assert.Len(t, got.ViewerEmails, MaxKeyViewers)

	_, err = store.AddKeyViewers(ctx, key.PK, []string{"extra@example.com"})
	require.ErrorIs(t, err, ErrTooManyViewers)

	_, err = store.AddKeyViewers(ctx, key.PK, []string{"user0@example.com"})
	require.NoError(t, err, "re-adding an existing viewer is a no-op even at the cap")
}

func TestStore_AddKeyViewersConcurrent(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()
	key, err := store.CreateKey(ctx, "openai", "sk-org", "org", 0, nil, nil)
	require.NoError(t, err)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := store.AddKeyViewers(ctx, key.PK, []string{fmt.Sprintf("u%d@example.com", i)})
			assert.NoError(t, err)
		}()
	}
	wg.Wait()

	got, err := store.GetKeyRecord(ctx, key.PK)
	require.NoError(t, err)
	assert.Len(t, got.ViewerEmails, 8)
}

func TestStore_UpdateViewerSetOnDeletedKeyReportsNotFound(t *testing.T) {
	store, _ := newFakeStore(t)
	err := store.updateViewerSet(context.Background(), KeyPrefix+"gone", "ADD", []string{"a@example.com"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}
