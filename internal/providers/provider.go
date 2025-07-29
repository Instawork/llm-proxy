package providers

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gorilla/mux"
)

// LLMResponseMetadata represents standardized response metadata across all providers
type LLMResponseMetadata struct {
	// Model used for the request
	Model string `json:"model"`

	// Token usage information
	InputTokens   int `json:"input_tokens"`
	OutputTokens  int `json:"output_tokens"`
	TotalTokens   int `json:"total_tokens"`
	ThoughtTokens int `json:"thought_tokens,omitempty"` // Added for Gemini thought tokens

	// Provider-specific information
	Provider  string `json:"provider"`
	RequestID string `json:"request_id,omitempty"`

	// Additional metadata for cost calculation
	IsStreaming  bool   `json:"is_streaming"`
	FinishReason string `json:"finish_reason,omitempty"`
}

// Provider defines the interface that all LLM providers must implement
type Provider interface {
	// GetName returns the name of the provider (e.g., "openai", "anthropic")
	GetName() string

	// IsStreamingRequest checks if the given request is a streaming request
	// This is provider-specific as different providers handle streaming differently
	IsStreamingRequest(req *http.Request) bool

	// ParseResponseMetadata extracts tokens and model information from a response
	// Works for both streaming and non-streaming responses
	ParseResponseMetadata(responseBody io.Reader, isStreaming bool) (*LLMResponseMetadata, error)

	// Proxy returns the HTTP handler for this provider (typically a reverse proxy)
	Proxy() http.Handler

	// GetHealthStatus returns the health status of the provider
	GetHealthStatus() map[string]interface{}

	// UserIDFromRequest extracts user ID from request body in a provider-specific way
	// Returns empty string if no user ID can be extracted
	UserIDFromRequest(req *http.Request) string

	// RegisterExtraRoutes allows providers to register additional routes beyond the standard ones
	// This is useful for provider-specific compatibility routes or special endpoints
	RegisterExtraRoutes(router *mux.Router)
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

// IsValidProvider checks if the given provider name is registered
func (pm *ProviderManager) IsValidProvider(name string) bool {
	_, exists := pm.providers[name]
	return exists
}

// CreateGenericDirector creates a generic director function for reverse proxy requests
// This eliminates code duplication across all providers by handling the common logic:
// - Setting the target host header
// - Stripping the provider prefix from the path
// - Logging the request with streaming detection
func CreateGenericDirector(provider Provider, targetURL *url.URL, originalDirector func(*http.Request)) func(*http.Request) {
	return func(req *http.Request) {
		// Call the original director first
		originalDirector(req)

		// Set the Host header to the target host
		req.Host = targetURL.Host

		// Strip the provider prefix from the path before forwarding
		// Note: mux PathPrefix matches but doesn't strip the prefix automatically
		// Note: URL rewriting for /meta/{userID}/provider/ is handled by URLRewritingMiddleware
		providerPrefix := "/" + provider.GetName()
		req.URL.Path = strings.TrimPrefix(req.URL.Path, providerPrefix)

		// Log the request, including streaming detection
		isStreaming := provider.IsStreamingRequest(req)
		if isStreaming {
			log.Printf("Proxying %s streaming request: %s %s", provider.GetName(), req.Method, req.URL.Path)
		} else {
			log.Printf("Proxying %s request: %s %s", provider.GetName(), req.Method, req.URL.Path)
		}
	}
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
