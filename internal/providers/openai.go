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

	"github.com/Instawork/llm-proxy/internal/proxylog"
	"github.com/Instawork/llm-proxy/internal/redact"
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

// NewOpenAIProxy creates a new OpenAI reverse proxy.
// Pass a ProxyOptions value to override defaults (e.g. DisableGzip: true for
// debug builds that need plain-text SSE wire bytes).
func NewOpenAIProxy(opts ...ProxyOptions) *OpenAIProxy {
	var opt ProxyOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	// Parse the OpenAI API URL. openAIBaseURL is a package constant, so a
	// parse failure here means the constant itself is malformed — a
	// programmer error that should produce a stack trace, not a silent
	// log.Fatalf that bypasses defers and tests.
	targetURL, err := url.Parse(openAIBaseURL)
	if err != nil {
		panic(fmt.Sprintf("invalid openAIBaseURL constant %q: %v", openAIBaseURL, err))
	}

	// Create the reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Create the OpenAI proxy instance
	openAIProxy := &OpenAIProxy{proxy: proxy}

	// Use the generic director function to handle common proxy logic
	originalDirector := proxy.Director
	proxy.Director = CreateGenericDirector(openAIProxy, targetURL, originalDirector, opt.DisableGzip)

	// Customize the transport for optimal streaming performance
	proxy.Transport = newProxyTransport(opt.DisableGzip, opt.ResponseHeaderTimeout)

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
		proxylog.Upstream("openai reverse proxy transport error: %v", err)

		// For streaming requests, we need to handle errors differently
		if openAIProxy.IsStreamingRequest(r) {
			// If we're in a streaming context, we might have already started writing
			// the response, so we need to handle this gracefully
			proxylog.Upstream("openai streaming transport error: %v", err)

			// Try to write an error in SSE format if possible
			if w.Header().Get("Content-Type") == "" {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set(proxylog.HeaderErrorSource, proxylog.ErrorSourceUpstream)
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprint(w, proxylog.UpstreamSSEDataLine("openai transport: %v", err))
				fmt.Fprintf(w, "data: [DONE]\n\n")
			} else {
				// Headers already sent, just log the error
				proxylog.Proxy("openai cannot send error response, headers already sent")
			}
		} else {
			proxylog.UpstreamHTTPError(w, fmt.Sprintf("openai transport: %v", err), http.StatusBadGateway)
		}
	}

	return openAIProxy
}

// GetName returns the name of the provider
func (o *OpenAIProxy) GetName() string {
	return "openai"
}

// IsStreamingRequest checks if the request is likely to be a streaming
// request for OpenAI. This assumes the caller has already routed the request
// to OpenAI; the cross-provider OR-pattern is constrained at the
// ProviderManager layer (see ProviderManager.IsStreamingRequest), which
// resolves provider-from-path before consulting any provider — so e.g. a
// /health request with Accept: text/event-stream no longer flips into the
// streaming code path.
func (o *OpenAIProxy) IsStreamingRequest(req *http.Request) bool {
	if strings.Contains(req.Header.Get("Accept"), "text/event-stream") {
		return true
	}

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
			proxylog.Proxy("openai streaming check: error getting cached request body: %v", err)
			return false
		}
		defer bodyReader.Close()
		bodyBytes, err = io.ReadAll(bodyReader)
		if err != nil {
			proxylog.Proxy("openai streaming check: error reading cached request body: %v", err)
			return false
		}
	} else {
		// Read the body for the first time
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			proxylog.Proxy("openai streaming check: error reading request body: %v", err)
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
		proxylog.Proxy("openai streaming check: error parsing request body JSON: %v", err)
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

// WrapTransport replaces the proxy's transport with the result of calling fn
// on the current transport.  Call this after construction to inject circuit
// breaking, tracing, or other transport-level middleware.
func (o *OpenAIProxy) WrapTransport(fn func(http.RoundTripper) http.RoundTripper) {
	o.proxy.Transport = fn(o.proxy.Transport)
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

	// Operator debug preview, routed through redact.LogPreview so that
	// (1) when pii_redact is enabled, PII is collapsed to ``[REDACTED:...]``
	// markers, and (2) when it isn't, the line still gives byte-length
	// context but never raw model output. See internal/redact/log_helper.go.
	log.Printf("🔍 Debug: OpenAI response body preview: %s",
		redact.LogPreview(context.Background(), string(bodyBytes), 100))

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
	// Allow lines up to 2 MB — large tool call / responses-API deltas can be wide.
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)

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
		proxylog.Proxy("openai failed to parse Responses API streaming chunk: %v", err)
		return nil, model, requestID, finishReason, thoughtTokens
	}

	// Check the type field to understand what kind of chunk this is
	typeField, hasType := chunkData["type"].(string)
	if hasType {
		// Handle different types of Responses API streaming chunks
		if typeField == "response.created" || typeField == "response.done" {
			// These events may contain usage information
			log.Printf("🔄 OpenAI Responses API: Found %s event", typeField)
			// Routed through redact.LogPreview: the chunk carries model
			// output text, so never dump it raw. See log_helper.go.
			log.Printf("🔄 OpenAI Responses API: Full chunk data: %s",
				redact.LogPreview(context.Background(), fmt.Sprintf("%+v", chunkData), 200))

			// Try to extract usage information from either response or event field
			var dataField map[string]interface{}
			var fieldName string

			// Check for response field first (actual API format)
			if responseField, hasResponse := chunkData["response"].(map[string]interface{}); hasResponse {
				dataField = responseField
				fieldName = "response"
				log.Printf("🔄 OpenAI Responses API: Response field found: %s",
					redact.LogPreview(context.Background(), fmt.Sprintf("%+v", responseField), 200))
			} else if eventField, hasEvent := chunkData["event"].(map[string]interface{}); hasEvent {
				// Fallback to event field (test data format)
				dataField = eventField
				fieldName = "event"
				log.Printf("🔄 OpenAI Responses API: Event field found: %s",
					redact.LogPreview(context.Background(), fmt.Sprintf("%+v", eventField), 200))
			}

			if dataField != nil {
				// Capture model and request ID
				if model == "" {
					if modelVal, ok := dataField["model"].(string); ok {
						model = modelVal
						log.Printf("🔄 OpenAI Responses API: Captured model: %s", model)
					}
				}
				if requestID == "" {
					if idVal, ok := dataField["id"].(string); ok {
						requestID = idVal
						log.Printf("🔄 OpenAI Responses API: Captured request ID: %s", requestID)
					}
				}

				// Extract usage information
				if usageField, hasUsage := dataField["usage"].(map[string]interface{}); hasUsage {
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
						log.Printf("🔄 OpenAI Responses API: Found usage data in %s (%s field)! Input: %d, Output: %d, Total: %d, Reasoning: %d",
							typeField, fieldName, inputTokens, outputTokens, totalTokens, reasoningTokens)

						// Extract finish reason from output if available
						if outputField, hasOutput := dataField["output"].([]interface{}); hasOutput {
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
		proxylog.Proxy("openai failed to parse streaming chunk: %v", err)
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

	// See companion preview above (ParseResponseMetadata) — same rationale.
	log.Printf("🔍 Debug: OpenAI Responses API response body preview: %s",
		redact.LogPreview(context.Background(), string(bodyBytes), 100))

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
		proxylog.Proxy("openai user ID extraction: error reading request body: %v", err)
		return ""
	}

	if len(bodyBytes) == 0 {
		return ""
	}

	// Parse JSON to extract user field
	var data map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		proxylog.Proxy("openai user ID extraction: error parsing request JSON: %v", err)
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

// ExtractRequestModelAndMessages extracts model and message text content from OpenAI request bodies
// Supports Chat Completions and Responses API minimal structures. Restores req.Body if read.
func (o *OpenAIProxy) ExtractRequestModelAndMessages(req *http.Request) (string, []string) {
	// Only attempt for OpenAI endpoints and POST requests
	if req == nil || req.Method != "POST" {
		return "", nil
	}
	path := req.URL.Path
	isOpenAIEndpoint := strings.HasPrefix(path, "/openai/") ||
		strings.HasPrefix(path, "/v1/chat/") ||
		strings.HasPrefix(path, "/v1/responses") ||
		strings.HasPrefix(path, "/v1/completions")
	if !isOpenAIEndpoint {
		return "", nil
	}

	// Reuse existing safe body reader
	bodyBytes, err := o.readRequestBodyForUserID(req)
	if err != nil || len(bodyBytes) == 0 {
		return "", nil
	}

	var data map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		return "", nil
	}

	// Model
	model := ""
	if mv, ok := data["model"].(string); ok {
		model = mv
	}

	messages := make([]string, 0, 8)

	// Chat Completions style: messages: [{ role, content }]
	if rawMsgs, ok := data["messages"].([]interface{}); ok {
		for _, m := range rawMsgs {
			if msg, ok := m.(map[string]interface{}); ok {
				if contentStr, ok := msg["content"].(string); ok && contentStr != "" {
					messages = append(messages, contentStr)
					continue
				}
				// content can be array for multimodal; collect text parts
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

	// Responses API: input can be string or array of blocks {type, text}
	if inputStr, ok := data["input"].(string); ok && inputStr != "" {
		messages = append(messages, inputStr)
	} else if inputArr, ok := data["input"].([]interface{}); ok {
		for _, it := range inputArr {
			if m, ok := it.(map[string]interface{}); ok {
				if t, ok := m["type"].(string); ok && (t == "input_text" || t == "text") {
					if txt, ok := m["text"].(string); ok && txt != "" {
						messages = append(messages, txt)
					}
				}
			} else if s, ok := it.(string); ok && s != "" {
				messages = append(messages, s)
			}
		}
	}

	// Legacy completions: prompt
	if prompt, ok := data["prompt"].(string); ok && prompt != "" {
		messages = append(messages, prompt)
	}

	return model, messages
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

	// Validate and potentially replace the key. Use the request context so
	// client cancellation / handler-level deadlines propagate into the
	// DynamoDB validation lookup.
	actualKey, provider, err := keyStore.ValidateAndGetActualKey(req.Context(), apiKey)
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
