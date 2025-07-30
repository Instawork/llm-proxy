package cost

import (
	"fmt"
	"log/slog"

	"github.com/DataDog/datadog-go/v5/statsd"

	configPkg "github.com/Instawork/llm-proxy/internal/config"
)

// DatadogTransportConfig holds configuration for the Datadog transport
type DatadogTransportConfig struct {
	Host       string       `json:"host"`        // DogStatsD host (default: localhost)
	Port       string       `json:"port"`        // DogStatsD port (default: 8125)
	Namespace  string       `json:"namespace"`   // Namespace to prefix metrics (default: "llm_proxy")
	Tags       []string     `json:"tags"`        // Global tags to apply to all metrics
	SampleRate float64      `json:"sample_rate"` // Global sample rate (default: 1.0)
	Logger     *slog.Logger `json:"-"`           // Logger instance
}

// DatadogTransport implements Transport interface for Datadog-based cost tracking
type DatadogTransport struct {
	client    *statsd.Client
	namespace string
	tags      []string
	logger    *slog.Logger
}

// NewDatadogTransport creates a new Datadog-based transport
func NewDatadogTransport(cfg DatadogTransportConfig) (*DatadogTransport, error) {
	// Set defaults
	if cfg.Host == "" {
		cfg.Host = "localhost"
	}
	if cfg.Port == "" {
		cfg.Port = "8125"
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "llm_proxy"
	}
	if cfg.SampleRate == 0 {
		cfg.SampleRate = 1.0
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	// Create DogStatsD client
	addr := fmt.Sprintf("%s:%s", cfg.Host, cfg.Port)
	client, err := statsd.New(addr,
		statsd.WithNamespace(cfg.Namespace),
		statsd.WithTags(cfg.Tags),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create DogStatsD client: %w", err)
	}

	transport := &DatadogTransport{
		client:    client,
		namespace: cfg.Namespace,
		tags:      cfg.Tags,
		logger:    logger,
	}

	return transport, nil
}

// FromConfig creates a DatadogTransport from configuration
func (dt *DatadogTransport) FromConfig(transportConfig interface{}, logger *slog.Logger) (Transport, error) {
	switch cfg := transportConfig.(type) {
	case *configPkg.TransportConfig:
		if cfg.Datadog == nil {
			return nil, fmt.Errorf("datadog transport configuration not found")
		}

		logger.Debug("💰 Datadog Transport: Creating from structured config",
			"host", cfg.Datadog.Host,
			"port", cfg.Datadog.Port,
			"namespace", cfg.Datadog.Namespace)

		config := DatadogTransportConfig{
			Host:       cfg.Datadog.Host,
			Port:       cfg.Datadog.Port,
			Namespace:  cfg.Datadog.Namespace,
			Tags:       cfg.Datadog.Tags,
			SampleRate: cfg.Datadog.SampleRate,
			Logger:     logger,
		}
		return NewDatadogTransport(config)

	case map[string]interface{}:
		datadogConfig, ok := cfg["datadog"].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("datadog transport configuration not found")
		}

		// Extract configuration with defaults
		host, _ := datadogConfig["host"].(string)
		port, _ := datadogConfig["port"].(string)
		namespace, _ := datadogConfig["namespace"].(string)
		sampleRate, _ := datadogConfig["sample_rate"].(float64)

		// Handle tags array
		var tags []string
		if tagsInterface, ok := datadogConfig["tags"].([]interface{}); ok {
			for _, tag := range tagsInterface {
				if tagStr, ok := tag.(string); ok {
					tags = append(tags, tagStr)
				}
			}
		}

		logger.Debug("💰 Datadog Transport: Creating from map config",
			"host", host,
			"port", port,
			"namespace", namespace)

		config := DatadogTransportConfig{
			Host:       host,
			Port:       port,
			Namespace:  namespace,
			Tags:       tags,
			SampleRate: sampleRate,
			Logger:     logger,
		}
		return NewDatadogTransport(config)

	default:
		return nil, fmt.Errorf("unsupported config type for datadog transport: %T", transportConfig)
	}
}

// NewDatadogTransportFromConfig creates a DatadogTransport from configuration (convenience function)
func NewDatadogTransportFromConfig(transportConfig interface{}, logger *slog.Logger) (Transport, error) {
	dt := &DatadogTransport{}
	return dt.FromConfig(transportConfig, logger)
}

// WriteRecord writes a cost record to Datadog as metrics
func (dt *DatadogTransport) WriteRecord(record *CostRecord) error {
	// Create metric tags from the record
	tags := make([]string, 0, len(dt.tags)+6)
	tags = append(tags, dt.tags...)
	tags = append(tags,
		fmt.Sprintf("provider:%s", record.Provider),
		fmt.Sprintf("model:%s", record.Model),
		fmt.Sprintf("endpoint:%s", record.Endpoint),
		fmt.Sprintf("streaming:%t", record.IsStreaming),
	)

	if record.UserID != "" {
		tags = append(tags, fmt.Sprintf("user_id:%s", record.UserID))
	}

	if record.FinishReason != "" {
		tags = append(tags, fmt.Sprintf("finish_reason:%s", record.FinishReason))
	}

	// Send token metrics
	if err := dt.client.Count("tokens.input", int64(record.InputTokens), tags, 1.0); err != nil {
		dt.logger.Warn("Failed to send input tokens metric to Datadog", "error", err)
	}

	if err := dt.client.Count("tokens.output", int64(record.OutputTokens), tags, 1.0); err != nil {
		dt.logger.Warn("Failed to send output tokens metric to Datadog", "error", err)
	}

	if err := dt.client.Count("tokens.total", int64(record.TotalTokens), tags, 1.0); err != nil {
		dt.logger.Warn("Failed to send total tokens metric to Datadog", "error", err)
	}

	// Send cost metrics (convert to cents to avoid floating point precision issues)
	inputCostCents := int64(record.InputCost * 100000) // Convert to 0.001 cent precision
	outputCostCents := int64(record.OutputCost * 100000)
	totalCostCents := int64(record.TotalCost * 100000)

	if err := dt.client.Count("cost.input_cents", inputCostCents, tags, 1.0); err != nil {
		dt.logger.Warn("Failed to send input cost metric to Datadog", "error", err)
	}

	if err := dt.client.Count("cost.output_cents", outputCostCents, tags, 1.0); err != nil {
		dt.logger.Warn("Failed to send output cost metric to Datadog", "error", err)
	}

	if err := dt.client.Count("cost.total_cents", totalCostCents, tags, 1.0); err != nil {
		dt.logger.Warn("Failed to send total cost metric to Datadog", "error", err)
	}

	// Send request count metric
	if err := dt.client.Incr("requests.count", tags, 1.0); err != nil {
		dt.logger.Warn("Failed to send request count metric to Datadog", "error", err)
	}

	dt.logger.Debug("💹 Datadog Transport: Metrics sent successfully",
		"provider", record.Provider,
		"model", record.Model,
		"total_cost", record.TotalCost,
		"total_tokens", record.TotalTokens)

	return nil
}

// Close closes the Datadog client
func (dt *DatadogTransport) Close() error {
	if dt.client != nil {
		return dt.client.Close()
	}
	return nil
}
