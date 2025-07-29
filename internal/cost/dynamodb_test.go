package cost

import (
	"log/slog"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/providers"
)

// TestDynamoDBTransportIntegration demonstrates how to use the DynamoDB transport
// This is an integration test that requires AWS credentials and DynamoDB access
func TestDynamoDBTransportIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}

	// Configuration for DynamoDB transport
	config := DynamoDBTransportConfig{
		TableName: "llm-proxy-cost-tracking-test",
		Region:    "us-west-2", // Change this to your preferred region
		Logger:    slog.Default(),
	}

	// Create DynamoDB transport
	transport, err := NewDynamoDBTransport(config)
	if err != nil {
		t.Skipf("Failed to create DynamoDB transport (skipping test): %v", err)
	}

	// Create cost tracker with DynamoDB transport
	tracker := NewCostTracker(transport)

	// Set up some test pricing
	testPricing := &ModelPricing{
		Tiers: []PricingTier{
			{
				Threshold: 0,
				Input:     0.5, // $0.50 per 1M input tokens
				Output:    1.5, // $1.50 per 1M output tokens
			},
		},
	}
	tracker.SetPricingForModel("openai", "gpt-3.5-turbo", testPricing)

	// Create test metadata
	metadata := &providers.LLMResponseMetadata{
		RequestID:    "test-request-123",
		Provider:     "openai",
		Model:        "gpt-3.5-turbo",
		InputTokens:  1000,
		OutputTokens: 500,
		TotalTokens:  1500,
		IsStreaming:  false,
		FinishReason: "stop",
	}

	// Track a test request
	err = tracker.TrackRequest(metadata, "test-user", "192.168.1.1", "/v1/chat/completions")
	if err != nil {
		t.Errorf("Failed to track request: %v", err)
	}

	// Wait a moment for eventual consistency
	time.Sleep(1 * time.Second)

	// Read records back
	since := time.Now().Add(-1 * time.Hour)
	records, err := transport.ReadRecords(since)
	if err != nil {
		t.Errorf("Failed to read records: %v", err)
	}

	// Verify we got at least one record
	if len(records) == 0 {
		t.Error("Expected at least one cost record, got none")
	}

	// Verify the record content
	found := false
	for _, record := range records {
		if record.RequestID == "test-request-123" {
			found = true
			if record.Provider != "openai" {
				t.Errorf("Expected provider 'openai', got '%s'", record.Provider)
			}
			if record.Model != "gpt-3.5-turbo" {
				t.Errorf("Expected model 'gpt-3.5-turbo', got '%s'", record.Model)
			}
			if record.InputTokens != 1000 {
				t.Errorf("Expected 1000 input tokens, got %d", record.InputTokens)
			}
			if record.OutputTokens != 500 {
				t.Errorf("Expected 500 output tokens, got %d", record.OutputTokens)
			}
			if record.TotalCost <= 0 {
				t.Errorf("Expected positive total cost, got %f", record.TotalCost)
			}
			break
		}
	}

	if !found {
		t.Error("Could not find the test record in the retrieved records")
	}

	// Test cost calculation
	totals, err := tracker.GetTotalCosts(since)
	if err != nil {
		t.Errorf("Failed to get total costs: %v", err)
	}

	if totals["total"] <= 0 {
		t.Errorf("Expected positive total cost, got %f", totals["total"])
	}

	t.Logf("Successfully tracked and retrieved cost records. Total cost: $%.6f", totals["total"])
}

// Example function showing how to create a DynamoDB-based cost tracker
func ExampleNewDynamoDBTransport() {
	// Configure DynamoDB transport
	config := DynamoDBTransportConfig{
		TableName: "llm-proxy-cost-tracking",
		Region:    "us-west-2",
		Logger:    slog.Default(),
	}

	// Create DynamoDB transport
	transport, err := NewDynamoDBTransport(config)
	if err != nil {
		panic(err)
	}

	// Create cost tracker with DynamoDB transport
	tracker := NewCostTracker(transport)

	// Set up pricing for your models
	openaiPricing := &ModelPricing{
		Tiers: []PricingTier{
			{
				Threshold: 0,
				Input:     0.5, // $0.50 per 1M input tokens
				Output:    1.5, // $1.50 per 1M output tokens
			},
		},
	}
	tracker.SetPricingForModel("openai", "gpt-3.5-turbo", openaiPricing)

	// Now you can use the tracker in your LLM proxy handlers
	// tracker.TrackRequest(metadata, userID, ipAddress, endpoint)

	// Output: DynamoDB transport configured successfully
	println("DynamoDB transport configured successfully")
}
