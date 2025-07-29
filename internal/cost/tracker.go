package cost

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/providers"
)

// Transport defines the interface for cost tracking transports
type Transport interface {
	// WriteRecord writes a cost record to the transport
	WriteRecord(record *CostRecord) error
	// ReadRecords reads cost records from the transport since the given time
	ReadRecords(since time.Time) ([]CostRecord, error)
}

// PricingTier represents a pricing tier with a token threshold.
type PricingTier struct {
	Threshold int     `json:"threshold"` // The token threshold for this tier
	Input     float64 `json:"input"`     // Cost per 1M input tokens in USD
	Output    float64 `json:"output"`    // Cost per 1M output tokens in USD
}

// ModelPricing represents pricing information for a model (matching config structure)
type ModelPricing struct {
	Tiers     []PricingTier `json:"tiers,omitempty"`
	Overrides map[string]struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
	} `json:"overrides,omitempty"`
}

// CostRecord represents a single request with cost information
type CostRecord struct {
	// Timestamp and identification
	Timestamp time.Time `json:"timestamp"`
	RequestID string    `json:"request_id,omitempty"`
	UserID    string    `json:"user_id,omitempty"`
	IPAddress string    `json:"ip_address,omitempty"`

	// Request details
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Endpoint    string `json:"endpoint"`
	IsStreaming bool   `json:"is_streaming"`

	// Token usage
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`

	// Cost calculation
	InputCost  float64 `json:"input_cost"`  // Cost for input tokens in USD
	OutputCost float64 `json:"output_cost"` // Cost for output tokens in USD
	TotalCost  float64 `json:"total_cost"`  // Total cost in USD

	// Additional metadata
	FinishReason string `json:"finish_reason,omitempty"`
}

// CostTracker manages cost tracking and output through transports
type CostTracker struct {
	pricingConfig map[string]map[string]*ModelPricing // provider -> model -> pricing
	transport     Transport
	logger        *slog.Logger
}

// NewCostTracker creates a new cost tracker with the specified transport
func NewCostTracker(transport Transport) *CostTracker {
	return &CostTracker{
		pricingConfig: make(map[string]map[string]*ModelPricing),
		transport:     transport,
		logger:        slog.Default(), // Use default logger initially
	}
}

// NewFileBasedCostTracker creates a new cost tracker with file-based transport (convenience function)
func NewFileBasedCostTracker(outputFile string) *CostTracker {
	return NewCostTracker(NewFileTransport(outputFile))
}

// NewDynamoDBBasedCostTracker creates a new cost tracker with DynamoDB transport (convenience function)
func NewDynamoDBBasedCostTracker(tableName, region string) (*CostTracker, error) {
	config := DynamoDBTransportConfig{
		TableName: tableName,
		Region:    region,
	}
	transport, err := NewDynamoDBTransport(config)
	if err != nil {
		return nil, err
	}
	return NewCostTracker(transport), nil
}

// SetLogger sets the logger for the cost tracker
func (ct *CostTracker) SetLogger(logger *slog.Logger) {
	ct.logger = logger
}

// SetPricingForModel sets pricing information for a specific provider and model
func (ct *CostTracker) SetPricingForModel(provider, model string, pricing *ModelPricing) {
	if ct.pricingConfig[provider] == nil {
		ct.pricingConfig[provider] = make(map[string]*ModelPricing)
	}
	ct.pricingConfig[provider][model] = pricing
	ct.logger.Debug("💰 Cost Tracker: Set pricing for model", "provider", provider, "model", model)
}

// GetPricingForModel retrieves pricing information for a specific provider and model
func (ct *CostTracker) GetPricingForModel(provider, model string, inputTokens int) (*PricingTier, error) {
	if providerPricing, exists := ct.pricingConfig[provider]; exists {
		if modelPricing, exists := providerPricing[model]; exists {
			// Handle overrides first
			if override, ok := modelPricing.Overrides[model]; ok {
				return &PricingTier{Input: override.Input, Output: override.Output}, nil
			}
			// Handle tiered pricing
			if len(modelPricing.Tiers) > 0 {
				for _, tier := range modelPricing.Tiers {
					if tier.Threshold == 0 || inputTokens <= tier.Threshold {
						return &tier, nil
					}
				}
			}
		}
	}
	return nil, fmt.Errorf("no pricing configured for provider %s model %s", provider, model)
}

// CalculateCost calculates the cost for a request based on token usage
func (ct *CostTracker) CalculateCost(provider, model string, inputTokens, outputTokens int) (float64, float64, float64, error) {
	pricing, err := ct.GetPricingForModel(provider, model, inputTokens)
	if err != nil {
		return 0, 0, 0, err
	}

	// Calculate costs (pricing is per 1M tokens)
	inputCost := (float64(inputTokens) / 1_000_000.0) * pricing.Input
	outputCost := (float64(outputTokens) / 1_000_000.0) * pricing.Output
	totalCost := inputCost + outputCost
	return inputCost, outputCost, totalCost, nil
}

// TrackRequest processes a request and writes cost information to file
func (ct *CostTracker) TrackRequest(metadata *providers.LLMResponseMetadata, userID, ipAddress, endpoint string) error {
	// Calculate costs
	inputCost, outputCost, totalCost, err := ct.CalculateCost(
		metadata.Provider,
		metadata.Model,
		metadata.InputTokens,
		metadata.OutputTokens,
	)
	if err != nil {
		ct.logger.Debug("Could not calculate cost for request", "provider", metadata.Provider, "model", metadata.Model, "error", err)
		// Continue with zero costs rather than failing
		inputCost, outputCost, totalCost = 0, 0, 0
	}

	// Create cost record
	record := &CostRecord{
		Timestamp:    time.Now(),
		RequestID:    metadata.RequestID,
		UserID:       userID,
		IPAddress:    ipAddress,
		Provider:     metadata.Provider,
		Model:        metadata.Model,
		Endpoint:     endpoint,
		IsStreaming:  metadata.IsStreaming,
		InputTokens:  metadata.InputTokens,
		OutputTokens: metadata.OutputTokens,
		TotalTokens:  metadata.TotalTokens,
		InputCost:    inputCost,
		OutputCost:   outputCost,
		TotalCost:    totalCost,
		FinishReason: metadata.FinishReason,
	}

	// Log the cost information
	if totalCost > 0 {
		ct.logger.Debug("💵 Cost Tracking: Request processed",
			"provider", metadata.Provider,
			"model", metadata.Model,
			"total_tokens", metadata.TotalTokens,
			"input_tokens", metadata.InputTokens,
			"output_tokens", metadata.OutputTokens,
			"total_cost", totalCost,
			"input_cost", inputCost,
			"output_cost", outputCost)
	} else {
		ct.logger.Debug("💵 Cost Tracking: Request processed (no pricing configured)",
			"provider", metadata.Provider,
			"model", metadata.Model,
			"total_tokens", metadata.TotalTokens,
			"input_tokens", metadata.InputTokens,
			"output_tokens", metadata.OutputTokens)
	}

	// Write to file
	return ct.transport.WriteRecord(record)
}

// GetTotalCosts reads the cost file and calculates total costs
func (ct *CostTracker) GetTotalCosts(since time.Time) (map[string]float64, error) {
	records, err := ct.transport.ReadRecords(since)
	if err != nil {
		return nil, err
	}

	totals := make(map[string]float64)

	for _, record := range records {
		// Aggregate costs by provider/model
		key := fmt.Sprintf("%s/%s", record.Provider, record.Model)
		totals[key] += record.TotalCost
		totals["total"] += record.TotalCost
		totals[record.Provider] += record.TotalCost
	}

	return totals, nil
}

// GetStats returns cost statistics from the file
func (ct *CostTracker) GetStats(since time.Time) (map[string]interface{}, error) {
	totals, err := ct.GetTotalCosts(since)
	if err != nil {
		return nil, err
	}

	stats := make(map[string]interface{})
	stats["total_cost"] = totals["total"]
	stats["provider_costs"] = make(map[string]float64)
	stats["model_costs"] = make(map[string]float64)

	for key, cost := range totals {
		if key == "total" {
			continue
		}

		// Separate provider costs from model costs
		if !strings.Contains(key, "/") {
			// It's a provider total
			stats["provider_costs"].(map[string]float64)[key] = cost
		} else {
			// It's a model total
			stats["model_costs"].(map[string]float64)[key] = cost
		}
	}

	return stats, nil
}

// CreateTransportFromConfig creates a transport based on the provided configuration
func CreateTransportFromConfig(transportConfig interface{}, logger *slog.Logger) (Transport, error) {
	// Use type assertion to work with different config types
	switch cfg := transportConfig.(type) {
	case *config.TransportConfig:
		// Handle structured config (from yamlConfig.GetTransportConfig())
		logger.Debug("💰 Cost Tracker: Processing structured transport config", "type", cfg.Type)
		switch cfg.Type {
		case "file":
			if cfg.File == nil {
				return nil, fmt.Errorf("file transport configuration not found")
			}
			logger.Debug("💰 Cost Tracker: Creating file transport", "path", cfg.File.Path)
			return NewFileTransport(cfg.File.Path), nil

		case "dynamodb":
			if cfg.DynamoDB == nil {
				return nil, fmt.Errorf("dynamodb transport configuration not found")
			}

			logger.Debug("💰 Cost Tracker: Creating DynamoDB transport",
				"table_name", cfg.DynamoDB.TableName,
				"region", cfg.DynamoDB.Region)

			config := DynamoDBTransportConfig{
				TableName: cfg.DynamoDB.TableName,
				Region:    cfg.DynamoDB.Region,
				Logger:    logger,
			}
			return NewDynamoDBTransport(config)

		default:
			return nil, fmt.Errorf("unsupported transport type: %s", cfg.Type)
		}

	case map[string]interface{}:
		// Handle generic map interface (from YAML parsing)
		transportType, ok := cfg["type"].(string)
		if !ok {
			return nil, fmt.Errorf("transport type not specified")
		}

		logger.Debug("💰 Cost Tracker: Processing map-based transport config", "type", transportType)

		switch transportType {
		case "file":
			fileConfig, ok := cfg["file"].(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("file transport configuration not found")
			}
			path, ok := fileConfig["path"].(string)
			if !ok {
				return nil, fmt.Errorf("file path not specified")
			}
			logger.Debug("💰 Cost Tracker: Creating file transport from map", "path", path)
			return NewFileTransport(path), nil

		case "dynamodb":
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

			logger.Debug("💰 Cost Tracker: Creating DynamoDB transport from map",
				"table_name", tableName,
				"region", region)

			config := DynamoDBTransportConfig{
				TableName: tableName,
				Region:    region,
				Logger:    logger,
			}
			return NewDynamoDBTransport(config)

		default:
			return nil, fmt.Errorf("unsupported transport type: %s", transportType)
		}
	default:
		return nil, fmt.Errorf("unsupported transport config type: %T", transportConfig)
	}
}
