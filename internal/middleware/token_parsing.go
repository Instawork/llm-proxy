package middleware

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/Instawork/llm-proxy/internal/providers"
)

// MetadataCallback is a function that can be hooked into the TokenParsingMiddleware
// to process LLM response metadata.
type MetadataCallback func(r *http.Request, metadata *providers.LLMResponseMetadata)

// GetProviderFromRequest determines which provider to use based on the request path
func GetProviderFromRequest(providerManager *providers.ProviderManager, req *http.Request) providers.Provider {
	if strings.HasPrefix(req.URL.Path, "/openai/") {
		return providerManager.GetProvider("openai")
	} else if strings.HasPrefix(req.URL.Path, "/anthropic/") {
		return providerManager.GetProvider("anthropic")
	} else if strings.HasPrefix(req.URL.Path, "/gemini/") {
		return providerManager.GetProvider("gemini")
	}
	return nil
}

// TokenParsingMiddleware intercepts responses to parse and log token usage
func TokenParsingMiddleware(providerManager *providers.ProviderManager, callbacks ...MetadataCallback) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Determine which provider this request is for
			provider := GetProviderFromRequest(providerManager, r)
			
			// Check if this is a streaming request
			isStreaming := providerManager.IsStreamingRequest(r)
			
			// Create a custom response writer that can capture the response
			captureWriter := &responseCapture{
				ResponseWriter: w,
				body:           &bytes.Buffer{},
				isStreaming:    isStreaming,
				provider:       provider,
				lastMetadata:   nil,
			}
			
			// Debug logging
			log.Printf("🔍 Debug: Request path: %s, Provider: %v", r.URL.Path, provider != nil)
			
			next.ServeHTTP(captureWriter, r)
			
			// Debug logging for endpoint matching
			isAPIEndpoint := strings.Contains(r.URL.Path, "/chat/completions") || 
				strings.Contains(r.URL.Path, "/completions") || 
				strings.Contains(r.URL.Path, "/messages") || 
				strings.Contains(r.URL.Path, ":generateContent")
			
			log.Printf("🔍 Debug: Provider: %v, API endpoint: %v, Response body length: %d", 
				provider != nil, isAPIEndpoint, captureWriter.body.Len())
			
			// Only process if we have a provider and this is an API endpoint
			if provider != nil && isAPIEndpoint {
				var metadata *providers.LLMResponseMetadata
				var err error
				
				// For streaming responses, use the last metadata captured during streaming
				if isStreaming && captureWriter.lastMetadata != nil {
					metadata = captureWriter.lastMetadata
					log.Printf("🔍 Token Parser: Using captured streaming metadata - Input: %d, Output: %d, Total: %d", 
						metadata.InputTokens, metadata.OutputTokens, metadata.TotalTokens)
				} else {
					// For non-streaming responses, parse the final response
					bodyReader := bytes.NewReader(captureWriter.body.Bytes())
					metadata, err = provider.ParseResponseMetadata(bodyReader, isStreaming)
					if isStreaming && metadata != nil {
						log.Printf("🔍 Token Parser: Got final parse metadata - Input: %d, Output: %d, Total: %d", 
							metadata.InputTokens, metadata.OutputTokens, metadata.TotalTokens)
					}
				}
				
				if err != nil {
					// For streaming responses, partial data is expected and not necessarily an error
					if isStreaming {
						log.Printf("Info: Partial streaming response data for %s: %v", provider.GetName(), err)
					} else {
						log.Printf("Warning: Failed to parse response metadata for %s: %v", provider.GetName(), err)
					}
					// Add debug logging for response body if parsing fails
					if captureWriter.body.Len() > 0 {
						log.Printf("🔍 Debug: Response body preview: %s", string(captureWriter.body.Bytes()[:min(200, captureWriter.body.Len())]))
					}
				} else if metadata != nil {
					// Log the metadata for cost tracking and rate limiting
					log.Printf("🔢 LLM Response Metadata:\n"+
						"   Provider: %s\n"+
						"   Model: %s\n"+
						"   Request ID: %s\n"+
						"   Input Tokens: %d\n"+
						"   Output Tokens: %d\n"+
						"   Total Tokens: %d\n"+
						"   Streaming: %t\n"+
						"   Finish Reason: %s",
						metadata.Provider, metadata.Model, metadata.RequestID, metadata.InputTokens, metadata.OutputTokens, 
						metadata.TotalTokens, metadata.IsStreaming, metadata.FinishReason)
					
					// Additional detailed logging for cost tracking
					if metadata.TotalTokens > 0 {
						// Include thought tokens in the logging if available
						log.Printf("💰 Token Usage Summary:\n"+
							"   Provider/Model: %s/%s\n"+
							"   Input Tokens: %d\n"+
							"   Output Tokens: %d\n"+
							"   Thought Tokens: %d\n"+
							"   Total Tokens: %d", 
							metadata.Provider, metadata.Model, metadata.InputTokens, metadata.OutputTokens, metadata.ThoughtTokens, metadata.TotalTokens)
					} else if metadata.IsStreaming {
						log.Printf("ℹ️  Streaming Response: Usage information not yet available (partial response captured)")
					}
					
					// Add custom header with token usage information
					w.Header().Set("X-LLM-Input-Tokens", fmt.Sprintf("%d", metadata.InputTokens))
					w.Header().Set("X-LLM-Output-Tokens", fmt.Sprintf("%d", metadata.OutputTokens))
					w.Header().Set("X-LLM-Total-Tokens", fmt.Sprintf("%d", metadata.TotalTokens))
					w.Header().Set("X-LLM-Thought-Tokens", fmt.Sprintf("%d", metadata.ThoughtTokens))
					w.Header().Set("X-LLM-Provider", metadata.Provider)
					w.Header().Set("X-LLM-Model", metadata.Model)
					if metadata.RequestID != "" {
						w.Header().Set("X-LLM-Request-ID", metadata.RequestID)
					}

					// Execute all registered callbacks with the metadata
					for _, callback := range callbacks {
						if callback != nil {
							callback(r, metadata)
						}
					}
				} else if isStreaming {
					// For streaming responses without metadata, just log that we're still waiting
					log.Printf("ℹ️  Streaming Response: Still waiting for complete usage information")
				}
			}
		})
	}
}

// responseCapture captures the response body for parsing
type responseCapture struct {
	http.ResponseWriter
	body *bytes.Buffer
	isStreaming bool
	provider providers.Provider
	lastMetadata *providers.LLMResponseMetadata
	lastParsedPos int  // Track the last position we parsed to avoid re-parsing
}

func (rc *responseCapture) Write(b []byte) (int, error) {
	// Write to both the original response and our buffer
	rc.body.Write(b)
	
	// For streaming responses, only parse new data to avoid redundant parsing
	if rc.isStreaming && rc.provider != nil {
		// Get the current buffer content
		allData := rc.body.Bytes()
		
		// Only parse if we have new data since the last parse
		if len(allData) > rc.lastParsedPos {
			log.Printf("🔍 Token Parser: Parsing streaming data, buffer size: %d, new data: %d bytes", 
				len(allData), len(allData) - rc.lastParsedPos)
			
			// For streaming, we need to parse the entire buffer since usage info 
			// comes at the end and we might have partial events
			bodyReader := bytes.NewReader(allData)
			if metadata, err := rc.provider.ParseResponseMetadata(bodyReader, true); err == nil && metadata != nil {
				log.Printf("🔍 Token Parser: Got metadata - Input: %d, Output: %d, Total: %d", 
					metadata.InputTokens, metadata.OutputTokens, metadata.TotalTokens)
				// Update the last successful metadata
				rc.lastMetadata = metadata
			} else if err != nil {
				log.Printf("🔍 Token Parser: Parse error (expected for partial data): %v", err)
			}
			// Update the last parsed position
			rc.lastParsedPos = len(allData)
		}
	}
	
	return rc.ResponseWriter.Write(b)
}

// Helper function to find minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
} 

// ExtractUserIDFromRequest extracts user ID from request headers, query parameters, or provider-specific methods
// Follows the priority order: headers → query parameters → provider-specific extraction → fallback to IP
func ExtractUserIDFromRequest(req *http.Request, provider providers.Provider) string {
	// Priority 1: Check for custom user ID header
	if userID := req.Header.Get("X-User-ID"); userID != "" {
		log.Printf("🔍 User ID from X-User-ID header: %s", userID)
		return userID
	}

	// Priority 2: Provider-specific extraction from request body
	if provider != nil {
		if userID := provider.UserIDFromRequest(req); userID != "" {
			log.Printf("🔍 User ID from provider-specific extraction: %s", userID)
			return userID
		}
	}
	
	// Priority 3: Check query parameters
	if userID := req.URL.Query().Get("llm_user_id"); userID != "" {
		log.Printf("🔍 User ID from query parameter: %s", userID)
		return userID
	}

	// Priority 4: Check Authorization header for API key or JWT token
	if auth := req.Header.Get("Authorization"); auth != "" {
		// For API keys, use a hash of the key as user ID (for privacy)
		if strings.HasPrefix(auth, "Bearer ") {
			// Use first 8 characters of the token for identification
			token := auth[7:]
			if len(token) > 8 {
				tokenID := fmt.Sprintf("token:%s", token[:8])
				log.Printf("🔍 User ID from Authorization header: %s", tokenID)
				return tokenID
			}
			tokenID := fmt.Sprintf("token:%s", token)
			log.Printf("🔍 User ID from Authorization header: %s", tokenID)
			return tokenID
		}
	}
		
	// Fallback to IP address if no user identification
	ipAddr := ExtractIPAddressFromRequest(req)
	log.Printf("🔍 User ID fallback to IP address: %s", ipAddr)
	return fmt.Sprintf("ip:%s", ipAddr)
}

// ExtractIPAddressFromRequest extracts IP address from request headers
func ExtractIPAddressFromRequest(req *http.Request) string {
	// Check for forwarded headers
	if forwarded := req.Header.Get("X-Forwarded-For"); forwarded != "" {
		return forwarded
	}
	
	if realIP := req.Header.Get("X-Real-IP"); realIP != "" {
		return realIP
	}
	
	return req.RemoteAddr
} 
