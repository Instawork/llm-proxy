package apikeys

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const (
	// KeyPrefix is the prefix for all internal API keys
	KeyPrefix = "iw:"
	// KeyLength is the length of the random part of the key
	KeyLength = 32
)

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
}

// Store handles API key storage in DynamoDB
type Store struct {
	client    *dynamodb.Client
	tableName string
	logger    *slog.Logger
}

// StoreConfig holds configuration for the API key store
type StoreConfig struct {
	TableName string
	Region    string
	Logger    *slog.Logger
}

// NewStore creates a new API key store
func NewStore(cfg StoreConfig) (*Store, error) {
	// Load AWS configuration
	awsConfig, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(cfg.Region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	// Create DynamoDB client
	client := dynamodb.NewFromConfig(awsConfig)

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	store := &Store{
		client:    client,
		tableName: cfg.TableName,
		logger:    logger,
	}

	// Ensure table exists
	if err := store.ensureTableExists(context.TODO()); err != nil {
		return nil, fmt.Errorf("failed to ensure table exists: %w", err)
	}

	return store, nil
}

// ensureTableExists creates the DynamoDB table if it doesn't exist
func (s *Store) ensureTableExists(ctx context.Context) error {
	// Check if table exists
	_, err := s.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(s.tableName),
	})
	if err == nil {
		s.logger.Debug("API key table already exists", "table", s.tableName)
		return nil
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

// GenerateKey generates a new API key with the iw: prefix
func GenerateKey() (string, error) {
	bytes := make([]byte, KeyLength)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random key: %w", err)
	}
	return KeyPrefix + hex.EncodeToString(bytes), nil
}

// CreateKey creates a new API key record
func (s *Store) CreateKey(ctx context.Context, provider, actualKey, description string, dailyCostLimit int64, tags map[string]string) (*APIKey, error) {
	// Generate new key
	newKey, err := GenerateKey()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	apiKey := &APIKey{
		PK:             newKey,
		Provider:       provider,
		ActualKey:      actualKey,
		DailyCostLimit: dailyCostLimit,
		Description:    description,
		CreatedAt:      now,
		UpdatedAt:      now,
		Enabled:        true,
		Tags:           tags,
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
		"key", newKey,
		"provider", provider,
		"description", description,
		"daily_cost_limit", dailyCostLimit)

	return apiKey, nil
}

// GetKey retrieves an API key by its iw: prefixed key
func (s *Store) GetKey(ctx context.Context, key string) (*APIKey, error) {
	// Validate key format
	if !strings.HasPrefix(key, KeyPrefix) {
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

	// Check if key is expired
	if apiKey.ExpiresAt != nil && apiKey.ExpiresAt.Before(time.Now()) {
		return nil, fmt.Errorf("API key has expired")
	}

	// Check if key is enabled
	if !apiKey.Enabled {
		return nil, fmt.Errorf("API key is disabled")
	}

	return &apiKey, nil
}

// UpdateKey updates an existing API key
func (s *Store) UpdateKey(ctx context.Context, key string, updates map[string]interface{}) error {
	// Build update expression
	var updateExpr strings.Builder
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
		case "daily_cost_limit":
			updateExpr.WriteString(", daily_cost_limit = :daily_cost_limit")
			exprAttrValues[":daily_cost_limit"] = &types.AttributeValueMemberN{Value: fmt.Sprintf("%d", value.(int64))}
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
		}
	}

	input := &dynamodb.UpdateItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: key},
		},
		UpdateExpression:          aws.String(updateExpr.String()),
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

	s.logger.Info("Updated API key", "key", key, "updates", updates)
	return nil
}

// DeleteKey deletes an API key
func (s *Store) DeleteKey(ctx context.Context, key string) error {
	_, err := s.client.DeleteItem(ctx, &dynamodb.DeleteItemInput{
		TableName: aws.String(s.tableName),
		Key: map[string]types.AttributeValue{
			"pk": &types.AttributeValueMemberS{Value: key},
		},
		ConditionExpression: aws.String("attribute_exists(pk)"),
	})
	if err != nil {
		return fmt.Errorf("failed to delete API key: %w", err)
	}

	s.logger.Info("Deleted API key", "key", key)
	return nil
}

// ListKeys lists all API keys, optionally filtered by provider
func (s *Store) ListKeys(ctx context.Context, provider string) ([]*APIKey, error) {
	var keys []*APIKey

	if provider != "" {
		// Query by provider index
		result, err := s.client.Query(ctx, &dynamodb.QueryInput{
			TableName:              aws.String(s.tableName),
			IndexName:              aws.String("ProviderIndex"),
			KeyConditionExpression: aws.String("provider = :provider"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":provider": &types.AttributeValueMemberS{Value: provider},
			},
		})
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
	} else {
		// Scan all keys
		result, err := s.client.Scan(ctx, &dynamodb.ScanInput{
			TableName: aws.String(s.tableName),
		})
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
	}

	return keys, nil
}

// ValidateAndGetActualKey validates an API key and returns the actual provider key
func (s *Store) ValidateAndGetActualKey(ctx context.Context, key string) (string, string, error) {
	// If key doesn't have our prefix, return it as-is (passthrough)
	if !strings.HasPrefix(key, KeyPrefix) {
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
