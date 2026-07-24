package cost

import (
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/providers"
)

// TestToDynamoDBRecord_UTCKeysAndKeyID guards two regressions: (1) partition
// and sort keys must be derived from UTC — the "Z" in the layout is a literal,
// so formatting a local-time value would stamp local wall-clock time as UTC
// and bucket records near midnight into the wrong day partition; (2) KeyID
// must be persisted, not silently dropped.
func TestToDynamoDBRecord_UTCKeysAndKeyID(t *testing.T) {
	dt := &DynamoDBTransport{}

	// 20:30 in UTC-7 is 03:30 the NEXT day in UTC.
	loc := time.FixedZone("UTC-7", -7*3600)
	record := &CostRecord{
		Timestamp: time.Date(2026, 7, 24, 20, 30, 0, 0, loc),
		RequestID: "req-1",
		UserID:    "user-1",
		KeyID:     "iw:abcd****wxyz",
		Provider:  "openai",
		Model:     "gpt-4o",
	}

	out := dt.toDynamoDBRecord(record)

	if out.PK != "COST#2026-07-25" {
		t.Errorf("PK must bucket by UTC day, got %q", out.PK)
	}
	if !strings.HasPrefix(out.SK, "TIMESTAMP#2026-07-25T03:30:00.000Z#") {
		t.Errorf("SK must carry the UTC timestamp, got %q", out.SK)
	}
	if out.KeyID != record.KeyID {
		t.Errorf("KeyID must be persisted, got %q want %q", out.KeyID, record.KeyID)
	}
	if out.Timestamp != record.Timestamp.Unix() {
		t.Errorf("numeric timestamp changed: got %d want %d", out.Timestamp, record.Timestamp.Unix())
	}
}

// TestDynamoDBTransportIntegration demonstrates how to use the DynamoDB transport
// This is an integration test that requires AWS credentials and DynamoDB access.
//
// Skipped by default unless LLM_PROXY_RUN_INTEGRATION=1 is exported.
// Without the explicit opt-in, `go test ./...` from a developer laptop
// could otherwise dial real AWS DynamoDB (with whatever profile the
// shell happens to have exported) — at best wasting credentials, at
// worst quietly auto-creating tables in the wrong account if
// AutoCreateTable were ever flipped on.
func TestDynamoDBTransportIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test in short mode")
	}
	if os.Getenv("LLM_PROXY_RUN_INTEGRATION") != "1" {
		t.Skip("Skipping DynamoDB integration test; set LLM_PROXY_RUN_INTEGRATION=1 to enable")
	}

	// Configuration for DynamoDB transport. AutoCreateTable is explicitly
	// false: integration runs must pre-provision the table out-of-band so
	// a misconfigured test cannot create a production resource.
	config := DynamoDBTransportConfig{
		TableName:       "llm-proxy-cost-tracking-test",
		Region:          "us-west-2",
		AutoCreateTable: false,
		Logger:          slog.Default(),
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
	err = tracker.TrackRequest(metadata, "test-user", "192.0.2.1", "/v1/chat/completions", "")
	if err != nil {
		t.Errorf("Failed to track request: %v", err)
	}

	// Test passed - successfully wrote cost record to DynamoDB
	t.Log("Successfully tracked cost record to DynamoDB (write-only)")
}
