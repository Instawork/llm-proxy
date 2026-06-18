package apikeys

import (
	"context"
	"encoding/hex"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func resetKeyPrefixBase(t *testing.T) {
	t.Helper()
	t.Cleanup(func() { SetKeyPrefixBase(DefaultKeyPrefixBase) })
	SetKeyPrefixBase(DefaultKeyPrefixBase)
}

func TestKeyPrefix_DefaultGenerationPrefix(t *testing.T) {
	resetKeyPrefixBase(t)
	assert.Equal(t, "sk-iw-", KeyPrefix)
	assert.Equal(t, "iw", KeyPrefixBase())
}

func TestGenerateKey_SkPrefixFormat(t *testing.T) {
	resetKeyPrefixBase(t)

	key, err := GenerateKey()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(key, "sk-iw-"))

	body := strings.TrimPrefix(key, "sk-iw-")
	assert.Len(t, body, KeyLength*2)
	assert.True(t, hexRegexp.MatchString(body), "random segment should be lowercase hex")
	_, err = hex.DecodeString(body)
	assert.NoError(t, err)
}

func TestTrimKeyPrefix_SkAndLegacySeparators(t *testing.T) {
	resetKeyPrefixBase(t)

	tests := []struct {
		in   string
		want string
	}{
		{"sk-iw-deadbeef", "deadbeef"},
		{"iw:legacy", "legacy"},
		{"iw_legacy", "legacy"},
		{"iw-legacy", "legacy"},
		{"sk-proj-upstream", "sk-proj-upstream"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			assert.Equal(t, tc.want, TrimKeyPrefix(tc.in))
		})
	}
}

func TestRedactKey_PreservesSkPrefix(t *testing.T) {
	resetKeyPrefixBase(t)

	got := RedactKey("sk-iw-" + strings.Repeat("a", 64))
	assert.True(t, strings.HasPrefix(got, "sk-iw-"))
	assert.Contains(t, got, "…")
}

func TestSetKeyPrefixBase_CustomBaseAndSkLead(t *testing.T) {
	resetKeyPrefixBase(t)

	SetKeyPrefixBase("acme")
	assert.Equal(t, "sk-acme-", KeyPrefix)
	assert.True(t, HasKeyPrefix("sk-acme-secret"))
	assert.False(t, HasKeyPrefix("sk-iw-secret"))

	key, err := GenerateKey()
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(key, "sk-acme-"))
}

func TestSetKeyPrefixBase_BlankIgnored(t *testing.T) {
	resetKeyPrefixBase(t)

	SetKeyPrefixBase("   ")
	assert.Equal(t, "sk-iw-", KeyPrefix)
}

func TestStore_ValidateAndGetActualKey_LegacyIwColonKey(t *testing.T) {
	store, fake := newFakeStore(t)
	ctx := context.Background()

	const legacyPK = "iw:legacy-key-body"
	fake.InjectItem("test-keys", legacyPK, map[string]any{
		"pk":         map[string]any{"S": legacyPK},
		"provider":   map[string]any{"S": "openai"},
		"actual_key": map[string]any{"S": "upstream-openai"},
		"enabled":    map[string]any{"BOOL": true},
		"created_at": map[string]any{"S": time.Now().UTC().Format(time.RFC3339)},
		"updated_at": map[string]any{"S": time.Now().UTC().Format(time.RFC3339)},
	})

	actual, provider, err := store.ValidateAndGetActualKey(ctx, legacyPK)
	require.NoError(t, err)
	assert.Equal(t, "upstream-openai", actual)
	assert.Equal(t, "openai", provider)
}

func TestStore_ValidateAndGetActualKey_PassthroughUpstreamSk(t *testing.T) {
	store, _ := newFakeStore(t)

	actual, provider, err := store.ValidateAndGetActualKey(
		context.Background(),
		"sk-proj-upstream-key-not-ours",
	)
	require.NoError(t, err)
	assert.Equal(t, "sk-proj-upstream-key-not-ours", actual)
	assert.Equal(t, "", provider)
}

func TestStore_CreateKey_PKUsesSkPrefix(t *testing.T) {
	resetKeyPrefixBase(t)
	store, _ := newFakeStore(t)

	created, err := store.CreateKey(context.Background(), "openai", "upstream", "", 0, nil, nil)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(created.PK, "sk-iw-"))
	assert.True(t, HasKeyPrefix(created.PK))
}

func TestStore_ListKeys_IncludesSkPrefixedAndLegacyKeys(t *testing.T) {
	resetKeyPrefixBase(t)
	store, fake := newFakeStore(t)
	ctx := context.Background()

	created, err := store.CreateKey(ctx, "openai", "upstream", "", 0, nil, nil)
	require.NoError(t, err)

	const legacyPK = "iw:listed-legacy"
	fake.InjectItem("test-keys", legacyPK, map[string]any{
		"pk":         map[string]any{"S": legacyPK},
		"provider":   map[string]any{"S": "anthropic"},
		"actual_key": map[string]any{"S": "upstream-anthropic"},
		"enabled":    map[string]any{"BOOL": true},
		"created_at": map[string]any{"S": time.Now().UTC().Format(time.RFC3339)},
		"updated_at": map[string]any{"S": time.Now().UTC().Format(time.RFC3339)},
	})
	_, err = store.CreateShareLink(ctx, created.PK, "admin@example.com")
	require.NoError(t, err)

	keys, err := store.ListKeys(ctx, "")
	require.NoError(t, err)

	pks := make([]string, 0, len(keys))
	for _, k := range keys {
		pks = append(pks, k.PK)
	}
	assert.Contains(t, pks, created.PK)
	assert.Contains(t, pks, legacyPK)
	assert.NotContains(t, pks, ShareKeyPrefix)
}

var hexRegexp = regexp.MustCompile(`^[0-9a-f]+$`)
