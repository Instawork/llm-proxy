package providers

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
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
	ThoughtTokens int `json:"thought_tokens,omitempty"`

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

	// ValidateAPIKey validates and potentially replaces the API key in the request
	// If the key starts with "iw:", it looks it up in the key store and replaces it with the actual key
	// Returns an error if the key is invalid or disabled
	ValidateAPIKey(req *http.Request, keyStore APIKeyStore) error
}

// APIKeyStore defines the interface for API key storage operations
type APIKeyStore interface {
	// ValidateAndGetActualKey validates a key and returns the actual provider key
	// If the key doesn't start with "iw:", it returns the key as-is
	// Returns (actualKey, provider, error)
	ValidateAndGetActualKey(ctx context.Context, key string) (string, string, error)
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

// DecompressResponseIfNeeded checks if the response is gzip compressed and decompresses it.
// This is a shared utility function that all providers can use to handle gzip-compressed responses
// when DisableCompression is set to true in the transport.
func DecompressResponseIfNeeded(reader io.Reader) (io.Reader, error) {
	// Read the first few bytes to check for gzip magic number
	buffer := make([]byte, 2)
	peekReader := bufio.NewReader(reader)
	n, err := peekReader.Read(buffer)
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("failed to peek at response: %w", err)
	}

	// Check if this looks like gzip (magic number 0x1f, 0x8b)
	if n >= 2 && buffer[0] == 0x1f && buffer[1] == 0x8b {
		log.Printf("🔍 Debug: Detected gzip compressed response, decompressing...")

		// Create a new reader that includes the peeked bytes
		combinedReader := io.MultiReader(bytes.NewReader(buffer[:n]), peekReader)

		// Create gzip reader
		gzipReader, err := gzip.NewReader(combinedReader)
		if err != nil {
			return nil, fmt.Errorf("failed to create gzip reader: %w", err)
		}

		return gzipReader, nil
	}

	// Not gzipped, return the original reader with peeked bytes restored
	return io.MultiReader(bytes.NewReader(buffer[:n]), peekReader), nil
}

// EstimateRequestTokens provides a generic, provider-agnostic estimation of request tokens.
// It avoids reading the full body by default; if the body is small (per config), it samples
// the body to attempt to extract the model and then restores the body for downstream handlers.
// Estimation primarily uses Content-Length divided by BytesPerToken.
type estimationConfig interface {
	// Adapter to decouple from config package to avoid import cycles in tests
	GetMaxSampleBytes() int
	GetBytesPerToken() int
}

// YAMLConfigEstimationAdapter adapts config.EstimationConfig
type YAMLConfigEstimationAdapter struct {
	MaxSampleBytes int
	BytesPerToken  int
}

func (a YAMLConfigEstimationAdapter) GetMaxSampleBytes() int { return a.MaxSampleBytes }
func (a YAMLConfigEstimationAdapter) GetBytesPerToken() int  { return a.BytesPerToken }

// EstimateRequestTokens returns (estimatedTokens, model)
func EstimateRequestTokens(req *http.Request, cfg estimationConfig) (int, string) {
	if cfg == nil {
		return 0, ""
	}
	bytesPerToken := cfg.GetBytesPerToken()
	if bytesPerToken <= 0 {
		bytesPerToken = 4
	}

	var model string
	// Primary: use Content-Length
	est := 0
	if req.ContentLength > 0 {
		est = int(req.ContentLength) / bytesPerToken
	}

	// Optional sampling: only if small and JSON
	maxSample := cfg.GetMaxSampleBytes()
	if maxSample >= 0 {
		ct := req.Header.Get("Content-Type")
		if strings.Contains(ct, "application/json") {
			if req.ContentLength >= 0 && req.ContentLength <= int64(maxSample) {
				// Read and restore body
				buf := &bytes.Buffer{}
				if req.Body != nil {
					_, _ = io.CopyN(buf, req.Body, int64(maxSample))
				}
				// Restore body for downstream
				req.Body = io.NopCloser(bytes.NewReader(buf.Bytes()))
				// Try to parse minimal JSON to extract model
				var tmp map[string]interface{}
				if err := json.Unmarshal(buf.Bytes(), &tmp); err == nil {
					if mv, ok := tmp["model"].(string); ok {
						model = mv
					}
				}
				// If no Content-Length, estimate from buffer length
				if est == 0 && buf.Len() > 0 {
					est = buf.Len() / bytesPerToken
				}
			}
		}
	}
	if est < 0 {
		est = 0
	}
	return est, model
}
