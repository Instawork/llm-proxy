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
	// OpenAI API base URL
	openAIBaseURL = "https://api.openai.com"
)

// OpenAIProxy handles OpenAI API requests and implements the Provider interface
type OpenAIProxy struct {
	proxy *httputil.ReverseProxy
}

// NewOpenAIProxy creates a new OpenAI reverse proxy
func NewOpenAIProxy() *OpenAIProxy {
	// Parse the OpenAI API URL
	targetURL, err := url.Parse(openAIBaseURL)
	if err != nil {
		log.Fatalf("Failed to parse OpenAI API URL: %v", err)
	}

	// Create the reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Create the OpenAI proxy instance
	openAIProxy := &OpenAIProxy{proxy: proxy}

	// Customize the director function to modify requests
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		
		// Set the Host header to the target host
		req.Host = targetURL.Host
		
		// Strip the /openai prefix from the path before forwarding to OpenAI
		if strings.HasPrefix(req.URL.Path, "/openai/") {
			req.URL.Path = strings.TrimPrefix(req.URL.Path, "/openai")
		}
		
		// Log the request, including streaming detection
		isStreaming := openAIProxy.IsStreamingRequest(req)
		if isStreaming {
			log.Printf("Proxying OpenAI streaming request: %s %s", req.Method, req.URL.Path)
		} else {
			log.Printf("Proxying OpenAI request: %s %s", req.Method, req.URL.Path)
		}
	}

	// Customize the transport for optimal streaming performance
	proxy.Transport = newProxyTransport()

	// Add custom response modifier for streaming support
	proxy.ModifyResponse = func(resp *http.Response) error {
		// Handle streaming responses
		if openAIProxy.isStreamingResponse(resp) {
			log.Printf("Detected streaming response from OpenAI")
			
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
		log.Printf("OpenAI proxy error: %v", err)
		
		// For streaming requests, we need to handle errors differently
		if openAIProxy.IsStreamingRequest(r) {
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
			fmt.Fprintf(w, "OpenAI proxy error: %v", err)
		}
	}

	return openAIProxy
}

// GetName returns the name of the provider
func (o *OpenAIProxy) GetName() string {
	return "openai"
}

// IsStreamingRequest checks if the request is likely to be a streaming request for OpenAI
func (o *OpenAIProxy) IsStreamingRequest(req *http.Request) bool {
	// Check for streaming in the Accept header first (fast check)
	if strings.Contains(req.Header.Get("Accept"), "text/event-stream") {
		return true
	}
	
	// Only check OpenAI-specific endpoints
	if !strings.HasPrefix(req.URL.Path, "/openai/") {
		return false
	}
	
	// For completion endpoints, check the request body for stream: true
	if req.Method == "POST" && (strings.Contains(req.URL.Path, "/chat/completions") || strings.Contains(req.URL.Path, "/completions")) {
		return o.checkStreamingInBody(req)
	}
	
	return false
}

// checkStreamingInBody reads the request body to check for "stream": true
func (o *OpenAIProxy) checkStreamingInBody(req *http.Request) bool {
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
func (o *OpenAIProxy) isStreamingResponse(resp *http.Response) bool {
	contentType := resp.Header.Get("Content-Type")
	return strings.Contains(contentType, "text/event-stream")
}

// RegisterRoutes registers OpenAI-specific routes with the given router
func (o *OpenAIProxy) RegisterRoutes(r *mux.Router) {
	// Handle chat completions endpoint specifically (primary streaming endpoint)
	r.PathPrefix("/openai/v1/chat/completions").Handler(o.proxy).Methods("POST", "OPTIONS")
	// Handle completions endpoint (legacy, but also supports streaming)
	r.PathPrefix("/openai/v1/completions").Handler(o.proxy).Methods("POST", "OPTIONS")
	// Handle other OpenAI API endpoints
	r.PathPrefix("/openai/v1/").Handler(o.proxy).Methods("GET", "POST", "PUT", "DELETE", "OPTIONS")
	log.Printf("OpenAI routes registered at /openai/v1/ with streaming support")
}

// GetHealthStatus returns the health status of the OpenAI proxy
func (o *OpenAIProxy) GetHealthStatus() map[string]interface{} {
	return map[string]interface{}{
		"provider":          "openai",
		"status":            "healthy",
		"baseURL":           openAIBaseURL,
		"streaming_support": true,
		"body_parsing":      true,
	}
}

// EstimateTokensFromRequest estimates tokens for OpenAI requests
func (o *OpenAIProxy) EstimateTokensFromRequest(req *http.Request) int64 {
	return 100	
}


// OpenAIResponse represents the structure of OpenAI API responses
type OpenAIResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Usage   OpenAIUsage        `json:"usage"`
	Choices []OpenAIChoice     `json:"choices"`
}

// OpenAIUsage represents token usage in OpenAI responses
type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// OpenAIChoice represents a choice in OpenAI responses
type OpenAIChoice struct {
	Index        int                    `json:"index"`
	Message      OpenAIMessage          `json:"message,omitempty"`
	Delta        OpenAIMessage          `json:"delta,omitempty"`
	FinishReason string                 `json:"finish_reason"`
}

// OpenAIMessage represents a message in OpenAI responses
type OpenAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// OpenAIStreamResponse represents streaming response chunks
type OpenAIStreamResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Usage   *OpenAIUsage   `json:"usage,omitempty"`
	Choices []OpenAIChoice `json:"choices"`
}

// ParseResponseMetadata extracts tokens and model information from OpenAI responses
func (o *OpenAIProxy) ParseResponseMetadata(responseBody io.Reader, isStreaming bool) (*LLMResponseMetadata, error) {
	if isStreaming {
		return o.parseStreamingResponse(responseBody)
	}
	return o.parseNonStreamingResponse(responseBody)
}

// parseNonStreamingResponse handles standard OpenAI JSON responses
func (o *OpenAIProxy) parseNonStreamingResponse(responseBody io.Reader) (*LLMResponseMetadata, error) {
	bodyBytes, err := io.ReadAll(responseBody)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var response OpenAIResponse
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		return nil, fmt.Errorf("failed to parse OpenAI response: %w", err)
	}

	metadata := &LLMResponseMetadata{
		Model:        response.Model,
		InputTokens:  response.Usage.PromptTokens,
		OutputTokens: response.Usage.CompletionTokens,
		TotalTokens:  response.Usage.TotalTokens,
		Provider:     "openai",
		RequestID:    response.ID,
		IsStreaming:  false,
	}

	// Extract finish reason from the first choice if available
	if len(response.Choices) > 0 {
		metadata.FinishReason = response.Choices[0].FinishReason
	}

	return metadata, nil
}

// parseStreamingResponse handles OpenAI server-sent events
func (o *OpenAIProxy) parseStreamingResponse(responseBody io.Reader) (*LLMResponseMetadata, error) {
	scanner := bufio.NewScanner(responseBody)
	var metadata *LLMResponseMetadata
	var model string
	var requestID string
	var finishReason string
	var hasData bool
	var chunkCount int

	log.Printf("🔄 OpenAI: Starting to parse streaming response")

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
			log.Printf("🔄 OpenAI: Found [DONE] marker, ending stream parse")
			break
		}
		
		hasData = true
		chunkCount++
		
		var streamResponse OpenAIStreamResponse
		if err := json.Unmarshal([]byte(jsonData), &streamResponse); err != nil {
			// Log error but continue processing other chunks
			log.Printf("Warning: failed to parse streaming chunk: %v", err)
			continue
		}
		
		// Capture model and request ID from any chunk
		if model == "" && streamResponse.Model != "" {
			model = streamResponse.Model
			log.Printf("🔄 OpenAI: Captured model: %s", model)
		}
		if requestID == "" && streamResponse.ID != "" {
			requestID = streamResponse.ID
			log.Printf("🔄 OpenAI: Captured request ID: %s", requestID)
		}
		
		// Extract finish reason from choices
		if len(streamResponse.Choices) > 0 && streamResponse.Choices[0].FinishReason != "" {
			finishReason = streamResponse.Choices[0].FinishReason
			log.Printf("🔄 OpenAI: Captured finish reason: %s", finishReason)
		}
		
		// The usage information is typically in the last chunk
		if streamResponse.Usage != nil {
			log.Printf("🔄 OpenAI: Found usage data! Input: %d, Output: %d, Total: %d", 
				streamResponse.Usage.PromptTokens, streamResponse.Usage.CompletionTokens, streamResponse.Usage.TotalTokens)
			metadata = &LLMResponseMetadata{
				Model:        model,
				InputTokens:  streamResponse.Usage.PromptTokens,
				OutputTokens: streamResponse.Usage.CompletionTokens,
				TotalTokens:  streamResponse.Usage.TotalTokens,
				Provider:     "openai",
				RequestID:    requestID,
				IsStreaming:  true,
				FinishReason: finishReason,
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading streaming response: %w", err)
	}

	log.Printf("🔄 OpenAI: Processed %d chunks, hasData: %v, hasUsage: %v", chunkCount, hasData, metadata != nil)

	// If we have usage metadata, return it
	if metadata != nil {
		return metadata, nil
	}

	// If we found streaming data but no usage information, return partial metadata
	// This can happen when the stream is not yet complete or usage info is in a later chunk
	if hasData && (model != "" || requestID != "") {
		log.Printf("🔄 OpenAI: Returning partial metadata - usage data not yet available")
		return &LLMResponseMetadata{
			Model:        model,
			InputTokens:  0,  // Unknown at this point
			OutputTokens: 0,  // Unknown at this point
			TotalTokens:  0,  // Unknown at this point
			Provider:     "openai",
			RequestID:    requestID,
			IsStreaming:  true,
			FinishReason: finishReason,
		}, nil
	}

	return nil, fmt.Errorf("no usage information found in streaming response")
}

// ParseRateLimitFromResponse extracts rate limit information from OpenAI response headers
func (o *OpenAIProxy) ParseRateLimitFromResponse(resp *http.Response) *RateLimitInfo {
	if resp == nil || resp.Header == nil {
		log.Printf("🔄 OpenAI: Response or headers are nil")
		return nil
	}
	
	rateLimitInfo := &RateLimitInfo{
		Provider: "openai",
	}
	
	log.Printf("🔄 OpenAI: Parsing rate limit headers...")
	
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
	
	// Parse OpenAI rate limit headers
	// Request limits
	if limitStr := getHeaderValue("x-ratelimit-limit-requests"); limitStr != "" {
		log.Printf("🔄 OpenAI: Found request limit header: %s", limitStr)
		rateLimitInfo.RequestLimit = parseIntFromString(limitStr)
		rateLimitInfo.HasRateLimitInfo = true
	}
	
	if remainingStr := getHeaderValue("x-ratelimit-remaining-requests"); remainingStr != "" {
		log.Printf("🔄 OpenAI: Found request remaining header: %s", remainingStr)
		rateLimitInfo.RequestRemaining = parseIntFromString(remainingStr)
		rateLimitInfo.HasRateLimitInfo = true
	}
	
	if resetStr := getHeaderValue("x-ratelimit-reset-requests"); resetStr != "" {
		log.Printf("🔄 OpenAI: Found request reset header: %s", resetStr)
		rateLimitInfo.RequestReset = parseDurationFromString(resetStr)
		rateLimitInfo.HasRateLimitInfo = true
	}
	
	// Token limits
	if tokenLimitStr := getHeaderValue("x-ratelimit-limit-tokens"); tokenLimitStr != "" {
		log.Printf("🔄 OpenAI: Found token limit header: %s", tokenLimitStr)
		rateLimitInfo.TokenLimit = parseIntFromString(tokenLimitStr)
		rateLimitInfo.HasRateLimitInfo = true
	}
	
	if tokenRemainingStr := getHeaderValue("x-ratelimit-remaining-tokens"); tokenRemainingStr != "" {
		log.Printf("🔄 OpenAI: Found token remaining header: %s", tokenRemainingStr)
		rateLimitInfo.TokenRemaining = parseIntFromString(tokenRemainingStr)
		rateLimitInfo.HasRateLimitInfo = true
	}
	
	if tokenResetStr := getHeaderValue("x-ratelimit-reset-tokens"); tokenResetStr != "" {
		log.Printf("🔄 OpenAI: Found token reset header: %s", tokenResetStr)
		rateLimitInfo.TokenReset = parseDurationFromString(tokenResetStr)
		rateLimitInfo.HasRateLimitInfo = true
	}
	
	log.Printf("🔄 OpenAI: HasRateLimitInfo = %t", rateLimitInfo.HasRateLimitInfo)
	
	// If no rate limit info was found, return nil
	if !rateLimitInfo.HasRateLimitInfo {
		log.Printf("🔄 OpenAI: No rate limit info found, returning nil")
		return nil
	}
	
	log.Printf("🔄 OpenAI: Parsed rate limit info - Request: %d/%d, Token: %d/%d", 
		rateLimitInfo.RequestRemaining, rateLimitInfo.RequestLimit,
		rateLimitInfo.TokenRemaining, rateLimitInfo.TokenLimit)
	
	return rateLimitInfo
} 
