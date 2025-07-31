package cost

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	configPkg "github.com/Instawork/llm-proxy/internal/config"
)

// DynamoDBTransportConfig holds configuration for the DynamoDB transport
type DynamoDBTransportConfig struct {
	TableName string
	Region    string
	Logger    *slog.Logger
}

// DynamoDBTransport implements Transport interface for DynamoDB-based cost tracking
type DynamoDBTransport struct {
	client    *dynamodb.Client
	tableName string
	logger    *slog.Logger
}

// DynamoDBCostRecord represents a cost record as stored in DynamoDB
type DynamoDBCostRecord struct {
	PK           string  `dynamodbav:"pk"`        // Partition key: "COST#YYYY-MM-DD"
	SK           string  `dynamodbav:"sk"`        // Sort key: "TIMESTAMP#requestId"
	GSI1PK       string  `dynamodbav:"gsi1pk"`    // ProviderModelIndex partition key: "PROVIDER#providerName"
	GSI1SK       string  `dynamodbav:"gsi1sk"`    // ProviderModelIndex sort key: "MODEL#modelName#TIMESTAMP"
	GSI2PK       string  `dynamodbav:"gsi2pk"`    // UserProviderIndex partition key: "USER#userID"
	GSI2SK       string  `dynamodbav:"gsi2sk"`    // UserProviderIndex sort key: "PROVIDER#providerName#TIMESTAMP"
	GSI3PK       string  `dynamodbav:"gsi3pk"`    // ModelProviderIndex partition key: "MODEL#modelName"
	GSI3SK       string  `dynamodbav:"gsi3sk"`    // ModelProviderIndex sort key: "PROVIDER#providerName#TIMESTAMP"
	TTL          int64   `dynamodbav:"ttl"`       // TTL for automatic cleanup (optional)
	Timestamp    int64   `dynamodbav:"timestamp"` // Unix timestamp for easier queries
	RequestID    string  `dynamodbav:"request_id,omitempty"`
	UserID       string  `dynamodbav:"user_id,omitempty"`
	IPAddress    string  `dynamodbav:"ip_address,omitempty"`
	Provider     string  `dynamodbav:"provider"`
	Model        string  `dynamodbav:"model"`
	Endpoint     string  `dynamodbav:"endpoint"`
	IsStreaming  bool    `dynamodbav:"is_streaming"`
	InputTokens  int     `dynamodbav:"input_tokens"`
	OutputTokens int     `dynamodbav:"output_tokens"`
	TotalTokens  int     `dynamodbav:"total_tokens"`
	InputCost    float64 `dynamodbav:"input_cost"`
	OutputCost   float64 `dynamodbav:"output_cost"`
	TotalCost    float64 `dynamodbav:"total_cost"`
	FinishReason string  `dynamodbav:"finish_reason,omitempty"`
}

// NewDynamoDBTransport creates a new DynamoDB-based transport
func NewDynamoDBTransport(cfg DynamoDBTransportConfig) (*DynamoDBTransport, error) {
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

	transport := &DynamoDBTransport{
		client:    client,
		tableName: cfg.TableName,
		logger:    logger,
	}

	// Ensure table exists
	if err := transport.ensureTableExists(context.TODO()); err != nil {
		return nil, fmt.Errorf("failed to ensure table exists: %w", err)
	}

	return transport, nil
}

// FromConfig creates a DynamoDBTransport from configuration
func (dt *DynamoDBTransport) FromConfig(transportConfig interface{}, logger *slog.Logger) (Transport, error) {
	switch cfg := transportConfig.(type) {
	case *configPkg.TransportConfig:
		if cfg.DynamoDB == nil {
			return nil, fmt.Errorf("dynamodb transport configuration not found")
		}

		logger.Debug("💰 DynamoDB Transport: Creating from structured config",
			"table_name", cfg.DynamoDB.TableName,
			"region", cfg.DynamoDB.Region)

		config := DynamoDBTransportConfig{
			TableName: cfg.DynamoDB.TableName,
			Region:    cfg.DynamoDB.Region,
			Logger:    logger,
		}
		return NewDynamoDBTransport(config)

	case map[string]interface{}:
		dynamoConfig, ok := cfg["dynamodb"].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("dynamodb transport configuration not found")
		}
		tableName, ok := dynamoConfig["table_name"].(string)
		if !ok {
			return nil, fmt.Errorf("dynamodb table_name not specified")
		}
		region, ok := dynamoConfig["region"].(string)
		if !ok {
			return nil, fmt.Errorf("dynamodb region not specified")
		}

		logger.Debug("💰 DynamoDB Transport: Creating from map config",
			"table_name", tableName,
			"region", region)

		config := DynamoDBTransportConfig{
			TableName: tableName,
			Region:    region,
			Logger:    logger,
		}
		return NewDynamoDBTransport(config)

	default:
		return nil, fmt.Errorf("unsupported config type for dynamodb transport: %T", transportConfig)
	}
}

// NewDynamoDBTransportFromConfig creates a DynamoDBTransport from configuration (convenience function)
func NewDynamoDBTransportFromConfig(transportConfig interface{}, logger *slog.Logger) (Transport, error) {
	dt := &DynamoDBTransport{}
	return dt.FromConfig(transportConfig, logger)
}

// ensureTableExists creates the DynamoDB table if it doesn't exist
func (dt *DynamoDBTransport) ensureTableExists(ctx context.Context) error {
	// Check if table exists
	_, err := dt.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(dt.tableName),
	})
	if err == nil {
		dt.logger.Debug("DynamoDB table already exists", "table", dt.tableName)
		return nil
	}

	// Create table if it doesn't exist
	dt.logger.Info("Creating DynamoDB table for cost tracking", "table", dt.tableName)

	createInput := &dynamodb.CreateTableInput{
		TableName: aws.String(dt.tableName),
		KeySchema: []types.KeySchemaElement{
			{
				AttributeName: aws.String("pk"),
				KeyType:       types.KeyTypeHash,
			},
			{
				AttributeName: aws.String("sk"),
				KeyType:       types.KeyTypeRange,
			},
		},
		AttributeDefinitions: []types.AttributeDefinition{
			{
				AttributeName: aws.String("pk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String("sk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String("gsi1pk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String("gsi1sk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String("gsi2pk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String("gsi2sk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String("gsi3pk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
			{
				AttributeName: aws.String("gsi3sk"),
				AttributeType: types.ScalarAttributeTypeS,
			},
		},
		GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{
			{
				IndexName: aws.String("ProviderModelIndex"),
				KeySchema: []types.KeySchemaElement{
					{
						AttributeName: aws.String("gsi1pk"),
						KeyType:       types.KeyTypeHash,
					},
					{
						AttributeName: aws.String("gsi1sk"),
						KeyType:       types.KeyTypeRange,
					},
				},
				Projection: &types.Projection{
					ProjectionType: types.ProjectionTypeAll,
				},
			},
			{
				IndexName: aws.String("UserProviderIndex"),
				KeySchema: []types.KeySchemaElement{
					{
						AttributeName: aws.String("gsi2pk"),
						KeyType:       types.KeyTypeHash,
					},
					{
						AttributeName: aws.String("gsi2sk"),
						KeyType:       types.KeyTypeRange,
					},
				},
				Projection: &types.Projection{
					ProjectionType: types.ProjectionTypeAll,
				},
			},
			{
				IndexName: aws.String("ModelProviderIndex"),
				KeySchema: []types.KeySchemaElement{
					{
						AttributeName: aws.String("gsi3pk"),
						KeyType:       types.KeyTypeHash,
					},
					{
						AttributeName: aws.String("gsi3sk"),
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

	_, err = dt.client.CreateTable(ctx, createInput)
	if err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	// Wait for table to become active
	waiter := dynamodb.NewTableExistsWaiter(dt.client)
	err = waiter.Wait(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(dt.tableName),
	}, 5*time.Minute)
	if err != nil {
		return fmt.Errorf("failed waiting for table to become active: %w", err)
	}

	dt.logger.Info("DynamoDB table created successfully", "table", dt.tableName)
	return nil
}

// WriteRecord writes a cost record to DynamoDB
func (dt *DynamoDBTransport) WriteRecord(record *CostRecord) error {
	ctx := context.TODO()

	// Convert CostRecord to DynamoDBCostRecord
	dynamoRecord := dt.toDynamoDBRecord(record)

	// Marshal to DynamoDB item
	item, err := attributevalue.MarshalMap(dynamoRecord)
	if err != nil {
		return fmt.Errorf("failed to marshal record: %w", err)
	}

	// Put item to DynamoDB
	_, err = dt.client.PutItem(ctx, &dynamodb.PutItemInput{
		TableName: aws.String(dt.tableName),
		Item:      item,
	})
	if err != nil {
		return fmt.Errorf("failed to write record to DynamoDB: %w", err)
	}

	dt.logger.Debug("Cost record written to DynamoDB",
		"table", dt.tableName,
		"provider", record.Provider,
		"model", record.Model,
		"cost", record.TotalCost)

	return nil
}

// toDynamoDBRecord converts a CostRecord to DynamoDBCostRecord
func (dt *DynamoDBTransport) toDynamoDBRecord(record *CostRecord) *DynamoDBCostRecord {
	dateStr := record.Timestamp.Format("2006-01-02")
	timestampStr := record.Timestamp.Format("2006-01-02T15:04:05.000Z")

	return &DynamoDBCostRecord{
		PK:           fmt.Sprintf("COST#%s", dateStr),
		SK:           fmt.Sprintf("TIMESTAMP#%s#%s", timestampStr, record.RequestID),
		GSI1PK:       fmt.Sprintf("PROVIDER#%s", record.Provider),
		GSI1SK:       fmt.Sprintf("MODEL#%s#%s", record.Model, timestampStr),
		GSI2PK:       fmt.Sprintf("USER#%s", record.UserID),
		GSI2SK:       fmt.Sprintf("PROVIDER#%s#%s", record.Provider, timestampStr),
		GSI3PK:       fmt.Sprintf("MODEL#%s", record.Model),
		GSI3SK:       fmt.Sprintf("PROVIDER#%s#%s", record.Provider, timestampStr),
		TTL:          record.Timestamp.AddDate(1, 0, 0).Unix(), // 1 year TTL
		Timestamp:    record.Timestamp.Unix(),
		RequestID:    record.RequestID,
		UserID:       record.UserID,
		IPAddress:    record.IPAddress,
		Provider:     record.Provider,
		Model:        record.Model,
		Endpoint:     record.Endpoint,
		IsStreaming:  record.IsStreaming,
		InputTokens:  record.InputTokens,
		OutputTokens: record.OutputTokens,
		TotalTokens:  record.TotalTokens,
		InputCost:    record.InputCost,
		OutputCost:   record.OutputCost,
		TotalCost:    record.TotalCost,
		FinishReason: record.FinishReason,
	}
}
