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

	// Use the generic director function to handle common proxy logic
	originalDirector := proxy.Director
	proxy.Director = CreateGenericDirector(anthropicProxy, targetURL, originalDirector)

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

// Proxy returns the HTTP handler for the Anthropic provider
func (a *AnthropicProxy) Proxy() http.Handler {
	return a.proxy
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
