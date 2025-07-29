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

	// Use the generic director function to handle common proxy logic
	originalDirector := proxy.Director
	proxy.Director = CreateGenericDirector(openAIProxy, targetURL, originalDirector)

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

// Proxy returns the HTTP handler for the OpenAI provider
func (o *OpenAIProxy) Proxy() http.Handler {
	return o.proxy
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

// OpenAIResponse represents the structure of OpenAI API responses
type OpenAIResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Usage   OpenAIUsage    `json:"usage"`
	Choices []OpenAIChoice `json:"choices"`
}

// OpenAIUsage represents token usage in OpenAI responses
type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// OpenAIChoice represents a choice in OpenAI responses
type OpenAIChoice struct {
	Index        int           `json:"index"`
	Message      OpenAIMessage `json:"message,omitempty"`
	Delta        OpenAIMessage `json:"delta,omitempty"`
	FinishReason string        `json:"finish_reason"`
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
			InputTokens:  0, // Unknown at this point
			OutputTokens: 0, // Unknown at this point
			TotalTokens:  0, // Unknown at this point
			Provider:     "openai",
			RequestID:    requestID,
			IsStreaming:  true,
			FinishReason: finishReason,
		}, nil
	}

	return nil, fmt.Errorf("no usage information found in streaming response")
}

// UserIDFromRequest extracts user ID from OpenAI request body
// OpenAI supports passing user ID in the "user" field for safety tracking
// See: https://platform.openai.com/docs/guides/safety-best-practices#end-user-ids
func (o *OpenAIProxy) UserIDFromRequest(req *http.Request) string {
	if req.Body == nil || req.Method != "POST" {
		return ""
	}

	// Only check OpenAI-specific endpoints
	if !strings.HasPrefix(req.URL.Path, "/openai/") {
		return ""
	}

	// Read request body
	bodyBytes, err := o.readRequestBodyForUserID(req)
	if err != nil {
		log.Printf("Error reading OpenAI request body for user ID extraction: %v", err)
		return ""
	}

	if len(bodyBytes) == 0 {
		return ""
	}

	// Parse JSON to extract user field
	var data map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		log.Printf("Error parsing OpenAI request JSON for user ID extraction: %v", err)
		return ""
	}

	// Extract user ID from the "user" field
	if userValue, ok := data["user"].(string); ok && userValue != "" {
		log.Printf("🔍 OpenAI: Extracted user ID: %s", userValue)
		return userValue
	}

	return ""
}

// RegisterExtraRoutes is a no-op for OpenAI as it doesn't need extra routes
func (o *OpenAIProxy) RegisterExtraRoutes(router *mux.Router) {
	// No extra routes needed for OpenAI
}

// readRequestBodyForUserID safely reads the request body for user ID extraction
func (o *OpenAIProxy) readRequestBodyForUserID(req *http.Request) ([]byte, error) {
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
