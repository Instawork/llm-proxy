package providers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

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

// NewAnthropicProxy creates a new Anthropic reverse proxy
func NewAnthropicProxy() *AnthropicProxy {
	// Parse the Anthropic API URL
	targetURL, err := url.Parse(anthropicBaseURL)
	if err != nil {
		log.Fatalf("Failed to parse Anthropic API URL: %v", err)
	}

	// Create the reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Create the Anthropic proxy instance
	anthropicProxy := &AnthropicProxy{proxy: proxy}

	// Customize the director function to modify requests
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		
		// Set the Host header to the target host
		req.Host = targetURL.Host
		
		// Strip the /anthropic prefix from the path before forwarding to Anthropic
		if strings.HasPrefix(req.URL.Path, "/anthropic/") {
			req.URL.Path = strings.TrimPrefix(req.URL.Path, "/anthropic")
		}
		
		// Log the request, including streaming detection
		isStreaming := anthropicProxy.IsStreamingRequest(req)
		if isStreaming {
			log.Printf("Proxying Anthropic streaming request: %s %s", req.Method, req.URL.Path)
		} else {
			log.Printf("Proxying Anthropic request: %s %s", req.Method, req.URL.Path)
		}
	}

	// Customize the transport for optimal streaming performance
	proxy.Transport = newProxyTransport()

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

// IsStreamingRequest checks if the request is likely to be a streaming request for Anthropic
func (a *AnthropicProxy) IsStreamingRequest(req *http.Request) bool {
	// Check for streaming in the Accept header first (fast check)
	if strings.Contains(req.Header.Get("Accept"), "text/event-stream") {
		return true
	}
	
	// Only check Anthropic-specific endpoints
	if !strings.HasPrefix(req.URL.Path, "/anthropic/") {
		return false
	}
	
	// For Anthropic messages endpoint, check the request body for stream: true
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

// RegisterRoutes registers Anthropic-specific routes with the given router
func (a *AnthropicProxy) RegisterRoutes(r *mux.Router) {
	// Handle messages endpoint (primary streaming endpoint for Anthropic)
	r.PathPrefix("/anthropic/v1/messages").Handler(a.proxy).Methods("POST", "OPTIONS")
	
	// Handle other Anthropic API endpoints
	r.PathPrefix("/anthropic/v1/").Handler(a.proxy).Methods("GET", "POST", "PUT", "DELETE", "OPTIONS")
	
	log.Printf("Anthropic routes registered at /anthropic/v1/ with streaming support")
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

// EstimateTokensFromRequest estimates tokens for Anthropic requests
func (a *AnthropicProxy) EstimateTokensFromRequest(req *http.Request) int64 {
	// Try to extract model and content from request body
	bodyBytes, err := a.readRequestBody(req)
	if err != nil {
		log.Printf("Error reading request body for Anthropic token estimation: %v", err)
		return a.getDefaultTokenEstimate(req.URL.Path, "")
	}
	
	if len(bodyBytes) == 0 {
		return a.getDefaultTokenEstimate(req.URL.Path, "")
	}
	
	// Parse JSON to extract model and messages
	var data map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		log.Printf("Error parsing Anthropic request JSON for token estimation: %v", err)
		return a.getDefaultTokenEstimate(req.URL.Path, "")
	}
	
	// Extract model for model-specific estimation
	model := ""
	if modelValue, ok := data["model"].(string); ok {
		model = modelValue
	}
	
	// Estimate input tokens based on content
	inputTokens := int64(0)
	
	// Check for messages array (Anthropic format)
	if messages, ok := data["messages"].([]interface{}); ok {
		inputTokens = a.estimateTokensFromMessages(messages, model)
	} else {
		// Fallback to body size estimation
		inputTokens = a.estimateTokensFromBodySize(len(bodyBytes), model)
	}
	
	// Add system message tokens if present
	if system, ok := data["system"].(string); ok {
		inputTokens += a.estimateTokensFromText(system, model)
	}
	
	// Estimate output tokens based on max_tokens parameter
	outputTokens := int64(0)
	if maxTokens, ok := data["max_tokens"].(float64); ok {
		outputTokens = int64(maxTokens * 0.6) // Estimate 60% of max will be used
	} else {
		outputTokens = a.getDefaultOutputEstimate(model)
	}
	
	totalTokens := inputTokens + outputTokens
	
	// Apply model-specific adjustments
	totalTokens = a.applyModelAdjustments(totalTokens, model)
	
	log.Printf("🤖 Anthropic token estimation: %d tokens (%d input + %d output) for model %s", 
		totalTokens, inputTokens, outputTokens, model)
	return totalTokens
}

// Helper methods for Anthropic token estimation

func (a *AnthropicProxy) readRequestBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	
	if req.GetBody != nil {
		bodyReader, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		defer bodyReader.Close()
		return io.ReadAll(bodyReader)
	}
	
	bodyBytes, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	
	req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewBuffer(bodyBytes)), nil
	}
	
	return bodyBytes, nil
}

func (a *AnthropicProxy) estimateTokensFromMessages(messages []interface{}, model string) int64 {
	totalTokens := int64(0)
	
	for _, msg := range messages {
		if msgMap, ok := msg.(map[string]interface{}); ok {
			if content, ok := msgMap["content"].(string); ok {
				totalTokens += a.estimateTokensFromText(content, model)
			}
			// Add overhead for message structure (role, etc.)
			totalTokens += 3
		}
	}
	
	// Add overhead for messages format
	totalTokens += int64(len(messages)) * 2
	return totalTokens
}

func (a *AnthropicProxy) estimateTokensFromText(text string, model string) int64 {
	// Anthropic's general rule: ~3.5 characters per token (slightly more efficient than OpenAI)
	charsPerToken := 3.5
	
	// Adjust for different Claude models
	if strings.Contains(model, "claude-3-opus") {
		charsPerToken = 3.4 // Opus is slightly more efficient
	} else if strings.Contains(model, "claude-3-sonnet") {
		charsPerToken = 3.5
	} else if strings.Contains(model, "claude-3-haiku") {
		charsPerToken = 3.6 // Haiku is slightly less efficient but faster
	}
	
	return int64(float64(len(text)) / charsPerToken)
}

func (a *AnthropicProxy) estimateTokensFromBodySize(bodySize int, model string) int64 {
	// Fallback estimation from body size
	// Assume about 75% of body is actual content for Anthropic's cleaner JSON structure
	estimatedContentSize := float64(bodySize) * 0.75
	return int64(estimatedContentSize / 3.5)
}

func (a *AnthropicProxy) getDefaultOutputEstimate(model string) int64 {
	// Default output token estimates based on typical usage patterns
	if strings.Contains(model, "claude-3-opus") {
		return 200 // Opus tends to give more detailed responses
	} else if strings.Contains(model, "claude-3-sonnet") {
		return 150
	} else if strings.Contains(model, "claude-3-haiku") {
		return 100 // Haiku is more concise
	}
	return 125
}

func (a *AnthropicProxy) getDefaultTokenEstimate(path string, model string) int64 {
	// Endpoint-specific defaults
	if strings.Contains(path, "/messages") {
		if strings.Contains(model, "claude-3-opus") {
			return 250
		} else if strings.Contains(model, "claude-3-sonnet") {
			return 180
		} else if strings.Contains(model, "claude-3-haiku") {
			return 120
		}
		return 150
	}
	return 75
}

func (a *AnthropicProxy) applyModelAdjustments(tokens int64, model string) int64 {
	// Apply model-specific multipliers for accuracy
	if strings.Contains(model, "claude-3-opus") {
		// Opus requests tend to be more complex and produce longer responses
		tokens = int64(float64(tokens) * 1.15)
	} else if strings.Contains(model, "claude-3-haiku") {
		// Haiku is more efficient and concise
		tokens = int64(float64(tokens) * 0.9)
	}
	
	// Cap at reasonable maximum to prevent over-consumption
	if tokens > 10000 {
		tokens = 10000
	}
	
	// Ensure reasonable minimum
	if tokens < 10 {
		tokens = 10
	}
	
	return tokens
}

// AnthropicResponse represents the structure of Anthropic API responses
type AnthropicResponse struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	Role         string            `json:"role"`
	Content      []AnthropicContent `json:"content"`
	Model        string            `json:"model"`
	StopReason   string            `json:"stop_reason"`
	StopSequence *string           `json:"stop_sequence"`
	Usage        AnthropicUsage    `json:"usage"`
}

// AnthropicUsage represents token usage in Anthropic responses
type AnthropicUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// AnthropicContent represents content in Anthropic responses
type AnthropicContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// AnthropicStreamResponse represents streaming response chunks
type AnthropicStreamResponse struct {
	Type         string             `json:"type"`
	Message      *AnthropicMessage  `json:"message,omitempty"`
	Index        int                `json:"index,omitempty"`
	ContentBlock *AnthropicContent  `json:"content_block,omitempty"`
	Delta        *AnthropicDelta    `json:"delta,omitempty"`
	Usage        *AnthropicUsage    `json:"usage,omitempty"`
}

// AnthropicMessage represents a message in streaming responses
type AnthropicMessage struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	Role         string            `json:"role"`
	Content      []AnthropicContent `json:"content"`
	Model        string            `json:"model"`
	StopReason   string            `json:"stop_reason"`
	StopSequence *string           `json:"stop_sequence"`
	Usage        AnthropicUsage    `json:"usage"`
}

// AnthropicDelta represents delta changes in streaming responses
type AnthropicDelta struct {
	Type         string  `json:"type"`
	Text         string  `json:"text,omitempty"`
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
	bodyBytes, err := io.ReadAll(responseBody)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

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
	scanner := bufio.NewScanner(responseBody)
	var metadata *LLMResponseMetadata
	var model string
	var requestID string
	var finishReason string
	var hasData bool
	
	// Track token usage as we accumulate it from different events
	var inputTokens int = 0
	var outputTokens int = 0

	for scanner.Scan() {
		line := scanner.Text()
		
		// Skip empty lines and non-data lines
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		
		// Extract JSON data
		jsonData := strings.TrimPrefix(line, "data: ")
		
		// Skip [DONE] marker
		if strings.TrimSpace(jsonData) == "[DONE]" {
			break
		}
		
		hasData = true
		
		var streamResponse AnthropicStreamResponse
		if err := json.Unmarshal([]byte(jsonData), &streamResponse); err != nil {
			// Log error but continue processing other chunks
			log.Printf("Warning: failed to parse Anthropic streaming chunk: %v", err)
			continue
		}
		
		// Handle different event types
		switch streamResponse.Type {
		case "message_start":
			if streamResponse.Message != nil {
				model = streamResponse.Message.Model
				requestID = streamResponse.Message.ID
				// Extract initial usage information from message_start
				if streamResponse.Message.Usage.InputTokens > 0 {
					inputTokens = streamResponse.Message.Usage.InputTokens
				}
				if streamResponse.Message.Usage.OutputTokens > 0 {
					outputTokens = streamResponse.Message.Usage.OutputTokens
				}
				log.Printf("🔍 Anthropic: message_start - Input: %d, Output: %d", 
					inputTokens, outputTokens)
			}
		case "message_delta":
			if streamResponse.Delta != nil && streamResponse.Delta.StopReason != "" {
				finishReason = streamResponse.Delta.StopReason
			}
			// Accumulate additional token usage from message_delta events
			if streamResponse.Usage != nil {
				if streamResponse.Usage.OutputTokens > 0 {
					// For delta events, add the additional output tokens
					outputTokens += streamResponse.Usage.OutputTokens
					log.Printf("🔍 Anthropic: message_delta - Added %d output tokens, total output: %d", 
						streamResponse.Usage.OutputTokens, outputTokens)
				}
			}
		case "message_stop":
			// Final message - create metadata with accumulated usage information
			if inputTokens > 0 || outputTokens > 0 {
				metadata = &LLMResponseMetadata{
					Model:        model,
					InputTokens:  inputTokens,
					OutputTokens: outputTokens,
					TotalTokens:  inputTokens + outputTokens,
					Provider:     "anthropic",
					RequestID:    requestID,
					IsStreaming:  true,
					FinishReason: finishReason,
				}
				log.Printf("🔍 Anthropic: message_stop - Final tokens - Input: %d, Output: %d, Total: %d", 
					inputTokens, outputTokens, inputTokens + outputTokens)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading streaming response: %w", err)
	}

	// If we have usage metadata, return it
	if metadata != nil {
		return metadata, nil
	}
	
	// If we have accumulated token counts even without message_stop, create metadata
	if hasData && (inputTokens > 0 || outputTokens > 0) && (model != "" || requestID != "") {
		log.Printf("🔍 Anthropic: Creating metadata from accumulated usage - Input: %d, Output: %d", 
			inputTokens, outputTokens)
		return &LLMResponseMetadata{
			Model:        model,
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
			TotalTokens:  inputTokens + outputTokens,
			Provider:     "anthropic",
			RequestID:    requestID,
			IsStreaming:  true,
			FinishReason: finishReason,
		}, nil
	}

	// If we found streaming data but no usage information, return partial metadata
	// This can happen when the stream is not yet complete or usage info is in a later chunk
	if hasData && (model != "" || requestID != "") {
		return &LLMResponseMetadata{
			Model:        model,
			InputTokens:  0,  // Unknown at this point
			OutputTokens: 0,  // Unknown at this point
			TotalTokens:  0,  // Unknown at this point
			Provider:     "anthropic",
			RequestID:    requestID,
			IsStreaming:  true,
			FinishReason: finishReason,
		}, nil
	}

	return nil, fmt.Errorf("no usage information found in streaming response")
}

// ParseRateLimitFromResponse extracts rate limit information from Anthropic response headers
func (a *AnthropicProxy) ParseRateLimitFromResponse(resp *http.Response) *RateLimitInfo {
	if resp == nil || resp.Header == nil {
		return nil
	}
	
	rateLimitInfo := &RateLimitInfo{
		Provider: "anthropic",
	}
	
	// Helper function to get header value - try both Get() and direct access
	getHeaderValue := func(headerName string) string {
		// First try the standard way
		if value := resp.Header.Get(headerName); value != "" {
			return value
		}
		
		// If that fails, try direct access
		if values, ok := resp.Header[headerName]; ok && len(values) > 0 {
			return values[0]
		}
		
		return ""
	}
	
	// Parse Anthropic rate limit headers
	// Request limits
	if limitStr := getHeaderValue("anthropic-ratelimit-requests-limit"); limitStr != "" {
		rateLimitInfo.RequestLimit = parseIntFromString(limitStr)
		rateLimitInfo.HasRateLimitInfo = true
	}
	
	if remainingStr := getHeaderValue("anthropic-ratelimit-requests-remaining"); remainingStr != "" {
		rateLimitInfo.RequestRemaining = parseIntFromString(remainingStr)
		rateLimitInfo.HasRateLimitInfo = true
	}
	
	if resetStr := getHeaderValue("anthropic-ratelimit-requests-reset"); resetStr != "" {
		rateLimitInfo.RequestReset = parseDurationFromString(resetStr)
		rateLimitInfo.HasRateLimitInfo = true
	}
	
	// General token limits
	if limitStr := getHeaderValue("anthropic-ratelimit-tokens-limit"); limitStr != "" {
		rateLimitInfo.TokenLimit = parseIntFromString(limitStr)
		rateLimitInfo.HasRateLimitInfo = true
	}
	
	if remainingStr := getHeaderValue("anthropic-ratelimit-tokens-remaining"); remainingStr != "" {
		rateLimitInfo.TokenRemaining = parseIntFromString(remainingStr)
		rateLimitInfo.HasRateLimitInfo = true
	}
	
	if resetStr := getHeaderValue("anthropic-ratelimit-tokens-reset"); resetStr != "" {
		rateLimitInfo.TokenReset = parseDurationFromString(resetStr)
		rateLimitInfo.HasRateLimitInfo = true
	}
	
	// Input token limits (Anthropic specific)
	if limitStr := getHeaderValue("anthropic-ratelimit-input-tokens-limit"); limitStr != "" {
		rateLimitInfo.InputTokenLimit = parseIntFromString(limitStr)
		rateLimitInfo.HasRateLimitInfo = true
	}
	
	if remainingStr := getHeaderValue("anthropic-ratelimit-input-tokens-remaining"); remainingStr != "" {
		rateLimitInfo.InputTokenRemaining = parseIntFromString(remainingStr)
		rateLimitInfo.HasRateLimitInfo = true
	}
	
	if resetStr := getHeaderValue("anthropic-ratelimit-input-tokens-reset"); resetStr != "" {
		rateLimitInfo.InputTokenReset = parseDurationFromString(resetStr)
		rateLimitInfo.HasRateLimitInfo = true
	}
	
	// Output token limits (Anthropic specific)
	if limitStr := getHeaderValue("anthropic-ratelimit-output-tokens-limit"); limitStr != "" {
		rateLimitInfo.OutputTokenLimit = parseIntFromString(limitStr)
		rateLimitInfo.HasRateLimitInfo = true
	}
	
	if remainingStr := getHeaderValue("anthropic-ratelimit-output-tokens-remaining"); remainingStr != "" {
		rateLimitInfo.OutputTokenRemaining = parseIntFromString(remainingStr)
		rateLimitInfo.HasRateLimitInfo = true
	}
	
	if resetStr := getHeaderValue("anthropic-ratelimit-output-tokens-reset"); resetStr != "" {
		rateLimitInfo.OutputTokenReset = parseDurationFromString(resetStr)
		rateLimitInfo.HasRateLimitInfo = true
	}
	
	// If no rate limit info was found, return nil
	if !rateLimitInfo.HasRateLimitInfo {
		return nil
	}
	
	log.Printf("🔄 Anthropic: Parsed rate limit info - Request: %d/%d, Token: %d/%d, Input: %d/%d, Output: %d/%d", 
		rateLimitInfo.RequestRemaining, rateLimitInfo.RequestLimit,
		rateLimitInfo.TokenRemaining, rateLimitInfo.TokenLimit,
		rateLimitInfo.InputTokenRemaining, rateLimitInfo.InputTokenLimit,
		rateLimitInfo.OutputTokenRemaining, rateLimitInfo.OutputTokenLimit)
	
	return rateLimitInfo
} 
