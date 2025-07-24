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

	// Use the generic director function to handle common proxy logic
	originalDirector := proxy.Director
	proxy.Director = CreateGenericDirector(geminiProxy, targetURL, originalDirector)

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

// Proxy returns the HTTP handler for the Gemini provider
func (g *GeminiProxy) Proxy() http.Handler {
	return g.proxy
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



// UserIDFromRequest extracts user ID from Gemini request body
// For Gemini, we only support passing user ID down, not extracting it
func (g *GeminiProxy) UserIDFromRequest(req *http.Request) string {
	// Gemini doesn't support user ID extraction, only passing it down
	return ""
}

// RegisterExtraRoutes registers Gemini-specific compatibility routes
func (g *GeminiProxy) RegisterExtraRoutes(router *mux.Router) {
	// Special compatibility routes for Gemini (these were in the original Gemini RegisterRoutes)
	router.PathPrefix("/v1beta/models/gemini").Handler(g.Proxy()).Methods("GET", "POST", "PUT", "DELETE", "OPTIONS")
	router.PathPrefix("/v1/models/gemini").Handler(g.Proxy()).Methods("GET", "POST", "PUT", "DELETE", "OPTIONS")
} 
