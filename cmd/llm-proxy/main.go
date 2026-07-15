package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Instawork/llm-proxy/internal/admin"
	"github.com/Instawork/llm-proxy/internal/adminrollup"
	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/circuit"
	"github.com/Instawork/llm-proxy/internal/circuitstats"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/cost"
	"github.com/Instawork/llm-proxy/internal/coststats"
	"github.com/Instawork/llm-proxy/internal/fake"
	"github.com/Instawork/llm-proxy/internal/history"
	"github.com/Instawork/llm-proxy/internal/idgatestats"
	"github.com/Instawork/llm-proxy/internal/middleware"
	"github.com/Instawork/llm-proxy/internal/modelstatusstats"
	"github.com/Instawork/llm-proxy/internal/observability"
	"github.com/Instawork/llm-proxy/internal/ocr"
	"github.com/Instawork/llm-proxy/internal/pii"
	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/provision"
	"github.com/Instawork/llm-proxy/internal/proxylog"
	"github.com/Instawork/llm-proxy/internal/ratelimit"
	"github.com/Instawork/llm-proxy/internal/ratelimitstats"
	"github.com/Instawork/llm-proxy/internal/redact"
	"github.com/Instawork/llm-proxy/internal/redactapi"
	"github.com/Instawork/llm-proxy/internal/usagestats"
	"github.com/gorilla/mux"
)

// CustomPrettyHandler implements a custom slog.Handler for pretty local output
type CustomPrettyHandler struct {
	level slog.Level
	w     io.Writer
}

func NewCustomPrettyHandler(w io.Writer, level slog.Level) *CustomPrettyHandler {
	return &CustomPrettyHandler{
		level: level,
		w:     w,
	}
}

func (h *CustomPrettyHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *CustomPrettyHandler) Handle(_ context.Context, r slog.Record) error {
	timeStr := r.Time.Format("15:04:05")

	// Build the message with all attributes inline
	message := r.Message
	var allAttrs []string

	r.Attrs(func(a slog.Attr) bool {
		allAttrs = append(allAttrs, fmt.Sprintf("%s=%v", a.Key, a.Value))
		return true
	})

	// Add attributes to the message if any exist
	if len(allAttrs) > 0 {
		message = fmt.Sprintf("%s; %s", message, strings.Join(allAttrs, ", "))
	}

	_, err := fmt.Fprintf(h.w, "%s [%s] %s\n", r.Level.String(), timeStr, message)
	return err
}

func (h *CustomPrettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return h // Ignore attributes for pretty output
}

func (h *CustomPrettyHandler) WithGroup(name string) slog.Handler {
	return h // Ignore groups for pretty output
}

var logger *slog.Logger

func logProxyError(msg string, args ...any) {
	logger.Error(proxylog.ProxyMsg(msg), args...)
}

func logProxyWarn(msg string, args ...any) {
	logger.Warn(proxylog.ProxyMsg(msg), args...)
}

const (
	// Version of the LLM Proxy
	version = "1.0.0"

	// Default port for the proxy server
	defaultPort = "9002"
)

// Global provider manager instance
var globalProviderManager *providers.ProviderManager

// Global cost tracker instance
var globalCostTracker *cost.CostTracker

// Global in-process cost spend rollup for the admin dashboard. Populated when
// cost tracking is enabled; surfaced via /admin/api/cost stats.
var globalCostStatsRecorder *coststats.Recorder

// Global API key store instance
var (
	globalAPIKeyStore          providers.APIKeyStore
	globalAPIKeyStoreInitError error
	globalAdminUserStore       *adminusers.Store
	globalAdminUserStoreError  error
	globalKeyProvisioner       *provision.Manager
)

// Global rate limiter instance
var globalRateLimiter ratelimit.RateLimiter

// Global rate-limit stats recorder (memory + optional redis/history rollups).
var globalRateLimitStatsRecorder *ratelimitstats.Recorder

// Shared row-history sink (local JSONL or S3).
var globalHistorySink *history.Sink

// Global in-process PII redaction stats recorder. Populated when the PII
// redaction middleware is installed; surfaced via /admin/api/pii. Stores
// metadata only (entity types/counts, masked key IDs) — never raw PII.
var globalPIIRecorder *pii.Recorder

// Global in-process government-ID gate stats (OCR on embedded images).
var globalIDGateRecorder *idgatestats.Recorder

// Global usage stats for the admin Usage page (fed from cost tracking).
var globalUsageStatsRecorder *usagestats.Recorder

// Redis-backed admin dashboard daily rollups (charts survive restart).
var (
	globalAdminRollupStore *adminrollup.Store
	globalAdminRollupStop  chan struct{}
)

// Global circuit breaker store instance
var globalCircuitStore circuit.Store

// In-process circuit activity (checks, probes, fast-fails) for the admin UI.
var globalCircuitStatsRecorder *circuitstats.Recorder

// In-process model status (retired, deprecated, unknown) for the admin UI.
var globalModelStatusRecorder *modelstatusstats.Recorder

// Resolved circuit-breaker config after Defaults() is applied.  Captured at
// startup so /health can surface the effective mode / backend / thresholds
// without re-reading YAML.
var globalCircuitConfig circuit.Config

// Set to true when the configured backend was "redis" but the Redis store
// could not be constructed and we transparently fell back to MemoryStore.
// Surfaced in /health so operators can tell at a glance that the circuit
// breaker is running without distributed coordination.
var globalCircuitRedisFallback bool

var globalAnalyzeCacheClose func() error

// Known provider names the circuit breaker tracks.  Kept as a single source
// of truth so the wiring code, /health handler, and any future diagnostics
// agree on the list.  Bedrock is appended at runtime in runServer when the
// YAML provider block is enabled — keeping it off the default list means
// existing deployments without a `providers.bedrock` section see no extra
// /health rollup entries.
var circuitBreakerProviders = []string{"openai", "anthropic", "gemini"}

// redisPingTimeout bounds the blocking startup PING to Redis.  Long enough
// to succeed on a warm connection with some network latency, short enough
// that a dead Redis never holds up proxy startup past its health-check window.
const redisPingTimeout = 2 * time.Second

func init() {
	logLevel := os.Getenv("LOG_LEVEL")
	var level slog.Level
	switch strings.ToLower(logLevel) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	// Use pretty text format for local development, JSON for production
	logFormat := os.Getenv("LOG_FORMAT")
	var handler slog.Handler

	if logFormat == "json" {
		// JSON format for production/machine parsing with AWS CloudWatch compatible timestamp
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level: level,
			ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
				// Format time with consistent RFC3339 format for better log parsing
				// This is more precise and timezone-aware than the basic AWS format
				if a.Key == slog.TimeKey && len(groups) == 0 {
					return slog.String(a.Key, a.Value.Time().Format("2006-01-02 15:04:05,"))
				}
				return a
			},
		})
	} else {
		// Custom pretty format for local development (default)
		handler = NewCustomPrettyHandler(os.Stderr, level)
	}

	logger = slog.New(handler)

	// Set our custom logger as the default slog logger
	// This ensures that any slog.Info() calls throughout the codebase use our configured logger
	slog.SetDefault(logger)
}

// initializeCostTracker creates and configures the cost tracker with pricing data from config
func initializeCostTracker(yamlConfig *config.YAMLConfig) *cost.CostTracker {
	// Check if cost tracking is enabled
	if !yamlConfig.Features.CostTracking.Enabled {
		logger.Info("💰 Cost Tracker: Cost tracking is disabled in config")
		return nil
	}

	// Get all transport configurations
	transportConfigs := yamlConfig.GetAllTransports()
	if len(transportConfigs) == 0 {
		logProxyError("💰 Cost Tracker: No transport configurations found")
		return nil
	}

	logger.Info("💰 Cost Tracker: Initializing transports", "transport_count", len(transportConfigs))

	// Create all configured transports
	var transports []cost.Transport
	var failedTransports []string

	for i, transportConfig := range transportConfigs {
		logger.Info("💰 Cost Tracker: Creating transport", "transport_index", i+1, "configured_type", transportConfig.Type)

		// Log additional transport config details
		switch transportConfig.Type {
		case "dynamodb":
			if transportConfig.DynamoDB != nil {
				logger.Info("💰 Cost Tracker: DynamoDB configuration",
					"table_name", transportConfig.DynamoDB.TableName,
					"region", transportConfig.DynamoDB.Region)
			}
		case "file":
			if transportConfig.File != nil {
				logger.Info("💰 Cost Tracker: File configuration", "path", transportConfig.File.Path)
			}
		case "datadog":
			if transportConfig.Datadog != nil {
				logger.Info("💰 Cost Tracker: Datadog configuration",
					"host", transportConfig.Datadog.Host,
					"port", transportConfig.Datadog.Port,
					"namespace", transportConfig.Datadog.Namespace)
			}
		}

		transport, err := cost.CreateTransportFromConfig(&transportConfig, logger)
		if err != nil {
			// Log the failed config details
			switch transportConfig.Type {
			case "dynamodb":
				if transportConfig.DynamoDB != nil {
					logProxyError("💰 Cost Tracker: Failed to create DynamoDB transport",
						"configured_type", transportConfig.Type,
						"table_name", transportConfig.DynamoDB.TableName,
						"region", transportConfig.DynamoDB.Region,
						"error", err)
				} else {
					logProxyError("💰 Cost Tracker: Failed to create transport", "configured_type", transportConfig.Type, "error", err)
				}
			case "file":
				if transportConfig.File != nil {
					logProxyError("💰 Cost Tracker: Failed to create file transport",
						"configured_type", transportConfig.Type,
						"path", transportConfig.File.Path,
						"error", err)
				} else {
					logProxyError("💰 Cost Tracker: Failed to create transport", "configured_type", transportConfig.Type, "error", err)
				}
			case "datadog":
				if transportConfig.Datadog != nil {
					logProxyError("💰 Cost Tracker: Failed to create Datadog transport",
						"configured_type", transportConfig.Type,
						"host", transportConfig.Datadog.Host,
						"port", transportConfig.Datadog.Port,
						"error", err)
				} else {
					logProxyError("💰 Cost Tracker: Failed to create transport", "configured_type", transportConfig.Type, "error", err)
				}
			default:
				logProxyError("💰 Cost Tracker: Failed to create transport", "configured_type", transportConfig.Type, "error", err)
			}
			failedTransports = append(failedTransports, transportConfig.Type)
			continue
		}

		logger.Info("💰 Cost Tracker: Transport created successfully", "transport_type", transportConfig.Type)
		transports = append(transports, transport)
	}

	// Check if we have at least one working transport
	if len(transports) == 0 {
		logProxyError("💰 Cost Tracker: No transports could be created, falling back to file transport")

		// Fallback to file transport with env var or default
		outputFile := os.Getenv("COST_TRACKING_FILE")
		if outputFile == "" {
			outputFile = "logs/cost-tracking.jsonl"
		}

		logProxyWarn("💰 Cost Tracker: Falling back to file transport", "fallback_type", "file", "output_file", outputFile)
		transport := cost.NewFileTransport(outputFile)
		transports = append(transports, transport)
		logger.Info("💰 Cost Tracker: Initialized with fallback", "actual_transport_type", "file", "output_file", outputFile)
	}

	// Create cost tracker with all working transports
	costTracker := cost.NewCostTracker(transports...)

	// Log successful initialization
	transportTypes := make([]string, len(transports))
	for i := range transports {
		if i < len(transportConfigs) {
			transportTypes[i] = transportConfigs[i].Type
		}
	}

	if len(failedTransports) > 0 {
		logProxyWarn("💰 Cost Tracker: Initialized with some transport failures",
			"successful_transports", transportTypes,
			"failed_transports", failedTransports)
	} else {
		logger.Info("💰 Cost Tracker: Initialized successfully",
			"transport_types", transportTypes,
			"transport_count", len(transports))
	}

	// Set up logger for the cost tracker
	costTracker.SetLogger(logger)

	// Configure async mode if enabled
	if yamlConfig.Features.CostTracking.Async {
		workers := yamlConfig.Features.CostTracking.Workers
		if workers <= 0 {
			workers = 5 // Default
		}
		queueSize := yamlConfig.Features.CostTracking.QueueSize
		if queueSize <= 0 {
			queueSize = 1000 // Default
		}
		flushInterval := yamlConfig.Features.CostTracking.FlushInterval
		if flushInterval <= 0 {
			flushInterval = 15 // Default
		}

		costTracker.ConfigureAsync(workers, queueSize, flushInterval)

		// Start the async workers
		if err := costTracker.StartAsyncWorkers(); err != nil {
			logProxyError("💰 Cost Tracker: Failed to start async workers", "error", err)
			logProxyWarn("💰 Cost Tracker: Falling back to synchronous mode")
			costTracker.SetSyncMode()
		} else {
			logger.Info("💰 Cost Tracker: Async mode enabled", "workers", workers, "queue_size", queueSize, "flush_interval_seconds", flushInterval)
		}
	} else {
		logger.Info("💰 Cost Tracker: Synchronous mode enabled")
	}

	// Load pricing data from config for each provider and model
	totalModelsConfigured := 0

	for providerName, providerConfig := range yamlConfig.Providers {
		if !providerConfig.Enabled {
			continue
		}

		for modelName, modelConfig := range providerConfig.Models {
			if !modelConfig.Enabled {
				continue
			}

			if modelConfig.Pricing != nil {
				// Convert YAML pricing to cost tracker format
				modelPricing, ok := modelConfig.Pricing.(*config.ModelPricing)
				if !ok {
					logProxyWarn("Could not parse pricing", "provider", providerName, "model", modelName)
					continue
				}

				var costTrackerPricing cost.ModelPricing
				for _, tier := range modelPricing.Tiers {
					costTrackerPricing.Tiers = append(costTrackerPricing.Tiers, cost.PricingTier{
						Threshold: tier.Threshold,
						Input:     tier.Input,
						Output:    tier.Output,
					})
				}

				if modelPricing.Overrides != nil {
					costTrackerPricing.Overrides = make(map[string]struct {
						Input  float64 `json:"input"`
						Output float64 `json:"output"`
					})
					for alias, override := range modelPricing.Overrides {
						costTrackerPricing.Overrides[alias] = struct {
							Input  float64 `json:"input"`
							Output float64 `json:"output"`
						}{Input: override.Input, Output: override.Output}
					}
				}

				// Set pricing for main model name
				costTracker.SetPricingForModel(providerName, modelName, &costTrackerPricing)
				totalModelsConfigured++

				// Set pricing for all aliases
				for _, alias := range modelConfig.Aliases {
					costTracker.SetPricingForModel(providerName, alias, &costTrackerPricing)
					totalModelsConfigured++
				}
			} else {
				logProxyWarn("Model has no pricing configured", "provider", providerName, "model", modelName)
			}
		}
	}

	logger.Info("💰 Cost Tracker: Configured pricing", "total_models_configured", totalModelsConfigured)

	globalCostStatsRecorder = coststats.NewRecorder()
	costTracker.SetStatsRecorder(globalCostStatsRecorder)
	globalUsageStatsRecorder = usagestats.NewRecorder()
	costTracker.SetUsageStatsRecorder(globalUsageStatsRecorder)

	return costTracker
}

// isTestModeAllowed evaluates the three-condition gate that controls
// whether the proxy honours X-LLM-Proxy-Test-Mode / llm_proxy_test_mode.
// ALL THREE conditions must be satisfied:
//  1. The circuit breaker feature is enabled (otherwise there is
//     nothing for the test-mode flag to exercise, and any honouring
//     of the header is pure attack surface).
//  2. TestModeEnabled is true in YAML (explicit opt-in).
//  3. LLM_PROXY_ALLOW_TEST_MODE=1 is set in the process environment
//     (belt-and-suspenders guard against a misconfigured YAML in
//     production).
//
// The result of this gate is plumbed into circuit.Config.TestModeEnabled
// so the transport layer refuses to honour the test-mode signals when
// any guard fails — without this, a client could smuggle synthetic
// degraded responses past the middleware by sending a request that
// hits the transport directly.
func isTestModeAllowed(yc *config.YAMLConfig) bool {
	return yc.Features.CircuitBreaker.Enabled &&
		yc.Features.CircuitBreaker.TestModeEnabled &&
		os.Getenv("LLM_PROXY_ALLOW_TEST_MODE") == "1"
}

// isFakeModeAllowed gates global fake-upstream responses. Both YAML opt-in
// and LLM_PROXY_ALLOW_FAKE_MODE=1 are required.
func isFakeModeAllowed(yc *config.YAMLConfig) bool {
	return yc.Features.FakeUpstream.Enabled &&
		os.Getenv("LLM_PROXY_ALLOW_FAKE_MODE") == "1"
}

func fakeConfigFromYAML(yc *config.YAMLConfig, allowed bool) fake.Config {
	fu := yc.Features.FakeUpstream
	est := providers.YAMLConfigEstimationAdapter{
		MaxSampleBytes:        yc.Features.RateLimiting.Estimation.MaxSampleBytes,
		BytesPerToken:         yc.Features.RateLimiting.Estimation.BytesPerToken,
		CharsPerToken:         yc.Features.RateLimiting.Estimation.CharsPerToken,
		ProviderCharsPerToken: yc.Features.RateLimiting.Estimation.ProviderCharsPerToken,
	}
	if est.MaxSampleBytes == 0 {
		est.MaxSampleBytes = 200000
	}
	if est.BytesPerToken == 0 {
		est.BytesPerToken = 4
	}
	if est.CharsPerToken == 0 {
		est.CharsPerToken = 4
	}
	return fake.Config{
		Enabled:          allowed,
		ChaosFailureRate: fu.ChaosFailureRate,
		ChaosSeed:        fu.ChaosSeed,
		LatencyMS:        fu.LatencyMS,
		JitterMS:         fu.JitterMS,
		Estimation:       est,
	}
}

func wrapProviderWithFake(
	p interface {
		WrapTransport(func(http.RoundTripper) http.RoundTripper)
	},
	providerName string,
	cfg fake.Config,
) {
	p.WrapTransport(func(inner http.RoundTripper) http.RoundTripper {
		return fake.NewTransport(inner, providerName, cfg)
	})
}

// circuitConfigFromYAML converts the YAML circuit breaker config into the
// internal circuit.Config (with defaults applied). Centralised so we don't
// drift between the store-initialisation and transport-wrapping call sites.
//
// testModeAllowed must be the result of isTestModeAllowed for the same
// yamlConfig.  It is threaded in rather than re-derived so every call
// site sees an identical value (there is no scenario where one layer
// should honour test-mode and another should not).
func circuitConfigFromYAML(cb config.CircuitBreakerConfig, testModeAllowed bool) circuit.Config {
	// BypassAllowed defaults to true when the YAML field is omitted, on
	// the principle that callers without a fallback should be able to
	// opt out of fast-fail by default.  An explicit `bypass_allowed:
	// false` (which the *bool unmarshalling preserves) disables the
	// safety valve entirely.
	bypassAllowed := true
	if cb.BypassAllowed != nil {
		bypassAllowed = *cb.BypassAllowed
	}
	cfg := circuit.Config{
		Enabled:                         cb.Enabled,
		Mode:                            cb.Mode,
		Backend:                         cb.Backend,
		FailureThreshold:                cb.FailureThreshold,
		WindowSeconds:                   cb.WindowSeconds,
		CooldownSeconds:                 cb.CooldownSeconds,
		MaxTransientRetries:             cb.MaxTransientRetries,
		MaxRateLimitRetries:             cb.MaxRateLimitRetries,
		RetryContributionMode:           cb.RetryContributionMode,
		GlobalRateLimitEscalationWindow: cb.GlobalRateLimitEscalationWindow,
		DegradedSignal:                  cb.DegradedSignal,
		TestModeEnabled:                 testModeAllowed,
		BypassAllowed:                   bypassAllowed,
		PerProviderRollupThreshold:      cb.PerProviderRollupThreshold,
		PerProviderRollupWindowSeconds:  cb.PerProviderRollupWindowSeconds,
		BypassReasonAllowlist:           cb.BypassReasonAllowlist,
	}
	if cb.Redis != nil {
		// Expand ${VAR} / $VAR tokens from the process environment so we
		// can thread secrets (e.g. a REDIS_URL rendered from a secret
		// manager by the deploy system) through without baking them
		// into the YAML or requiring a separate secrets flavour.  Values
		// without any `$` pass through unchanged.
		cfg.RedisURL = os.ExpandEnv(cb.Redis.URL)
		cfg.RedisAddress = os.ExpandEnv(cb.Redis.Address)
		cfg.RedisPassword = os.ExpandEnv(cb.Redis.Password)
		cfg.RedisDB = cb.Redis.DB
		cfg.RedisDBSet = cb.Redis.DBSet
	}
	return cfg.Defaults()
}

// initializeCircuitStore creates the circuit breaker state store from config.
//
// Redis failures are intentionally non-fatal:
//   - If NewRedisStore itself errors (e.g. malformed URL or failed PING), we
//     log and fall back to a MemoryStore so the proxy still comes up.
//   - If the Redis-backed store later encounters Redis errors, steady-state
//     Store operations fail-open to StateClosed so transient outages self-heal.
//
// Net effect: a Redis outage degrades the circuit breaker to "per-instance
// best effort" without ever taking the sidecar down.
func initializeCircuitStore(yamlConfig *config.YAMLConfig) circuit.Store {
	cb := yamlConfig.Features.CircuitBreaker
	if !cb.Enabled {
		logger.Info("⚡ Circuit Breaker: disabled in config")
		return nil
	}

	cfg := circuitConfigFromYAML(cb, isTestModeAllowed(yamlConfig))
	if err := cfg.Validate(); err != nil {
		logProxyError("⚡ Circuit Breaker: invalid configuration — falling back to memory store", "error", err)
		cfg.Backend = "memory"
	}

	store, err := circuit.Factory(cfg)
	if err != nil {
		logProxyError(
			"⚡ Circuit Breaker: store construction failed — falling back to memory store",
			"backend", cfg.Backend,
			"error", err,
		)
		store = circuit.NewMemoryStore(cfg)
		globalCircuitRedisFallback = true
	}

	if rs, ok := store.(*circuit.RedisStore); ok {
		pingCtx, cancel := context.WithTimeout(context.Background(), redisPingTimeout)
		if pingErr := rs.Ping(pingCtx); pingErr != nil {
			logProxyWarn(
				"⚡ Circuit Breaker: Redis PING failed at startup — proxy will continue (store fails open on Redis errors)",
				"error", pingErr,
			)
		} else {
			logger.Info("⚡ Circuit Breaker: Redis reachable")
		}
		cancel()
	}

	// Capture the resolved config so /health can report it without
	// re-applying Defaults() on every request.
	globalCircuitConfig = cfg.Defaults()

	logger.Info(
		"⚡ Circuit Breaker: initialized",
		"backend", cfg.Backend,
		"redis_fallback", globalCircuitRedisFallback,
		"mode", cfg.Mode,
		"failure_threshold", cfg.FailureThreshold,
		"window_seconds", cfg.WindowSeconds,
		"cooldown_seconds", cfg.CooldownSeconds,
		"max_transient_retries", cfg.MaxTransientRetries,
		"max_rate_limit_retries", cfg.MaxRateLimitRetries,
		"retry_contribution_mode", cfg.RetryContributionMode,
		"degraded_signal", cfg.DegradedSignal,
		"bypass_allowed", cfg.BypassAllowed,
		"bypass_reason_allowlist", cfg.BypassReasonAllowlist,
		"per_provider_rollup_threshold", cfg.PerProviderRollupThreshold,
		"per_provider_rollup_window_seconds", cfg.PerProviderRollupWindowSeconds,
	)
	return store
}

// wrapProviderWithCircuitBreaker injects a circuit-breaking transport into the
// provider, keyed by providerName.  Optional opts (metrics sink, model
// extractor, env tag) flow through to circuit.NewTransport.
func wrapProviderWithCircuitBreaker(
	p interface {
		WrapTransport(func(http.RoundTripper) http.RoundTripper)
	},
	store circuit.Store,
	cfg circuit.Config,
	providerName string,
	opts ...circuit.Option,
) {
	p.WrapTransport(func(inner http.RoundTripper) http.RoundTripper {
		return circuit.NewTransport(inner, store, cfg, providerName, logger, opts...)
	})
}

// circuitDatadogConfig returns the circuit-breaker Datadog transport config
// when one is declared under features.circuit_breaker.datadog.
func circuitDatadogConfig(yamlConfig *config.YAMLConfig) *config.DatadogTransportConfig {
	cb := yamlConfig.Features.CircuitBreaker
	if cb.Datadog == nil {
		return nil
	}
	return cb.Datadog
}

// initializeCircuitMetrics builds a dogstatsd sink for circuit-breaker metrics.
func initializeCircuitMetrics(yamlConfig *config.YAMLConfig) circuit.MetricsSink {
	ddCfg := circuitDatadogConfig(yamlConfig)
	if ddCfg == nil {
		logger.Info("⚡ Circuit Breaker: dogstatsd metrics disabled (no circuit_breaker.datadog config)")
	}
	return observability.NewMetricsSink(ddCfg, logger, "circuit_breaker")
}

func initializePIIMetrics(yamlConfig *config.YAMLConfig) observability.MetricsSink {
	return observability.NewMetricsSink(yamlConfig.Features.PIIRedact.Datadog, logger, "pii_redact")
}

func initializeIDGateMetrics(yamlConfig *config.YAMLConfig) observability.MetricsSink {
	return observability.NewMetricsSink(yamlConfig.Features.IDGate.Datadog, logger, "id_gate")
}

// circuitModelExtractor returns a ModelFromRequestFunc that dispatches to
// the right provider's request-body parser by URL prefix.  Used by the
// circuit transport on failure paths to enrich logs and metrics with
// the LLM model name.  Returns "" for any unrecognised path so log
// fields stay schema-stable rather than panicking on a stray request.
//
// bedrockProvider is optional (nil when the YAML disables Bedrock); when
// nil we skip the Bedrock-specific branch entirely so we don't panic on a
// stray `/bedrock/...` path that the router somehow let through.
func circuitModelExtractor(
	openAIProvider *providers.OpenAIProxy,
	anthropicProvider *providers.AnthropicProxy,
	geminiProvider *providers.GeminiProxy,
	bedrockProvider *providers.BedrockProxy,
	bedrockMantleProvider *providers.BedrockMantleProxy,
) circuit.ModelFromRequestFunc {
	return func(req *http.Request) string {
		if req == nil || req.URL == nil {
			return ""
		}
		path := req.URL.Path
		switch {
		case strings.HasPrefix(path, "/openai/"),
			strings.HasPrefix(path, "/v1/chat/"),
			strings.HasPrefix(path, "/v1/responses"),
			strings.HasPrefix(path, "/v1/completions"):
			model, _ := openAIProvider.ExtractRequestModelAndMessages(req)
			return model
		case strings.HasPrefix(path, "/anthropic/"),
			strings.HasPrefix(path, "/v1/messages"):
			model, _ := anthropicProvider.ExtractRequestModelAndMessages(req)
			return model
		case strings.HasPrefix(path, "/gemini/"),
			strings.HasPrefix(path, "/v1beta/models/"),
			strings.HasPrefix(path, "/v1/models/"):
			model, _ := geminiProvider.ExtractRequestModelAndMessages(req)
			return model
		case strings.HasPrefix(path, "/bedrock/"), strings.HasPrefix(path, "/model/"):
			if bedrockProvider == nil {
				return ""
			}
			model, _ := bedrockProvider.ExtractRequestModelAndMessages(req)
			return model
		case strings.HasPrefix(path, "/bedrock-mantle/"):
			if bedrockMantleProvider == nil {
				return ""
			}
			model, _ := bedrockMantleProvider.ExtractRequestModelAndMessages(req)
			return model
		}
		return ""
	}
}

// circuitCallerExtractor returns a caller-label extractor for circuit
// failure observability. It reads the resolved proxy API-key record off
// the request context (set by the auth middleware) and returns its
// Description — a stable, human-readable label like "finch-prod" — never
// the secret key value. Empty when no key is on the context; the circuit
// transport normalizes that to the "unknown" tag.
func circuitCallerExtractor() circuit.CallerFromRequestFunc {
	return func(req *http.Request) string {
		if req == nil {
			return ""
		}
		if rec, ok := apikeys.FromContext(req.Context()); ok && rec != nil {
			return rec.Description
		}
		return ""
	}
}

// initializeAPIKeyStore creates and configures the API key store from config
func initializeAPIKeyStore(yamlConfig *config.YAMLConfig) providers.APIKeyStore {
	// Check if API key management is enabled
	if !yamlConfig.Features.APIKeyManagement.Enabled {
		logger.Info("🔑 API Key Store: API key management is disabled in config")
		return nil
	}

	// Get API key management configuration
	apiKeyConfig := yamlConfig.Features.APIKeyManagement
	if apiKeyConfig.TableName == "" || apiKeyConfig.Region == "" {
		logProxyError("🔑 API Key Store: Missing required configuration (table_name or region)")
		return nil
	}

	// Resolve the proxy key prefix base, env var taking precedence over YAML.
	// A blank value leaves the apikeys default in place. New keys are minted
	// as "sk-<base>-<random>"; legacy "<base>-" / "<base>_" / "<base>:" keys
	// still validate.
	keyPrefixBase := os.Getenv("LLM_PROXY_API_KEY_PREFIX")
	if keyPrefixBase == "" {
		keyPrefixBase = apiKeyConfig.KeyPrefix
	}
	apikeys.SetKeyPrefixBase(keyPrefixBase)
	logger.Info("🔑 API Key Store: key prefix configured",
		"prefix_base", apikeys.KeyPrefixBase(),
		"generation_prefix", apikeys.KeyPrefix)

	logger.Info("🔑 API Key Store: Initializing API key store",
		"table_name", apiKeyConfig.TableName,
		"region", apiKeyConfig.Region,
		"endpoint_url", apiKeyConfig.EndpointURL)

	endpointURL := apiKeyConfig.EndpointURL
	if endpointURL == "" {
		endpointURL = os.Getenv("AWS_ENDPOINT_URL")
	}

	// Create the API key store. AutoCreateTable is intentionally driven by
	// the YAML config and defaults to false so a misconfigured production
	// deploy cannot provision DynamoDB resources in the active AWS account.
	store, err := apikeys.NewStore(apikeys.StoreConfig{
		TableName:       apiKeyConfig.TableName,
		Region:          apiKeyConfig.Region,
		EndpointURL:     endpointURL,
		Logger:          logger,
		AutoCreateTable: apiKeyConfig.AutoCreateTable,
	})
	if err != nil {
		globalAPIKeyStoreInitError = err
		logProxyError("🔑 API Key Store: Failed to create API key store", "error", err)
		return nil
	}

	logger.Info("🔑 API Key Store: Successfully initialized API key store")
	return store
}

func initializeAdminUserStore(yamlConfig *config.YAMLConfig) *adminusers.Store {
	if yamlConfig == nil || !yamlConfig.Features.AdminDashboard.Enabled {
		logger.Info("👤 Admin User Store: admin dashboard disabled")
		return nil
	}

	userCfg := yamlConfig.Features.AdminDashboard.Users.DynamoDB
	if userCfg.TableName == "" || userCfg.Region == "" {
		logProxyError("👤 Admin User Store: missing table_name or region")
		globalAdminUserStoreError = fmt.Errorf("admin users dynamodb table_name and region are required")
		return nil
	}

	endpointURL := userCfg.EndpointURL
	if endpointURL == "" {
		endpointURL = os.Getenv("AWS_ENDPOINT_URL")
	}

	store, err := adminusers.NewStore(adminusers.StoreConfig{
		TableName:       userCfg.TableName,
		Region:          userCfg.Region,
		EndpointURL:     endpointURL,
		Logger:          logger,
		AutoCreateTable: userCfg.AutoCreateTable,
	})
	if err != nil {
		globalAdminUserStoreError = err
		logProxyError("👤 Admin User Store: failed to initialize", "error", err)
		return nil
	}

	logger.Info("👤 Admin User Store: successfully initialized", "table", userCfg.TableName)
	return store
}

// piiSummaryFunc returns a snapshot closure for the admin /pii endpoint, or
// newPerKeyOverrideProvider returns a cached PerKeyOverrideFunc that resolves
// an iw: key's rate-limit overrides from its DynamoDB record. Results (hits
// and misses) are cached with a short TTL so the hot path doesn't hit
// DynamoDB on every request; a key whose limits change takes up to the TTL
// to propagate.
func newPerKeyOverrideProvider(store *apikeys.Store, logger *slog.Logger) ratelimit.PerKeyOverrideFunc {
	const ttl = 30 * time.Second
	type entry struct {
		limits config.LimitsConfig
		found  bool
		exp    time.Time
	}
	var mu sync.Mutex
	cache := make(map[string]entry)

	return func(keyID string) (config.LimitsConfig, bool) {
		now := time.Now()
		mu.Lock()
		if e, ok := cache[keyID]; ok && now.Before(e.exp) {
			mu.Unlock()
			return e.limits, e.found
		}
		mu.Unlock()

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		e := entry{exp: now.Add(ttl)}
		if !apikeys.HasKeyPrefix(keyID) {
			mu.Lock()
			cache[keyID] = e
			mu.Unlock()
			return e.limits, e.found
		}

		rec, err := store.GetKeyRecord(ctx, keyID)
		if err == nil && rec != nil {
			if lc, found := apikeys.RateLimitOverrides(rec); found {
				e.limits = lc
				e.found = true
			}
		} else if err != nil {
			logger.Debug("per-key rate-limit lookup failed", "key", apikeys.RedactKey(keyID), "error", err)
		}

		mu.Lock()
		cache[keyID] = e
		mu.Unlock()
		return e.limits, e.found
	}
}

// nil when PII redaction (and thus its recorder) was never installed.
func piiSummaryFunc() func() map[string]interface{} {
	if globalPIIRecorder == nil {
		return nil
	}
	return globalPIIRecorder.Snapshot
}

func idGateSummaryFunc() func() map[string]interface{} {
	if globalIDGateRecorder == nil {
		return nil
	}
	return globalIDGateRecorder.Snapshot
}

// nil when cost tracking (and thus its stats recorder) was never installed.
func costSummaryFunc() func() map[string]interface{} {
	if globalCostStatsRecorder == nil {
		return nil
	}
	return globalCostStatsRecorder.Snapshot
}

func usageSummaryFunc() func() map[string]interface{} {
	if globalUsageStatsRecorder == nil {
		return nil
	}
	return globalUsageStatsRecorder.Snapshot
}

func rateLimitSummaryFunc() func() map[string]interface{} {
	if globalRateLimitStatsRecorder == nil {
		return nil
	}
	return globalRateLimitStatsRecorder.Snapshot
}

func circuitActivitySummaryFunc() func() map[string]interface{} {
	if globalCircuitStatsRecorder == nil {
		return nil
	}
	return globalCircuitStatsRecorder.Snapshot
}

func modelStatusSummaryFunc() func() map[string]interface{} {
	if globalModelStatusRecorder == nil {
		return nil
	}
	return globalModelStatusRecorder.Snapshot
}

func initHistory(yamlConfig *config.YAMLConfig) {
	hc := yamlConfig.Features.History
	if hc.Backend == "" || strings.EqualFold(hc.Backend, "none") {
		logger.Info("History sink: disabled (backend: none)")
		return
	}
	sink, err := history.New(history.ConfigFromYAML(hc, logger))
	if err != nil {
		logProxyWarn("History sink: disabled", "error", err)
		return
	}
	if sink == nil {
		return
	}
	globalHistorySink = sink
	streams := hc.Streams
	if globalCostStatsRecorder != nil && history.StreamEnabled(streams, history.StreamCost) {
		globalCostStatsRecorder.BindHistory(sink, history.StreamCost)
	}
	if globalPIIRecorder != nil && history.StreamEnabled(streams, history.StreamPII) {
		globalPIIRecorder.BindHistory(sink, history.StreamPII)
	}
	if globalIDGateRecorder != nil && history.StreamEnabled(streams, history.StreamIDGate) {
		globalIDGateRecorder.BindHistory(sink, history.StreamIDGate)
	}
	if globalUsageStatsRecorder != nil && history.StreamEnabled(streams, history.StreamUsage) {
		globalUsageStatsRecorder.BindHistory(sink, history.StreamUsage)
	}
	if globalRateLimitStatsRecorder != nil && history.StreamEnabled(streams, history.StreamRateLimit) {
		globalRateLimitStatsRecorder.BindHistory(sink, history.StreamRateLimit)
	}
	logger.Info("History sink: ENABLED", "backend", hc.Backend, "streams", streams)
}

func initAdminRollups(yamlConfig *config.YAMLConfig) {
	rollupCfg := adminrollup.ConfigFromYAML(yamlConfig.Features.AdminDashboard)
	rollupCfg.Logger = logger
	store, err := adminrollup.NewStore(rollupCfg)
	if err != nil {
		logProxyWarn("Admin rollups: disabled", "error", err)
		return
	}
	if store == nil {
		return
	}
	globalAdminRollupStore = store
	logger.Info(
		"Admin rollups: ENABLED",
		"backend", store.Backend(),
		"history_days", store.HistoryDays(),
		"redis_db", rollupDBLabel(yamlConfig.Features.AdminDashboard.Rollups.Redis),
	)

	if globalCostStatsRecorder != nil {
		globalCostStatsRecorder.BindRollup(store, adminrollup.NewPersister(store, adminrollup.MetricCost))
	}
	if globalPIIRecorder != nil {
		globalPIIRecorder.BindRollup(store, adminrollup.NewPersister(store, adminrollup.MetricPII))
	}
	if globalIDGateRecorder != nil {
		globalIDGateRecorder.BindRollup(store, adminrollup.NewPersister(store, adminrollup.MetricIDGate))
	}
	if globalUsageStatsRecorder != nil {
		globalUsageStatsRecorder.BindRollup(store, adminrollup.NewPersister(store, adminrollup.MetricUsage))
	}
	if globalRateLimitStatsRecorder != nil {
		globalRateLimitStatsRecorder.BindRollup(store, adminrollup.NewPersister(store, adminrollup.MetricRateLimit))
	}
	if globalCircuitStatsRecorder != nil {
		globalCircuitStatsRecorder.BindRollup(store, adminrollup.NewPersister(store, adminrollup.MetricCircuitActivity))
	}
	if globalModelStatusRecorder != nil {
		globalModelStatusRecorder.BindRollup(store, adminrollup.NewPersister(store, adminrollup.MetricModelStatus))
	}
	if globalCircuitStore != nil {
		globalAdminRollupStop = make(chan struct{})
		go adminrollup.RunCircuitArchiver(
			store,
			adminrollup.NewPersister(store, adminrollup.MetricCircuit),
			globalCircuitStore,
			circuitBreakerProviders,
			globalCircuitConfig.PerProviderRollupThreshold,
			globalCircuitConfig.PerProviderRollupWindowSeconds,
			globalAdminRollupStop,
		)
	}
}

func rollupDBLabel(r *config.RedisConfig) int {
	if r == nil {
		return 0
	}
	return r.DB
}

// healthHandler provides a simple health check endpoint.
//
// When the circuit breaker is enabled, the response includes a
// `circuit_breaker` block describing the effective mode, backend, whether
// we fell back from Redis to in-memory, and per-provider state/failure
// counts.  This is the canonical signal operators should key dashboards
// and alerts off of.
func healthHandler(w http.ResponseWriter, r *http.Request) {
	providerHealth := globalProviderManager.GetHealthStatus()

	var circuitBlock map[string]interface{}
	if globalCircuitStore != nil {
		perProvider := make(map[string]interface{}, len(circuitBreakerProviders))
		totalFailures := 0
		for _, name := range circuitBreakerProviders {
			stats, err := circuit.ProviderStatsFor(r.Context(), globalCircuitStore, name)
			if err != nil {
				// /health is unauthenticated, so we MUST NOT leak the
				// raw error string (it can contain Redis URLs, host
				// names, port numbers, or other infrastructure detail
				// that helps an attacker enumerate the deployment).
				// Operators get the full detail via the structured
				// logger below.
				logProxyWarn(
					"⚡ Circuit Breaker: GetStats error on /health",
					"provider", name,
					"error", err,
				)
				perProvider[name] = map[string]interface{}{
					"error": "stats_unavailable",
				}
				continue
			}
			entry := map[string]interface{}{
				"state":    stats.State.String(),
				"failures": stats.Failures,
			}
			totalFailures += stats.Failures
			if stats.CooldownUntil != nil {
				entry["cooldown_until"] = stats.CooldownUntil.Unix()
			}

			// Per-provider rollup snapshot.  In per-model keying mode
			// the bare-provider state above is rarely Open (it only
			// trips when the model extractor cannot identify a model);
			// the rollup is what tells operators how many models are
			// currently degraded and which ones.  Surfacing both lets
			// dashboards alert on the right signal.
			if rec, ok := globalCircuitStore.(circuit.RollupRecorder); ok && globalCircuitConfig.PerProviderRollupThreshold > 0 {
				keys, _ := rec.RolledUpKeys(r.Context(), name, globalCircuitConfig.PerProviderRollupWindowSeconds)
				rollupOpen, count, _ := rec.RollupOpen(r.Context(), name,
					globalCircuitConfig.PerProviderRollupThreshold,
					globalCircuitConfig.PerProviderRollupWindowSeconds)
				rollup := map[string]interface{}{
					"enabled":        true,
					"open":           rollupOpen,
					"count":          count,
					"threshold":      globalCircuitConfig.PerProviderRollupThreshold,
					"window_seconds": globalCircuitConfig.PerProviderRollupWindowSeconds,
				}
				if len(keys) > 0 {
					rollup["open_keys"] = keys
				}
				entry["rollup"] = rollup
			}
			perProvider[name] = entry

			// Keep the legacy provider-level fields populated so any
			// existing consumers that read providers[X].circuit_state
			// don't break.
			ph, _ := providerHealth[name].(map[string]interface{})
			if ph == nil {
				ph = make(map[string]interface{})
			}
			ph["circuit_state"] = stats.State.String()
			ph["circuit_failures"] = stats.Failures
			if stats.CooldownUntil != nil {
				ph["circuit_cooldown_until"] = stats.CooldownUntil.Unix()
			}
			providerHealth[name] = ph
		}
		circuitBlock = map[string]interface{}{
			"enabled":         true,
			"mode":            globalCircuitConfig.Mode,
			"backend":         globalCircuitConfig.Backend,
			"redis_fallback":  globalCircuitRedisFallback,
			"providers":       perProvider,
			"total_failures":  totalFailures,
			"degraded_signal": globalCircuitConfig.DegradedSignal,
		}
		// Daily-history enrichment hits Redis (MGET of all retained day keys
		// + a merge write). /health is the container liveness probe and runs
		// on a tight interval, so we MUST NOT couple it to Redis: a slow or
		// down Redis would add latency / flap the probe even though the proxy
		// itself is perfectly able to serve LLM traffic. Only do the Redis
		// work when a caller explicitly opts in (the admin dashboard sends
		// ?history=1); bare infra probes stay Redis-free.
		if globalAdminRollupStore != nil && r.URL.Query().Get("history") == "1" {
			cbSnap := adminrollup.SnapshotCircuit(
				r.Context(),
				globalCircuitStore,
				circuitBreakerProviders,
				globalCircuitConfig.PerProviderRollupThreshold,
				globalCircuitConfig.PerProviderRollupWindowSeconds,
			)
			hctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			globalAdminRollupStore.MergeHistory(hctx, adminrollup.MetricCircuit, cbSnap)
			globalAdminRollupStore.MergeHourlySnapshots(hctx, adminrollup.MetricCircuit, cbSnap)
			cancel()
			circuitBlock["daily_history"] = cbSnap["daily_history"]
			circuitBlock["daily_history_available"] = cbSnap["daily_history_available"]
			circuitBlock["hourly_history"] = cbSnap["hourly_history"]
			circuitBlock["hourly_history_available"] = cbSnap["hourly_history_available"]
		}
	}

	health := map[string]interface{}{
		"status":    "healthy",
		"timestamp": time.Now().Unix(),
		"providers": providerHealth,
		"features": map[string]bool{
			"cost_tracking":   globalCostTracker != nil,
			"circuit_breaker": globalCircuitStore != nil,
		},
	}
	if circuitBlock != nil {
		health["circuit_breaker"] = circuitBlock
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health) //nolint:errcheck
}

// handleConfigValidation handles the --validate-config flag functionality
func handleConfigValidation(validateConfigArg string) {
	// Parse comma-separated file paths
	filePaths := strings.Split(validateConfigArg, ",")
	for i, path := range filePaths {
		filePaths[i] = strings.TrimSpace(path)
	}

	fmt.Printf("Validating configuration files: %s\n", strings.Join(filePaths, ", "))

	// Load and merge the configuration files using config package function
	mergedConfig, err := config.LoadAndMergeConfigs(filePaths)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ Configuration validation failed: %v\n", err)
		os.Exit(1)
	}

	// Print success message with summary
	fmt.Printf("✅ Configuration validation successful!\n")
	fmt.Printf("📊 Configuration summary:\n")
	fmt.Printf("   - Enabled: %v\n", mergedConfig.Enabled)
	fmt.Printf("   - Cost tracking: %v\n", mergedConfig.Features.CostTracking.Enabled)

	if mergedConfig.Features.CostTracking.Enabled {
		transports := mergedConfig.GetAllTransports()
		fmt.Printf("   - Transports: %d configured\n", len(transports))
		for i, transport := range transports {
			fmt.Printf("     %d. Type: %s\n", i+1, transport.Type)
		}
	}

	fmt.Printf("   - Providers: %d configured\n", len(mergedConfig.Providers))
	for providerName, provider := range mergedConfig.Providers {
		if provider.Enabled {
			fmt.Printf("     - %s: %d models\n", providerName, len(provider.Models))
		}
	}

	fmt.Printf("🎉 All configuration files are valid and merged successfully!\n")
	os.Exit(0)
}

// handleVersionFlag handles the --version flag functionality
func handleVersionFlag(yamlConfig *config.YAMLConfig) {
	fmt.Printf("LLM Proxy version %s\n", version)
	fmt.Println("Configuration:")

	// Print configuration to stdout in a readable format
	yamlConfig.LogConfiguration(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	// Also print as JSON for machine parsing
	fmt.Println("\nConfiguration JSON:")
	configJSON, err := json.MarshalIndent(yamlConfig, "", "  ")
	if err != nil {
		fmt.Printf("Error marshaling config to JSON: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(string(configJSON))

	fmt.Println("Build successful - configuration loaded without errors")
	os.Exit(0)
}

// buildMantleModelProjects flattens a Bedrock Mantle provider config into a
// model-id/alias -> project-id map for OpenAI-Project header injection. Project
// ids are expanded with os.ExpandEnv so account-specific values stay out of the
// committed YAML (e.g. project_id: "${LLM_PROXY_..._PROJECT_ID}"). Models with
// no configured (or env-empty) project id are skipped, leaving the account
// default in force.
func buildMantleModelProjects(cfg config.ProviderConfig) map[string]string {
	projects := make(map[string]string)
	for name, model := range cfg.Models {
		project := os.ExpandEnv(model.ProjectID)
		if project == "" {
			continue
		}
		projects[name] = project
		for _, alias := range model.Aliases {
			if alias != "" {
				projects[alias] = project
			}
		}
	}
	if len(projects) == 0 {
		return nil
	}
	return projects
}

// runServer starts and runs the LLM proxy server
// registerProviders constructs each provider proxy, registers it with the
// global provider manager, and returns concrete handles needed by the
// circuit-breaker wiring below. Bedrock is opt-in via
// `providers.bedrock.enabled: true` so existing deployments are unaffected.
func registerProviders(yamlConfig *config.YAMLConfig, disableGzip bool) (
	openAI *providers.OpenAIProxy,
	anthropic *providers.AnthropicProxy,
	gemini *providers.GeminiProxy,
	bedrock *providers.BedrockProxy,
	bedrockMantle *providers.BedrockMantleProxy,
) {
	proxyOpts := providers.ProxyOptions{DisableGzip: disableGzip}
	if disableGzip {
		logProxyWarn("Gzip disabled: Accept-Encoding stripped and transport compression off (debug mode)")
	}

	openAI = providers.NewOpenAIProxy(proxyOpts)
	globalProviderManager.RegisterProvider(openAI)

	anthropic = providers.NewAnthropicProxy(proxyOpts)
	globalProviderManager.RegisterProvider(anthropic)

	gemini = providers.NewGeminiProxy(proxyOpts)
	globalProviderManager.RegisterProvider(gemini)

	if bedrockCfg, ok := yamlConfig.Providers["bedrock"]; ok && bedrockCfg.Enabled {
		bedrock = providers.NewBedrockProxy(proxyOpts)
		globalProviderManager.RegisterProvider(bedrock)
		circuitBreakerProviders = append(circuitBreakerProviders, "bedrock")
		logger.Info("☁️  Bedrock provider: ENABLED", "region", bedrock.Region())
	} else {
		logger.Info("☁️  Bedrock provider: DISABLED (set providers.bedrock.enabled: true to enable)")
	}
	if mantleCfg, ok := yamlConfig.Providers["bedrock-mantle"]; ok && mantleCfg.Enabled {
		mantleOpts := proxyOpts
		mantleOpts.MantleModelProjects = buildMantleModelProjects(mantleCfg)
		mantleOpts.MantleTaskSigV4Auth = strings.EqualFold(strings.TrimSpace(mantleCfg.Auth), config.ProviderAuthTaskSigV4)
		var mantleErr error
		bedrockMantle, mantleErr = providers.NewBedrockMantleProxy(mantleOpts)
		if mantleErr != nil {
			logger.Warn("☁️  Bedrock Mantle provider: UNAVAILABLE (AWS config failed; other providers still registered)", "error", mantleErr)
			bedrockMantle = nil
		} else {
			globalProviderManager.RegisterProvider(bedrockMantle)
			circuitBreakerProviders = append(circuitBreakerProviders, "bedrock-mantle")
			if len(mantleOpts.MantleModelProjects) > 0 {
				logger.Info("☁️  Bedrock Mantle project routing: ENABLED", "models", len(mantleOpts.MantleModelProjects))
			}
			mantleHealth := bedrockMantle.GetHealthStatus()
			logger.Info("☁️  Bedrock Mantle provider: ENABLED", "region", mantleHealth["region"], "anthropic_region", mantleHealth["anthropic_region"], "auth", mantleHealth["auth"])
		}
	} else {
		logger.Info("☁️  Bedrock Mantle provider: DISABLED (set providers.bedrock-mantle.enabled: true to enable)")
	}
	return openAI, anthropic, gemini, bedrock, bedrockMantle
}

func runServer(yamlConfig *config.YAMLConfig, disableGzip bool) {
	// Get port from environment variable or use default
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	// Log configuration
	yamlConfig.LogConfiguration(logger)

	// Create router
	r := mux.NewRouter()

	// Initialize global provider manager
	globalProviderManager = providers.NewProviderManager()

	// Initialize cost tracker
	globalCostTracker = initializeCostTracker(yamlConfig)
	if globalCostTracker != nil {
		globalCostTracker.SetLogger(logger)
	}

	// Initialize API key store if enabled
	globalAPIKeyStore = initializeAPIKeyStore(yamlConfig)
	globalAdminUserStore = initializeAdminUserStore(yamlConfig)

	provRT := provision.RuntimeFromYAML(yamlConfig.Features.APIKeyManagement.Provisioning)
	keyProvisioner, provErr := provision.NewManagerFromRuntime(provRT, logger)
	if provErr != nil {
		logProxyError("Key provisioning disabled: setup failed", "error", provErr)
	} else {
		globalKeyProvisioner = keyProvisioner
		if keyProvisioner.Enabled() {
			logger.Info("Key provisioning: ENABLED")
		}
	}

	// Initialize rate limiter if enabled
	if yamlConfig.Features.RateLimiting.Enabled {
		lim, err := ratelimit.Factory(yamlConfig)
		if err != nil {
			logProxyError("Failed to initialize rate limiter", "error", err)
		} else {
			globalRateLimiter = lim
			globalRateLimitStatsRecorder = ratelimitstats.NewRecorder()
			// Layer dynamic per-key rate-limit overrides (from the API-key
			// record) on top of static YAML overrides, if the backend and
			// store both support it.
			if ov, ok := lim.(ratelimit.PerKeyOverridable); ok {
				if store, ok := globalAPIKeyStore.(*apikeys.Store); ok {
					ov.SetPerKeyOverride(newPerKeyOverrideProvider(store, logger))
					logger.Info("Rate limiting: per-key overrides wired to API-key store")
				}
			}
			logger.Info("Rate limiting: ENABLED",
				"backend", yamlConfig.Features.RateLimiting.Backend,
				"rpm", yamlConfig.Features.RateLimiting.Limits.RequestsPerMinute,
				"tpm", yamlConfig.Features.RateLimiting.Limits.TokensPerMinute,
				"rpd", yamlConfig.Features.RateLimiting.Limits.RequestsPerDay,
				"tpd", yamlConfig.Features.RateLimiting.Limits.TokensPerDay)
		}
	}

	// Initialize circuit breaker
	globalCircuitStore = initializeCircuitStore(yamlConfig)
	if globalCircuitStore != nil {
		if rs, ok := globalCircuitStore.(*circuit.RedisStore); ok && rs.RedisClient() != nil {
			globalCircuitStatsRecorder = circuitstats.NewRedisRecorder(rs.RedisClient(), logger)
			logger.Info("⚡ Circuit activity: Redis-backed (shared across tasks)")
		} else {
			globalCircuitStatsRecorder = circuitstats.NewRecorder()
		}
	}

	openAIProvider, anthropicProvider, geminiProvider, bedrockProvider, bedrockMantleProvider := registerProviders(yamlConfig, disableGzip)

	fakeAllowed := isFakeModeAllowed(yamlConfig)
	fakeCfg := fakeConfigFromYAML(yamlConfig, fakeAllowed)
	if fakeAllowed {
		logProxyWarn(
			"🎭 Fake upstream: ENABLED — synthetic LLM responses, no real provider calls",
			"chaos_failure_rate", yamlConfig.Features.FakeUpstream.ChaosFailureRate,
		)
	} else if yamlConfig.Features.FakeUpstream.Enabled {
		logProxyWarn(
			"🎭 Fake upstream: requested in YAML but LLM_PROXY_ALLOW_FAKE_MODE is not set; fake transport NOT installed",
		)
	}

	type namedProvider struct {
		name string
		p    interface {
			WrapTransport(func(http.RoundTripper) http.RoundTripper)
		}
	}
	namedProviders := []namedProvider{
		{"openai", openAIProvider},
		{"anthropic", anthropicProvider},
		{"gemini", geminiProvider},
	}
	if bedrockProvider != nil {
		namedProviders = append(namedProviders, namedProvider{"bedrock", bedrockProvider})
	}
	if bedrockMantleProvider != nil {
		namedProviders = append(namedProviders, namedProvider{"bedrock-mantle", bedrockMantleProvider})
	}

	if fakeAllowed {
		for _, np := range namedProviders {
			wrapProviderWithFake(np.p, np.name, fakeCfg)
		}
	}

	// Inject circuit-breaking transports when the feature is enabled.
	if globalCircuitStore != nil {
		cbCfg := circuitConfigFromYAML(yamlConfig.Features.CircuitBreaker, isTestModeAllowed(yamlConfig))

		// Build the optional observability sinks once, then share them
		// across every provider's wrapped transport.  Both are nil-safe
		// — circuit.NewTransport falls back to no-op behaviour if either
		// is missing, which keeps test fixtures and Datadog-less
		// deployments working unchanged.
		circuitMetrics := initializeCircuitMetrics(yamlConfig)
		modelFn := circuitModelExtractor(openAIProvider, anthropicProvider, geminiProvider, bedrockProvider, bedrockMantleProvider)
		opts := []circuit.Option{
			circuit.WithModelExtractor(modelFn),
			circuit.WithCallerExtractor(circuitCallerExtractor()),
			circuit.WithMetrics(circuitMetrics),
		}
		if globalCircuitStatsRecorder != nil {
			opts = append(opts, circuit.WithActivityRecorder(globalCircuitStatsRecorder))
		}

		for _, np := range namedProviders {
			wrapProviderWithCircuitBreaker(np.p, globalCircuitStore, cbCfg, np.name, opts...)
		}
		logger.Info(
			"⚡ Circuit Breaker: transports wrapped for all providers",
			"providers", circuitBreakerProviders,
			"metrics_enabled", circuitDatadogConfig(yamlConfig) != nil,
		)
	}

	// Add middleware (order matters for streaming)
	r.Use(middleware.MetaURLRewritingMiddleware(globalProviderManager)) // URL rewriting must happen first

	if yamlConfig.Features.ClientGzip.Enabled {
		r.Use(middleware.ClientGzipMiddleware(globalProviderManager))
		logger.Info("🗜️  Client gzip enabled for non-streaming responses when Accept-Encoding: gzip")
	}

	// API key validation runs before PII redaction so per-key redact_pii
	// overrides can be resolved from the DynamoDB record stashed in context.
	if globalAPIKeyStore != nil {
		r.Use(middleware.APIKeyValidationMiddleware(
			globalProviderManager,
			globalAPIKeyStore,
			yamlConfig.Features.PIIRedact.Enabled,
			yamlConfig.Features.BYOKeys.Enabled,
		))
	}

	globalModelStatusRecorder = modelstatusstats.NewRecorder()
	modelStatusMetrics := initializeCircuitMetrics(yamlConfig)
	r.Use(middleware.ModelStatusMiddleware(globalProviderManager, yamlConfig, globalModelStatusRecorder, modelStatusMetrics))

	if globalCostStatsRecorder != nil && yamlConfig.Features.CostTracking.Enabled {
		costLimitOpts := middleware.CostLimitOptions{
			FailClosedOnReadError: yamlConfig.Features.CostTracking.FailClosedOnReadError,
		}
		// When the cost tracker is available, enable synchronous cluster-wide
		// reservations: estimate a call's cost up front, reserve it atomically
		// against the cap, and reconcile to the actual cost after the response.
		// This stops concurrent / multi-instance requests from overshooting a
		// daily cap in the check-before / charge-after window.
		if globalCostTracker != nil {
			costLimitOpts.Estimate = func(provider, model string, inputTokens, outputTokens int) float64 {
				_, _, total, err := globalCostTracker.CalculateCost(provider, model, inputTokens, outputTokens)
				if err != nil {
					return 0
				}
				return total
			}
			costLimitOpts.Estimation = providers.YAMLConfigEstimationAdapter{
				MaxSampleBytes:        yamlConfig.Features.RateLimiting.Estimation.MaxSampleBytes,
				BytesPerToken:         yamlConfig.Features.RateLimiting.Estimation.BytesPerToken,
				CharsPerToken:         yamlConfig.Features.RateLimiting.Estimation.CharsPerToken,
				ProviderCharsPerToken: yamlConfig.Features.RateLimiting.Estimation.ProviderCharsPerToken,
			}
		}
		r.Use(middleware.CostLimitMiddleware(globalProviderManager, globalCostStatsRecorder, costLimitOpts))
	}

	piiCfg := yamlConfig.Features.PIIRedact
	redactAPICfg := yamlConfig.Features.RedactAPI
	idGateCfg := yamlConfig.Features.IDGate
	needsRedactor := piiCfg.Enabled || piiCfg.AllowPerKeyOverride || redactAPICfg.Enabled || idGateCfg.Enabled
	var redactor *redact.Redactor
	if needsRedactor {
		analyzerURL := piiCfg.AnalyzerURL
		if envURL := os.Getenv("PRESIDIO_ANALYZER_URL"); envURL != "" {
			analyzerURL = envURL
		}

		redactCfg := redact.Config{
			AnalyzerURL:        analyzerURL,
			Timeout:            time.Duration(piiCfg.TimeoutMs) * time.Millisecond,
			ScoreThreshold:     piiCfg.ScoreThreshold,
			EntityTypes:        piiCfg.EntityTypes,
			Language:           piiCfg.Language,
			AllowTestEmails:    piiCfg.AllowTestEmails,
			AnalyzeConcurrency: piiCfg.AnalyzeConcurrency,
		}
		cacheCfg := redact.AnalyzeCacheConfigFromYAML(piiCfg.AnalyzeCache)
		fingerprint := redact.AnalyzeCacheFingerprint(redactCfg)
		analyzeCache, closeCache, cacheErr := redact.NewAnalyzeCache(cacheCfg, fingerprint)
		if cacheErr != nil {
			logProxyError("Failed to construct PII analyze cache; continuing without cache",
				"error", cacheErr)
		} else if analyzeCache != nil {
			redactCfg.AnalyzeCache = analyzeCache
			globalAnalyzeCacheClose = closeCache
		}
		var err error
		redactor, err = redact.New(redactCfg)
		if err != nil {
			logProxyError("Failed to construct PII redactor; redaction features disabled",
				"error", err)
			redactor = nil
		} else {
			redact.SetGlobal(redactor)
			if analyzeCache != nil {
				logger.Info(
					"PII analyze cache enabled",
					"ttl_seconds", int(cacheCfg.TTL.Seconds()),
					"memory", cacheCfg.MemoryEnabled,
					"redis", cacheCfg.RedisEnabled,
				)
			}
		}
	}

	if idGateCfg.Enabled {
		if redactor == nil {
			logProxyError("id_gate enabled but Presidio redactor unavailable; ID gate disabled")
		} else {
			globalIDGateRecorder = idgatestats.NewRecorder()
			ocrURL := idGateCfg.OCRSidecarURL
			if envURL := os.Getenv("OCR_SIDECAR_URL"); envURL != "" {
				ocrURL = envURL
			}
			ocrTimeout := time.Duration(idGateCfg.TimeoutMs) * time.Millisecond
			if ocrTimeout <= 0 {
				ocrTimeout = 30 * time.Second
			}
			ocrClient := ocr.New(ocrURL, ocrTimeout)
			idGateFailClosed := idGateCfg.FailMode == "closed"
			scoreThreshold := idGateCfg.ScoreThreshold
			if scoreThreshold <= 0 {
				scoreThreshold = 0.4
			}
			r.Use(middleware.IDGateMiddleware(ocrClient, redactor, middleware.IDGateConfig{
				FailClosed:       idGateFailClosed,
				MaxBodyBytes:     idGateCfg.MaxBodyBytes,
				MaxImageBytes:    idGateCfg.MaxImageBytes,
				ScoreThreshold:   scoreThreshold,
				EntityTypes:      idGateCfg.EntityTypes,
				ImageConcurrency: idGateCfg.ImageConcurrency,
				Logger:           logger,
				Metrics:          initializeIDGateMetrics(yamlConfig),
				Recorder:         globalIDGateRecorder,
			}))
			logger.Info(
				"🪪  Government ID gate middleware installed",
				"ocr_sidecar_url", ocrURL,
				"fail_mode", idGateCfg.FailMode,
				"score_threshold", scoreThreshold,
			)
		}
	}

	if redactor != nil && (piiCfg.Enabled || piiCfg.AllowPerKeyOverride) {
		failClosed := piiCfg.FailMode == "closed"
		globalPIIRecorder = pii.NewRecorder()
		wirePlaceholders := true
		if piiCfg.WirePlaceholders != nil {
			wirePlaceholders = *piiCfg.WirePlaceholders
		}
		defaultAllowStreaming := true
		if piiCfg.DefaultAllowStreaming != nil {
			defaultAllowStreaming = *piiCfg.DefaultAllowStreaming
		}
		env := strings.ToLower(os.Getenv("ENVIRONMENT"))
		devLogRawEntities := env == "dev" || env == "local"
		r.Use(middleware.PIIRedactMiddleware(redactor, middleware.PIIRedactConfig{
			GlobalEnabled:           piiCfg.Enabled,
			FailClosed:              failClosed,
			MaxBodyBytes:            piiCfg.MaxBodyBytes,
			Logger:                  logger,
			Recorder:                globalPIIRecorder,
			Metrics:                 initializePIIMetrics(yamlConfig),
			WirePlaceholders:        wirePlaceholders,
			DefaultAllowStreaming:   defaultAllowStreaming,
			DevLogRawEntities:       devLogRawEntities,
			AnalyzeTimeout:          time.Duration(piiCfg.TimeoutMs) * time.Millisecond,
			AnalyzeTimeoutPer100KiB: time.Duration(piiCfg.TimeoutMsPer100KB) * time.Millisecond,
			AnalyzeTimeoutMax:       time.Duration(piiCfg.TimeoutMsMax) * time.Millisecond,
		}))
		logger.Info(
			"🛡️  PII redaction middleware installed",
			"global_enabled", piiCfg.Enabled,
			"allow_per_key_override", piiCfg.AllowPerKeyOverride,
			"wire_placeholders", wirePlaceholders,
			"default_allow_streaming", defaultAllowStreaming,
			"dev_log_raw_entities", devLogRawEntities,
			"analyzer_url", piiCfg.AnalyzerURL,
			"fail_mode", piiCfg.FailMode,
			"timeout_ms", piiCfg.TimeoutMs,
			"timeout_ms_per_100kb", piiCfg.TimeoutMsPer100KB,
			"timeout_ms_max", piiCfg.TimeoutMsMax,
			"analyze_concurrency", piiCfg.AnalyzeConcurrency,
		)
	}

	initAdminRollups(yamlConfig)
	initHistory(yamlConfig)

	// Add test-mode middleware when enabled (integration tests only).
	//
	// We require THREE conditions before enabling this middleware, any one
	// of which is sufficient to disable it:
	//   1. The circuit breaker feature itself is Enabled.  Without it, the
	//      test-mode header has nothing to toggle and only adds attack
	//      surface.
	//   2. TestModeEnabled is true in YAML.  This is the explicit opt-in
	//      used by integration test configs.
	//   3. LLM_PROXY_ALLOW_TEST_MODE=1 in the environment.  This belt-and-
	//      suspenders guard ensures a production binary cannot accidentally
	//      honour a test-mode header even if a misconfigured YAML slips
	//      through review.
	testModeAllowed := isTestModeAllowed(yamlConfig)
	if testModeAllowed {
		cbCfg := circuitConfigFromYAML(yamlConfig.Features.CircuitBreaker, testModeAllowed)
		r.Use(middleware.NewTestModeMiddleware(cbCfg.DegradedSignal))
		logProxyWarn("⚡ Circuit Breaker: test-mode middleware ENABLED (not for production)")
	} else if yamlConfig.Features.CircuitBreaker.TestModeEnabled {
		logProxyWarn(
			"⚡ Circuit Breaker: test-mode requested in YAML but one or more guards not satisfied; middleware NOT installed",
			"circuit_breaker_enabled", yamlConfig.Features.CircuitBreaker.Enabled,
			"allow_env_set", os.Getenv("LLM_PROXY_ALLOW_TEST_MODE") == "1",
		)
	}

	r.Use(middleware.LoggingMiddleware(globalProviderManager))
	if globalRateLimiter != nil {
		r.Use(middleware.RateLimitingMiddleware(globalProviderManager, yamlConfig, globalRateLimiter, globalRateLimitStatsRecorder))
	}
	if redactAPICfg.Enabled {
		r.Use(middleware.RedactRateLimitMiddleware(redactAPICfg.RequestsPerMinute))
	}
	r.Use(middleware.CORSMiddleware(globalProviderManager))

	// Create callbacks for cost tracking
	var callbacks []middleware.MetadataCallback

	// Add cost tracking callback if enabled
	if globalCostTracker != nil {
		costTrackingCallback := func(r *http.Request, metadata *providers.LLMResponseMetadata) {
			if metadata.TotalTokens > 0 {
				provider := middleware.GetProviderFromRequest(globalProviderManager, r)
				userID := middleware.ExtractUserIDFromRequest(r, provider)
				ipAddress := middleware.ExtractIPAddressFromRequest(r)
				keyID := ""
				if keyRecord, ok := apikeys.FromContext(r.Context()); ok && keyRecord != nil {
					keyID = middleware.MaskKeyID(keyRecord.PK)
					if store, ok := globalAPIKeyStore.(*apikeys.Store); ok && store != nil {
						if err := store.MarkFirstRequest(r.Context(), keyRecord.PK, time.Now()); err != nil {
							logProxyWarn("Failed to mark first request", "error", err, "key", keyID)
						}
					}
				}
				if err := globalCostTracker.TrackRequest(metadata, userID, ipAddress, r.URL.Path, keyID); err != nil {
					logProxyWarn("Failed to track request cost", "error", err)
				}
			}
		}
		callbacks = append(callbacks, costTrackingCallback)
	}

	r.Use(middleware.TokenParsingMiddleware(globalProviderManager, callbacks...)) // Add token parsing middleware with callbacks
	r.Use(middleware.PIIResponseRestoreMiddleware(globalProviderManager))
	r.Use(middleware.StreamingMiddleware(globalProviderManager))

	// Health check endpoint
	r.HandleFunc("/health", healthHandler).Methods("GET", "HEAD")

	if redactAPICfg.Enabled && redactor != nil {
		var keyLookup redactapi.ProxyKeyLookup
		allowUnauth := redactAPICfg.DevAllowUnauthenticated
		if store, ok := globalAPIKeyStore.(*apikeys.Store); ok && store != nil {
			keyLookup = store
		} else if !allowUnauth {
			logProxyError("redact_api enabled but API key store unavailable — POST /redact not registered")
		}
		if keyLookup != nil || allowUnauth {
			maxBody := redactAPICfg.MaxBodyBytes
			if maxBody <= 0 && piiCfg.MaxBodyBytes > 0 {
				maxBody = piiCfg.MaxBodyBytes
			}
			r.Handle("/redact", redactapi.NewHandler(redactor, keyLookup, redactapi.Config{
				MaxBodyBytes:         maxBody,
				AllowUnauthenticated: allowUnauth,
			}, logger)).Methods(http.MethodPost, http.MethodOptions)
			logger.Info("🔒 POST /redact API enabled",
				"fail_mode", redactAPICfg.FailMode,
				"requests_per_minute", redactAPICfg.RequestsPerMinute,
				"dev_allow_unauthenticated", allowUnauth)
		}
	} else if redactAPICfg.Enabled {
		logProxyError("redact_api enabled but redactor unavailable — POST /redact not registered")
	}

	// robots.txt: keep the admin dashboard and (capability-URL) share pages
	// out of search indexes. This is defense-in-depth for accidental URL
	// leaks, not an access control — share UUIDs are still the real gate.
	r.HandleFunc("/robots.txt", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		io.WriteString(w, "User-agent: *\nDisallow: /admin/\n") //nolint:errcheck
	}).Methods("GET")

	if yamlConfig.Features.AdminDashboard.Enabled {
		// Override the PII-off bypass allowlist from config (falls back to the
		// built-in default when unset) so roster changes don't need a deploy.
		apikeys.SetPIIOffNonBedrockBypassAdmins(yamlConfig.Features.AdminDashboard.PIIOffBypassAdmins)
		var keyStore *apikeys.Store
		if s, ok := globalAPIKeyStore.(*apikeys.Store); ok {
			keyStore = s
		}
		admin.RegisterRoutes(r, admin.Deps{
			Logger:             logger,
			YAMLConfig:         yamlConfig,
			APIKeyStore:        keyStore,
			APIKeyStoreError:   globalAPIKeyStoreInitError,
			UserStore:          globalAdminUserStore,
			UserStoreError:     globalAdminUserStoreError,
			RateLimiter:        globalRateLimiter,
			HealthFunc:         healthHandler,
			PIISummary:         piiSummaryFunc(),
			IDGateSummary:      idGateSummaryFunc(),
			CostSummary:        costSummaryFunc(),
			UsageSummary:       usageSummaryFunc(),
			RateLimitSummary:   rateLimitSummaryFunc(),
			CircuitActivity:    circuitActivitySummaryFunc(),
			ModelStatusSummary: modelStatusSummaryFunc(),
			KeyProvisioner:     globalKeyProvisioner,
			AdminRollupStore:   globalAdminRollupStore,
		})
		logger.Info("Admin dashboard: ENABLED")
	}

	// Register routes for all providers centrally
	for name, provider := range globalProviderManager.GetAllProviders() {
		// Direct provider routes
		r.PathPrefix(fmt.Sprintf("/%s/", name)).Handler(provider.Proxy()).Methods("GET", "POST", "PUT", "DELETE", "OPTIONS")

		// Meta routes with user ID pattern: /meta/{userID}/provider/
		// These are handled by URLRewritingMiddleware which rewrites them to /provider/ before reaching here
		r.PathPrefix(fmt.Sprintf("/meta/{userID}/%s/", name)).Handler(provider.Proxy()).Methods("GET", "POST", "PUT", "DELETE", "OPTIONS")

		logger.Info("Registered provider routes", "provider", name,
			"direct_path", fmt.Sprintf("/%s/", name),
			"meta_path", fmt.Sprintf("/meta/{userID}/%s/", name))
	}

	// Register extra routes for all providers (e.g., compatibility routes)
	for name, provider := range globalProviderManager.GetAllProviders() {
		provider.RegisterExtraRoutes(r)
		logger.Info("Registered extra routes for provider", "provider", name)
	}

	// Start server
	logger.Info("Starting LLM Proxy server", "port", port)

	// Log features
	features := []string{"Streaming support", "CORS", "Request logging", "Token parsing"}
	if globalCostTracker != nil {
		features = append(features, "Cost tracking")
	}
	if globalRateLimiter != nil {
		features = append(features, "Rate limiting")
	}
	if globalCircuitStore != nil {
		features = append(features, "Circuit breaker")
	}
	logger.Info("Features enabled", "features", strings.Join(features, ", "))

	logger.Info("Health check available", "url", "http://0.0.0.0:"+port+"/health")

	// Log cost tracking status
	if globalCostTracker != nil {
		logger.Info("Cost tracking: ENABLED")
	} else {
		logger.Info("Cost tracking: DISABLED")
	}

	// Log registered providers
	for name := range globalProviderManager.GetAllProviders() {
		logger.Info("Registered provider", "provider", name)
	}

	logger.Info("OpenAI API endpoints available", "url", "http://0.0.0.0:"+port+"/openai/")
	logger.Info("Anthropic API endpoints available", "url", "http://0.0.0.0:"+port+"/anthropic/")
	logger.Info("Gemini API endpoints available", "url", "http://0.0.0.0:"+port+"/gemini/")
	if bedrockProvider != nil {
		logger.Info("Bedrock API endpoints available", "url", "http://0.0.0.0:"+port+"/bedrock/", "region", bedrockProvider.Region())
	}
	if bedrockMantleProvider != nil {
		logger.Info("Bedrock Mantle API endpoints available", "url", "http://0.0.0.0:"+port+"/bedrock-mantle/", "region", bedrockMantleProvider.GetHealthStatus()["region"])
	}
	logger.Info("Meta routes with user ID available", "pattern", "http://0.0.0.0:"+port+"/meta/{userID}/{provider}/")

	// Server-level timeouts to bound resource usage and avoid Slowloris-style
	// stalls. WriteTimeout is intentionally generous to accommodate long SSE
	// streams; per-request deadlines are still enforced by upstream provider
	// transports and per-handler context.WithTimeout helpers.
	server := &http.Server{
		Addr:              "0.0.0.0:" + port,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      0, // streaming: rely on per-handler ctx deadlines
		IdleTimeout:       300 * time.Second,
	}

	// Set up graceful shutdown
	go func() {
		logger.Info("🚀 Starting server", "address", "0.0.0.0:"+port)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logProxyError("Server failed to start", "error", err)
		}
	}()

	// Set up signal handling for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	sig := <-sigChan
	logger.Info("🛑 Received shutdown signal", "signal", sig.String())
	gracefulShutdown(server)
}

// closeGlobalHistorySink flushes buffered row history on shutdown.
func closeGlobalHistorySink() error {
	if globalHistorySink == nil {
		return nil
	}
	return globalHistorySink.Close()
}

// gracefulShutdown drains the HTTP server, stops async cost-tracking workers,
// and releases any Redis-backed circuit-breaker resources. Each step is best
// effort — failures are logged and shutdown proceeds — because at this point
// the process is already on its way down.
func gracefulShutdown(server *http.Server) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	logger.Info("🔄 Shutting down HTTP server...")
	if err := server.Shutdown(ctx); err != nil {
		logProxyError("HTTP server shutdown failed", "error", err)
	} else {
		logger.Info("✅ HTTP server shut down successfully")
	}

	if globalCostTracker != nil {
		logger.Info("🔄 Stopping cost tracking workers and flushing queue...")
		globalCostTracker.StopAsyncWorkers()
		logger.Info("✅ Cost tracking workers stopped and queue flushed")
	}

	if globalCostStatsRecorder != nil {
		globalCostStatsRecorder.FlushRollup()
	}
	if globalPIIRecorder != nil {
		globalPIIRecorder.FlushRollup()
	}
	if globalIDGateRecorder != nil {
		globalIDGateRecorder.FlushRollup()
	}
	if globalUsageStatsRecorder != nil {
		globalUsageStatsRecorder.FlushRollup()
	}
	if globalRateLimitStatsRecorder != nil {
		globalRateLimitStatsRecorder.FlushRollup()
	}
	if globalCircuitStatsRecorder != nil {
		globalCircuitStatsRecorder.FlushRollup()
	}
	if globalModelStatusRecorder != nil {
		globalModelStatusRecorder.FlushRollup()
	}
	if globalHistorySink != nil {
		logger.Info("🔄 Flushing history sink...")
		if err := closeGlobalHistorySink(); err != nil {
			logProxyWarn("History sink: close failed", "error", err)
		} else {
			logger.Info("✅ History sink flushed")
		}
	}
	if globalAdminRollupStop != nil {
		close(globalAdminRollupStop)
	}
	if globalAdminRollupStore != nil {
		if err := globalAdminRollupStore.Close(); err != nil {
			logProxyWarn("Admin rollups: Redis close failed", "error", err)
		}
	}
	if globalAnalyzeCacheClose != nil {
		if err := globalAnalyzeCacheClose(); err != nil {
			logProxyWarn("PII analyze cache: Redis close failed", "error", err)
		}
	}

	if rs, ok := globalCircuitStore.(*circuit.RedisStore); ok {
		if err := rs.Close(); err != nil {
			logProxyWarn("Circuit Breaker: Redis close failed", "error", err)
		} else {
			logger.Info("✅ Circuit Breaker: Redis client closed")
		}
	}

	// Release the rate limiter's Redis client (the memory backend is a no-op
	// and does not implement io.Closer). Mirrors the circuit-breaker cleanup.
	if globalRateLimiter != nil {
		if closer, ok := globalRateLimiter.(interface{ Close() error }); ok {
			if err := closer.Close(); err != nil {
				logProxyWarn("Rate limiter: Redis close failed", "error", err)
			} else {
				logger.Info("✅ Rate limiter: Redis client closed")
			}
		}
	}

	logger.Info("👋 Server shutdown complete")
}

func main() {
	// Parse command line flags
	var showVersion bool
	var validateConfig string
	var disableGzip bool
	flag.BoolVar(&showVersion, "version", false, "Show version and configuration, then exit")
	flag.StringVar(&validateConfig, "validate-config", "", "Validate configuration files (comma-separated paths) and exit")
	flag.BoolVar(&disableGzip, "disable-gzip", false, "Strip Accept-Encoding and disable transport compression (forces plain-text upstream bytes; useful for debugging SSE streams)")
	flag.Parse()

	// Handle config validation if requested
	if validateConfig != "" {
		handleConfigValidation(validateConfig)
	}

	// Load environment-based configuration (base.yml + environment-specific config).
	//
	// Outside of explicit local-dev (LLM_PROXY_ALLOW_DEFAULT_CONFIG=1 or
	// ENVIRONMENT=dev) we fail-fast on load errors rather than silently
	// running with the in-binary defaults — a misconfigured staging/prod
	// deploy must be visible at startup, not 15 minutes into traffic.
	yamlConfig, err := config.LoadEnvironmentConfig()
	if err != nil {
		env := os.Getenv("ENVIRONMENT")
		allowDefault := os.Getenv("LLM_PROXY_ALLOW_DEFAULT_CONFIG") == "1" || env == "" || env == "dev" || env == "local"
		if !allowDefault {
			logProxyError("Failed to load environment config; refusing to start with in-binary defaults",
				"error", err, "environment", env,
				"hint", "set LLM_PROXY_ALLOW_DEFAULT_CONFIG=1 to opt in to default config")
			os.Exit(1)
		}
		logProxyWarn("Failed to load environment config, using defaults (dev only)", "error", err)
		yamlConfig = config.GetDefaultYAMLConfig()
	}

	// Handle version flag if requested
	if showVersion {
		handleVersionFlag(yamlConfig)
	}

	// Start the server
	runServer(yamlConfig, disableGzip)
}
