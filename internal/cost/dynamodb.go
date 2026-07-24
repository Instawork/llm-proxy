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
	// AutoCreateTable controls whether NewDynamoDBTransport will issue
	// CreateTable when the configured table is missing. Defaults to false
	// so staging/production deployments cannot accidentally provision
	// resources in the active AWS account.
	AutoCreateTable bool
	// WriteTimeout bounds the per-record PutItem call. Defaults to 5s.
	WriteTimeout time.Duration
}

// defaultDynamoDBWriteTimeout caps each PutItem to keep cost tracking
// from monopolizing a request goroutine when sync writes are configured.
const defaultDynamoDBWriteTimeout = 5 * time.Second

// DynamoDBTransport implements Transport interface for DynamoDB-based cost tracking
type DynamoDBTransport struct {
	client       *dynamodb.Client
	tableName    string
	logger       *slog.Logger
	writeTimeout time.Duration
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
	KeyID        string  `dynamodbav:"key_id,omitempty"`
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

// NewDynamoDBTransport creates a new DynamoDB-based transport.
//
// Startup uses a bounded context (30s) for AWS config + table verification
// so a misconfigured account cannot hang the process indefinitely. If
// cfg.AutoCreateTable is false (the default) the constructor only verifies
// the table exists; otherwise it falls back to CreateTable.
func NewDynamoDBTransport(cfg DynamoDBTransportConfig) (*DynamoDBTransport, error) {
	startupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	awsConfig, err := config.LoadDefaultConfig(
		startupCtx,
		config.WithRegion(cfg.Region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := dynamodb.NewFromConfig(awsConfig)

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	writeTimeout := cfg.WriteTimeout
	if writeTimeout <= 0 {
		writeTimeout = defaultDynamoDBWriteTimeout
	}

	transport := &DynamoDBTransport{
		client:       client,
		tableName:    cfg.TableName,
		logger:       logger,
		writeTimeout: writeTimeout,
	}

	if cfg.AutoCreateTable {
		if err := transport.ensureTableExists(startupCtx); err != nil {
			return nil, fmt.Errorf("failed to ensure table exists: %w", err)
		}
	} else {
		if err := transport.verifyTableExists(startupCtx); err != nil {
			return nil, fmt.Errorf("dynamodb table %q is not accessible (pass AutoCreateTable: true in dev only): %w", cfg.TableName, err)
		}
	}

	return transport, nil
}

// verifyTableExists checks that the configured table is reachable without
// attempting to create it. Used for the default (production-safe) path.
func (dt *DynamoDBTransport) verifyTableExists(ctx context.Context) error {
	_, err := dt.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(dt.tableName),
	})
	return err
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
			TableName:       cfg.DynamoDB.TableName,
			Region:          cfg.DynamoDB.Region,
			Logger:          logger,
			AutoCreateTable: cfg.DynamoDB.AutoCreateTable,
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

		autoCreate, _ := dynamoConfig["auto_create_table"].(bool)
		logger.Debug("💰 DynamoDB Transport: Creating from map config",
			"table_name", tableName,
			"region", region)

		config := DynamoDBTransportConfig{
			TableName:       tableName,
			Region:          region,
			Logger:          logger,
			AutoCreateTable: autoCreate,
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

// buildCostTableSchema returns the table creation input used by
// ensureTableExists. Extracted so the (large) schema literal does not bury the
// control flow in ensureTableExists.
func (dt *DynamoDBTransport) buildCostTableSchema() *dynamodb.CreateTableInput {
	stringAttr := func(name string) types.AttributeDefinition {
		return types.AttributeDefinition{
			AttributeName: aws.String(name),
			AttributeType: types.ScalarAttributeTypeS,
		}
	}
	gsi := func(name, pk, sk string) types.GlobalSecondaryIndex {
		return types.GlobalSecondaryIndex{
			IndexName: aws.String(name),
			KeySchema: []types.KeySchemaElement{
				{AttributeName: aws.String(pk), KeyType: types.KeyTypeHash},
				{AttributeName: aws.String(sk), KeyType: types.KeyTypeRange},
			},
			Projection: &types.Projection{ProjectionType: types.ProjectionTypeAll},
		}
	}
	return &dynamodb.CreateTableInput{
		TableName: aws.String(dt.tableName),
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("pk"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("sk"), KeyType: types.KeyTypeRange},
		},
		AttributeDefinitions: []types.AttributeDefinition{
			stringAttr("pk"), stringAttr("sk"),
			stringAttr("gsi1pk"), stringAttr("gsi1sk"),
			stringAttr("gsi2pk"), stringAttr("gsi2sk"),
			stringAttr("gsi3pk"), stringAttr("gsi3sk"),
		},
		GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{
			gsi("ProviderModelIndex", "gsi1pk", "gsi1sk"),
			gsi("UserProviderIndex", "gsi2pk", "gsi2sk"),
			gsi("ModelProviderIndex", "gsi3pk", "gsi3sk"),
		},
		BillingMode: types.BillingModePayPerRequest,
	}
}

// ensureTableExists creates the DynamoDB table if it doesn't exist
func (dt *DynamoDBTransport) ensureTableExists(ctx context.Context) error {
	_, err := dt.client.DescribeTable(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(dt.tableName),
	})
	if err == nil {
		dt.logger.Debug("DynamoDB table already exists", "table", dt.tableName)
		return nil
	}

	dt.logger.Info("Creating DynamoDB table for cost tracking", "table", dt.tableName)
	if _, err := dt.client.CreateTable(ctx, dt.buildCostTableSchema()); err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	waiter := dynamodb.NewTableExistsWaiter(dt.client)
	if err := waiter.Wait(ctx, &dynamodb.DescribeTableInput{
		TableName: aws.String(dt.tableName),
	}, 5*time.Minute); err != nil {
		return fmt.Errorf("failed waiting for table to become active: %w", err)
	}

	dt.logger.Info("DynamoDB table created successfully", "table", dt.tableName)
	return nil
}

// WriteRecord writes a cost record to DynamoDB.
//
// Each PutItem is wrapped in a context with writeTimeout so that a stalled
// DynamoDB endpoint cannot block the calling goroutine indefinitely; in
// sync-tracker mode this is the request goroutine.
func (dt *DynamoDBTransport) WriteRecord(record *CostRecord) error {
	ctx, cancel := context.WithTimeout(context.Background(), dt.writeTimeout)
	defer cancel()

	dynamoRecord := dt.toDynamoDBRecord(record)

	item, err := attributevalue.MarshalMap(dynamoRecord)
	if err != nil {
		return fmt.Errorf("failed to marshal record: %w", err)
	}

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
	// Convert to UTC before formatting: the "Z" in the layout is a literal,
	// so formatting a local-time value would stamp local wall-clock time as
	// UTC and bucket the partition key by local day instead of UTC day.
	ts := record.Timestamp.UTC()
	dateStr := ts.Format("2006-01-02")
	timestampStr := ts.Format("2006-01-02T15:04:05.000Z")

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
		KeyID:        record.KeyID,
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
