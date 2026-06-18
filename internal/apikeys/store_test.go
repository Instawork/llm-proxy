package apikeys

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Instawork/llm-proxy/internal/testhelpers/dynamodbfake"
)

// newFakeStore wires the shared dynamodbfake server into a Store under
// test.  The shared fake lives in internal/testhelpers/dynamodbfake so
// the cost-tracking package and any future DynamoDB-backed module can
// reuse the same scaffolding without forking it.
func newFakeStore(t *testing.T) (*Store, *dynamodbfake.Server) {
	t.Helper()
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())
	store, err := NewStore(StoreConfig{TableName: "test-keys", Region: "us-west-2", AutoCreateTable: true})
	require.NoError(t, err)
	return store, fake
}

// ----------------------------------------------------------------------------
// Pure helper coverage
// ----------------------------------------------------------------------------

func TestGenerateKey_HasPrefixAndIsRandom(t *testing.T) {
	a, err := GenerateKey()
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(a, KeyPrefix))
	assert.True(t, strings.HasPrefix(a, "sk-"))
	assert.Greater(t, len(a), len(KeyPrefix)+10)

	b, err := GenerateKey()
	require.NoError(t, err)
	assert.NotEqual(t, a, b, "two generated keys should differ")
}

func TestSetKeyPrefixBase_GeneratesSkPrefixedKeys(t *testing.T) {
	t.Cleanup(func() { SetKeyPrefixBase(DefaultKeyPrefixBase) })

	SetKeyPrefixBase("acme")
	assert.Equal(t, "sk-acme-", KeyPrefix)

	key, err := GenerateKey()
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(key, "sk-acme-"))
}

func TestHasKeyPrefix_AcceptsCurrentLegacyAndRejectsUpstreamSk(t *testing.T) {
	t.Cleanup(func() { SetKeyPrefixBase(DefaultKeyPrefixBase) })

	assert.True(t, HasKeyPrefix(KeyPrefix+"abc123"))
	assert.True(t, HasKeyPrefix("iw:legacy"))
	assert.True(t, HasKeyPrefix("iw_legacy"))
	assert.True(t, HasKeyPrefix("iw-legacy"))
	assert.False(t, HasKeyPrefix("sk-direct"))
	assert.False(t, HasKeyPrefix("sk-proj-upstream"))
}

// ----------------------------------------------------------------------------
// Constructor success path (table appears to already exist)
// ----------------------------------------------------------------------------

func TestNewStore_TableAlreadyExists(t *testing.T) {
	store, _ := newFakeStore(t)
	require.NotNil(t, store)
	assert.Equal(t, "test-keys", store.tableName)
}

// ----------------------------------------------------------------------------
// CreateKey -> GetKey round-trip
// ----------------------------------------------------------------------------

func TestStore_CreateAndGetKey(t *testing.T) {
	store, _ := newFakeStore(t)

	created, err := store.CreateKey(context.Background(), "openai", "real-sk", "test key", 1000, map[string]string{"team": "platform"}, nil)
	require.NoError(t, err)
	require.NotNil(t, created)
	assert.True(t, strings.HasPrefix(created.PK, KeyPrefix))
	assert.Equal(t, "openai", created.Provider)
	assert.Equal(t, "real-sk", created.ActualKey)
	assert.True(t, created.Enabled)

	got, err := store.GetKey(context.Background(), created.PK)
	require.NoError(t, err)
	assert.Equal(t, created.PK, got.PK)
	assert.Equal(t, "openai", got.Provider)
}

func TestStore_CreateKey_WithRateLimits(t *testing.T) {
	store, _ := newFakeStore(t)

	created, err := store.CreateKey(
		context.Background(),
		"openai",
		"real-sk",
		"limited",
		100,
		nil,
		nil,
		KeyRateLimits{RPM: 10, TPM: 20, RPD: 30, TPD: 40},
	)
	require.NoError(t, err)
	assert.Equal(t, 10, created.RateLimitRPM)
	assert.Equal(t, 20, created.RateLimitTPM)
	assert.Equal(t, 30, created.RateLimitRPD)
	assert.Equal(t, 40, created.RateLimitTPD)

	record, err := store.GetKeyRecord(context.Background(), created.PK)
	require.NoError(t, err)
	assert.Equal(t, 10, record.RateLimitRPM)
	assert.Equal(t, 20, record.RateLimitTPM)

	lc, ok := RateLimitOverrides(record)
	require.True(t, ok)
	assert.Equal(t, 10, lc.RequestsPerMinute)
	assert.Equal(t, 40, lc.TokensPerDay)
}

func TestStore_GetKey_InvalidFormat(t *testing.T) {
	store, _ := newFakeStore(t)
	_, err := store.GetKey(context.Background(), "no-prefix")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid key format")
}

func TestStore_GetKey_NotFound(t *testing.T) {
	store, _ := newFakeStore(t)
	_, err := store.GetKey(context.Background(), KeyPrefix+"missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ----------------------------------------------------------------------------
// UpdateKey covers all the field branches in the switch
// ----------------------------------------------------------------------------

func TestStore_UpdateKey_AllFields(t *testing.T) {
	store, _ := newFakeStore(t)
	created, err := store.CreateKey(context.Background(), "openai", "real-key-v1", "initial", 1000, nil, nil)
	require.NoError(t, err)

	expiresAt := time.Now().Add(1 * time.Hour).UTC().Truncate(time.Second)
	updates := map[string]interface{}{
		"actual_key":       "new-real-key",
		"daily_cost_limit": int64(2000),
		"description":      "updated",
		"enabled":          false,
		"expires_at":       expiresAt,
		"tags":             map[string]string{"a": "b"},
		"rate_limit_rpm":   10,
		"rate_limit_tpm":   20,
		"rate_limit_rpd":   30,
		"rate_limit_tpd":   40,
		"unknown_field":    "ignored", // exercises default branch
	}
	require.NoError(t, store.UpdateKey(context.Background(), created.PK, updates))
	// Note: the dynamodbfake does not interpret DynamoDB UpdateExpressions,
	// so we cannot assert that every field round-tripped via GetKey here.
	// The integration tests (gated by LLM_PROXY_RUN_INTEGRATION) cover
	// that. This unit test still exercises the marshal/expression-build
	// code paths in Store.UpdateKey, which is its primary value.
}

func TestStore_LookupProxyKey(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()

	created, err := store.CreateKey(ctx, "openai", "real-sk", "", 100, nil, nil)
	require.NoError(t, err)

	record, err := store.LookupProxyKey(ctx, created.PK)
	require.NoError(t, err)
	require.NotNil(t, record)
	assert.Equal(t, created.PK, record.PK)

	noPrefix, err := store.LookupProxyKey(ctx, "sk-direct")
	require.NoError(t, err)
	assert.Nil(t, noPrefix)

	_, err = store.LookupProxyKey(ctx, KeyPrefix+"missing")
	require.Error(t, err)
}

// ----------------------------------------------------------------------------
// DeleteKey
// ----------------------------------------------------------------------------

func TestStore_DeleteKey(t *testing.T) {
	store, _ := newFakeStore(t)
	created, err := store.CreateKey(context.Background(), "openai", "real-sk", "", 100, nil, nil)
	require.NoError(t, err)

	// Sanity: GetKey works pre-delete.
	_, err = store.GetKey(context.Background(), created.PK)
	require.NoError(t, err)

	require.NoError(t, store.DeleteKey(context.Background(), created.PK))

	// Round-trip: post-delete GetKey must surface a "not found" error.
	// Without this assertion the previous version of the test passed
	// even if DeleteKey silently no-op'd.
	_, err = store.GetKey(context.Background(), created.PK)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

// ----------------------------------------------------------------------------
// ListKeys (both query and scan paths)
// ----------------------------------------------------------------------------

func TestStore_ListKeys_ByProviderAndAll(t *testing.T) {
	store, _ := newFakeStore(t)
	_, err := store.CreateKey(context.Background(), "openai", "real-1", "", 100, nil, nil)
	require.NoError(t, err)
	_, err = store.CreateKey(context.Background(), "anthropic", "real-2", "", 100, nil, nil)
	require.NoError(t, err)

	all, err := store.ListKeys(context.Background(), "")
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(all), 2)

	openaiKeys, err := store.ListKeys(context.Background(), "openai")
	require.NoError(t, err)
	for _, k := range openaiKeys {
		assert.NotEmpty(t, k.PK)
	}
}

// ----------------------------------------------------------------------------
// ValidateAndGetActualKey: both passthrough and lookup branches
// ----------------------------------------------------------------------------

func TestStore_ValidateAndGetActualKey_Passthrough(t *testing.T) {
	store, _ := newFakeStore(t)
	actual, provider, err := store.ValidateAndGetActualKey(context.Background(), "no-prefix-key")
	require.NoError(t, err)
	assert.Equal(t, "no-prefix-key", actual)
	assert.Equal(t, "", provider)
}

func TestStore_ValidateAndGetActualKey_Lookup(t *testing.T) {
	store, _ := newFakeStore(t)
	created, err := store.CreateKey(context.Background(), "openai", "real-key-2", "", 100, nil, nil)
	require.NoError(t, err)

	actual, provider, err := store.ValidateAndGetActualKey(context.Background(), created.PK)
	require.NoError(t, err)
	assert.Equal(t, "real-key-2", actual)
	assert.Equal(t, "openai", provider)
}

func TestStore_ValidateAndGetActualKey_NotFound(t *testing.T) {
	store, _ := newFakeStore(t)
	_, _, err := store.ValidateAndGetActualKey(context.Background(), KeyPrefix+"missing")
	require.Error(t, err)
}

// ----------------------------------------------------------------------------
// Error paths in NewStore — table describe fails permanently
// ----------------------------------------------------------------------------

func TestNewStore_DescribeTableFails_TriggersCreatePath(t *testing.T) {
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())

	// Pre-fail DescribeTable so NewStore's ensureTableExists falls through
	// to CreateTable.  The waiter then re-polls DescribeTable, which
	// succeeds on the second call. This path is only reachable when
	// AutoCreateTable is set true (dev only).
	fake.FailOnce("DescribeTable", errors.New("ResourceNotFoundException"))
	store, err := NewStore(StoreConfig{
		TableName:       "tbl",
		Region:          "us-west-2",
		AutoCreateTable: true,
	})
	require.NoError(t, err)
	require.NotNil(t, store)
}

// TestNewStore_DescribeTableFails_DefaultRefusesToCreate verifies the
// production-safe default (AutoCreateTable: false) propagates the
// DescribeTable error instead of silently creating the table.
func TestNewStore_DescribeTableFails_DefaultRefusesToCreate(t *testing.T) {
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())

	fake.FailOnce("DescribeTable", errors.New("ResourceNotFoundException"))
	_, err := NewStore(StoreConfig{TableName: "tbl", Region: "us-west-2"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not accessible")
}

func TestStore_GetKey_DisabledReturnsError(t *testing.T) {
	store, fake := newFakeStore(t)
	pk := KeyPrefix + "disabled"
	now := time.Now().Format(time.RFC3339Nano)
	fake.InjectItem("test-keys", pk, map[string]any{
		"pk":               map[string]any{"S": pk},
		"provider":         map[string]any{"S": "openai"},
		"actual_key":       map[string]any{"S": "real"},
		"daily_cost_limit": map[string]any{"N": "0"},
		"created_at":       map[string]any{"S": now},
		"updated_at":       map[string]any{"S": now},
		"enabled":          map[string]any{"BOOL": false},
	})
	_, err := store.GetKey(context.Background(), pk)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "disabled")
}

func TestStore_GetKey_ExpiredReturnsError(t *testing.T) {
	store, fake := newFakeStore(t)
	pk := KeyPrefix + "expired"
	now := time.Now().Format(time.RFC3339Nano)
	past := time.Now().Add(-1 * time.Hour).Format(time.RFC3339Nano)
	fake.InjectItem("test-keys", pk, map[string]any{
		"pk":               map[string]any{"S": pk},
		"provider":         map[string]any{"S": "openai"},
		"actual_key":       map[string]any{"S": "real"},
		"daily_cost_limit": map[string]any{"N": "0"},
		"created_at":       map[string]any{"S": now},
		"updated_at":       map[string]any{"S": now},
		"expires_at":       map[string]any{"S": past},
		"enabled":          map[string]any{"BOOL": true},
	})
	_, err := store.GetKey(context.Background(), pk)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expired")
}

// CreateKey error path: server returns ConditionalCheckFailedException
// when the key already exists.
func TestStore_CreateKey_PutItemError(t *testing.T) {
	store, fake := newFakeStore(t)
	fake.FailOnce("PutItem", errors.New("ConditionalCheckFailedException"))
	_, err := store.CreateKey(context.Background(), "openai", "real", "", 100, nil, nil)
	require.Error(t, err)
}

// UpdateKey error path: server returns an error.
func TestStore_UpdateKey_Error(t *testing.T) {
	store, fake := newFakeStore(t)
	fake.FailOnce("UpdateItem", errors.New("ResourceNotFoundException"))
	err := store.UpdateKey(context.Background(), KeyPrefix+"x", map[string]interface{}{"enabled": false})
	require.Error(t, err)
}

// DeleteKey error path: server returns an error.
func TestStore_DeleteKey_Error(t *testing.T) {
	store, fake := newFakeStore(t)
	fake.FailOnce("DeleteItem", errors.New("ConditionalCheckFailedException"))
	err := store.DeleteKey(context.Background(), KeyPrefix+"x")
	require.Error(t, err)
}

// ListKeys error paths: Query and Scan both fail.
func TestStore_ListKeys_QueryError(t *testing.T) {
	store, fake := newFakeStore(t)
	fake.FailOnce("Query", errors.New("InternalServerError"))
	_, err := store.ListKeys(context.Background(), "openai")
	require.Error(t, err)
}

func TestStore_ListKeys_ScanError(t *testing.T) {
	store, fake := newFakeStore(t)
	fake.FailOnce("Scan", errors.New("InternalServerError"))
	_, err := store.ListKeys(context.Background(), "")
	require.Error(t, err)
}

func TestStore_EnsureOwnerProviderIndexOnExistingTable(t *testing.T) {
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())

	store, err := NewStore(StoreConfig{
		TableName:       "test-keys",
		Region:          "us-west-2",
		AutoCreateTable: true,
	})
	require.NoError(t, err)
	require.NotNil(t, store)

	_, err = store.ListKeysByOwner(context.Background(), "viewer@example.com", "")
	require.NoError(t, err)
}

func TestStore_CreatePersonalKeyAndListByOwner(t *testing.T) {
	store, _ := newFakeStore(t)
	ctx := context.Background()

	key, err := store.CreatePersonalKey(ctx, "viewer@example.com", "openai", "sk-viewer", "mine", 1000, KeyCreateMeta{})
	require.NoError(t, err)
	assert.Equal(t, "viewer@example.com", key.OwnerEmail)
	assert.Equal(t, int64(1000), key.MonthlyCostLimit)
	assert.Equal(t, int64(0), key.DailyCostLimit)
	assert.Equal(t, "true", key.Tags["personal"])

	existing, err := store.GetOwnerKeyByProvider(ctx, "viewer@example.com", "openai")
	require.NoError(t, err)
	require.NotNil(t, existing)
	assert.Equal(t, key.PK, existing.PK)

	_, err = store.CreatePersonalKey(ctx, "viewer@example.com", "openai", "sk-dup", "", 1000, KeyCreateMeta{})
	require.ErrorIs(t, err, ErrOwnerKeyExists)

	require.NoError(t, store.DeleteKey(ctx, key.PK))

	recreated, err := store.CreatePersonalKey(ctx, "viewer@example.com", "openai", "sk-viewer-2", "mine again", 1000, KeyCreateMeta{})
	require.NoError(t, err)
	assert.NotEqual(t, key.PK, recreated.PK)

	keys, err := store.ListKeysByOwner(ctx, "viewer@example.com", "")
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, recreated.PK, keys[0].PK)

	filtered, err := store.ListKeysByOwner(ctx, "viewer@example.com", "anthropic")
	require.NoError(t, err)
	assert.Empty(t, filtered)

	other, err := store.ListKeysByOwner(ctx, "other@example.com", "")
	require.NoError(t, err)
	assert.Empty(t, other)
}

func TestStore_GetKeyRecord_LoadsMonthlyCostLimit(t *testing.T) {
	store, fake := newFakeStore(t)
	pk := KeyPrefix + "personal-monthly"
	now := time.Now().Format(time.RFC3339Nano)
	fake.InjectItem("test-keys", pk, map[string]any{
		"pk":                 map[string]any{"S": pk},
		"provider":           map[string]any{"S": "gemini"},
		"actual_key":         map[string]any{"S": "real"},
		"daily_cost_limit":   map[string]any{"N": "0"},
		"monthly_cost_limit": map[string]any{"N": "1000"},
		"owner_email":        map[string]any{"S": "viewer@example.com"},
		"tags":               map[string]any{"M": map[string]any{"personal": map[string]any{"S": "true"}}},
		"created_at":         map[string]any{"S": now},
		"updated_at":         map[string]any{"S": now},
		"enabled":            map[string]any{"BOOL": true},
	})

	record, err := store.GetKeyRecord(context.Background(), pk)
	require.NoError(t, err)
	require.NotNil(t, record)
	assert.True(t, IsPersonalKey(record))
	assert.Equal(t, int64(0), record.DailyCostLimit)
	assert.Equal(t, int64(1000), record.MonthlyCostLimit)
}
