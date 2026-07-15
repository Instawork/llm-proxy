package providers

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
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
// LLMResponseMetadata is the canonical post-response metadata snapshot
// that every provider implementation populates and the cost tracker
// consumes. Field-level provider coverage:
//
//	Field                       openai  anthropic  gemini  bedrock
//	------------------------------------------------------------
//	Model                       Y       Y          Y       Y
//	InputTokens                 Y       Y          Y       Y
//	OutputTokens                Y       Y          Y       Y
//	TotalTokens                 Y       Y          Y       Y
//	ThoughtTokens               -       extended*  Y       extended*
//	CacheReadInputTokens        Y       Y          -       Y (Converse)
//	CacheCreationInputTokens    -       Y          -       Y (Converse)
//	FinishReason                Y       Y          Y       Y
//	RequestID                   Y       Y          Y       -
//
// * Anthropic and Bedrock-Anthropic surface thinking tokens only when
// `extended_thinking` is enabled on the request; otherwise the field
// is zero. Gemini always populates ThoughtTokens for 2.0-pro+.
//
// Providers that do not surface a field MUST leave it at the zero value
// — never invent or estimate — so the cost tracker can treat a zero
// cache/thought field as "explicitly unsupported" instead of "missed
// during parse".
type LLMResponseMetadata struct {
	// Model used for the request
	Model string `json:"model"`

	// Token usage information
	InputTokens   int `json:"input_tokens"`
	OutputTokens  int `json:"output_tokens"`
	TotalTokens   int `json:"total_tokens"`
	ThoughtTokens int `json:"thought_tokens,omitempty"`

	// Prompt-cache token breakdown. See the provider-coverage table on
	// the type docstring for which providers populate these.
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`

	// Provider-specific information
	Provider  string `json:"provider"`
	RequestID string `json:"request_id,omitempty"`

	// Additional metadata for cost calculation
	IsStreaming  bool   `json:"is_streaming"`
	FinishReason string `json:"finish_reason,omitempty"`

	// Latency measurements (populated by the proxy middleware)
	// TTFBMS is the time from the proxy receiving the request to writing the first
	// response byte back to the caller (time-to-first-byte, in milliseconds).
	TTFBMS int64 `json:"ttfb_ms,omitempty"`
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

	// ExtractRequestModelAndMessages extracts the model and textual message content from a request body
	// for the purpose of token estimation by character count. Implementations must restore req.Body
	// if they read from it, to avoid impacting downstream handlers.
	// Return values: (model, messages)
	ExtractRequestModelAndMessages(req *http.Request) (string, []string)
}

// APIKeyStore defines the interface for API key storage operations
type APIKeyStore interface {
	// ValidateAndGetActualKey validates a key and returns the actual provider key
	// If the key doesn't start with "iw:", it returns the key as-is
	// Returns (actualKey, provider, error)
	ValidateAndGetActualKey(ctx context.Context, key string) (string, string, error)
}

// ProxyOptions configures optional behaviour for provider proxy constructors.
// All fields default to their zero value (false, 0, "") which corresponds to
// the recommended production setting.
type ProxyOptions struct {
	// DisableGzip strips Accept-Encoding from upstream requests and sets
	// DisableCompression on the transport, forcing uncompressed wire bytes.
	// Useful for debugging SSE streams where you want plain-text event data
	// visible in logs.  Defaults to false (gzip enabled).
	DisableGzip bool

	// MantleModelProjects maps a Bedrock Mantle model id (or alias) to the
	// Bedrock project id that should handle it. When a request's model matches,
	// the Mantle proxy sets the OpenAI-Project header so Mantle resolves
	// data-retention (and other project-scoped policy) against that project
	// instead of the account default. Consumed only by the Bedrock Mantle
	// proxy; nil/empty leaves the account-level policy in force.
	MantleModelProjects map[string]string

	// MantleAnthropicRegion overrides the AWS region used for Bedrock Mantle
	// `/anthropic/v1/messages` traffic. Claude Mantle SKUs are provisioned in a
	// different region than the OpenAI SKUs, so Anthropic requests are
	// retargeted (host + SigV4) here. Empty falls back to the proxy's default
	// region (public constructor: BEDROCK_MANTLE_ANTHROPIC_REGION env or
	// us-east-1). Consumed only by the Bedrock Mantle proxy.
	MantleAnthropicRegion string

	// MantleTaskSigV4Auth allows Bedrock Mantle callers with no iw-* key (or a
	// non-proxy placeholder) when the sidecar trusts the local network and
	// authenticates upstream with the task role. Standalone proxies leave this
	// false so Mantle still requires a registered Bedrock-family proxy key.
	MantleTaskSigV4Auth bool
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

// GetAllProviders returns a defensive copy of the registered providers map.
// Returning the internal map directly would let callers mutate the
// registration set (e.g. tests that delete providers) and observe partial
// state in IsStreamingRequest / ProviderForRequest.
func (pm *ProviderManager) GetAllProviders() map[string]Provider {
	out := make(map[string]Provider, len(pm.providers))
	for k, v := range pm.providers {
		out[k] = v
	}
	return out
}

// IsStreamingRequest checks if the request is streaming for the provider that
// owns its path. Crucially, this does NOT iterate every registered provider
// with an OR pattern: a /health request carrying Accept: text/event-stream
// would otherwise match anthropic.IsStreamingRequest (which only inspects the
// Accept header) and silently flip the request into the streaming wrapper.
// We resolve the owning provider from the request path first; if no provider
// claims the request, streaming is false.
func (pm *ProviderManager) IsStreamingRequest(req *http.Request) bool {
	p := pm.ProviderForRequest(req)
	if p == nil {
		return false
	}
	return p.IsStreamingRequest(req)
}

// ProviderForRequest returns the Provider responsible for handling req based
// on its URL path, or nil when no registered provider claims it. Match is by
// the canonical "/<provider-name>/" prefix; Gemini's compatibility routes
// (/v1beta/models/gemini..., /v1/models/gemini...) and Bedrock's SigV4
// passthrough route (/model/...) are recognized via their respective entries.
func (pm *ProviderManager) ProviderForRequest(req *http.Request) Provider {
	if req == nil || req.URL == nil {
		return nil
	}
	path := req.URL.Path
	for name, provider := range pm.providers {
		if strings.HasPrefix(path, "/"+name+"/") {
			return provider
		}
	}
	// Gemini compatibility routes.
	if g, ok := pm.providers["gemini"]; ok {
		if strings.HasPrefix(path, "/v1beta/models/gemini") || strings.HasPrefix(path, "/v1/models/gemini") {
			return g
		}
	}
	// Bedrock SigV4 passthrough route (the client signed against /model/...,
	// so the proxy mounts that root in addition to /bedrock/).
	if b, ok := pm.providers["bedrock"]; ok {
		if strings.HasPrefix(path, "/model/") {
			return b
		}
	}
	return nil
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

// CreateGenericDirector creates a generic director function for reverse proxy requests.
// This eliminates code duplication across all providers by handling the common logic:
//   - Setting the target host header
//   - Stripping the provider prefix from the path
//   - Optionally stripping client-supplied Accept-Encoding (when disableGzip=true) so
//     upstream returns uncompressed responses. Useful for debugging SSE streams where
//     plain-text event data is easier to inspect in logs. By default gzip is allowed.
//   - Logging the request with streaming detection
func CreateGenericDirector(provider Provider, targetURL *url.URL, originalDirector func(*http.Request), disableGzip bool) func(*http.Request) {
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

		// When gzip is disabled, force upstream to send uncompressed bytes.
		// Without this, a client that sets Accept-Encoding: gzip causes Go's
		// transport to pass that header through and return raw gzipped bytes
		// (DisableCompression only suppresses gzip when the client did NOT request it).
		if disableGzip {
			req.Header.Del("Accept-Encoding")
		}

		// Log the request, including streaming detection
		isStreaming := provider.IsStreamingRequest(req)
		if isStreaming {
			log.Printf("Proxying %s streaming request: %s %s", provider.GetName(), req.Method, req.URL.Path)
		} else {
			log.Printf("Proxying %s request: %s %s", provider.GetName(), req.Method, req.URL.Path)
		}
	}
}

// newProxyTransport creates a new http.Transport with optimized settings for
// proxying LLM requests.  When disableGzip is true, DisableCompression is set
// so Go's transport never auto-decompresses upstream gzip responses — useful
// alongside Accept-Encoding stripping in CreateGenericDirector for debug builds.
// Defaults to false (gzip allowed).
func newProxyTransport(disableGzip bool) *http.Transport {
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
		DisableCompression:    disableGzip,
	}
}

// newGeminiProxyTransport creates a transport specifically for Gemini requests.
// Gemini models (especially larger ones) can take significantly longer to start
// returning response headers than other providers, so we use a longer
// ResponseHeaderTimeout to avoid premature 502s that the google-genai client
// would then retry multiple times, leading to very long total request durations.
func newGeminiProxyTransport(disableGzip bool) *http.Transport {
	t := newProxyTransport(disableGzip)
	t.ResponseHeaderTimeout = 5 * time.Minute
	return t
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

// Estimation primarily uses Content-Length divided by BytesPerToken.
type estimationConfig interface {
	// Adapter to decouple from config package to avoid import cycles in tests
	GetMaxSampleBytes() int
	GetBytesPerToken() int
	GetCharsPerToken() int
	GetProviderCharsPerToken(provider string) int
}

// YAMLConfigEstimationAdapter adapts config.EstimationConfig
type YAMLConfigEstimationAdapter struct {
	MaxSampleBytes        int
	BytesPerToken         int
	CharsPerToken         int
	ProviderCharsPerToken map[string]int
}

func (a YAMLConfigEstimationAdapter) GetMaxSampleBytes() int { return a.MaxSampleBytes }
func (a YAMLConfigEstimationAdapter) GetBytesPerToken() int  { return a.BytesPerToken }
func (a YAMLConfigEstimationAdapter) GetCharsPerToken() int  { return a.CharsPerToken }
func (a YAMLConfigEstimationAdapter) GetProviderCharsPerToken(provider string) int {
	if a.ProviderCharsPerToken == nil {
		return 0
	}
	if v, ok := a.ProviderCharsPerToken[provider]; ok {
		return v
	}
	return 0
}

// EstimateRequestTokens returns (estimatedTokens, model)
// It primarily uses Content-Length divided by BytesPerToken. When the request is small enough
// (<= GetMaxSampleBytes) and is JSON, it will delegate to the provider to extract messages and
// estimate tokens by character count of those messages.
func EstimateRequestTokens(req *http.Request, cfg estimationConfig, provider Provider) (int, string) {
	if cfg == nil {
		return 0, ""
	}

	// Bytes-per-token used for Content-Length based estimation
	bytesPerToken := cfg.GetBytesPerToken()
	if bytesPerToken <= 0 {
		bytesPerToken = 4
	}

	var model string
	// Primary: use Content-Length
	est := 0
	estSource := "none"
	if req.ContentLength > 0 {
		est = int(req.ContentLength) / bytesPerToken
		estSource = "content-length"
	}

	// Optional sampling: only if small and JSON
	maxSample := cfg.GetMaxSampleBytes()
	bodyTooLarge := false
	if maxSample >= 0 {
		ct := req.Header.Get("Content-Type")
		if strings.Contains(ct, "application/json") {
			if req.ContentLength >= 0 && req.ContentLength <= int64(maxSample) {
				if provider != nil {
					provModel, messages := provider.ExtractRequestModelAndMessages(req)
					if provModel != "" {
						model = provModel
					}
					if len(messages) > 0 {
						// chars-per-token for character-count based estimation, with provider override if present
						charsPerToken := cfg.GetCharsPerToken()
						if cpt := cfg.GetProviderCharsPerToken(provider.GetName()); cpt > 0 {
							charsPerToken = cpt
						}
						if charsPerToken <= 0 {
							charsPerToken = 4
						}

						totalChars := 0
						for _, m := range messages {
							totalChars += len([]rune(m))
						}
						msgEst := totalChars / charsPerToken
						if msgEst > 0 {
							est = msgEst
							estSource = "messages"
						}
					}
				}
			} else if req.ContentLength > int64(maxSample) {
				bodyTooLarge = true
			}
		}
	}
	if est < 0 {
		est = 0
	}
	// Single, accurate log line. Includes whether the body was too large to
	// sample (which is why "model" can be empty for big requests).
	if bodyTooLarge {
		log.Printf("🔍 Estimated tokens=%d model=%q source=%s (body %d bytes > max_sample %d, skipped body parse)",
			est, model, estSource, req.ContentLength, maxSample)
	} else {
		log.Printf("🔍 Estimated tokens=%d model=%q source=%s", est, model, estSource)
	}
	return est, model
}
