package apikeys

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCredentialHashSuffix_MatchesMaskSuffix(t *testing.T) {
	raw := "sk-ant-api03-secret-material-here"
	hash := CredentialHashSuffix(raw)
	assert.Len(t, hash, 8)

	masked := "sk-ant-…" + hash
	parsed, err := ParseCredentialHashFromMaskedID(masked)
	require.NoError(t, err)
	assert.Equal(t, hash, parsed)
}

func TestParseCredentialHashFromMaskedID_RejectsInvalid(t *testing.T) {
	_, err := ParseCredentialHashFromMaskedID("")
	assert.Error(t, err)

	_, err = ParseCredentialHashFromMaskedID("sk-ant-no-hash")
	assert.Error(t, err)
}

func TestStore_BanAndUnbanBYOCredential(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()

	masked := "sk-ant-…" + CredentialHashSuffix("sk-ant-test-key")
	ban, err := store.BanBYOCredential(ctx, "anthropic", masked, "admin@example.com", "abuse")
	require.NoError(t, err)
	assert.Equal(t, "anthropic", ban.Provider)
	assert.Equal(t, masked, ban.MaskedID)

	_, err = store.BanBYOCredential(ctx, "openai", masked, "admin@example.com", "wrong provider")
	assert.Error(t, err)

	banned, err := store.IsBYOCredentialBanned(ctx, "anthropic", ban.Hash)
	require.NoError(t, err)
	assert.True(t, banned)

	bans, err := store.ListBYOBans(ctx, "")
	require.NoError(t, err)
	require.Len(t, bans, 1)

	require.NoError(t, store.UnbanBYOCredential(ctx, "anthropic", ban.Hash))
	banned, err = store.IsBYOCredentialBanned(ctx, "anthropic", ban.Hash)
	require.NoError(t, err)
	assert.False(t, banned)
}
