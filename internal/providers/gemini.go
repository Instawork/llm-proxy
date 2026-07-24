// Gemini provider implementation
// EXTREMELY IMPORTANT:
// DO NOT USE THE BODY PARSING FEATURE OF THE PROXY FOR GEMINI.
// We do not need to parse the body for Gemini since they use ?alt=sse to indicate streaming.
// If we do, then video uploads will cause massive memory usage (max upload is 315MB for Gemini)

package providers

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/Instawork/llm-proxy/internal/proxylog"
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

// NewGeminiProxy creates a new Gemini reverse proxy.
// Pass a ProxyOptions value to override defaults (e.g. DisableGzip: true for
// debug builds that need plain-text SSE wire bytes).
func NewGeminiProxy(opts ...ProxyOptions) *GeminiProxy {
	var opt ProxyOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	// Parse the Gemini API URL. See NewOpenAIProxy for rationale on
	// using panic over log.Fatalf for malformed package constants.
	targetURL, err := url.Parse(geminiBaseURL)
	if err != nil {
		panic(fmt.Sprintf("invalid geminiBaseURL constant %q: %v", geminiBaseURL, err))
	}

	// Create the reverse proxy
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Create the Gemini proxy instance
	geminiProxy := &GeminiProxy{proxy: proxy}

	// Use the generic director function to handle common proxy logic
	originalDirector := proxy.Director
	proxy.Director = CreateGenericDirector(geminiProxy, targetURL, originalDirector, opt.DisableGzip)

	// Customize the transport for optimal streaming performance. Gemini historically
	// needed a longer ResponseHeaderTimeout than other providers; the shared default
	// is now 5m and can be overridden via providers.gemini.response_header_timeout_seconds.
	proxy.Transport = newProxyTransport(opt.DisableGzip, opt.ResponseHeaderTimeout)

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
		proxylog.Upstream("gemini reverse proxy transport error: %v", err)

		// For streaming requests, we need to handle errors differently
		if geminiProxy.IsStreamingRequest(r) {
			// If we're in a streaming context, we might have already started writing
			// the response, so we need to handle this gracefully
			proxylog.Upstream("gemini streaming transport error: %v", err)

			// Try to write an error in SSE format if possible
			if w.Header().Get("Content-Type") == "" {
				w.Header().Set("Content-Type", "text/event-stream")
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set(proxylog.HeaderErrorSource, proxylog.ErrorSourceUpstream)
				w.WriteHeader(http.StatusBadGateway)
				fmt.Fprint(w, proxylog.UpstreamSSEDataLine("gemini transport: %v", err))
				fmt.Fprintf(w, "data: [DONE]\n\n")
			} else {
				// Headers already sent, just log the error
				proxylog.Proxy("gemini cannot send error response, headers already sent")
			}
		} else {
			proxylog.WriteUpstreamJSONError(w, http.StatusBadGateway, fmt.Sprintf("gemini transport: %v", err))
		}
	}

	return geminiProxy
}

// GetName returns the name of the provider
func (g *GeminiProxy) GetName() string {
	return "gemini"
}

// IsStreamingRequest checks if the request is likely to be a streaming
// request for Gemini. The cross-provider routing gate lives in
// ProviderManager.IsStreamingRequest — see openai.go for the rationale.
func (g *GeminiProxy) IsStreamingRequest(req *http.Request) bool {
	if strings.Contains(req.Header.Get("Accept"), "text/event-stream") {
		return true
	}

	// Check for alt=sse query parameter (Gemini SSE streaming format)
	if req.URL.Query().Get("alt") == "sse" {
		return true
	}

	// The OpenAI-compatibility endpoint signals streaming via "stream": true
	// in the JSON body, like OpenAI proper. Scoped to /chat/completions so
	// native Gemini requests (including large media uploads) are never
	// body-inspected.
	if req.Method == "POST" && isChatCompletionsPath(req.URL.Path) {
		return requestBodyHasStreamTrue(req, "gemini")
	}

	// For Gemini generateContent endpoints, rely on explicit streaming indicators
	// via the URL path (streamGenerateContent) rather than inspecting the body.
	if req.Method == "POST" && (strings.Contains(req.URL.Path, ":generateContent") ||
		strings.Contains(req.URL.Path, ":streamGenerateContent")) {

		// Explicit streaming endpoint
		if strings.Contains(req.URL.Path, ":streamGenerateContent") {
			return true
		}

		// For :generateContent without explicit streaming indicators, treat as non-streaming
		return false
	}

	return false
}

// Body inspection for streaming detection removed to preserve upload streaming

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

// WrapTransport replaces the proxy's transport with fn(current transport).
func (g *GeminiProxy) WrapTransport(fn func(http.RoundTripper) http.RoundTripper) {
	g.proxy.Transport = fn(g.proxy.Transport)
}

// GetHealthStatus returns the health status of the Gemini proxy
func (g *GeminiProxy) GetHealthStatus() map[string]interface{} {
	return map[string]interface{}{
		"provider":          "gemini",
		"status":            "healthy",
		"baseURL":           geminiBaseURL,
		"streaming_support": true,
		"body_parsing":      false,
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
	// Handle potential gzip compression
	decompressedReader, err := DecompressResponseIfNeeded(responseBody)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress response: %w", err)
	}
	// Match OpenAI/Anthropic/Bedrock: close the gzip reader on the way out
	// so the decoder's window slices return to its sync.Pool. Without this
	// a long-running proxy steadily leaks gzip Reader buffers (~64 KiB
	// each) under sustained Gemini load.
	if gzipReader, ok := decompressedReader.(*gzip.Reader); ok {
		defer gzipReader.Close()
	}

	bodyBytes, err := io.ReadAll(decompressedReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Gemini's OpenAI-compatibility endpoint (/v1beta/openai/chat/completions)
	// returns OpenAI-shaped JSON ("choices" + usage.prompt_tokens). Parsing
	// it as native Gemini silently yields zero token counts, which disables
	// cost tracking and cost-limit enforcement for that traffic.
	if looksLikeOpenAIChatJSON(bodyBytes) {
		return parseOpenAICompatMetadata(bodyBytes, false, "gemini")
	}

	var response GeminiResponse
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		return nil, fmt.Errorf("failed to parse Gemini response: %w", err)
	}

	// Extract model from model version or use default
	model := response.ModelVersion
	if model == "" {
		model = "gemini" // fallback if model version is not provided
	} else {
		// Strip the "models/" prefix from the model name for consistency with configuration
		model = strings.TrimPrefix(model, "models/")
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
	// Handle potential gzip compression
	decompressedReader, err := DecompressResponseIfNeeded(responseBody)
	if err != nil {
		return nil, fmt.Errorf("failed to decompress response: %w", err)
	}
	if gzipReader, ok := decompressedReader.(*gzip.Reader); ok {
		defer gzipReader.Close()
	}

	// The OpenAI-compatibility endpoint streams OpenAI-style
	// chat.completion.chunk events, not Gemini candidate chunks. Peek at the
	// head to classify the stream; only compat streams are then read in full
	// (the OpenAI parser needs the final usage chunk), so the native path
	// keeps decompressing incrementally instead of materializing the whole
	// decompressed body.
	buffered := bufio.NewReaderSize(decompressedReader, compatStreamSniffLen)
	head, err := buffered.Peek(compatStreamSniffLen)
	if err != nil && err != io.EOF && len(head) == 0 {
		return nil, fmt.Errorf("failed to read streaming response: %w", err)
	}
	if looksLikeOpenAIChatStream(head) {
		data, err := io.ReadAll(buffered)
		if err != nil {
			return nil, fmt.Errorf("failed to read streaming response: %w", err)
		}
		return parseOpenAICompatMetadata(data, true, "gemini")
	}

	// :streamGenerateContent WITHOUT alt=sse returns a plain JSON array of
	// GenerateContentResponse objects, not SSE "data:" lines. The line
	// scanner below would find no data and report an error, silently losing
	// the request's tokens, so detect the array shape and parse it directly.
	if isJSONArrayHead(head) {
		data, err := io.ReadAll(buffered)
		if err != nil {
			return nil, fmt.Errorf("failed to read streaming response: %w", err)
		}
		return parseGeminiJSONArrayStream(data)
	}

	scanner := bufio.NewScanner(buffered)
	// Allow lines up to 2 MB — large Gemini SSE JSON chunks (e.g. with grounded
	// citations or long thinking traces) can exceed the default 64 KB.
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)

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
			proxylog.Proxy("gemini failed to parse streaming chunk: %v", err)
			continue
		}

		// Capture model information
		if model == "" && streamResponse.ModelVersion != "" {
			model = strings.TrimPrefix(streamResponse.ModelVersion, "models/")
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
			InputTokens:  0, // Unknown at this point
			OutputTokens: 0, // Unknown at this point
			TotalTokens:  0, // Unknown at this point
			Provider:     "gemini",
			IsStreaming:  true,
			FinishReason: finishReason,
		}, nil
	}

	return nil, fmt.Errorf("no usage information found in streaming response")
}

// isJSONArrayHead reports whether the peeked stream head starts a JSON array
// (ignoring leading whitespace).
func isJSONArrayHead(head []byte) bool {
	for _, b := range head {
		switch b {
		case ' ', '\t', '\r', '\n':
			continue
		case '[':
			return true
		default:
			return false
		}
	}
	return false
}

// parseGeminiJSONArrayStream handles the :streamGenerateContent response
// shape used when the client does not pass alt=sse: a JSON array of
// GenerateContentResponse chunks, the last of which carries usageMetadata.
func parseGeminiJSONArrayStream(data []byte) (*LLMResponseMetadata, error) {
	var chunks []GeminiStreamResponse
	if err := json.Unmarshal(data, &chunks); err != nil {
		return nil, fmt.Errorf("failed to parse gemini JSON-array stream: %w", err)
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("no usage information found in streaming response")
	}
	var model, finishReason string
	var metadata *LLMResponseMetadata
	for _, chunk := range chunks {
		if model == "" && chunk.ModelVersion != "" {
			model = strings.TrimPrefix(chunk.ModelVersion, "models/")
		}
		if len(chunk.Candidates) > 0 && chunk.Candidates[0].FinishReason != "" {
			finishReason = chunk.Candidates[0].FinishReason
		}
		if chunk.UsageMetadata != nil {
			metadata = &LLMResponseMetadata{
				InputTokens:   chunk.UsageMetadata.PromptTokenCount,
				OutputTokens:  chunk.UsageMetadata.CandidatesTokenCount,
				TotalTokens:   chunk.UsageMetadata.TotalTokenCount,
				ThoughtTokens: chunk.UsageMetadata.ThoughtsTokenCount,
				Provider:      "gemini",
				IsStreaming:   true,
			}
		}
	}
	if model == "" {
		model = "gemini"
	}
	if metadata == nil {
		return &LLMResponseMetadata{Model: model, Provider: "gemini", IsStreaming: true, FinishReason: finishReason}, nil
	}
	metadata.Model = model
	metadata.FinishReason = finishReason
	return metadata, nil
}

// UserIDFromRequest extracts user ID from Gemini request body
// For Gemini, we only support passing user ID down, not extracting it
func (g *GeminiProxy) UserIDFromRequest(req *http.Request) string {
	// Gemini doesn't support user ID extraction, only passing it down
	return ""
}

// RegisterExtraRoutes registers Gemini-specific compatibility routes
func (g *GeminiProxy) RegisterExtraRoutes(router *mux.Router) {
	// None needed now with `google-genai` package
}

// ValidateAPIKey validates and potentially replaces the API key in the request
func (g *GeminiProxy) ValidateAPIKey(req *http.Request, keyStore APIKeyStore) error {
	// Gemini uses API keys in three ways:
	// 1. In the URL query parameter as "key"
	// 2. In the x-goog-api-key header
	// 3. As "Authorization: Bearer <key>" (some OpenAI-compatible clients).
	//    Unlike Anthropic, we leave this key in the Authorization header
	//    rather than moving it to x-goog-api-key, since Gemini's
	//    OpenAI-compatible endpoint expects Bearer auth there.

	// Check query parameter first
	query := req.URL.Query()
	apiKeyFromQuery := query.Get("key")

	// Check header if no query parameter
	apiKeyFromHeader := req.Header.Get("x-goog-api-key")

	const bearerPrefix = "Bearer "
	apiKeyFromAuth := ""
	if authHeader := req.Header.Get("Authorization"); strings.HasPrefix(authHeader, bearerPrefix) {
		apiKeyFromAuth = strings.TrimPrefix(authHeader, bearerPrefix)
	}

	// Use whichever is present (query takes precedence, then x-goog-api-key,
	// then Authorization).
	inboundKey := apiKeyFromQuery
	if inboundKey == "" {
		inboundKey = apiKeyFromHeader
	}
	if inboundKey == "" {
		inboundKey = apiKeyFromAuth
	}

	if inboundKey == "" {
		// No API key provided, let the provider handle the error
		return nil
	}

	// Validate and potentially replace the key. Use the request context so
	// client cancellation / handler-level deadlines propagate into the
	// DynamoDB validation lookup.
	actualKey, provider, err := keyStore.ValidateAndGetActualKey(req.Context(), inboundKey)
	if err != nil {
		return fmt.Errorf("API key validation failed: %w", err)
	}

	// If a provider was returned, verify it matches
	if provider != "" && provider != "gemini" {
		return fmt.Errorf("API key is for provider %s, not gemini", provider)
	}

	// Replace the key in the appropriate location if it was translated
	if actualKey != inboundKey {
		if apiKeyFromQuery != "" {
			// Replace in query parameter
			query.Set("key", actualKey)
			req.URL.RawQuery = query.Encode()
		} else if apiKeyFromHeader != "" {
			// Replace in header
			req.Header.Set("x-goog-api-key", actualKey)
		} else if apiKeyFromAuth != "" {
			// Replace in place, keeping it in the Authorization header.
			req.Header.Set("Authorization", bearerPrefix+actualKey)
		}
		log.Printf("🔑 Gemini: Translated API key from iw: format")
	}

	return nil
}

// ExtractRequestModelAndMessages extracts model and message text content from Gemini request bodies.
// IMPORTANT: Keep this lightweight and only rely on already-present body (do not force full reads of large uploads).
// We only act on small JSON requests as gated by caller. We do minimal parsing of "contents" text parts.
func (g *GeminiProxy) ExtractRequestModelAndMessages(req *http.Request) (string, []string) {
	if req == nil || req.Method != "POST" {
		return "", nil
	}
	// Only consider Gemini endpoints
	isGeminiEndpoint := strings.HasPrefix(req.URL.Path, "/gemini/") ||
		strings.HasPrefix(req.URL.Path, "/v1beta/models/gemini") ||
		strings.HasPrefix(req.URL.Path, "/v1/models/gemini")
	if !isGeminiEndpoint {
		return "", nil
	}

	// Attempt to read small cached body if available; avoid heavy operations
	var bodyBytes []byte
	if req.GetBody != nil {
		r, err := req.GetBody()
		if err == nil {
			defer r.Close()
			bodyBytes, _ = io.ReadAll(r)
		}
	} else if req.Body != nil {
		// Read and restore because caller gated by size
		b, err := io.ReadAll(req.Body)
		if err != nil {
			return "", nil
		}
		req.Body = io.NopCloser(bytes.NewBuffer(b))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewBuffer(b)), nil }
		bodyBytes = b
	}
	if len(bodyBytes) == 0 {
		return "", nil
	}

	var data map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		return "", nil
	}

	// Model may be in URL; also allow explicit model field if present
	model := ""
	if mv, ok := data["model"].(string); ok && mv != "" {
		model = strings.TrimPrefix(mv, "models/")
	} else {
		// Fallback: extract from path like /v1/models/gemini-1.5-pro:generateContent
		path := req.URL.Path
		idx := strings.Index(path, "/models/")
		if idx >= 0 {
			m := path[idx+len("/models/"):]
			// trim suffix after :
			if j := strings.Index(m, ":"); j >= 0 {
				m = m[:j]
			}
			model = strings.TrimPrefix(m, "models/")
		}
	}

	messages := make([]string, 0, 8)
	// Gemini content structure: contents: [{role, parts:[{text:...}, ...]}]
	if contents, ok := data["contents"].([]interface{}); ok {
		for _, c := range contents {
			if cm, ok := c.(map[string]interface{}); ok {
				if parts, ok := cm["parts"].([]interface{}); ok {
					for _, p := range parts {
						if pm, ok := p.(map[string]interface{}); ok {
							if txt, ok := pm["text"].(string); ok && txt != "" {
								messages = append(messages, txt)
							}
						}
					}
				}
			}
		}
	}

	return model, messages
}
