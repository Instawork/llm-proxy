package apikeys

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestShareLink_ID(t *testing.T) {
	link := &ShareLink{PK: ShareKeyPrefix + "abc-123"}
	assert.Equal(t, "abc-123", link.ID())
}

func TestStore_CreateAndGetShareLink(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()

	key, err := store.CreateKey(ctx, "openai", "sk-real", "share me", 100, nil, nil)
	require.NoError(t, err)

	link, err := store.CreateShareLink(ctx, key.PK, "admin@example.com")
	require.NoError(t, err)
	require.NotEmpty(t, link.ID())
	assert.Equal(t, ShareKeyPrefix+link.ID(), link.PK)
	assert.Equal(t, key.PK, link.APIKey)
	assert.Equal(t, "openai", link.Provider)
	assert.Equal(t, "admin@example.com", link.CreatedBy)

	got, err := store.GetShareLink(ctx, link.ID())
	require.NoError(t, err)
	assert.Equal(t, link.PK, got.PK)
	assert.Equal(t, key.PK, got.APIKey)
	require.NotNil(t, link.ExpiresAt)
}

func TestStore_CreateShareLink_ReusesWithinTTL(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()

	key, err := store.CreateKey(ctx, "openai", "sk-real", "", 0, nil, nil)
	require.NoError(t, err)

	first, err := store.CreateShareLink(ctx, key.PK, "admin@example.com")
	require.NoError(t, err)

	second, err := store.CreateShareLink(ctx, key.PK, "other@example.com")
	require.NoError(t, err)

	assert.Equal(t, first.ID(), second.ID())
	assert.Equal(t, first.PK, second.PK)
	assert.Equal(t, "admin@example.com", second.CreatedBy, "reuse should not rewrite creator")
}

func TestStore_CreateShareLink_NewUUIDAfterExpiry(t *testing.T) {
	store, fake := newFakeStore(t)
	ctx := context.Background()

	key, err := store.CreateKey(ctx, "openai", "sk-real", "", 0, nil, nil)
	require.NoError(t, err)

	expiredID := "11111111-2222-3333-4444-555555555555"
	past := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)
	fake.InjectItem("test-keys", ShareKeyPrefix+expiredID, map[string]any{
		"pk":             map[string]any{"S": ShareKeyPrefix + expiredID},
		"share_api_key":  map[string]any{"S": key.PK},
		"share_provider": map[string]any{"S": "openai"},
		"created_at":     map[string]any{"S": time.Now().Add(-25 * time.Hour).Format(time.RFC3339Nano)},
		"expires_at":     map[string]any{"S": past},
	})

	link, err := store.CreateShareLink(ctx, key.PK, "admin@example.com")
	require.NoError(t, err)
	assert.NotEqual(t, expiredID, link.ID())
	require.NotNil(t, link.ExpiresAt)
	assert.True(t, link.ExpiresAt.After(time.Now()))

	_, err = store.GetShareLink(ctx, expiredID)
	require.Error(t, err)
}

func TestStore_CreateShareLink_UnknownKey(t *testing.T) {
	store, _ := newFakeStore(t)
	_, err := store.CreateShareLink(context.Background(), KeyPrefix+"missing", "admin@example.com")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown key")
}

func TestStore_GetShareLink_NotFound(t *testing.T) {
	store, _ := newFakeStore(t)
	_, err := store.GetShareLink(context.Background(), "00000000-0000-0000-0000-000000000000")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_GetShareLink_Expired(t *testing.T) {
	store, fake := newFakeStore(t)
	past := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339Nano)
	id := "deadbeef-dead-beef-dead-deadbeefdead"
	fake.InjectItem("test-keys", ShareKeyPrefix+id, map[string]any{
		"pk":             map[string]any{"S": ShareKeyPrefix + id},
		"share_api_key":  map[string]any{"S": KeyPrefix + "abc"},
		"share_provider": map[string]any{"S": "openai"},
		"created_at":     map[string]any{"S": time.Now().Format(time.RFC3339Nano)},
		"expires_at":     map[string]any{"S": past},
	})
	_, err := store.GetShareLink(context.Background(), id)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
}

func TestValidateShareID(t *testing.T) {
	valid := []string{
		"00000000-0000-0000-0000-000000000000",
		"11111111-2222-3333-4444-555555555555",
		"deadbeef-dead-beef-dead-deadbeefdead",
		"DEADBEEF-DEAD-BEEF-DEAD-DEADBEEFDEAD",
	}
	for _, id := range valid {
		assert.NoError(t, ValidateShareID(id), "expected valid UUID: %q", id)
	}

	invalid := []string{
		"",
		"not-a-real-id",
		"missing",
		"dead-beef-dead-beef-deadbeefdead",
		"11111111-2222-3333-4444-55555555555g",
		"11111111_2222_3333_4444_555555555555",
		"11111111-2222-3333-4444-5555555555550",
		ShareKeyPrefix + "11111111-2222-3333-4444-555555555555",
		strings.Repeat("a", 2049),
		strings.Repeat("a", 3000),
	}
	for _, id := range invalid {
		err := ValidateShareID(id)
		require.ErrorIs(t, err, ErrInvalidShareID, "expected ErrInvalidShareID for len=%d", len(id))
	}
}

func TestStore_ShareLink_RejectsMalformedAndOversizedBeforeDynamoDB(t *testing.T) {
	store, fake := newFakeStore(t)
	ctx := context.Background()

	cases := []struct {
		name string
		id   string
	}{
		{"empty", ""},
		{"malformed", "not-a-uuid"},
		{"non-hex", "11111111-2222-3333-4444-55555555555z"},
		{"oversized_over_2048_bytes", strings.Repeat("a", 2500)},
	}

	for _, tc := range cases {
		t.Run("get/"+tc.name, func(t *testing.T) {
			fake.FailOnce("GetItem", errors.New("ValidationException"))
			_, err := store.GetShareLink(ctx, tc.id)
			require.ErrorIs(t, err, ErrInvalidShareID)

			// Confirm the armed DynamoDB failure was NOT consumed by the invalid ID.
			_, err = store.GetShareLink(ctx, "00000000-0000-0000-0000-000000000000")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "ValidationException")
		})

		t.Run("delete/"+tc.name, func(t *testing.T) {
			fake.FailOnce("DeleteItem", errors.New("ValidationException"))
			err := store.DeleteShareLink(ctx, tc.id)
			require.ErrorIs(t, err, ErrInvalidShareID)

			// Confirm the armed DynamoDB failure was NOT consumed by the invalid ID.
			err = store.DeleteShareLink(ctx, "00000000-0000-0000-0000-000000000000")
			require.Error(t, err)
			assert.Contains(t, err.Error(), "ValidationException")
		})
	}
}

func TestStore_GetShareLink_UppercaseUUIDResolvesLowercaseRecord(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()

	key, err := store.CreateKey(ctx, "openai", "sk-real", "desc", 0, nil, nil)
	require.NoError(t, err)
	link, err := store.CreateShareLink(ctx, key.PK, "admin@example.com")
	require.NoError(t, err)

	got, err := store.GetShareLink(ctx, strings.ToUpper(link.ID()))
	require.NoError(t, err)
	assert.Equal(t, link.PK, got.PK)
}

func TestStore_DeleteShareLink(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()

	key, err := store.CreateKey(ctx, "anthropic", "sk-ant", "", 0, nil, nil)
	require.NoError(t, err)
	link, err := store.CreateShareLink(ctx, key.PK, "")
	require.NoError(t, err)

	require.NoError(t, store.DeleteShareLink(ctx, link.ID()))

	_, err = store.GetShareLink(ctx, link.ID())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestStore_ListKeys_ExcludesShareLinks(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()

	key, err := store.CreateKey(ctx, "openai", "sk-real", "", 0, nil, nil)
	require.NoError(t, err)
	_, err = store.CreateShareLink(ctx, key.PK, "admin@example.com")
	require.NoError(t, err)

	keys, err := store.ListKeys(ctx, "")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.True(t, keys[0].PK == key.PK)
	for _, k := range keys {
		assert.NotContains(t, k.PK, ShareKeyPrefix)
	}
}
