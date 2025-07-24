package providers

import (
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
)

// RateLimitInfo represents rate limit information extracted from response headers
type RateLimitInfo struct {
	// Requests per minute/day limits
	RequestLimit     int           `json:"request_limit,omitempty"`
	RequestRemaining int           `json:"request_remaining,omitempty"`
	RequestReset     time.Duration `json:"request_reset,omitempty"`
	
	// Token limits (general)
	TokenLimit       int           `json:"token_limit,omitempty"`
	TokenRemaining   int           `json:"token_remaining,omitempty"`
	TokenReset       time.Duration `json:"token_reset,omitempty"`
	
	// Input token limits (Anthropic specific)
	InputTokenLimit     int           `json:"input_token_limit,omitempty"`
	InputTokenRemaining int           `json:"input_token_remaining,omitempty"`
	InputTokenReset     time.Duration `json:"input_token_reset,omitempty"`
	
	// Output token limits (Anthropic specific)
	OutputTokenLimit     int           `json:"output_token_limit,omitempty"`
	OutputTokenRemaining int           `json:"output_token_remaining,omitempty"`
	OutputTokenReset     time.Duration `json:"output_token_reset,omitempty"`
	
	// Provider name
	Provider string `json:"provider"`
	
	// Whether any rate limit info was found
	HasRateLimitInfo bool `json:"has_rate_limit_info"`
}

// LLMResponseMetadata represents standardized response metadata across all providers
type LLMResponseMetadata struct {
	// Model used for the request
	Model string `json:"model"`
	
	// Token usage information
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	TotalTokens     int `json:"total_tokens"`
	ThoughtTokens   int `json:"thought_tokens,omitempty"`  // Added for Gemini thought tokens
	
	// Provider-specific information
	Provider        string `json:"provider"`
	RequestID       string `json:"request_id,omitempty"`
	
	// Additional metadata for cost calculation and rate limiting
	IsStreaming     bool   `json:"is_streaming"`
	FinishReason    string `json:"finish_reason,omitempty"`
}

// Provider defines the interface that all LLM providers must implement
type Provider interface {
	// GetName returns the name of the provider (e.g., "openai", "anthropic")
	GetName() string
	
	// IsStreamingRequest checks if the given request is a streaming request
	// This is provider-specific as different providers handle streaming differently
	IsStreamingRequest(req *http.Request) bool
	
	// EstimateTokensFromRequest estimates the number of tokens for a request
	// This allows each provider to implement their own token estimation logic
	EstimateTokensFromRequest(req *http.Request) int64
	
	// ParseResponseMetadata extracts tokens and model information from a response
	// Works for both streaming and non-streaming responses
	ParseResponseMetadata(responseBody io.Reader, isStreaming bool) (*LLMResponseMetadata, error)
	
	// ParseRateLimitFromResponse extracts rate limit information from response headers
	// Returns nil if the provider doesn't support rate limit headers
	ParseRateLimitFromResponse(resp *http.Response) *RateLimitInfo
	
	// RegisterRoutes registers the provider's routes with the given router
	RegisterRoutes(r *mux.Router)
	
	// GetHealthStatus returns the health status of the provider
	GetHealthStatus() map[string]interface{}
	
	// UserIDFromRequest extracts user ID from request body in a provider-specific way
	// Returns empty string if no user ID can be extracted
	UserIDFromRequest(req *http.Request) string
}

// ProviderManager manages multiple providers
type ProviderManager struct {
	providers map[string]Provider
}

// NewProviderManager creates a new provider manager
func NewProviderManager() *ProviderManager {
	return &ProviderManager{
		providers: make(map[string]Provider),
	}
}

// RegisterProvider registers a new provider
func (pm *ProviderManager) RegisterProvider(provider Provider) {
	pm.providers[provider.GetName()] = provider
}

// GetProvider returns a provider by name
func (pm *ProviderManager) GetProvider(name string) Provider {
	return pm.providers[name]
}

// GetAllProviders returns all registered providers
func (pm *ProviderManager) GetAllProviders() map[string]Provider {
	return pm.providers
}

// IsStreamingRequest checks if the request is streaming for any provider
func (pm *ProviderManager) IsStreamingRequest(req *http.Request) bool {
	for _, provider := range pm.providers {
		if provider.IsStreamingRequest(req) {
			return true
		}
	}
	return false
}

// GetHealthStatus returns the health status of all providers
func (pm *ProviderManager) GetHealthStatus() map[string]interface{} {
	status := make(map[string]interface{})
	for name, provider := range pm.providers {
		status[name] = provider.GetHealthStatus()
	}
	return status
}

// Helper functions for parsing rate limit values

// parseDurationFromString parses duration strings like "1s", "6m0s", etc.
func parseDurationFromString(durationStr string) time.Duration {
	if durationStr == "" {
		return 0
	}
	
	duration, err := time.ParseDuration(durationStr)
	if err != nil {
		// Try parsing as seconds if it's just a number
		if seconds, err2 := strconv.Atoi(durationStr); err2 == nil {
			return time.Duration(seconds) * time.Second
		}
		return 0
	}
	return duration
}

// parseIntFromString parses integer strings, returns 0 if invalid
func parseIntFromString(intStr string) int {
	if intStr == "" {
		return 0
	}
	
	value, err := strconv.Atoi(intStr)
	if err != nil {
		return 0
	}
	return value
} 

// newProxyTransport creates a new http.Transport with optimized settings for proxying LLM requests.
func newProxyTransport() *http.Transport {
	// These settings are based on http.DefaultTransport, but customized for the proxy.
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100, // Each provider gets its own transport, so this is 100 idle connections per provider.
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		// A generous timeout for the response header, as some LLM providers
		// may take a while to start streaming a response.
		ResponseHeaderTimeout: 3 * time.Minute,
		// Disable compression to let the client handle it and to avoid buffering
		// issues with streaming responses.
		DisableCompression: true,
	}
} 
