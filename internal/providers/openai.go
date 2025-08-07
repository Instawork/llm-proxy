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

	// For completion endpoints and responses API, check the request body for stream: true
	if req.Method == "POST" && (strings.Contains(req.URL.Path, "/chat/completions") ||
		strings.Contains(req.URL.Path, "/completions") ||
		strings.Contains(req.URL.Path, "/responses")) {
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

// OpenAIResponsesAPIResponse represents the Responses API response structure
type OpenAIResponsesAPIResponse struct {
	ID                 string                 `json:"id"`
	CreatedAt          float64                `json:"created_at"`
	Model              string                 `json:"model"`
	Object             string                 `json:"object"`
	Output             []OpenAIResponseOutput `json:"output"`
	Usage              OpenAIResponsesUsage   `json:"usage"`
	Status             string                 `json:"status"`
	Temperature        float64                `json:"temperature"`
	TopP               float64                `json:"top_p"`
	MaxOutputTokens    *int                   `json:"max_output_tokens"`
	PreviousResponseID *string                `json:"previous_response_id"`
	Store              bool                   `json:"store"`
}

// OpenAIResponseOutput represents an output item in the Responses API
type OpenAIResponseOutput struct {
	ID      string                  `json:"id"`
	Type    string                  `json:"type"`
	Role    string                  `json:"role,omitempty"`
	Content []OpenAIResponseContent `json:"content,omitempty"`
	Status  string                  `json:"status,omitempty"`
}

// OpenAIResponseContent represents content in a Responses API output
type OpenAIResponseContent struct {
	Type        string        `json:"type"`
	Text        string        `json:"text,omitempty"`
	Annotations []interface{} `json:"annotations,omitempty"`
}

// OpenAIResponsesUsage represents usage data for Responses API
type OpenAIResponsesUsage struct {
	InputTokens         int                         `json:"input_tokens"`
	OutputTokens        int                         `json:"output_tokens"`
	TotalTokens         int                         `json:"total_tokens"`
	InputTokensDetails  *OpenAITokenDetails         `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *OpenAIResponseTokenDetails `json:"output_tokens_details,omitempty"`
}

// OpenAITokenDetails represents token details
type OpenAITokenDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// OpenAIResponseTokenDetails represents output token details for responses API
type OpenAIResponseTokenDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// OpenAIResponsesStreamChunk represents a streaming chunk from Responses API
type OpenAIResponsesStreamChunk struct {
	Type  string               `json:"type"`
	Delta string               `json:"delta,omitempty"`
	Event OpenAIResponsesEvent `json:"event,omitempty"`
}

// OpenAIResponsesEvent represents an event in Responses API streaming
type OpenAIResponsesEvent struct {
	ID        string                 `json:"id,omitempty"`
	Object    string                 `json:"object,omitempty"`
	CreatedAt float64                `json:"created_at,omitempty"`
	Model     string                 `json:"model,omitempty"`
	Usage     *OpenAIResponsesUsage  `json:"usage,omitempty"`
	Output    []OpenAIResponseOutput `json:"output,omitempty"`
	Status    string                 `json:"status,omitempty"`
}

// ParseResponseMetadata extracts tokens and model information from OpenAI responses
func (o *OpenAIProxy) ParseResponseMetadata(responseBody io.Reader, isStreaming bool) (*LLMResponseMetadata, error) {
	// Create a buffer to read the response body
	bodyBytes, err := io.ReadAll(responseBody)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// For streaming responses, use unified parsing
	if isStreaming {
		reader := bytes.NewReader(bodyBytes)
		return o.parseUnifiedStreamingResponse(reader)
	}

	// For non-streaming, check the structure
	// Responses API has "output" field while Chat Completions has "choices"
	var checkResponse map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &checkResponse); err == nil {
		if _, hasOutput := checkResponse["output"]; hasOutput {
			// This is a Responses API response
			reader := bytes.NewReader(bodyBytes)
			return o.parseResponsesNonStreamingResponse(reader)
		}
	}

	// Traditional Chat Completions API response
	reader := bytes.NewReader(bodyBytes)
	return o.parseNonStreamingResponse(reader)
}

// parseNonStreamingResponse handles standard OpenAI JSON responses
func (o *OpenAIProxy) parseNonStreamingResponse(responseBody io.Reader) (*LLMResponseMetadata, error) {
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

	// Log the response body preview for debugging
	log.Printf("🔍 Debug: OpenAI response body preview: %s", string(bodyBytes[:min(100, len(bodyBytes))]))

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

// parseUnifiedStreamingResponse handles both Responses API and Chat Completions API streaming responses
// by determining the API type on the first data line and processing accordingly
func (o *OpenAIProxy) parseUnifiedStreamingResponse(responseBody io.Reader) (*LLMResponseMetadata, error) {
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
	var metadata *LLMResponseMetadata
	var model string
	var requestID string
	var finishReason string
	var hasData bool
	var chunkCount int
	var apiType string // "responses" or "completions"
	var thoughtTokens int

	log.Printf("🔄 OpenAI: Starting to parse unified streaming response")

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

		// Determine API type on the first data line if not already determined
		if apiType == "" {
			apiType = o.detectAPIType(jsonData)
		}

		// Process based on determined API type
		if apiType == "responses" {
			metadata, model, requestID, finishReason, thoughtTokens = o.parseResponsesStreamingChunk(jsonData, model, requestID, finishReason, thoughtTokens)
		} else {
			metadata, model, requestID, finishReason = o.parseCompletionsStreamingChunk(jsonData, model, requestID, finishReason)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading streaming response: %w", err)
	}

	log.Printf("🔄 OpenAI: Processed %d chunks, hasData: %v, hasUsage: %v, API Type: %s", chunkCount, hasData, metadata != nil, apiType)

	// If we have usage metadata, return it
	if metadata != nil {
		return metadata, nil
	}

	// If we found streaming data but no usage information, return partial metadata
	// This can happen when the stream is not yet complete or usage info is in a later chunk
	if hasData && (model != "" || requestID != "") {
		log.Printf("🔄 OpenAI: Returning partial metadata - usage data not yet available")
		return &LLMResponseMetadata{
			Model:         model,
			InputTokens:   0, // Unknown at this point
			OutputTokens:  0, // Unknown at this point
			TotalTokens:   0, // Unknown at this point
			Provider:      "openai",
			RequestID:     requestID,
			IsStreaming:   true,
			FinishReason:  finishReason,
			ThoughtTokens: thoughtTokens,
		}, nil
	}

	return nil, fmt.Errorf("no usage information found in streaming response")
}

// detectAPIType determines whether the streaming response is from Responses API or Chat Completions API
func (o *OpenAIProxy) detectAPIType(jsonData string) string {
	var checkData map[string]interface{}
	if err := json.Unmarshal([]byte(jsonData), &checkData); err == nil {
		// Responses API streaming has "type" field (e.g., "response.output_text.delta")
		// or has "output" field in the event
		if typeField, hasType := checkData["type"].(string); hasType &&
			strings.HasPrefix(typeField, "response.") {
			log.Printf("🔄 OpenAI: Detected Responses API based on type field: %s", typeField)
			return "responses"
		} else if _, hasOutput := checkData["output"]; hasOutput {
			log.Printf("🔄 OpenAI: Detected Responses API based on output field")
			return "responses"
		} else if _, hasChoices := checkData["choices"]; hasChoices {
			log.Printf("🔄 OpenAI: Detected Chat Completions API based on choices field")
			return "completions"
		}
	}
	return ""
}

// parseResponsesStreamingChunk processes a single streaming chunk from the Responses API
func (o *OpenAIProxy) parseResponsesStreamingChunk(jsonData string, model, requestID, finishReason string, thoughtTokens int) (*LLMResponseMetadata, string, string, string, int) {
	// Try to parse the chunk - could be an event or a delta
	var chunkData map[string]interface{}
	if err := json.Unmarshal([]byte(jsonData), &chunkData); err != nil {
		// Log error but continue processing other chunks
		log.Printf("Warning: failed to parse Responses API streaming chunk: %v", err)
		return nil, model, requestID, finishReason, thoughtTokens
	}

	// Check the type field to understand what kind of chunk this is
	typeField, hasType := chunkData["type"].(string)
	if hasType {
		// Handle different types of Responses API streaming chunks
		if typeField == "response.created" {
			// This is the final event with usage information
			log.Printf("🔄 OpenAI Responses API: Found response.created event")
			log.Printf("🔄 OpenAI Responses API: Full chunk data: %+v", chunkData)

			// Extract usage information from the response field
			if responseField, hasResponse := chunkData["response"].(map[string]interface{}); hasResponse {
				log.Printf("🔄 OpenAI Responses API: Response field found: %+v", responseField)
				// Capture model and request ID
				if model == "" {
					if modelVal, ok := responseField["model"].(string); ok {
						model = modelVal
						log.Printf("🔄 OpenAI Responses API: Captured model: %s", model)
					}
				}
				if requestID == "" {
					if idVal, ok := responseField["id"].(string); ok {
						requestID = idVal
						log.Printf("🔄 OpenAI Responses API: Captured request ID: %s", requestID)
					}
				}

				// Extract usage information
				if usageField, hasUsage := responseField["usage"].(map[string]interface{}); hasUsage {
					inputTokens := 0
					outputTokens := 0
					totalTokens := 0
					reasoningTokens := 0

					if inputVal, ok := usageField["input_tokens"].(float64); ok {
						inputTokens = int(inputVal)
					}
					if outputVal, ok := usageField["output_tokens"].(float64); ok {
						outputTokens = int(outputVal)
					}
					if totalVal, ok := usageField["total_tokens"].(float64); ok {
						totalTokens = int(totalVal)
					}
					if outputDetails, ok := usageField["output_tokens_details"].(map[string]interface{}); ok {
						if reasoningVal, ok := outputDetails["reasoning_tokens"].(float64); ok {
							reasoningTokens = int(reasoningVal)
						}
					}

					if inputTokens > 0 || outputTokens > 0 || totalTokens > 0 {
						log.Printf("🔄 OpenAI Responses API: Found usage data in response.created! Input: %d, Output: %d, Total: %d, Reasoning: %d",
							inputTokens, outputTokens, totalTokens, reasoningTokens)

						// Extract finish reason from output if available
						if outputField, hasOutput := responseField["output"].([]interface{}); hasOutput {
							for _, output := range outputField {
								if outputMap, ok := output.(map[string]interface{}); ok {
									if status, ok := outputMap["status"].(string); ok && status != "" && status != "in_progress" {
										finishReason = status
										log.Printf("🔄 OpenAI Responses API: Captured finish reason: %s", finishReason)
										break
									}
								}
							}
						}

						metadata := &LLMResponseMetadata{
							Model:         model,
							InputTokens:   inputTokens,
							OutputTokens:  outputTokens,
							TotalTokens:   totalTokens,
							Provider:      "openai",
							RequestID:     requestID,
							IsStreaming:   true,
							FinishReason:  finishReason,
							ThoughtTokens: reasoningTokens,
						}
						thoughtTokens = reasoningTokens
						return metadata, model, requestID, finishReason, thoughtTokens
					}
				}
			}
		} else if strings.HasPrefix(typeField, "response.") {
			// This is a delta chunk (e.g., "response.output_text.delta")
			// Skip these for metadata extraction as they don't contain usage info
			return nil, model, requestID, finishReason, thoughtTokens
		}
	}

	// Try to parse as a full event with usage data (fallback for other event types)
	var event OpenAIResponsesEvent
	if err := json.Unmarshal([]byte(jsonData), &event); err != nil {
		// Not a full event, continue
		return nil, model, requestID, finishReason, thoughtTokens
	}

	// Capture model and request ID from any chunk
	if model == "" && event.Model != "" {
		model = event.Model
		log.Printf("🔄 OpenAI Responses API: Captured model: %s", model)
	}
	if requestID == "" && event.ID != "" {
		requestID = event.ID
		log.Printf("🔄 OpenAI Responses API: Captured request ID: %s", requestID)
	}

	// Extract finish reason from output
	for _, output := range event.Output {
		if output.Status != "" && output.Status != "in_progress" {
			finishReason = output.Status
			log.Printf("🔄 OpenAI Responses API: Captured finish reason: %s", finishReason)
		}
	}

	var metadata *LLMResponseMetadata

	// The usage information is typically in the final event
	if event.Usage != nil {
		reasoningTokens := 0
		if event.Usage.OutputTokensDetails != nil {
			reasoningTokens = event.Usage.OutputTokensDetails.ReasoningTokens
		}

		log.Printf("🔄 OpenAI Responses API: Found usage data! Input: %d, Output: %d, Total: %d, Reasoning: %d",
			event.Usage.InputTokens, event.Usage.OutputTokens, event.Usage.TotalTokens, reasoningTokens)

		metadata = &LLMResponseMetadata{
			Model:         model,
			InputTokens:   event.Usage.InputTokens,
			OutputTokens:  event.Usage.OutputTokens,
			TotalTokens:   event.Usage.TotalTokens,
			Provider:      "openai",
			RequestID:     requestID,
			IsStreaming:   true,
			FinishReason:  finishReason,
			ThoughtTokens: reasoningTokens,
		}
		thoughtTokens = reasoningTokens
	}

	return metadata, model, requestID, finishReason, thoughtTokens
}

// parseCompletionsStreamingChunk processes a single streaming chunk from the Chat Completions API
func (o *OpenAIProxy) parseCompletionsStreamingChunk(jsonData string, model, requestID, finishReason string) (*LLMResponseMetadata, string, string, string) {
	var streamResponse OpenAIStreamResponse
	if err := json.Unmarshal([]byte(jsonData), &streamResponse); err != nil {
		// Log error but continue processing other chunks
		log.Printf("Warning: failed to parse streaming chunk: %v", err)
		return nil, model, requestID, finishReason
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

	var metadata *LLMResponseMetadata

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

	return metadata, model, requestID, finishReason
}

// parseResponsesNonStreamingResponse handles Responses API non-streaming responses
func (o *OpenAIProxy) parseResponsesNonStreamingResponse(responseBody io.Reader) (*LLMResponseMetadata, error) {
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

	// Log the response body preview for debugging
	log.Printf("🔍 Debug: OpenAI Responses API response body preview: %s", string(bodyBytes[:min(100, len(bodyBytes))]))

	var response OpenAIResponsesAPIResponse
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		return nil, fmt.Errorf("failed to parse OpenAI Responses API response: %w", err)
	}

	// Calculate output tokens including reasoning tokens if present
	outputTokens := response.Usage.OutputTokens
	reasoningTokens := 0
	if response.Usage.OutputTokensDetails != nil {
		reasoningTokens = response.Usage.OutputTokensDetails.ReasoningTokens
	}

	metadata := &LLMResponseMetadata{
		Model:         response.Model,
		InputTokens:   response.Usage.InputTokens,
		OutputTokens:  outputTokens,
		TotalTokens:   response.Usage.TotalTokens,
		Provider:      "openai",
		RequestID:     response.ID,
		IsStreaming:   false,
		ThoughtTokens: reasoningTokens,
	}

	// Extract finish reason from the output if available
	for _, output := range response.Output {
		if output.Type == "message" && output.Status != "" {
			metadata.FinishReason = output.Status
			break
		}
	}

	return metadata, nil
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

// ValidateAPIKey validates and potentially replaces the API key in the request
func (o *OpenAIProxy) ValidateAPIKey(req *http.Request, keyStore APIKeyStore) error {
	// Get the API key from the Authorization header
	authHeader := req.Header.Get("Authorization")
	if authHeader == "" {
		// No API key provided, let the provider handle the error
		return nil
	}

	// Extract the API key from "Bearer <key>" format
	const bearerPrefix = "Bearer "
	if !strings.HasPrefix(authHeader, bearerPrefix) {
		// Not in expected format, let it through as-is
		return nil
	}

	apiKey := strings.TrimPrefix(authHeader, bearerPrefix)

	// Validate and potentially replace the key
	actualKey, provider, err := keyStore.ValidateAndGetActualKey(context.Background(), apiKey)
	if err != nil {
		return fmt.Errorf("API key validation failed: %w", err)
	}

	// If a provider was returned, verify it matches
	if provider != "" && provider != "openai" {
		return fmt.Errorf("API key is for provider %s, not openai", provider)
	}

	// Replace the key in the request header if it was translated
	if actualKey != apiKey {
		req.Header.Set("Authorization", bearerPrefix+actualKey)
		log.Printf("🔑 OpenAI: Translated API key from iw: format")
	}

	return nil
}
