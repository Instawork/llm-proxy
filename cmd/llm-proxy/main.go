package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/cost"
	"github.com/Instawork/llm-proxy/internal/middleware"
	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/Instawork/llm-proxy/internal/ratelimit"
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
	
	// Build the message with key context information
	message := r.Message
	var contextParts []string
	
	r.Attrs(func(a slog.Attr) bool {
		// Include important context attributes
		switch a.Key {
		case "provider", "model", "user", "error", "count", "status":
			contextParts = append(contextParts, fmt.Sprintf("%s=%v", a.Key, a.Value))
		}
		return true
	})
	
	if len(contextParts) > 0 {
		message = fmt.Sprintf("%s (%s)", message, strings.Join(contextParts, ", "))
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

const (
	// Default port for the proxy server
	defaultPort = "9002"
)

// Global provider manager instance
var globalProviderManager *providers.ProviderManager

// Global rate limiter instances
var globalRateLimiter ratelimit.RateLimiter
var globalRateLimitMiddleware *ratelimit.RateLimitMiddleware

// Global cost tracker instance
var globalCostTracker *cost.CostTracker

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
		// JSON format for production/machine parsing
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level: level,
		})
	} else {
		// Custom pretty format for local development (default)
		handler = NewCustomPrettyHandler(os.Stderr, level)
	}
	
	logger = slog.New(handler)
}

// convertYAMLToRateLimitProviderConfig converts YAML config to rate limiter provider config format
func convertYAMLToRateLimitProviderConfig(yamlConfig *config.YAMLConfig) ratelimit.ProviderConfigMap {
	rateLimitProviderConfig := make(ratelimit.ProviderConfigMap)
	
	for providerName, providerConfig := range yamlConfig.Providers {
		if !providerConfig.Enabled {
			continue
		}
		
		models := make(map[string]ratelimit.ModelConfig)
		for modelName, modelConfig := range providerConfig.Models {
			models[modelName] = ratelimit.ModelConfig{
				Enabled: modelConfig.Enabled,
				Aliases: modelConfig.Aliases,
			}
		}
		
		rateLimitProviderConfig[providerName] = ratelimit.ProviderConfig{
			Models: models,
		}
	}
	
	return rateLimitProviderConfig
}

// initializeCostTracker creates and configures the cost tracker with pricing data from config
func initializeCostTracker(yamlConfig *config.YAMLConfig) *cost.CostTracker {
	// Determine output file location
	outputFile := os.Getenv("COST_TRACKING_FILE")
	if outputFile == "" {
		outputFile = "logs/cost-tracking.jsonl" // Default location
	}
	
	// Create cost tracker
	costTracker := cost.NewFileBasedCostTracker(outputFile)
	logger.Info("💰 Cost Tracker: Initialized", "output_file", outputFile)
	
	// Load pricing data from config for each provider and model
	if yamlConfig.Features.CostTracking.Enabled {
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
						logger.Warn("Could not parse pricing", "provider", providerName, "model", modelName)
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
					logger.Warn("Model has no pricing configured", "provider", providerName, "model", modelName)
				}
			}
		}
		
		logger.Info("💰 Cost Tracker: Configured pricing", "total_models_configured", totalModelsConfigured)
	} else {
		logger.Info("💰 Cost Tracker: Cost tracking is disabled in config")
		return nil // Return nil if cost tracking is disabled
	}
	
	return costTracker
}

// healthHandler provides a simple health check endpoint
func healthHandler(w http.ResponseWriter, r *http.Request) {
	health := map[string]interface{}{
		"status":      "healthy",
		"timestamp":   time.Now().Unix(),
		"providers":   globalProviderManager.GetHealthStatus(),
		"features": map[string]bool{
			"rate_limiting": globalRateLimitMiddleware != nil && globalRateLimitMiddleware.IsEnabled(),
			"cost_tracking": globalCostTracker != nil,
		},
	}
	
	// Add cost tracking stats if available
	if globalCostTracker != nil {
		if stats, err := globalCostTracker.GetStats(time.Now().Add(-24 * time.Hour)); err == nil {
			health["cost_stats_24h"] = stats
		}
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}

func main() {
	// Get port from environment variable or use default
	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	// Load YAML configuration
	yamlConfig, err := config.LoadYAMLConfig("configs/config.yml")
	if err != nil {
		logger.Warn("Failed to load YAML config, using defaults", "error", err)
		yamlConfig = config.GetDefaultYAMLConfig()
	}

	// Log configuration
	yamlConfig.LogConfiguration(logger)

	// Create router
	r := mux.NewRouter()

	// Initialize global provider manager
	globalProviderManager = providers.NewProviderManager()

	// Create rate limiter configuration
	rateLimitConfig := &ratelimit.Config{
		Enabled: false, // Will be set based on YAML config
		Rules:   make([]*ratelimit.RateLimitRule, 0),
	}

	// Only configure rate limiting if enabled in YAML config
	if yamlConfig.Enabled && yamlConfig.Features.RateLimiting.Enabled {
		yamlRules := yamlConfig.ConvertToRateLimitRules()
		logger.Info("Generated rate limiting rules from YAML config", "count", len(yamlRules))
		
		// Enable rate limiting since we're adding rules from YAML
		rateLimitConfig.Enabled = true
		
		// Add YAML rules to the rate limit config, replacing existing rules with same names
		for _, yamlRule := range yamlRules {
			// Remove any existing rule with the same name
			rateLimitConfig.RemoveRule(yamlRule.Name)
			// Add the new YAML-based rule
			if err := rateLimitConfig.AddRule(yamlRule); err != nil {
				logger.Warn("Failed to add YAML rule", "rule_name", yamlRule.Name, "error", err)
			} else {
				logger.Info("Added rate limit rule", "name", yamlRule.Name, "description", yamlRule.Description)
			}
		}
		
		// Sort rules by priority
		rateLimitConfig.SortRulesByPriority()
	} else if yamlConfig.Enabled && !yamlConfig.Features.RateLimiting.Enabled {
		logger.Info("Rate limiting is disabled in YAML config features")
		rateLimitConfig.Enabled = false
	}
	
	rateLimitStore := ratelimit.NewMemoryStore()
	
	// Convert YAML provider config to rate limiter provider config for alias resolution
	rateLimitProviderConfig := convertYAMLToRateLimitProviderConfig(yamlConfig)
	
	globalRateLimiter = ratelimit.NewTokenBucketRateLimiterWithProviders(rateLimitConfig, rateLimitStore, rateLimitProviderConfig)
	globalRateLimitMiddleware = ratelimit.NewRateLimitMiddlewareWithConfig(globalRateLimiter, globalProviderManager, rateLimitConfig.Enabled, "X-Rate-Limit-Bypass", &ratelimit.DefaultResponseHandler{})
	
	// Initialize cost tracker
	globalCostTracker = initializeCostTracker(yamlConfig)
	if globalCostTracker != nil {
		globalCostTracker.SetLogger(logger)
	}
	
	// Register providers
	openAIProvider := providers.NewOpenAIProxy()
	globalProviderManager.RegisterProvider(openAIProvider)

	anthropicProvider := providers.NewAnthropicProxy()
	globalProviderManager.RegisterProvider(anthropicProvider)

	geminiProvider := providers.NewGeminiProxy()
	globalProviderManager.RegisterProvider(geminiProvider)

	// Add middleware (order matters for streaming)
	r.Use(middleware.LoggingMiddleware(globalProviderManager))
	r.Use(middleware.CORSMiddleware(globalProviderManager))
	r.Use(globalRateLimitMiddleware.Middleware())  // Add rate limiting before token parsing

	// Create callbacks for rate limiting and cost tracking
	var callbacks []middleware.MetadataCallback

	// Add rate limiting callback if enabled
	if globalRateLimitMiddleware != nil && globalRateLimitMiddleware.IsEnabled() {
		rateLimitCallback := func(r *http.Request, metadata *providers.LLMResponseMetadata) {
			globalRateLimitMiddleware.UpdateUsageFromMetadata(r, metadata)
		}
		callbacks = append(callbacks, rateLimitCallback)
	}

	// Add cost tracking callback if enabled
	if globalCostTracker != nil {
		costTrackingCallback := func(r *http.Request, metadata *providers.LLMResponseMetadata) {
			if metadata.TotalTokens > 0 {
				userID := middleware.ExtractUserIDFromRequest(r)
				ipAddress := middleware.ExtractIPAddressFromRequest(r)
				if err := globalCostTracker.TrackRequest(metadata, userID, ipAddress, r.URL.Path); err != nil {
					logger.Warn("Failed to track request cost", "error", err)
				}
			}
		}
		callbacks = append(callbacks, costTrackingCallback)
	}

	r.Use(middleware.TokenParsingMiddleware(globalProviderManager, callbacks...)) // Add token parsing middleware with callbacks
	r.Use(middleware.StreamingMiddleware(globalProviderManager))

	// Health check endpoint
	r.HandleFunc("/health", healthHandler).Methods("GET")

	// Register routes for all providers
	for _, provider := range globalProviderManager.GetAllProviders() {
		provider.RegisterRoutes(r)
	}

	// Start server
	logger.Info("Starting LLM Proxy server", "port", port)
	
	// Log features
	features := []string{"Streaming support", "CORS", "Request logging", "Token parsing"}
	if globalRateLimitMiddleware != nil && globalRateLimitMiddleware.IsEnabled() {
		features = append(features, "Rate limiting")
	}
	if globalCostTracker != nil {
		features = append(features, "Cost tracking")
	}
	logger.Info("Features enabled", "features", strings.Join(features, ", "))
	
	logger.Info("Health check available", "url", "http://localhost:"+port+"/health")
	
	// Log rate limiting status
	if globalRateLimitMiddleware != nil && globalRateLimitMiddleware.IsEnabled() {
		logger.Info("Rate limiting: ENABLED")
	} else {
		logger.Info("Rate limiting: DISABLED")
	}
	
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
	
	logger.Info("OpenAI API endpoints available", "url", "http://localhost:"+port+"/openai/")
	logger.Info("Anthropic API endpoints available", "url", "http://localhost:"+port+"/anthropic/")
	logger.Info("Gemini API endpoints available", "url", "http://localhost:"+port+"/gemini/")
	
	server := &http.Server{
		Addr:    "0.0.0.0:" + port,
		Handler: r,
	}

	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logger.Error("Server failed to start", "error", err)
	}
}
