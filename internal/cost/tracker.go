package cost

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/providers"
)

// Transport defines the interface for cost tracking transports
type Transport interface {
	// WriteRecord writes a cost record to the transport
	WriteRecord(record *CostRecord) error
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
	transports    []Transport                         // Multiple transports for parallel writes
	logger        *slog.Logger

	// Async tracking support
	async         bool               // Whether to use async tracking
	queue         chan *CostRecord   // Queue for async tracking
	workers       int                // Number of worker goroutines
	flushInterval time.Duration      // Interval for periodic flushing
	ctx           context.Context    // Context for cancelling workers
	cancel        context.CancelFunc // Cancel function for workers
	wg            sync.WaitGroup     // WaitGroup for tracking workers
	started       bool               // Whether async workers have been started
	mu            sync.RWMutex       // Mutex for protecting async state
}

// NewCostTracker creates a new cost tracker with the specified transports
func NewCostTracker(transports ...Transport) *CostTracker {
	ctx, cancel := context.WithCancel(context.Background())
	return &CostTracker{
		pricingConfig: make(map[string]map[string]*ModelPricing),
		transports:    transports,
		logger:        slog.Default(),   // Use default logger initially
		async:         false,            // Default to sync mode
		workers:       5,                // Default number of workers
		flushInterval: 15 * time.Second, // Default flush interval
		ctx:           ctx,
		cancel:        cancel,
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

// NewDatadogBasedCostTracker creates a new cost tracker with Datadog transport (convenience function)
func NewDatadogBasedCostTracker(host, port string) (*CostTracker, error) {
	config := DatadogTransportConfig{
		Host: host,
		Port: port,
	}
	transport, err := NewDatadogTransport(config)
	if err != nil {
		return nil, err
	}
	return NewCostTracker(transport), nil
}

// AddTransport adds a transport to the cost tracker
func (ct *CostTracker) AddTransport(transport Transport) {
	ct.transports = append(ct.transports, transport)
}

// SetLogger sets the logger for the cost tracker
func (ct *CostTracker) SetLogger(logger *slog.Logger) {
	ct.logger = logger
}

// ConfigureAsync configures the cost tracker for async tracking with the specified parameters
func (ct *CostTracker) ConfigureAsync(workers, queueSize, flushIntervalSeconds int) {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	if ct.started {
		ct.logger.Warn("Cannot reconfigure async settings while workers are running")
		return
	}

	ct.async = true
	ct.workers = workers
	if workers <= 0 {
		ct.workers = 5 // Default
	}
	if queueSize <= 0 {
		queueSize = 1000 // Default
	}
	if flushIntervalSeconds <= 0 {
		flushIntervalSeconds = 15 // Default
	}
	ct.queue = make(chan *CostRecord, queueSize)
	ct.flushInterval = time.Duration(flushIntervalSeconds) * time.Second

	ct.logger.Info("💰 Cost Tracker: Configured for async tracking",
		"workers", ct.workers,
		"queue_size", queueSize,
		"flush_interval_seconds", flushIntervalSeconds)
}

// SetSyncMode sets the cost tracker to synchronous mode
func (ct *CostTracker) SetSyncMode() {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	if ct.started {
		ct.logger.Warn("Cannot switch to sync mode while async workers are running")
		return
	}

	ct.async = false
	ct.logger.Info("💰 Cost Tracker: Set to synchronous mode")
}

// StartAsyncWorkers starts the async worker goroutines (must be called before using async tracking)
func (ct *CostTracker) StartAsyncWorkers() error {
	ct.mu.Lock()
	defer ct.mu.Unlock()

	if !ct.async {
		return fmt.Errorf("cost tracker is not configured for async mode")
	}

	if ct.started {
		return fmt.Errorf("async workers are already started")
	}

	if ct.queue == nil {
		return fmt.Errorf("async queue is not initialized")
	}

	ct.logger.Info("💰 Cost Tracker: Starting async workers", "worker_count", ct.workers)

	for i := 0; i < ct.workers; i++ {
		ct.wg.Add(1)
		go ct.asyncWorker(i)
	}

	ct.started = true
	ct.logger.Info("💰 Cost Tracker: All async workers started successfully")
	return nil
}

// StopAsyncWorkers stops all async workers and waits for them to finish processing
func (ct *CostTracker) StopAsyncWorkers() {
	ct.mu.Lock()
	if !ct.started || !ct.async {
		ct.mu.Unlock()
		return
	}

	ct.logger.Info("💰 Cost Tracker: Stopping async workers...")
	ct.cancel()     // Signal workers to stop
	close(ct.queue) // Close the queue to signal no more records
	ct.mu.Unlock()

	ct.wg.Wait() // Wait for all workers to finish

	ct.mu.Lock()
	ct.started = false
	ct.mu.Unlock()

	ct.logger.Info("💰 Cost Tracker: All async workers stopped")
}

// asyncWorker is the worker goroutine that processes cost records from the queue
func (ct *CostTracker) asyncWorker(workerID int) {
	defer ct.wg.Done()

	ct.logger.Debug("💰 Cost Tracker: Async worker started", "worker_id", workerID, "flush_interval", ct.flushInterval)

	// Create a ticker for periodic flushing
	flushTicker := time.NewTicker(ct.flushInterval)
	defer flushTicker.Stop()

	for {
		select {
		case record, ok := <-ct.queue:
			if !ok {
				// Queue is closed, process any remaining records and exit
				ct.logger.Debug("💰 Cost Tracker: Async worker exiting (queue closed)", "worker_id", workerID)
				ct.processRemainingRecords(workerID)
				return
			}

			// Process the record by writing to all transports
			if err := ct.writeRecordToTransports(record); err != nil {
				ct.logger.Debug("💰 Cost Tracker: Async worker failed to process record", "worker_id", workerID, "request_id", record.RequestID, "error", err)
			} else {
				ct.logger.Debug("💰 Cost Tracker: Async worker processed record successfully", "worker_id", workerID, "request_id", record.RequestID)
			}

		case <-flushTicker.C:
			// Periodic flush - process any queued records
			ct.logger.Debug("💰 Cost Tracker: Periodic flush triggered", "worker_id", workerID)
			ct.flushQueuedRecords(workerID)

		case <-ct.ctx.Done():
			// Context cancelled, process any remaining records and exit
			ct.logger.Debug("💰 Cost Tracker: Async worker exiting (context cancelled)", "worker_id", workerID)
			ct.processRemainingRecords(workerID)
			return
		}
	}
}

// flushQueuedRecords processes a batch of queued records without blocking
func (ct *CostTracker) flushQueuedRecords(workerID int) {
	processed := 0
	for {
		select {
		case record, ok := <-ct.queue:
			if !ok {
				return // Queue is closed
			}
			if err := ct.writeRecordToTransports(record); err != nil {
				ct.logger.Debug("💰 Cost Tracker: Flush failed to process record", "worker_id", workerID, "request_id", record.RequestID, "error", err)
			}
			processed++
		default:
			// No more records to process
			if processed > 0 {
				ct.logger.Debug("💰 Cost Tracker: Flush processed records", "worker_id", workerID, "records_processed", processed)
			}
			return
		}
	}
}

// processRemainingRecords processes any remaining records in the queue during shutdown
func (ct *CostTracker) processRemainingRecords(workerID int) {
	processed := 0
	for {
		select {
		case record, ok := <-ct.queue:
			if !ok {
				if processed > 0 {
					ct.logger.Info("💰 Cost Tracker: Shutdown processed remaining records", "worker_id", workerID, "records_processed", processed)
				}
				return // Queue is closed
			}
			if err := ct.writeRecordToTransports(record); err != nil {
				ct.logger.Warn("💰 Cost Tracker: Shutdown failed to process record", "worker_id", workerID, "request_id", record.RequestID, "error", err)
			}
			processed++
		default:
			// No more records to process
			if processed > 0 {
				ct.logger.Info("💰 Cost Tracker: Shutdown processed remaining records", "worker_id", workerID, "records_processed", processed)
			}
			return
		}
	}
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

// TrackRequest processes a request and writes cost information to transports (sync or async based on configuration)
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

	// Handle sync vs async processing
	ct.mu.RLock()
	async := ct.async
	started := ct.started
	ct.mu.RUnlock()

	if async && started {
		// Async mode - queue the record for processing
		select {
		case ct.queue <- record:
			// Successfully queued
			return nil
		default:
			// Queue is full, log warning and fall back to sync processing
			ct.logger.Warn("💵 Cost Tracking: Async queue is full, falling back to sync processing",
				"request_id", metadata.RequestID)
			return ct.writeRecordToTransports(record)
		}
	}

	// Sync mode - process immediately
	return ct.writeRecordToTransports(record)
}

// writeRecordToTransports writes a record to all configured transports and returns any error
func (ct *CostTracker) writeRecordToTransports(record *CostRecord) error {
	var lastErr error
	for _, transport := range ct.transports {
		if err := transport.WriteRecord(record); err != nil {
			ct.logger.Warn("Failed to write record to transport", "error", err)
			lastErr = err
		}
	}
	return lastErr
}

// TransportFactory defines a function type for creating transports from configuration
type TransportFactory func(transportConfig interface{}, logger *slog.Logger) (Transport, error)

// transportRegistry holds registered transport factories
var transportRegistry = map[string]TransportFactory{
	"file":     NewFileTransportFromConfig,
	"dynamodb": NewDynamoDBTransportFromConfig,
	"datadog":  NewDatadogTransportFromConfig,
}

// RegisterTransportFactory registers a new transport factory
func RegisterTransportFactory(transportType string, factory TransportFactory) {
	transportRegistry[transportType] = factory
}

// CreateTransportFromConfig creates a transport based on the provided configuration
func CreateTransportFromConfig(transportConfig interface{}, logger *slog.Logger) (Transport, error) {
	var transportType string

	// Extract transport type from different config formats
	switch cfg := transportConfig.(type) {
	case *config.TransportConfig:
		transportType = cfg.Type
		logger.Debug("💰 Cost Tracker: Processing structured transport config", "type", transportType)
	case map[string]interface{}:
		var ok bool
		transportType, ok = cfg["type"].(string)
		if !ok {
			return nil, fmt.Errorf("transport type not specified")
		}
		logger.Debug("💰 Cost Tracker: Processing map-based transport config", "type", transportType)
	default:
		return nil, fmt.Errorf("unsupported transport config type: %T", transportConfig)
	}

	// Look up the transport factory
	factory, exists := transportRegistry[transportType]
	if !exists {
		return nil, fmt.Errorf("unsupported transport type: %s (supported: %v)", transportType, getSupportedTransportTypes())
	}

	// Create the transport using the registered factory
	return factory(transportConfig, logger)
}

// getSupportedTransportTypes returns a list of supported transport types
func getSupportedTransportTypes() []string {
	types := make([]string, 0, len(transportRegistry))
	for transportType := range transportRegistry {
		types = append(types, transportType)
	}
	return types
}
