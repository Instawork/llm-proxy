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
	// Gemini API base URL
	geminiBaseURL = "https://generativelanguage.googleapis.com"
)

// GeminiProxy handles Gemini API requests and implements the Provider interface
type GeminiProxy struct {
	proxy *httputil.ReverseProxy
}

// NewGeminiProxy creates a new Gemini reverse proxy
func NewGeminiProxy() *GeminiProxy {
	// Parse the Gemini API URL
	targetURL, err := url.Parse(geminiBaseURL)
	if err != nil {
		log.Fatalf("Failed to parse Gemini API URL: %v", err)
	}

	// Create the reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Create the Gemini proxy instance
	geminiProxy := &GeminiProxy{proxy: proxy}

	// Customize the director function to modify requests
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		
		// Set the Host header to the target host
		req.Host = targetURL.Host
		
		// Strip the /gemini prefix from the path before forwarding to Gemini
		if strings.HasPrefix(req.URL.Path, "/gemini/") {
			req.URL.Path = strings.TrimPrefix(req.URL.Path, "/gemini")
		}
		
		// Log the request, including streaming detection
		isStreaming := geminiProxy.IsStreamingRequest(req)
		if isStreaming {
			log.Printf("Proxying Gemini streaming request: %s %s", req.Method, req.URL.Path)
		} else {
			log.Printf("Proxying Gemini request: %s %s", req.Method, req.URL.Path)
		}
	}

	// Customize the transport for optimal streaming performance
	proxy.Transport = newProxyTransport()

	// Add custom response modifier for streaming support
	proxy.ModifyResponse = func(resp *http.Response) error {
		// Handle streaming responses
		if geminiProxy.isStreamingResponse(resp) {
			log.Printf("Detected streaming response from Gemini")
			
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
		log.Printf("Gemini proxy error: %v", err)
		
		// For streaming requests, we need to handle errors differently
		if geminiProxy.IsStreamingRequest(r) {
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
			fmt.Fprintf(w, "Gemini proxy error: %v", err)
		}
	}

	return geminiProxy
}

// GetName returns the name of the provider
func (g *GeminiProxy) GetName() string {
	return "gemini"
}

// IsStreamingRequest checks if the request is likely to be a streaming request for Gemini
func (g *GeminiProxy) IsStreamingRequest(req *http.Request) bool {
	// Check for streaming in the Accept header first (fast check)
	if strings.Contains(req.Header.Get("Accept"), "text/event-stream") {
		return true
	}
	
	// Check if this is a Gemini-related endpoint (original or compatibility routes)
	isGeminiEndpoint := strings.HasPrefix(req.URL.Path, "/gemini/") ||
		strings.HasPrefix(req.URL.Path, "/v1beta/models/gemini") ||
		strings.HasPrefix(req.URL.Path, "/v1/models/gemini")
	
	if !isGeminiEndpoint {
		return false
	}
	
	// Check for alt=sse query parameter (Gemini SSE streaming format)
	if req.URL.Query().Get("alt") == "sse" {
		return true
	}
	
	// For Gemini generateContent endpoints, check the request body for stream: true
	// Also check for streamGenerateContent in the URL (alternative streaming endpoint)
	if req.Method == "POST" && (strings.Contains(req.URL.Path, ":generateContent") || 
		strings.Contains(req.URL.Path, ":streamGenerateContent")) {
		
		// Check if it's the explicit streaming endpoint
		if strings.Contains(req.URL.Path, ":streamGenerateContent") {
			return true
		}
		
		// For generateContent, check the request body for stream parameter
		return g.checkStreamingInBody(req)
	}
	
	return false
}

// checkStreamingInBody reads the request body to check for "stream": true
func (g *GeminiProxy) checkStreamingInBody(req *http.Request) bool {
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
func (g *GeminiProxy) isStreamingResponse(resp *http.Response) bool {
	contentType := resp.Header.Get("Content-Type")
	return strings.Contains(contentType, "text/event-stream") || 
		   strings.Contains(contentType, "application/x-ndjson") ||
		   strings.Contains(contentType, "text/plain")
}

// RegisterRoutes registers Gemini-specific routes with the given router
func (g *GeminiProxy) RegisterRoutes(r *mux.Router) {
	// Handle generateContent endpoint (primary endpoint for Gemini)
	r.PathPrefix("/gemini/v1/models/{model}:generateContent").Handler(g.proxy).Methods("POST", "OPTIONS")
	
	// Handle streamGenerateContent endpoint (explicit streaming endpoint)
	r.PathPrefix("/gemini/v1/models/{model}:streamGenerateContent").Handler(g.proxy).Methods("POST", "OPTIONS")
	
	// Handle other Gemini API endpoints
	r.PathPrefix("/gemini/v1/").Handler(g.proxy).Methods("GET", "POST", "PUT", "DELETE", "OPTIONS")
	
	// Add specific compatibility routes for v1beta and v1 with gemini model paths
	// These routes provide more specific matching for common Gemini usage patterns
	
	// v1beta routes for Gemini models
	r.PathPrefix("/v1beta/models/gemini").Handler(g.proxy).Methods("GET", "POST", "PUT", "DELETE", "OPTIONS")
	
	// v1 routes for Gemini models  
	r.PathPrefix("/v1/models/gemini").Handler(g.proxy).Methods("GET", "POST", "PUT", "DELETE", "OPTIONS")
	
	log.Printf("Gemini routes registered:")
	log.Printf("  - /gemini/v1/ (original routes with streaming support)")
	log.Printf("  - /v1beta/models/gemini* (compatibility routes)")
	log.Printf("  - /v1/models/gemini* (compatibility routes)")
}

// GetHealthStatus returns the health status of the Gemini proxy
func (g *GeminiProxy) GetHealthStatus() map[string]interface{} {
	return map[string]interface{}{
		"provider":          "gemini",
		"status":            "healthy",
		"baseURL":           geminiBaseURL,
		"streaming_support": true,
		"body_parsing":      true,
		"sse_support":       true,
	}
}

// EstimateTokensFromRequest estimates tokens for Gemini requests
func (g *GeminiProxy) EstimateTokensFromRequest(req *http.Request) int64 {
	// Extract model from URL path (Gemini includes model in path)
	model := g.extractModelFromPath(req.URL.Path)
	
	// Try to extract content from request body
	bodyBytes, err := g.readRequestBody(req)
	if err != nil {
		log.Printf("Error reading request body for Gemini token estimation: %v", err)
		return g.getDefaultTokenEstimate(req.URL.Path, model)
	}
	
	if len(bodyBytes) == 0 {
		return g.getDefaultTokenEstimate(req.URL.Path, model)
	}
	
	// Parse JSON to extract contents
	var data map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		log.Printf("Error parsing Gemini request JSON for token estimation: %v", err)
		return g.getDefaultTokenEstimate(req.URL.Path, model)
	}
	
	// Estimate input tokens based on contents
	inputTokens := int64(0)
	
	// Check for contents array (Gemini format)
	if contents, ok := data["contents"].([]interface{}); ok {
		inputTokens = g.estimateTokensFromContents(contents, model)
	} else {
		// Fallback to body size estimation
		inputTokens = g.estimateTokensFromBodySize(len(bodyBytes), model)
	}
	
	// Add system instruction tokens if present
	if systemInstruction, ok := data["systemInstruction"].(map[string]interface{}); ok {
		if parts, ok := systemInstruction["parts"].([]interface{}); ok {
			inputTokens += g.estimateTokensFromParts(parts, model)
		}
	}
	
	// Estimate output tokens based on generation config
	outputTokens := int64(0)
	if generationConfig, ok := data["generationConfig"].(map[string]interface{}); ok {
		if maxOutputTokens, ok := generationConfig["maxOutputTokens"].(float64); ok {
			outputTokens = int64(maxOutputTokens * 0.5) // Estimate 50% of max will be used
		}
	}
	
	if outputTokens == 0 {
		outputTokens = g.getDefaultOutputEstimate(model)
	}
	
	totalTokens := inputTokens + outputTokens
	
	// Apply model-specific adjustments
	totalTokens = g.applyModelAdjustments(totalTokens, model)
	
	log.Printf("🤖 Gemini token estimation: %d tokens (%d input + %d output) for model %s", 
		totalTokens, inputTokens, outputTokens, model)
	return totalTokens
}

// Helper methods for Gemini token estimation

func (g *GeminiProxy) extractModelFromPath(path string) string {
	// Extract model from path like /gemini/v1/models/{model}:generateContent
	if strings.Contains(path, "/models/") {
		parts := strings.Split(path, "/models/")
		if len(parts) > 1 {
			modelPart := strings.Split(parts[1], ":")[0]
			return modelPart
		}
	}
	return ""
}

func (g *GeminiProxy) readRequestBody(req *http.Request) ([]byte, error) {
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

func (g *GeminiProxy) estimateTokensFromContents(contents []interface{}, model string) int64 {
	totalTokens := int64(0)
	
	for _, content := range contents {
		if contentMap, ok := content.(map[string]interface{}); ok {
			if parts, ok := contentMap["parts"].([]interface{}); ok {
				totalTokens += g.estimateTokensFromParts(parts, model)
			}
			// Add overhead for content structure (role, etc.)
			totalTokens += 2
		}
	}
	
	// Add overhead for contents array format
	totalTokens += int64(len(contents)) * 2
	return totalTokens
}

func (g *GeminiProxy) estimateTokensFromParts(parts []interface{}, model string) int64 {
	totalTokens := int64(0)
	
	for _, part := range parts {
		if partMap, ok := part.(map[string]interface{}); ok {
			if text, ok := partMap["text"].(string); ok {
				totalTokens += g.estimateTokensFromText(text, model)
			}
			// Add overhead for other part types (images, files, etc.)
			// Images and files typically require more tokens
			if _, hasImage := partMap["inlineData"]; hasImage {
				totalTokens += 100 // Base cost for image processing
			}
			if _, hasFile := partMap["fileData"]; hasFile {
				totalTokens += 50 // Base cost for file processing
			}
		}
	}
	
	return totalTokens
}

func (g *GeminiProxy) estimateTokensFromText(text string, model string) int64 {
	// Gemini's general rule: ~3.8 characters per token (similar to GPT but varies by model)
	charsPerToken := 3.8
	
	// Adjust for different Gemini models
	if strings.Contains(model, "gemini-1.5-pro") {
		charsPerToken = 3.6 // Pro is more efficient
	} else if strings.Contains(model, "gemini-1.5-flash") {
		charsPerToken = 4.0 // Flash optimizes for speed over token efficiency
	} else if strings.Contains(model, "gemini-1.0-pro") {
		charsPerToken = 3.9 // Original Pro model
	}
	
	return int64(float64(len(text)) / charsPerToken)
}

func (g *GeminiProxy) estimateTokensFromBodySize(bodySize int, model string) int64 {
	// Fallback estimation from body size
	// Assume about 65% of body is actual content (Gemini has more nested structure)
	estimatedContentSize := float64(bodySize) * 0.65
	return int64(estimatedContentSize / 3.8)
}

func (g *GeminiProxy) getDefaultOutputEstimate(model string) int64 {
	// Default output token estimates based on typical usage patterns
	if strings.Contains(model, "gemini-1.5-pro") {
		return 180 // Pro gives detailed responses
	} else if strings.Contains(model, "gemini-1.5-flash") {
		return 120 // Flash is more concise and faster
	} else if strings.Contains(model, "gemini-1.0-pro") {
		return 150 // Original model
	}
	return 130
}

func (g *GeminiProxy) getDefaultTokenEstimate(path string, model string) int64 {
	// Endpoint-specific defaults
	if strings.Contains(path, ":generateContent") || strings.Contains(path, ":streamGenerateContent") {
		if strings.Contains(model, "gemini-1.5-pro") {
			return 220
		} else if strings.Contains(model, "gemini-1.5-flash") {
			return 140
		} else if strings.Contains(model, "gemini-1.0-pro") {
			return 180
		}
		return 160
	}
	return 80
}

func (g *GeminiProxy) applyModelAdjustments(tokens int64, model string) int64 {
	// Apply model-specific multipliers for accuracy
	if strings.Contains(model, "gemini-1.5-pro") {
		// Pro handles more complex requests and gives detailed responses
		tokens = int64(float64(tokens) * 1.1)
	} else if strings.Contains(model, "gemini-1.5-flash") {
		// Flash is optimized for speed and efficiency
		tokens = int64(float64(tokens) * 0.85)
	}
	
	// Cap at reasonable maximum to prevent over-consumption
	if tokens > 12000 {
		tokens = 12000
	}
	
	// Ensure reasonable minimum
	if tokens < 10 {
		tokens = 10
	}
	
	return tokens
}

// GeminiResponse represents the structure of Gemini API responses
type GeminiResponse struct {
	Candidates    []GeminiCandidate `json:"candidates"`
	UsageMetadata GeminiUsage       `json:"usageMetadata"`
	ModelVersion  string            `json:"modelVersion,omitempty"`
	ResponseId    string            `json:"responseId,omitempty"`
}

// GeminiUsage represents token usage in Gemini responses
type GeminiUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
	ThoughtsTokenCount   int `json:"thoughtsTokenCount,omitempty"`
}

// GeminiCandidate represents a candidate response
type GeminiCandidate struct {
	Content      GeminiContent `json:"content"`
	FinishReason string        `json:"finishReason"`
	Index        int           `json:"index"`
}

// GeminiContent represents content in Gemini responses
type GeminiContent struct {
	Parts []GeminiPart `json:"parts"`
	Role  string       `json:"role"`
}

// GeminiPart represents a part of content
type GeminiPart struct {
	Text string `json:"text"`
}

// GeminiStreamResponse represents streaming response chunks
type GeminiStreamResponse struct {
	Candidates    []GeminiCandidate `json:"candidates"`
	UsageMetadata *GeminiUsage      `json:"usageMetadata,omitempty"`
	ModelVersion  string            `json:"modelVersion,omitempty"`
}

// ParseResponseMetadata extracts tokens and model information from Gemini responses
func (g *GeminiProxy) ParseResponseMetadata(responseBody io.Reader, isStreaming bool) (*LLMResponseMetadata, error) {
	if isStreaming {
		return g.parseStreamingResponse(responseBody)
	}
	return g.parseNonStreamingResponse(responseBody)
}

// parseNonStreamingResponse handles standard Gemini JSON responses
func (g *GeminiProxy) parseNonStreamingResponse(responseBody io.Reader) (*LLMResponseMetadata, error) {
	bodyBytes, err := io.ReadAll(responseBody)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	var response GeminiResponse
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		return nil, fmt.Errorf("failed to parse Gemini response: %w", err)
	}

	// Extract model from model version or use default
	model := response.ModelVersion
	if model == "" {
		model = "gemini" // fallback if model version is not provided
	}

	metadata := &LLMResponseMetadata{
		Model:         model,
		InputTokens:   response.UsageMetadata.PromptTokenCount,
		OutputTokens:  response.UsageMetadata.CandidatesTokenCount,
		TotalTokens:   response.UsageMetadata.TotalTokenCount,
		ThoughtTokens: response.UsageMetadata.ThoughtsTokenCount,
		Provider:      "gemini",
		RequestID:     response.ResponseId,
		IsStreaming:   false,
	}

	// Extract finish reason from the first candidate if available
	if len(response.Candidates) > 0 {
		metadata.FinishReason = response.Candidates[0].FinishReason
	}

	return metadata, nil
}

// parseStreamingResponse handles Gemini server-sent events
func (g *GeminiProxy) parseStreamingResponse(responseBody io.Reader) (*LLMResponseMetadata, error) {
	scanner := bufio.NewScanner(responseBody)
	var metadata *LLMResponseMetadata
	var model string
	var finishReason string
	var hasData bool

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
		
		var streamResponse GeminiStreamResponse
		if err := json.Unmarshal([]byte(jsonData), &streamResponse); err != nil {
			// Log error but continue processing other chunks
			log.Printf("Warning: failed to parse Gemini streaming chunk: %v", err)
			continue
		}
		
		// Capture model information
		if model == "" && streamResponse.ModelVersion != "" {
			model = streamResponse.ModelVersion
		}
		
		// Extract finish reason from candidates
		if len(streamResponse.Candidates) > 0 && streamResponse.Candidates[0].FinishReason != "" {
			finishReason = streamResponse.Candidates[0].FinishReason
		}
		
		// The usage information is typically in the final chunk
		if streamResponse.UsageMetadata != nil {
			metadata = &LLMResponseMetadata{
				Model:         model,
				InputTokens:   streamResponse.UsageMetadata.PromptTokenCount,
				OutputTokens:  streamResponse.UsageMetadata.CandidatesTokenCount,
				TotalTokens:   streamResponse.UsageMetadata.TotalTokenCount,
				ThoughtTokens: streamResponse.UsageMetadata.ThoughtsTokenCount,
				Provider:      "gemini",
				IsStreaming:   true,
				FinishReason:  finishReason,
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading streaming response: %w", err)
	}

	// If we have usage metadata, return it
	if metadata != nil {
		// Set default model if not found
		if metadata.Model == "" {
			metadata.Model = "gemini"
		}
		return metadata, nil
	}

	// If we found streaming data but no usage information, return partial metadata
	// This can happen when the stream is not yet complete or usage info is in a later chunk
	if hasData {
		// Set default model if not found
		if model == "" {
			model = "gemini"
		}
		return &LLMResponseMetadata{
			Model:        model,
			InputTokens:  0,  // Unknown at this point
			OutputTokens: 0,  // Unknown at this point
			TotalTokens:  0,  // Unknown at this point
			Provider:     "gemini",
			IsStreaming:  true,
			FinishReason: finishReason,
		}, nil
	}

	return nil, fmt.Errorf("no usage information found in streaming response")
}

// ParseRateLimitFromResponse extracts rate limit information from Gemini response headers
// Gemini doesn't currently provide rate limit headers, so this returns nil
func (g *GeminiProxy) ParseRateLimitFromResponse(resp *http.Response) *RateLimitInfo {
	// Gemini API doesn't provide rate limit headers in their responses
	// Return nil to indicate no rate limit information is available
	log.Printf("🔄 Gemini: No rate limit headers available from Gemini API")
	return nil
}

// UserIDFromRequest extracts user ID from Gemini request body
// For Gemini, we only support passing user ID down, not extracting it
func (g *GeminiProxy) UserIDFromRequest(req *http.Request) string {
	// Gemini doesn't support user ID extraction, only passing it down
	return ""
} 
