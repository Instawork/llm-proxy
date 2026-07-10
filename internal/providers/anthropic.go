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
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/Instawork/llm-proxy/internal/redact"
	"github.com/gorilla/mux"
)

const (
	// Anthropic API base URL
	anthropicBaseURL = "https://api.anthropic.com"
)

// AnthropicProxy handles Anthropic API requests and implements the Provider interface
type AnthropicProxy struct {
	proxy *httputil.ReverseProxy
}

// NewAnthropicProxy creates a new Anthropic reverse proxy.
// Pass a ProxyOptions value to override defaults (e.g. DisableGzip: true for
// debug builds that need plain-text SSE wire bytes).
func NewAnthropicProxy(opts ...ProxyOptions) *AnthropicProxy {
	var opt ProxyOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	// Parse the Anthropic API URL. See NewOpenAIProxy for rationale on
	// using panic over log.Fatalf for malformed package constants.
	targetURL, err := url.Parse(anthropicBaseURL)
	if err != nil {
		panic(fmt.Sprintf("invalid anthropicBaseURL constant %q: %v", anthropicBaseURL, err))
	}

	// Create the reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Create the Anthropic proxy instance
	anthropicProxy := &AnthropicProxy{proxy: proxy}

	// Use the generic director function to handle common proxy logic
	originalDirector := proxy.Director
	proxy.Director = CreateGenericDirector(anthropicProxy, targetURL, originalDirector, opt.DisableGzip)

	// Customize the transport for optimal streaming performance
	proxy.Transport = newProxyTransport(opt.DisableGzip)

	// Add custom response modifier for streaming support
	proxy.ModifyResponse = func(resp *http.Response) error {
		// Handle streaming responses
		if anthropicProxy.isStreamingResponse(resp) {
			log.Printf("Detected streaming response from Anthropic")

			// Ensure proper headers for streaming
			resp.Header.Set("Cache-Control", "no-cache")
			resp.Header.Set("Connection", "keep-alive")
			resp.Header.Set("X-Accel-Buffering", "no") // Disable nginx buffering if used

			// Remove content-length header for streaming
			resp.Header.Del("Content-Length")
		}

		return nil
	}

	// Add error handler with streaming-specific error handling
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("Anthropic proxy error: %v", err)

		// For streaming requests, we need to handle errors differently
		if anthropicProxy.IsStreamingRequest(r) {
			// If we're in a streaming context, we might have already started writing
			// the response, so we need to handle this gracefully
			log.Printf("Error occurred during streaming request")

			// Try to write an error in SSE format if possible
			if w.Header().Get("Content-Type") == "" {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprintf(w, "data: {\"error\": \"Proxy error: %v\"}\n\n", err)
				fmt.Fprintf(w, "data: [DONE]\n\n")
			} else {
				// Headers already sent, just log the error
				log.Printf("Cannot send error response, headers already sent")
			}
		} else {
			// Regular error handling for non-streaming requests
			w.WriteHeader(http.StatusBadGateway)
			fmt.Fprintf(w, "Anthropic proxy error: %v", err)
		}
	}

	return anthropicProxy
}

// GetName returns the name of the provider
func (a *AnthropicProxy) GetName() string {
	return "anthropic"
}

// IsStreamingRequest checks if the request is likely to be a streaming
// request for Anthropic. The cross-provider routing gate lives in
// ProviderManager.IsStreamingRequest — see openai.go for the rationale.
func (a *AnthropicProxy) IsStreamingRequest(req *http.Request) bool {
	if strings.Contains(req.Header.Get("Accept"), "text/event-stream") {
		return true
	}

	if req.Method == "POST" && strings.Contains(req.URL.Path, "/messages") {
		return a.checkStreamingInBody(req)
	}

	return false
}

// checkStreamingInBody reads the request body to check for "stream": true
// Anthropic uses the same streaming pattern as OpenAI
func (a *AnthropicProxy) checkStreamingInBody(req *http.Request) bool {
	if req.Body == nil {
		return false
	}

	// Use GetBody if available (body was already read and cached)
	var bodyBytes []byte
	var err error

	if req.GetBody != nil {
		// Body was already cached, use GetBody to get a fresh reader
		bodyReader, err := req.GetBody()
		if err != nil {
			log.Printf("Error getting cached request body for streaming check: %v", err)
			return false
		}
		defer bodyReader.Close()
		bodyBytes, err = io.ReadAll(bodyReader)
		if err != nil {
			log.Printf("Error reading cached request body for streaming check: %v", err)
			return false
		}
	} else {
		// Read the body for the first time
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			log.Printf("Error reading request body for streaming check: %v", err)
			return false
		}

		// Restore the body and create GetBody for future use
		req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewBuffer(bodyBytes)), nil
		}
	}

	// Parse the JSON to check for stream field
	var requestData map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &requestData); err != nil {
		log.Printf("Error parsing request body JSON for streaming check: %v", err)
		return false
	}

	// Check if stream is set to true
	if streamValue, exists := requestData["stream"]; exists {
		if streamBool, ok := streamValue.(bool); ok {
			return streamBool
		}
	}

	return false
}

// isStreamingResponse checks if the response is a streaming response
func (a *AnthropicProxy) isStreamingResponse(resp *http.Response) bool {
	contentType := resp.Header.Get("Content-Type")
	return strings.Contains(contentType, "text/event-stream")
}

// Proxy returns the HTTP handler for the Anthropic provider
func (a *AnthropicProxy) Proxy() http.Handler {
	return a.proxy
}

// WrapTransport replaces the proxy's transport with fn(current transport).
func (a *AnthropicProxy) WrapTransport(fn func(http.RoundTripper) http.RoundTripper) {
	a.proxy.Transport = fn(a.proxy.Transport)
}

// GetHealthStatus returns the health status of the Anthropic proxy
func (a *AnthropicProxy) GetHealthStatus() map[string]interface{} {
	return map[string]interface{}{
		"provider":          "anthropic",
		"status":            "healthy",
		"baseURL":           anthropicBaseURL,
		"streaming_support": true,
		"body_parsing":      true,
	}
}

// AnthropicResponse represents the structure of Anthropic API responses
type AnthropicResponse struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Role         string             `json:"role"`
	Content      []AnthropicContent `json:"content"`
	Model        string             `json:"model"`
	StopReason   string             `json:"stop_reason"`
	StopSequence *string            `json:"stop_sequence"`
	Usage        AnthropicUsage     `json:"usage"`
}

// AnthropicUsage represents token usage in Anthropic responses.
// cache_read_input_tokens / cache_creation_input_tokens are only present when
// prompt caching is active (claude-3+).
type AnthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
}

// AnthropicContent represents content in Anthropic responses.
// For extended-thinking models the type may be "thinking" and Thinking holds the
// internal chain-of-thought (never "text").
type AnthropicContent struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
}

// AnthropicStreamResponse represents streaming response chunks
type AnthropicStreamResponse struct {
	Type         string            `json:"type"`
	Message      *AnthropicMessage `json:"message,omitempty"`
	Index        int               `json:"index,omitempty"`
	ContentBlock *AnthropicContent `json:"content_block,omitempty"`
	Delta        *AnthropicDelta   `json:"delta,omitempty"`
	Usage        *AnthropicUsage   `json:"usage,omitempty"`
}

// AnthropicMessage represents a message in streaming responses
type AnthropicMessage struct {
	ID           string             `json:"id"`
	Type         string             `json:"type"`
	Role         string             `json:"role"`
	Content      []AnthropicContent `json:"content"`
	Model        string             `json:"model"`
	StopReason   string             `json:"stop_reason"`
	StopSequence *string            `json:"stop_sequence"`
	Usage        AnthropicUsage     `json:"usage"`
}

// AnthropicDelta represents delta changes in streaming responses.
// ThinkingDelta carries incremental thinking text when extended thinking is on.
type AnthropicDelta struct {
	Type         string  `json:"type"`
	Text         string  `json:"text,omitempty"`
	Thinking     string  `json:"thinking,omitempty"`
	StopReason   string  `json:"stop_reason,omitempty"`
	StopSequence *string `json:"stop_sequence,omitempty"`
}

// ParseResponseMetadata extracts tokens and model information from Anthropic responses
func (a *AnthropicProxy) ParseResponseMetadata(responseBody io.Reader, isStreaming bool) (*LLMResponseMetadata, error) {
	if isStreaming {
		return a.parseStreamingResponse(responseBody)
	}
	return a.parseNonStreamingResponse(responseBody)
}

// parseNonStreamingResponse handles standard Anthropic JSON responses
func (a *AnthropicProxy) parseNonStreamingResponse(responseBody io.Reader) (*LLMResponseMetadata, error) {
	// Handle potential gzip compression
	decompressedReader, err := DecompressResponseIfNeeded(responseBody)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress response: %w", err)
	}

	// If we got a gzip reader, make sure to close it
	if gzipReader, ok := decompressedReader.(*gzip.Reader); ok {
		defer gzipReader.Close()
	}

	bodyBytes, err := io.ReadAll(decompressedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Operator debug preview, routed through redact.LogPreview so the
	// raw model output is never persisted to logs in plaintext when
	// pii_redact is enabled. See internal/redact/log_helper.go.
	log.Printf("🔍 Debug: Response body preview: %s",
		redact.LogPreview(context.Background(), string(bodyBytes), 100))

	var response AnthropicResponse
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		return nil, fmt.Errorf("failed to parse Anthropic response: %w", err)
	}

	metadata := &LLMResponseMetadata{
		Model:        response.Model,
		InputTokens:  response.Usage.InputTokens,
		OutputTokens: response.Usage.OutputTokens,
		TotalTokens:  response.Usage.InputTokens + response.Usage.OutputTokens,
		Provider:     "anthropic",
		RequestID:    response.ID,
		IsStreaming:  false,
		FinishReason: response.StopReason,
	}

	return metadata, nil
}

// parseStreamingResponse handles Anthropic server-sent events
func (a *AnthropicProxy) parseStreamingResponse(responseBody io.Reader) (*LLMResponseMetadata, error) {
	// Handle potential gzip compression
	decompressedReader, err := DecompressResponseIfNeeded(responseBody)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress streaming response: %w", err)
	}

	// If we got a gzip reader, make sure to close it
	if gzipReader, ok := decompressedReader.(*gzip.Reader); ok {
		defer gzipReader.Close()
	}

	scanner := bufio.NewScanner(decompressedReader)
	// Allow lines up to 2 MB — large tool call / thinking deltas can be wide.
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)

	var metadata *LLMResponseMetadata
	var model string
	var requestID string
	var finishReason string
	var hasData bool

	// Token accumulators.
	var inputTokens, outputTokens int
	var cacheReadTokens, cacheCreationTokens int
	// thoughtChars counts characters in thinking deltas so we can estimate
	// thought tokens (Anthropic does not expose them separately yet).
	var thoughtChars int

	for scanner.Scan() {
		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		jsonData := strings.TrimPrefix(line, "data: ")
		if strings.TrimSpace(jsonData) == "[DONE]" {
			break
		}

		hasData = true

		var streamResponse AnthropicStreamResponse
		if err := json.Unmarshal([]byte(jsonData), &streamResponse); err != nil {
			// Partial chunks are expected mid-stream; skip silently.
			continue
		}

		switch streamResponse.Type {
		case "message_start":
			if streamResponse.Message != nil {
				model = streamResponse.Message.Model
				requestID = streamResponse.Message.ID
				u := streamResponse.Message.Usage
				if u.InputTokens > 0 {
					inputTokens = u.InputTokens
				}
				if u.OutputTokens > 0 {
					outputTokens = u.OutputTokens
				}
				cacheReadTokens = u.CacheReadInputTokens
				cacheCreationTokens = u.CacheCreationInputTokens
				log.Printf("🔍 Anthropic message_start: input=%d output=%d cache_read=%d cache_write=%d model=%s",
					inputTokens, outputTokens, cacheReadTokens, cacheCreationTokens, model)
			}

		case "content_block_start":
			// Track thinking blocks (extended-thinking models).
			if streamResponse.ContentBlock != nil && streamResponse.ContentBlock.Type == "thinking" {
				log.Printf("🧠 Anthropic: thinking block started (index %d)", streamResponse.Index)
			}

		case "content_block_delta":
			if streamResponse.Delta != nil && streamResponse.Delta.Type == "thinking_delta" {
				thoughtChars += len(streamResponse.Delta.Thinking)
			}

		case "message_delta":
			if streamResponse.Delta != nil && streamResponse.Delta.StopReason != "" {
				finishReason = streamResponse.Delta.StopReason
			}
			if streamResponse.Usage != nil && streamResponse.Usage.OutputTokens > 0 {
				outputTokens += streamResponse.Usage.OutputTokens
			}

		case "message_stop":
			if inputTokens > 0 || outputTokens > 0 {
				// Estimate thought tokens from chars (Anthropic ~4 chars/token).
				thoughtTokens := thoughtChars / 4
				metadata = &LLMResponseMetadata{
					Model:                    model,
					InputTokens:              inputTokens,
					OutputTokens:             outputTokens,
					TotalTokens:              inputTokens + outputTokens,
					ThoughtTokens:            thoughtTokens,
					CacheReadInputTokens:     cacheReadTokens,
					CacheCreationInputTokens: cacheCreationTokens,
					Provider:                 "anthropic",
					RequestID:                requestID,
					IsStreaming:              true,
					FinishReason:             finishReason,
				}
				log.Printf("🔍 Anthropic message_stop: input=%d output=%d thought=%d total=%d",
					inputTokens, outputTokens, thoughtTokens, inputTokens+outputTokens)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading streaming response: %w", err)
	}

	if metadata != nil {
		return metadata, nil
	}

	// Accumulated tokens without an explicit message_stop (truncated stream).
	if hasData && (inputTokens > 0 || outputTokens > 0) && (model != "" || requestID != "") {
		thoughtTokens := thoughtChars / 4
		return &LLMResponseMetadata{
			Model:                    model,
			InputTokens:              inputTokens,
			OutputTokens:             outputTokens,
			TotalTokens:              inputTokens + outputTokens,
			ThoughtTokens:            thoughtTokens,
			CacheReadInputTokens:     cacheReadTokens,
			CacheCreationInputTokens: cacheCreationTokens,
			Provider:                 "anthropic",
			RequestID:                requestID,
			IsStreaming:              true,
			FinishReason:             finishReason,
		}, nil
	}

	// If we found streaming data but no usage information, return partial metadata
	// This can happen when the stream is not yet complete or usage info is in a later chunk
	if hasData && (model != "" || requestID != "") {
		return &LLMResponseMetadata{
			Model:        model,
			InputTokens:  0, // Unknown at this point
			OutputTokens: 0, // Unknown at this point
			TotalTokens:  0, // Unknown at this point
			Provider:     "anthropic",
			RequestID:    requestID,
			IsStreaming:  true,
			FinishReason: finishReason,
		}, nil
	}

	return nil, fmt.Errorf("no usage information found in streaming response")
}

// UserIDFromRequest extracts user ID from Anthropic request body
// Anthropic supports passing user ID in the "metadata.user_id" field
func (a *AnthropicProxy) UserIDFromRequest(req *http.Request) string {
	if req.Body == nil || req.Method != "POST" {
		return ""
	}

	// Only check Anthropic-specific endpoints
	if !strings.HasPrefix(req.URL.Path, "/anthropic/") {
		return ""
	}

	// Read request body
	bodyBytes, err := a.readRequestBodyForUserID(req)
	if err != nil {
		log.Printf("Error reading Anthropic request body for user ID extraction: %v", err)
		return ""
	}

	if len(bodyBytes) == 0 {
		return ""
	}

	// Parse JSON to extract metadata.user_id field
	var data map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		log.Printf("Error parsing Anthropic request JSON for user ID extraction: %v", err)
		return ""
	}

	// Extract user ID from the "metadata.user_id" field
	if metadata, ok := data["metadata"].(map[string]interface{}); ok {
		if userValue, ok := metadata["user_id"].(string); ok && userValue != "" {
			log.Printf("🔍 Anthropic: Extracted user ID: %s", userValue)
			return userValue
		}
	}

	return ""
}

// RegisterExtraRoutes is a no-op for Anthropic as it doesn't need extra routes
func (a *AnthropicProxy) RegisterExtraRoutes(router *mux.Router) {
	// No extra routes needed for Anthropic
}

// readRequestBodyForUserID safely reads the request body for user ID extraction
func (a *AnthropicProxy) readRequestBodyForUserID(req *http.Request) ([]byte, error) {
	if req.GetBody != nil {
		// Body was already cached, use GetBody to get a fresh reader
		bodyReader, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		defer bodyReader.Close()
		return io.ReadAll(bodyReader)
	}

	// Read the body for the first time
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}

	// Restore the body and create GetBody for future use
	req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewBuffer(bodyBytes)), nil
	}

	return bodyBytes, nil
}

// ExtractRequestModelAndMessages extracts model and message text content from Anthropic request bodies
// for the messages API. Restores req.Body if read.
func (a *AnthropicProxy) ExtractRequestModelAndMessages(req *http.Request) (string, []string) {
	if req == nil || req.Method != "POST" {
		return "", nil
	}
	path := req.URL.Path
	isAnthropicEndpoint := strings.HasPrefix(path, "/anthropic/") ||
		strings.HasPrefix(path, "/v1/messages")
	if !isAnthropicEndpoint {
		return "", nil
	}
	// Only consider the /messages endpoint for token estimation
	if !strings.Contains(path, "/messages") {
		return "", nil
	}

	bodyBytes, err := a.readRequestBodyForUserID(req)
	if err != nil || len(bodyBytes) == 0 {
		return "", nil
	}

	var data map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		return "", nil
	}

	model := ""
	if mv, ok := data["model"].(string); ok {
		model = mv
	}

	messages := make([]string, 0, 8)
	// Anthropic request: messages: [{role, content: [{type:"text", text:"..."}, ...]}]
	if rawMsgs, ok := data["messages"].([]interface{}); ok {
		for _, m := range rawMsgs {
			if msg, ok := m.(map[string]interface{}); ok {
				if parts, ok := msg["content"].([]interface{}); ok {
					for _, p := range parts {
						if pm, ok := p.(map[string]interface{}); ok {
							if t, ok := pm["type"].(string); ok && t == "text" {
								if txt, ok := pm["text"].(string); ok && txt != "" {
									messages = append(messages, txt)
								}
							}
						}
					}
				}
			}
		}
	}

	// Tool use/input blocks might include text; ignore non-text to avoid overcounting

	return model, messages
}

// ValidateAPIKey validates and potentially replaces the API key in the request
func (a *AnthropicProxy) ValidateAPIKey(req *http.Request, keyStore APIKeyStore) error {
	// Get the API key from the x-api-key header (Anthropic uses this header).
	apiKey := req.Header.Get("x-api-key")
	if apiKey == "" {
		// Some clients (e.g. OpenAI-compatible SDKs) send credentials as
		// "Authorization: Bearer <key>" instead. Translate that onto
		// x-api-key — the header Anthropic's API actually expects — so the
		// rest of this function, and the upstream request, only ever deal
		// with x-api-key.
		const bearerPrefix = "Bearer "
		if authHeader := req.Header.Get("Authorization"); strings.HasPrefix(authHeader, bearerPrefix) {
			apiKey = strings.TrimPrefix(authHeader, bearerPrefix)
			req.Header.Set("x-api-key", apiKey)
			req.Header.Del("Authorization")
		}
	}
	if apiKey == "" {
		// No API key provided, let the provider handle the error
		return nil
	}

	// Validate and potentially replace the key. Use the request context so
	// client cancellation / handler-level deadlines propagate into the
	// DynamoDB validation lookup.
	actualKey, provider, err := keyStore.ValidateAndGetActualKey(req.Context(), apiKey)
	if err != nil {
		return fmt.Errorf("API key validation failed: %w", err)
	}

	// If a provider was returned, verify it matches
	if provider != "" && provider != "anthropic" {
		return fmt.Errorf("API key is for provider %s, not anthropic", provider)
	}

	// Replace the key in the request header if it was translated
	if actualKey != apiKey {
		req.Header.Set("x-api-key", actualKey)
		log.Printf("🔑 Anthropic: Translated API key from iw: format")
	}

	return nil
}
