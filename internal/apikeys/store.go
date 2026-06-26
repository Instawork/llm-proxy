package apikeys

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	// KeyLength is the length of the random part of the key
	KeyLength = 32

	// DefaultKeyPrefixBase is the default prefix base (without separator).
	// New keys are generated as "sk-<base>-<hex>"; legacy "<base>-",
	// "<base>_", and "<base>:" forms are accepted on lookup.
	DefaultKeyPrefixBase = "iw"

	keyPrefixSkLead              = "sk-"
	keyPrefixSepNew              = "-"
	keyPrefixSepLegacyUnderscore = "_"
	keyPrefixSepLegacyColon      = ":"
)

var (
	// keyPrefixBase is the configured prefix base (without separator),
	// e.g. "iw". Set once at startup via SetKeyPrefixBase.
	keyPrefixBase = DefaultKeyPrefixBase

	// KeyPrefix is the prefix used to GENERATE new keys ("sk-<base>-").
	// Exported for backwards compatibility with callers/tests that build
	// keys as KeyPrefix+"...". Do NOT use it to decide whether a string is
	// one of our keys — that must accept legacy separators too, so use
	// HasKeyPrefix / TrimKeyPrefix instead.
	KeyPrefix = generationKeyPrefix(keyPrefixBase)

	// acceptedKeyPrefixes lists every prefix recognized as one of our proxy
	// keys, current separator first. Kept in sync by SetKeyPrefixBase.
	acceptedKeyPrefixes = acceptedKeyPrefixesForBase(keyPrefixBase)
)

func generationKeyPrefix(base string) string {
	return keyPrefixSkLead + base + keyPrefixSepNew
}

func acceptedKeyPrefixesForBase(base string) []string {
	return []string{
		generationKeyPrefix(base),
		base + keyPrefixSepNew,
		base + keyPrefixSepLegacyUnderscore,
		base + keyPrefixSepLegacyColon,
	}
}

// SetKeyPrefixBase configures the proxy key prefix base (e.g. "iw"). New
// keys are then generated as "sk-<base>-<hex>", while lookups continue to
// accept "<base>-", "<base>_", and "<base>:" for keys minted under older
// separators. A blank base is ignored so a missing config value falls back
// to the default. Call once at startup before serving traffic — it is not
// safe to call concurrently with key operations.
func SetKeyPrefixBase(base string) {
	base = strings.TrimSpace(base)
	if base == "" {
		return
	}
	keyPrefixBase = base
	KeyPrefix = generationKeyPrefix(base)
	acceptedKeyPrefixes = acceptedKeyPrefixesForBase(base)
}

// KeyPrefixBase returns the configured prefix base (without separator).
func KeyPrefixBase() string { return keyPrefixBase }

// HasKeyPrefix reports whether k carries a recognized proxy-key prefix
// (current "sk-<base>-" or legacy "<base>-" / "<base>_" / "<base>:").
func HasKeyPrefix(k string) bool {
	_, ok := matchedKeyPrefix(k)
	return ok
}

// matchedKeyPrefix returns the recognized prefix that k starts with, if any.
func matchedKeyPrefix(k string) (string, bool) {
	for _, p := range acceptedKeyPrefixes {
		if strings.HasPrefix(k, p) {
			return p, true
		}
	}
	return "", false
}

// TrimKeyPrefix strips a recognized proxy-key prefix from k and returns the
// remainder. If k carries no recognized prefix it is returned unchanged.
func TrimKeyPrefix(k string) string {
	if p, ok := matchedKeyPrefix(k); ok {
		return strings.TrimPrefix(k, p)
	}
	return k
}

// RedactKey returns a short, identifiable form of a proxy API key safe to
// emit to logs and observability sinks. The full key is a bearer secret;
// dumping it into stdout/Datadog/CloudWatch is equivalent to leaking
// credentials. We keep the recognized prefix (preserving whichever
// separator the key actually uses) and the first/last 4 hex chars so that a
// human still has enough signal to correlate the same key across log lines
// without exposing the secret material in the middle.
func RedactKey(k string) string {
	if k == "" {
		return ""
	}
	prefix, ok := matchedKeyPrefix(k)
	if !ok {
		prefix = KeyPrefix
	}
	stripped := strings.TrimPrefix(k, prefix)
	if len(stripped) <= 8 {
		return prefix + "***"
	}
	return prefix + stripped[:4] + "…" + stripped[len(stripped)-4:]
}

// updateFieldNames returns the sorted set of field names being updated.
// We log field names rather than the updates map directly because some
// update payloads include sensitive provider keys (e.g. `actual_key`)
// that should never be persisted into logs in cleartext.
func updateFieldNames(updates map[string]interface{}) []string {
	names := make([]string, 0, len(updates))
	for k := range updates {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

// APIKey represents an API key record in DynamoDB
type APIKey struct {
	// PK is the primary key (the iw: prefixed key)
	PK string `dynamodbav:"pk"`
	// Provider is the LLM provider (openai, anthropic, gemini)
	Provider string `dynamodbav:"provider"`
	// ActualKey is the real API key for the provider
	ActualKey string `dynamodbav:"actual_key"`
	// DailyCostLimit is the 24-hour cost limit in cents
	DailyCostLimit int64 `dynamodbav:"daily_cost_limit"`
	// Description is an optional description of the key
	Description string `dynamodbav:"description,omitempty"`
	// CreatedAt is when the key was created
	CreatedAt time.Time `dynamodbav:"created_at"`
	// UpdatedAt is when the key was last updated
	UpdatedAt time.Time `dynamodbav:"updated_at"`
	// ExpiresAt is when the key expires (optional)
	ExpiresAt *time.Time `dynamodbav:"expires_at,omitempty"`
	// Enabled indicates if the key is active
	Enabled bool `dynamodbav:"enabled"`
	// Tags for organizational purposes
	Tags map[string]string `dynamodbav:"tags,omitempty"`
	// RedactPII overrides the global pii_redact.enabled flag when set.
	// nil = inherit global default.
	RedactPII *bool `dynamodbav:"redact_pii,omitempty"`
	// AllowStreaming overrides the global pii_redact.default_allow_streaming
	// flag when wire-mode scrubbing is active. nil = inherit global default.
	// false forces stream:false on outbound provider requests so responses
	// can be buffered and restored without SSE chunk splitting.
	AllowStreaming *bool `dynamodbav:"allow_streaming,omitempty"`
	// Per-key rate-limit overrides. Zero means "inherit the global/default
	// limit" (i.e. no per-key override for that window). These map onto the
	// rate limiter's PerKey override for the "key:<iw key>" scope.
	RateLimitRPM int `dynamodbav:"rate_limit_rpm,omitempty"` // requests / minute
	RateLimitTPM int `dynamodbav:"rate_limit_tpm,omitempty"` // tokens / minute
	RateLimitRPD int `dynamodbav:"rate_limit_rpd,omitempty"` // requests / day
	RateLimitTPD int `dynamodbav:"rate_limit_tpd,omitempty"` // tokens / day
	// Provisioned is true when actual_key was minted by the provisioner.
	Provisioned bool `dynamodbav:"provisioned,omitempty"`
	// UpstreamKeyID identifies the vendor credential for revoke (service
	// account id, GCP key name, etc.).
	UpstreamKeyID string `dynamodbav:"upstream_key_id,omitempty"`
	// UpstreamKind classifies the upstream credential for revoke handlers.
	UpstreamKind string `dynamodbav:"upstream_kind,omitempty"`
	// OwnerEmail identifies the viewer who owns a personal key.
	OwnerEmail string `dynamodbav:"owner_email,omitempty"`
	// MonthlyCostLimit is the calendar-month cost cap in cents (0 = unlimited).
	MonthlyCostLimit int64 `dynamodbav:"monthly_cost_limit,omitempty"`
	// FirstRequestAt is set once when the proxy observes the first tracked LLM
	// request for this key (cost/usage path). Used by the admin UI to detect
	// keys that have never been wired up.
	FirstRequestAt *time.Time `dynamodbav:"first_request_at,omitempty"`
}

// ErrOwnerKeyExists is returned when an owner already has a key for a provider.
var ErrOwnerKeyExists = errors.New("owner already has a key for this provider")

// KeyRateLimits carries optional per-key rate-limit overrides for CreateKey.
// Zero fields mean "no override" for that window.
type KeyRateLimits struct {
	RPM int
	TPM int
	RPD int
	TPD int
}

// Store handles API key storage in DynamoDB
type Store struct {
	client    *dynamodb.Client
	tableName string
	logger    *slog.Logger
	// firstMarked dedupes MarkFirstRequest DynamoDB writes within a process.
	firstMarked sync.Map
}

// StoreConfig holds configuration for the API key store
type StoreConfig struct {
	TableName string
	Region    string
	// EndpointURL overrides the DynamoDB API endpoint (e.g. dynamodb-local).
	EndpointURL string
	Logger      *slog.Logger
	// AutoCreateTable controls whether NewStore is allowed to CreateTable
	// when the configured table is missing. Defaults to false so a
	// misconfigured staging/production deploy cannot provision resources
	// in whatever AWS account the process happens to authenticate against.
	AutoCreateTable bool
}

// NewStore creates a new API key store.
//
// Startup uses a 30s bounded context for AWS config + table verification.
// When cfg.AutoCreateTable is false (the default) the constructor only
// verifies the table is reachable; otherwise it falls back to CreateTable.
func NewStore(cfg StoreConfig) (*Store, error) {
	startupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	awsConfig, err := config.LoadDefaultConfig(
		startupCtx,
		config.WithRegion(cfg.Region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	endpointURL := cfg.EndpointURL
	if endpointURL == "" {
		endpointURL = os.Getenv("AWS_ENDPOINT_URL")
	}

	var client *dynamodb.Client
	if endpointURL != "" {
		client = dynamodb.NewFromConfig(awsConfig, func(o *dynamodb.Options) {
			o.BaseEndpoint = aws.String(endpointURL)
		})
	} else {
		client = dynamodb.NewFromConfig(awsConfig)
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	store := &Store{
		client:    client,
		tableName: cfg.TableName,
		logger:    logger,
	}

	if cfg.AutoCreateTable {
		if err := store.ensureTableExists(startupCtx); err != nil {
			return nil, fmt.Errorf("failed to ensure table exists: %w", err)
		}
	} else {
		if err := store.verifyTableExists(startupCtx); err != nil {
			return nil, fmt.Errorf("api keys table %q is not accessible (pass AutoCreateTable: true in dev only): %w", cfg.TableName, err)
		}
	}

	return store, nil
}

// verifyTableExists checks that the configured table is reachable and has the
// OwnerProviderIndex GSI required for personal keys.
func (s *Store) verifyTableExists(ctx context.Context) error {
	desc, err := s.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(s.tableName),
	})
	if err != nil {
		return err
	}
	if !ownerProviderIndexActive(desc) {
		return fmt.Errorf("table %s is missing required GSI %s", s.tableName, ownerProviderIndexName)
	}
	return nil
}

// ensureTableExists creates the DynamoDB table if it doesn't exist
func (s *Store) ensureTableExists(ctx context.Context) error {
	// Check if table exists
	desc, err := s.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(s.tableName),
	})
	if err == nil {
		s.logger.Debug("API key table already exists", "table", s.tableName)
		return s.ensureOwnerProviderIndex(ctx, desc)
	}

	// Create table if it doesn't exist
	s.logger.Info("Creating DynamoDB table for API keys", "table", s.tableName)

	createInput := &dynamodb.CreateTableInput{
		TableName: aws.String(s.tableName),
		KeySchema: []types.KeySchemaElement{
			{
				AttributeName: aws.String("pk"),
				KeyType:       types.KeyTypeHash,
			},
		},
		AttributeDefinitions: []types.AttributeDefinition{
			{
				AttributeName: aws.String("pk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String("provider"),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String("owner_email"),
				AttributeType: types.ScalarAttributeTypeS,
			},
		},
		GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{
			{
				IndexName: aws.String("ProviderIndex"),
				KeySchema: []types.KeySchemaElement{
					{
						AttributeName: aws.String("provider"),
						KeyType:       types.KeyTypeHash,
					},
				},
				Projection: &types.Projection{
					ProjectionType: types.ProjectionTypeAll,
				},
			},
			{
				IndexName: aws.String("OwnerProviderIndex"),
				KeySchema: []types.KeySchemaElement{
					{
						AttributeName: aws.String("owner_email"),
						KeyType:       types.KeyTypeHash,
					},
					{
						AttributeName: aws.String("provider"),
						KeyType:       types.KeyTypeRange,
					},
				},
				Projection: &types.Projection{
					ProjectionType: types.ProjectionTypeAll,
				},
			},
		},
		BillingMode: types.BillingModePayPerRequest,
	}

	_, err = s.client.CreateTable(ctx, createInput)
	if err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	// Wait for table to become active
	waiter := dynamodb.NewTableExistsWaiter(s.client)
	err = waiter.Wait(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(s.tableName),
	}, 5*time.Minute)
	if err != nil {
		return fmt.Errorf("failed waiting for table to become active: %w", err)
	}

	s.logger.Info("API key table created successfully", "table", s.tableName)
	return nil
}

const ownerProviderIndexName = "OwnerProviderIndex"

func normalizeOwnerEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

func normalizeProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func ownerProviderLockPK(ownerEmail, provider string) string {
	return "owner-lock:" + normalizeOwnerEmail(ownerEmail) + ":" + normalizeProvider(provider)
}

func ownerProviderIndexActive(desc *dynamodb.DescribeTableOutput) bool {
	if desc == nil || desc.Table == nil {
		return false
	}
	for _, gsi := range desc.Table.GlobalSecondaryIndexes {
		if aws.ToString(gsi.IndexName) == ownerProviderIndexName &&
			gsi.IndexStatus == types.IndexStatusActive {
			return true
		}
	}
	return false
}

func (s *Store) ensureOwnerProviderIndex(ctx context.Context, desc *dynamodb.DescribeTableOutput) error {
	if ownerProviderIndexActive(desc) {
		return nil
	}

	hasOwnerAttr := false
	hasProviderAttr := false
	if desc != nil && desc.Table != nil {
		for _, ad := range desc.Table.AttributeDefinitions {
			switch aws.ToString(ad.AttributeName) {
			case "owner_email":
				hasOwnerAttr = true
			case "provider":
				hasProviderAttr = true
			}
		}
	}

	attrDefs := make([]types.AttributeDefinition, 0, 2)
	if !hasOwnerAttr {
		attrDefs = append(attrDefs, types.AttributeDefinition{
			AttributeName: aws.String("owner_email"),
			AttributeType: types.ScalarAttributeTypeS,
		})
	}
	if !hasProviderAttr {
		attrDefs = append(attrDefs, types.AttributeDefinition{
			AttributeName: aws.String("provider"),
			AttributeType: types.ScalarAttributeTypeS,
		})
	}

	s.logger.Info("Adding OwnerProviderIndex GSI to API key table", "table", s.tableName)
	updateInput := &dynamodb.UpdateTableInput{
		TableName: aws.String(s.tableName),
		GlobalSecondaryIndexUpdates: []types.GlobalSecondaryIndexUpdate{
			{
				Create: &types.CreateGlobalSecondaryIndexAction{
					IndexName: aws.String(ownerProviderIndexName),
					KeySchema: []types.KeySchemaElement{
						{
							AttributeName: aws.String("owner_email"),
							KeyType:       types.KeyTypeHash,
						},
						{
							AttributeName: aws.String("provider"),
							KeyType:       types.KeyTypeRange,
						},
					},
					Projection: &types.Projection{
						ProjectionType: types.ProjectionTypeAll,
					},
				},
			},
		},
	}
	if len(attrDefs) > 0 {
		updateInput.AttributeDefinitions = attrDefs
	}

	if _, err := s.client.UpdateTable(ctx, updateInput); err != nil {
		return fmt.Errorf("failed to add OwnerProviderIndex GSI: %w", err)
	}
	return s.waitForOwnerProviderIndex(ctx)
}

func (s *Store) waitForOwnerProviderIndex(ctx context.Context) error {
	deadline := time.Now().Add(5 * time.Minute)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s GSI", ownerProviderIndexName)
		}

		desc, err := s.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
			TableName: aws.String(s.tableName),
		})
		if err != nil {
			return err
		}
		if ownerProviderIndexActive(desc) {
			s.logger.Info("OwnerProviderIndex GSI is active", "table", s.tableName)
			return nil
		}
		time.Sleep(2 * time.Second)
	}
}

// KeyCreateMeta carries optional auto-provision metadata for CreateKey.
type KeyCreateMeta struct {
	Provisioned   bool
	UpstreamKeyID string
	UpstreamKind  string
}

// GenerateKey generates a new API key using the current generation prefix
// ("sk-<base>-", e.g. "sk-iw-").
func GenerateKey() (string, error) {
	bytes := make([]byte, KeyLength)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random key: %w", err)
	}
	return KeyPrefix + hex.EncodeToString(bytes), nil
}

// CreateKey creates a new API key record.
func (s *Store) CreateKey(ctx context.Context, provider, actualKey, description string, dailyCostLimit int64, tags map[string]string, redactPII *bool, rateLimits ...KeyRateLimits) (*APIKey, error) {
	return s.CreateKeyWithMeta(ctx, provider, actualKey, description, dailyCostLimit, 0, tags, redactPII, KeyCreateMeta{}, rateLimits...)
}

// CreateKeyWithMeta creates a new API key record with optional provision metadata.
func (s *Store) CreateKeyWithMeta(ctx context.Context, provider, actualKey, description string, dailyCostLimit, monthlyCostLimit int64, tags map[string]string, redactPII *bool, meta KeyCreateMeta, rateLimits ...KeyRateLimits) (*APIKey, error) {
	// Generate new key
	newKey, err := GenerateKey()
	if err != nil {
		return nil, err
	}

	var rl KeyRateLimits
	if len(rateLimits) > 0 {
		rl = rateLimits[0]
	}

	now := time.Now()
	apiKey := &APIKey{
		PK:               newKey,
		Provider:         provider,
		ActualKey:        actualKey,
		DailyCostLimit:   dailyCostLimit,
		MonthlyCostLimit: monthlyCostLimit,
		Description:      description,
		CreatedAt:        now,
		UpdatedAt:        now,
		Enabled:          true,
		Tags:             tags,
		RedactPII:        redactPII,
		RateLimitRPM:     rl.RPM,
		RateLimitTPM:     rl.TPM,
		RateLimitRPD:     rl.RPD,
		RateLimitTPD:     rl.TPD,
		Provisioned:      meta.Provisioned,
		UpstreamKeyID:    meta.UpstreamKeyID,
		UpstreamKind:     meta.UpstreamKind,
	}

	// Marshal to DynamoDB attribute values
	av, err := attributevalue.MarshalMap(apiKey)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal API key: %w", err)
	}

	// Put item with condition that it doesn't already exist
	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(s.tableName),
		Item:                av,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create API key: %w", err)
	}

	s.logger.Info("Created new API key",
		"key", RedactKey(newKey),
		"provider", provider,
		"description", description,
		"daily_cost_limit", dailyCostLimit,
		"monthly_cost_limit", monthlyCostLimit)

	return apiKey, nil
}

// CreatePersonalKey creates a viewer-owned personal API key for one provider.
func (s *Store) CreatePersonalKey(ctx context.Context, ownerEmail, provider, actualKey, description string, monthlyCostLimit int64, meta KeyCreateMeta) (*APIKey, error) {
	ownerEmail = normalizeOwnerEmail(ownerEmail)
	provider = normalizeProvider(provider)

	existing, err := s.GetOwnerKeyByProvider(ctx, ownerEmail, provider)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		return nil, ErrOwnerKeyExists
	}

	lockPK := ownerProviderLockPK(ownerEmail, provider)
	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(s.tableName),
		Item: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: lockPK},
		},
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		var ccfe *types.ConditionalCheckFailedException
		if errors.As(err, &ccfe) {
			return nil, ErrOwnerKeyExists
		}
		return nil, fmt.Errorf("failed to acquire owner/provider lock: %w", err)
	}

	newKey, err := GenerateKey()
	if err != nil {
		_, _ = s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
			TableName: aws.String(s.tableName),
			Key: map[string]types.AttributeValue{
				"pk": &types.AttributeValueMemberS{Value: lockPK},
			},
		})
		return nil, err
	}

	now := time.Now()
	apiKey := &APIKey{
		PK:               newKey,
		Provider:         provider,
		ActualKey:        actualKey,
		DailyCostLimit:   0,
		MonthlyCostLimit: monthlyCostLimit,
		OwnerEmail:       ownerEmail,
		Description:      description,
		CreatedAt:        now,
		UpdatedAt:        now,
		Enabled:          true,
		Tags:             map[string]string{"personal": "true"},
		Provisioned:      meta.Provisioned,
		UpstreamKeyID:    meta.UpstreamKeyID,
		UpstreamKind:     meta.UpstreamKind,
	}

	av, err := attributevalue.MarshalMap(apiKey)
	if err != nil {
		_, _ = s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
			TableName: aws.String(s.tableName),
			Key: map[string]types.AttributeValue{
				"pk": &types.AttributeValueMemberS{Value: lockPK},
			},
		})
		return nil, fmt.Errorf("failed to marshal API key: %w", err)
	}

	_, err = s.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName:           aws.String(s.tableName),
		Item:                av,
		ConditionExpression: aws.String("attribute_not_exists(pk)"),
	})
	if err != nil {
		_, _ = s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
			TableName: aws.String(s.tableName),
			Key: map[string]types.AttributeValue{
				"pk": &types.AttributeValueMemberS{Value: lockPK},
			},
		})
		return nil, fmt.Errorf("failed to create personal API key: %w", err)
	}

	s.logger.Info("Created personal API key",
		"key", RedactKey(newKey),
		"provider", provider,
		"owner_email", ownerEmail,
		"monthly_cost_limit", monthlyCostLimit)

	return apiKey, nil
}

// GetKeyRecord retrieves an API key without enabled/expiry enforcement.
// Used by admin tooling that must inspect or mutate disabled keys.
func (s *Store) GetKeyRecord(ctx context.Context, key string) (*APIKey, error) {
	if !HasKeyPrefix(key) {
		return nil, fmt.Errorf("invalid key format: must start with %s", KeyPrefix)
	}

	result, err := s.client.GetItem(ctx, &dynamodb.GetItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: key},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to get API key: %w", err)
	}
	if result.Item == nil {
		return nil, fmt.Errorf("API key not found")
	}

	var apiKey APIKey
	if err := attributevalue.UnmarshalMap(result.Item, &apiKey); err != nil {
		return nil, fmt.Errorf("failed to unmarshal API key: %w", err)
	}
	return &apiKey, nil
}

// MarkFirstRequest records the first tracked LLM request time for a proxy key.
// Idempotent: no-op when first_request_at is already set. Uses an in-process
// dedupe map so hot keys do not hammer DynamoDB on every request.
func (s *Store) MarkFirstRequest(ctx context.Context, key string, at time.Time) error {
	if !HasKeyPrefix(key) {
		return nil
	}
	if _, loaded := s.firstMarked.LoadOrStore(key, struct{}{}); loaded {
		return nil
	}
	_, err := s.client.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: key},
		},
		UpdateExpression:    aws.String("SET first_request_at = :t, updated_at = :u"),
		ConditionExpression: aws.String("attribute_not_exists(first_request_at)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":t": &types.AttributeValueMemberS{Value: at.UTC().Format(time.RFC3339)},
			":u": &types.AttributeValueMemberS{Value: at.UTC().Format(time.RFC3339)},
		},
	})
	if err != nil {
		var cond *types.ConditionalCheckFailedException
		if errors.As(err, &cond) {
			return nil
		}
		s.firstMarked.Delete(key)
		return fmt.Errorf("mark first request: %w", err)
	}
	return nil
}

// GetKey retrieves an API key by its iw: prefixed key
func (s *Store) GetKey(ctx context.Context, key string) (*APIKey, error) {
	apiKey, err := s.GetKeyRecord(ctx, key)
	if err != nil {
		return nil, err
	}

	if apiKey.ExpiresAt != nil && apiKey.ExpiresAt.Before(time.Now()) {
		return nil, fmt.Errorf("API key has expired")
	}
	if !apiKey.Enabled {
		return nil, fmt.Errorf("API key is disabled")
	}

	return apiKey, nil
}

// UpdateKey updates an existing API key
func (s *Store) UpdateKey(ctx context.Context, key string, updates map[string]interface{}) error {
	// Build update expression
	var updateExpr strings.Builder
	var removeParts []string
	exprAttrValues := make(map[string]types.AttributeValue)
	exprAttrNames := make(map[string]string)

	updateExpr.WriteString("SET updated_at = :updated_at")
	exprAttrValues[":updated_at"] = &types.AttributeValueMemberS{
		Value: time.Now().Format(time.RFC3339),
	}

	for field, value := range updates {
		switch field {
		case "actual_key":
			updateExpr.WriteString(", actual_key = :actual_key")
			exprAttrValues[":actual_key"] = &types.AttributeValueMemberS{Value: value.(string)}
		case "upstream_key_id":
			updateExpr.WriteString(", upstream_key_id = :upstream_key_id")
			exprAttrValues[":upstream_key_id"] = &types.AttributeValueMemberS{Value: value.(string)}
		case "daily_cost_limit":
			updateExpr.WriteString(", daily_cost_limit = :daily_cost_limit")
			exprAttrValues[":daily_cost_limit"] = &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", value.(int64))}
		case "monthly_cost_limit":
			updateExpr.WriteString(", monthly_cost_limit = :monthly_cost_limit")
			exprAttrValues[":monthly_cost_limit"] = &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", value.(int64))}
		case "description":
			updateExpr.WriteString(", #desc = :description")
			exprAttrNames["#desc"] = "description"
			exprAttrValues[":description"] = &types.AttributeValueMemberS{Value: value.(string)}
		case "enabled":
			updateExpr.WriteString(", enabled = :enabled")
			exprAttrValues[":enabled"] = &types.AttributeValueMemberBOOL{Value: value.(bool)}
		case "expires_at":
			updateExpr.WriteString(", expires_at = :expires_at")
			if t, ok := value.(time.Time); ok {
				exprAttrValues[":expires_at"] = &types.AttributeValueMemberS{Value: t.Format(time.RFC3339)}
			}
		case "tags":
			updateExpr.WriteString(", tags = :tags")
			if tags, ok := value.(map[string]string); ok {
				av, _ := attributevalue.Marshal(tags)
				exprAttrValues[":tags"] = av
			}
		case "redact_pii":
			if value == nil {
				removeParts = append(removeParts, "redact_pii")
			} else {
				updateExpr.WriteString(", redact_pii = :redact_pii")
				exprAttrValues[":redact_pii"] = &types.AttributeValueMemberBOOL{Value: value.(bool)}
			}
		case "rate_limit_rpm":
			updateExpr.WriteString(", rate_limit_rpm = :rate_limit_rpm")
			exprAttrValues[":rate_limit_rpm"] = &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", value.(int))}
		case "rate_limit_tpm":
			updateExpr.WriteString(", rate_limit_tpm = :rate_limit_tpm")
			exprAttrValues[":rate_limit_tpm"] = &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", value.(int))}
		case "rate_limit_rpd":
			updateExpr.WriteString(", rate_limit_rpd = :rate_limit_rpd")
			exprAttrValues[":rate_limit_rpd"] = &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", value.(int))}
		case "rate_limit_tpd":
			updateExpr.WriteString(", rate_limit_tpd = :rate_limit_tpd")
			exprAttrValues[":rate_limit_tpd"] = &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", value.(int))}
		}
	}

	expression := updateExpr.String()
	if len(removeParts) > 0 {
		expression += " REMOVE " + strings.Join(removeParts, ", ")
	}

	input := &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: key},
		},
		UpdateExpression:          aws.String(expression),
		ExpressionAttributeValues: exprAttrValues,
		ConditionExpression:       aws.String("attribute_exists(pk)"),
	}

	if len(exprAttrNames) > 0 {
		input.ExpressionAttributeNames = exprAttrNames
	}

	_, err := s.client.UpdateItem(ctx, input)
	if err != nil {
		return fmt.Errorf("failed to update API key: %w", err)
	}

	s.logger.Info("Updated API key", "key", RedactKey(key), "update_fields", updateFieldNames(updates))
	return nil
}

// DeleteKey deletes an API key
func (s *Store) DeleteKey(ctx context.Context, key string) error {
	record, err := s.GetKeyRecord(ctx, key)
	if err != nil {
		return fmt.Errorf("failed to delete API key: %w", err)
	}

	_, err = s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: key},
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
	})
	if err != nil {
		return fmt.Errorf("failed to delete API key: %w", err)
	}

	if record.OwnerEmail != "" && record.Provider != "" {
		_, _ = s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
			TableName: aws.String(s.tableName),
			Key: map[string]types.AttributeValue{
				"pk": &types.AttributeValueMemberS{Value: ownerProviderLockPK(record.OwnerEmail, record.Provider)},
			},
		})
	}

	s.logger.Info("Deleted API key", "key", RedactKey(key))
	return nil
}

// ListKeys lists all API keys, optionally filtered by provider
func (s *Store) ListKeys(ctx context.Context, provider string) ([]*APIKey, error) {
	var keys []*APIKey

	if provider != "" {
		queryInput := &dynamodb.QueryInput{
			TableName:              aws.String(s.tableName),
			IndexName:              aws.String("ProviderIndex"),
			KeyConditionExpression: aws.String("provider = :provider"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":provider": &types.AttributeValueMemberS{Value: provider},
			},
		}
		for {
			result, err := s.client.Query(ctx, queryInput)
			if err != nil {
				return nil, fmt.Errorf("failed to query API keys by provider: %w", err)
			}
			for _, item := range result.Items {
				var apiKey APIKey
				if err := attributevalue.UnmarshalMap(item, &apiKey); err != nil {
					s.logger.Warn("Failed to unmarshal API key", "error", err)
					continue
				}
				keys = append(keys, &apiKey)
			}
			if len(result.LastEvaluatedKey) == 0 {
				break
			}
			queryInput.ExclusiveStartKey = result.LastEvaluatedKey
		}
	} else {
		// Scan all keys. Match current "sk-<base>-" keys and legacy
		// "<base>-" / "<base>_" / "<base>:" keys, while co-located
		// share-link records (pk "share:<uuid>") never get unmarshaled
		// into the key list. Paginate so large key sets are not truncated
		// at DynamoDB's 1 MiB response limit.
		scanInput := &dynamodb.ScanInput{
			TableName: aws.String(s.tableName),
			FilterExpression: aws.String(
				"begins_with(pk, :skPfx) OR begins_with(pk, :legacyPfx)",
			),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":skPfx":     &types.AttributeValueMemberS{Value: generationKeyPrefix(keyPrefixBase)},
				":legacyPfx": &types.AttributeValueMemberS{Value: keyPrefixBase},
			},
		}
		for {
			result, err := s.client.Scan(ctx, scanInput)
			if err != nil {
				return nil, fmt.Errorf("failed to scan API keys: %w", err)
			}
			for _, item := range result.Items {
				var apiKey APIKey
				if err := attributevalue.UnmarshalMap(item, &apiKey); err != nil {
					s.logger.Warn("Failed to unmarshal API key", "error", err)
					continue
				}
				keys = append(keys, &apiKey)
			}
			if len(result.LastEvaluatedKey) == 0 {
				break
			}
			scanInput.ExclusiveStartKey = result.LastEvaluatedKey
		}
	}

	return keys, nil
}

// ListKeysByOwner returns personal keys owned by ownerEmail, optionally filtered by provider.
func (s *Store) ListKeysByOwner(ctx context.Context, ownerEmail, providerFilter string) ([]*APIKey, error) {
	ownerEmail = normalizeOwnerEmail(ownerEmail)
	providerFilter = normalizeProvider(providerFilter)

	keyCond := "owner_email = :owner"
	exprValues := map[string]types.AttributeValue{
		":owner": &types.AttributeValueMemberS{Value: ownerEmail},
	}
	if providerFilter != "" {
		keyCond += " AND provider = :provider"
		exprValues[":provider"] = &types.AttributeValueMemberS{Value: providerFilter}
	}

	queryInput := &dynamodb.QueryInput{
		TableName:                 aws.String(s.tableName),
		IndexName:                 aws.String("OwnerProviderIndex"),
		KeyConditionExpression:    aws.String(keyCond),
		ExpressionAttributeValues: exprValues,
	}

	var keys []*APIKey
	for {
		result, err := s.client.Query(ctx, queryInput)
		if err != nil {
			return nil, fmt.Errorf("failed to query API keys by owner: %w", err)
		}
		for _, item := range result.Items {
			var apiKey APIKey
			if err := attributevalue.UnmarshalMap(item, &apiKey); err != nil {
				s.logger.Warn("Failed to unmarshal API key", "error", err)
				continue
			}
			keys = append(keys, &apiKey)
		}
		if len(result.LastEvaluatedKey) == 0 {
			break
		}
		queryInput.ExclusiveStartKey = result.LastEvaluatedKey
	}
	return keys, nil
}

// GetOwnerKeyByProvider returns the owner's key for a provider, or nil if none exists.
func (s *Store) GetOwnerKeyByProvider(ctx context.Context, ownerEmail, provider string) (*APIKey, error) {
	keys, err := s.ListKeysByOwner(ctx, ownerEmail, provider)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, nil
	}
	return keys[0], nil
}

// LookupProxyKey returns the DynamoDB record for a proxy bearer token
// (current "sk-<base>-" or legacy "<base>-" / "<base>_" / "<base>:" form).
func (s *Store) LookupProxyKey(ctx context.Context, bearer string) (*APIKey, error) {
	if !HasKeyPrefix(bearer) {
		return nil, nil
	}
	return s.GetKey(ctx, bearer)
}

// ValidateAndGetActualKey validates an API key and returns the actual provider key
func (s *Store) ValidateAndGetActualKey(ctx context.Context, key string) (string, string, error) {
	// If key doesn't have our prefix, return it as-is (passthrough)
	if !HasKeyPrefix(key) {
		return key, "", nil
	}

	// Look up the key in DynamoDB
	apiKey, err := s.GetKey(ctx, key)
	if err != nil {
		return "", "", fmt.Errorf("invalid API key: %w", err)
	}

	// Return the actual provider key and provider name
	return apiKey.ActualKey, apiKey.Provider, nil
}
