package cost

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configPkg "github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/testhelpers/dynamodbfake"
)

func TestNewDynamoDBTransport_FakeServer(t *testing.T) {
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())

	tp, err := NewDynamoDBTransport(DynamoDBTransportConfig{
		TableName: "tbl", Region: "us-west-2", Logger: slog.Default(),
	})
	require.NoError(t, err)
	require.NotNil(t, tp)
	assert.Equal(t, "tbl", tp.tableName)
}

func TestDynamoDBTransport_WriteRecord_FakeServer(t *testing.T) {
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())

	tp, err := NewDynamoDBTransport(DynamoDBTransportConfig{
		TableName: "tbl", Region: "us-west-2",
	})
	require.NoError(t, err)

	rec := &CostRecord{
		RequestID:    "r-1",
		UserID:       "u",
		IPAddress:    "10.0.0.1",
		Provider:     "openai",
		Model:        "gpt-4o",
		Endpoint:     "/openai/v1/chat/completions",
		IsStreaming:  false,
		InputTokens:  10,
		OutputTokens: 5,
		TotalTokens:  15,
		InputCost:    0.001,
		OutputCost:   0.0005,
		TotalCost:    0.0015,
	}
	require.NoError(t, tp.WriteRecord(rec))
}

func TestNewDynamoDBTransportFromConfig_StructConfig_FakeServer(t *testing.T) {
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())

	cfg := &configPkg.TransportConfig{
		Type: "dynamodb",
		DynamoDB: &configPkg.DynamoDBTransportConfig{
			TableName: "tbl",
			Region:    "us-west-2",
		},
	}
	tp, err := NewDynamoDBTransportFromConfig(cfg, slog.Default())
	require.NoError(t, err)
	require.NotNil(t, tp)
}

func TestNewDynamoDBTransportFromConfig_MapConfig_FakeServer(t *testing.T) {
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())

	cfg := map[string]interface{}{
		"dynamodb": map[string]interface{}{
			"table_name": "tbl",
			"region":     "us-west-2",
		},
	}
	tp, err := NewDynamoDBTransportFromConfig(cfg, slog.Default())
	require.NoError(t, err)
	require.NotNil(t, tp)
}

func TestNewDynamoDBBasedCostTracker_FakeServer(t *testing.T) {
	fake := dynamodbfake.New(t)
	dynamodbfake.UseFakeDynamo(t, fake.URL())

	ct, err := NewDynamoDBBasedCostTracker("tbl", "us-west-2")
	require.NoError(t, err)
	require.NotNil(t, ct)

	// TrackRequest writes via the fake server PutItem path.
	ct.SetPricingForModel("openai", "gpt-4o", &ModelPricing{
		Tiers: []PricingTier{{Threshold: 0, Input: 0.01, Output: 0.02}},
	})
	require.NoError(t, ct.TrackRequest(&providers.LLMResponseMetadata{
		Provider: "openai", Model: "gpt-4o", InputTokens: 1, OutputTokens: 1,
	}, "u", "", "/openai/v1/chat/completions"))
}
