package ratelimit

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/providers"
)

// RateLimitMiddleware provides HTTP middleware for rate limiting
type RateLimitMiddleware struct {
	rateLimiter       RateLimiter
	providerManager   *providers.ProviderManager
	enabled           bool
	bypassHeader      string           // Header to bypass rate limiting (for admin/testing)
	responseHandler   ResponseHandler  // Custom response handler for rate limit exceeded
}

// ResponseHandler defines how to handle rate limit exceeded responses
type ResponseHandler interface {
	HandleRateLimitExceeded(w http.ResponseWriter, r *http.Request, decision *RateLimitDecision)
}

// DefaultResponseHandler provides default rate limit response handling
type DefaultResponseHandler struct{}

// HandleRateLimitExceeded implements ResponseHandler for default responses
func (h *DefaultResponseHandler) HandleRateLimitExceeded(w http.ResponseWriter, r *http.Request, decision *RateLimitDecision) {
	// Set rate limit headers
	w.Header().Set("X-RateLimit-Exceeded", "true")
	w.Header().Set("X-RateLimit-Reason", decision.Reason)
	w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(decision.RemainingTokens, 10))
	
	if decision.RetryAfter > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt(int64(decision.RetryAfter.Seconds()), 10))
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(decision.ResetTime.Unix(), 10))
	}
	
	// Return 429 Too Many Requests
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusTooManyRequests)
	
	errorResponse := map[string]interface{}{
		"error": map[string]interface{}{
			"type":    "rate_limit_exceeded",
			"message": decision.Reason,
			"code":    "rate_limit_exceeded",
		},
		"retry_after": decision.RetryAfter.Seconds(),
		"reset_time": decision.ResetTime.Unix(),
	}
	
	json.NewEncoder(w).Encode(errorResponse)
}

// ContextKey type for context values
type ContextKey string

const (
	// RateLimitRequestContextKey is the key for storing RateLimitRequest in context
	RateLimitRequestContextKey ContextKey = "rate_limit_request"
	
	// RateLimitDecisionContextKey is the key for storing RateLimitDecision in context
	RateLimitDecisionContextKey ContextKey = "rate_limit_decision"
)

// NewRateLimitMiddleware creates a new rate limiting middleware
func NewRateLimitMiddleware(rateLimiter RateLimiter, providerManager *providers.ProviderManager) *RateLimitMiddleware {
	return &RateLimitMiddleware{
		rateLimiter:     rateLimiter,
		providerManager: providerManager,
		enabled:         true,
		bypassHeader:    "X-RateLimit-Bypass",
		responseHandler: &DefaultResponseHandler{},
	}
}

// NewRateLimitMiddlewareWithConfig creates a new rate limiting middleware with custom configuration
func NewRateLimitMiddlewareWithConfig(rateLimiter RateLimiter, providerManager *providers.ProviderManager, 
	enabled bool, bypassHeader string, responseHandler ResponseHandler) *RateLimitMiddleware {
	return &RateLimitMiddleware{
		rateLimiter:     rateLimiter,
		providerManager: providerManager,
		enabled:         enabled,
		bypassHeader:    bypassHeader,
		responseHandler: responseHandler,
	}
}

// Middleware returns the HTTP middleware function for rate limiting
func (m *RateLimitMiddleware) Middleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip rate limiting if disabled
			if !m.enabled {
				next.ServeHTTP(w, r)
				return
			}
			
			// Check for bypass header (for admin/testing purposes)
			if m.bypassHeader != "" && r.Header.Get(m.bypassHeader) != "" {
				slog.Debug("Rate limiting bypassed for request", "method", r.Method, "path", r.URL.Path)
				next.ServeHTTP(w, r)
				return
			}
			
			// Get the provider for this request
			provider := m.getProviderForRequest(r)
			if provider == nil {
				// No provider found, allow the request to proceed (will be handled by routing)
				next.ServeHTTP(w, r)
				return
			}
			
			// Extract rate limit request information
			rateLimitReq := ExtractRateLimitRequest(r, provider)
			
			// Check rate limits
			decision := m.rateLimiter.CheckRequest(rateLimitReq)
			
			slog.Debug("Rate limit check", 
				"method", r.Method, 
				"path", r.URL.Path, 
				"decision", decision.Allowed, 
				"user", rateLimitReq.UserID, 
				"provider", rateLimitReq.Provider, 
				"model", rateLimitReq.Model)
			
			if !decision.Allowed {
				// Rate limit exceeded
				slog.Debug("Rate limit exceeded", "reason", decision.Reason)
				m.responseHandler.HandleRateLimitExceeded(w, r, decision)
				return
			}
			
			// Rate limit passed, continue with request
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimitResponseWrapper wraps the response writer to capture response data and update usage
type RateLimitResponseWrapper struct {
	http.ResponseWriter
	request      *http.Request
	rateLimitReq *RateLimitRequest
	provider     providers.Provider
	rateLimiter  RateLimiter
	decision     *RateLimitDecision
	statusCode   int
	headersSent  bool
}

// WriteHeader captures the status code and adds rate limit headers
func (w *RateLimitResponseWrapper) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	
	// Add rate limit headers to successful responses
	if statusCode >= 200 && statusCode < 300 {
		w.Header().Set("X-RateLimit-Allowed", "true")
		w.Header().Set("X-RateLimit-Remaining", strconv.FormatInt(w.decision.RemainingTokens, 10))
		
		if !w.decision.ResetTime.IsZero() {
			w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(w.decision.ResetTime.Unix(), 10))
		}
	}
	
	w.headersSent = true
	w.ResponseWriter.WriteHeader(statusCode)
}

// Write captures the response body and updates usage statistics
func (w *RateLimitResponseWrapper) Write(data []byte) (int, error) {
	// Make sure headers are sent
	if !w.headersSent {
		w.WriteHeader(http.StatusOK)
	}
	
	// Write the data - no need to update usage here since it's handled by token parsing middleware
	return w.ResponseWriter.Write(data)
}

// Helper function to check if string contains substring
func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// getProviderForRequest determines which provider the request is for
func (m *RateLimitMiddleware) getProviderForRequest(r *http.Request) providers.Provider {
	path := r.URL.Path
	
	// Check for provider-specific paths
	if len(path) > 1 {
		if strings.HasPrefix(path, "/openai/") {
			return m.providerManager.GetProvider("openai")
		}
		if strings.HasPrefix(path, "/anthropic/") {
			return m.providerManager.GetProvider("anthropic")
		}
		if strings.HasPrefix(path, "/gemini/") {
			return m.providerManager.GetProvider("gemini")
		}
	}
	
	return nil
}

// UpdateUsageFromMetadata updates rate limiter usage statistics after request completion
func (m *RateLimitMiddleware) UpdateUsageFromMetadata(r *http.Request, metadata *providers.LLMResponseMetadata) {
	if !m.enabled {
		return
	}
	
				// Get the provider for this request
			provider := m.getProviderForRequest(r)
	if provider == nil {
		return
	}
	
	// Extract rate limit request information
	rateLimitReq := ExtractRateLimitRequest(r, provider)
	
	// Calculate token difference for potential refund
	estimatedTokens := rateLimitReq.EstimatedTokens
	actualTokens := int64(metadata.TotalTokens)
	
	// Log token comparison for debugging
	if estimatedTokens != actualTokens {
		slog.Debug("📊 Rate Limit Token Comparison", 
			"user", rateLimitReq.UserID,
			"provider", rateLimitReq.Provider,
			"model", rateLimitReq.Model,
			"estimated_tokens", estimatedTokens,
			"actual_tokens", actualTokens,
			"difference", estimatedTokens-actualTokens)
	}
	
	// Refund over-consumed tokens if we estimated too high
	if estimatedTokens > actualTokens {
		if err := m.rateLimiter.RefundTokens(rateLimitReq, estimatedTokens, actualTokens); err != nil {
			slog.Debug("Error refunding tokens", "error", err)
		} else {
			slog.Debug("💰 Refunded over-consumed tokens", 
				"refunded_tokens", estimatedTokens-actualTokens,
				"user", rateLimitReq.UserID,
				"provider", rateLimitReq.Provider,
				"model", rateLimitReq.Model)
		}
	}
	
	// Update usage statistics
	if err := m.rateLimiter.UpdateUsage(rateLimitReq, metadata); err != nil {
		slog.Debug("Error updating rate limit usage", "error", err)
	} else {
		slog.Debug("Updated rate limit usage", 
			"user", rateLimitReq.UserID,
			"tokens", actualTokens,
			"provider", rateLimitReq.Provider,
			"model", rateLimitReq.Model)
	}
}

// SetEnabled enables or disables rate limiting
func (rlm *RateLimitMiddleware) SetEnabled(enabled bool) {
	rlm.enabled = enabled
}

// IsEnabled returns whether rate limiting is enabled
func (rlm *RateLimitMiddleware) IsEnabled() bool {
	return rlm.enabled
}

// SetBypassHeader sets the header name for bypassing rate limits
func (rlm *RateLimitMiddleware) SetBypassHeader(header string) {
	rlm.bypassHeader = header
}

// SetResponseHandler sets a custom response handler
func (rlm *RateLimitMiddleware) SetResponseHandler(handler ResponseHandler) {
	rlm.responseHandler = handler
}

// GetStats returns statistics about the rate limiter
func (rlm *RateLimitMiddleware) GetStats() map[string]interface{} {
	return map[string]interface{}{
		"enabled":        rlm.enabled,
		"bypass_header":  rlm.bypassHeader,
		"handler_type":   fmt.Sprintf("%T", rlm.responseHandler),
	}
}

// HealthCheck returns health information about the rate limiting middleware
func (rlm *RateLimitMiddleware) HealthCheck() map[string]interface{} {
	return map[string]interface{}{
		"rate_limiting": map[string]interface{}{
			"enabled":     rlm.enabled,
			"status":      "healthy",
			"provider":    "token_bucket",
			"storage":     "memory",
			"last_check":  time.Now().UTC().Format(time.RFC3339),
		},
	}
}

// StreamingAwareResponseHandler provides streaming-aware rate limit response handling
type StreamingAwareResponseHandler struct {
	defaultHandler *DefaultResponseHandler
}

// NewStreamingAwareResponseHandler creates a new streaming-aware response handler
func NewStreamingAwareResponseHandler() *StreamingAwareResponseHandler {
	return &StreamingAwareResponseHandler{
		defaultHandler: &DefaultResponseHandler{},
	}
}

// HandleRateLimitExceeded implements ResponseHandler for streaming-aware responses
func (h *StreamingAwareResponseHandler) HandleRateLimitExceeded(w http.ResponseWriter, r *http.Request, decision *RateLimitDecision) {
	// Check if this is a streaming request
	isStreaming := contains(r.Header.Get("Accept"), "text/event-stream") || 
		contains(r.Header.Get("Accept"), "application/x-ndjson")
	
	if isStreaming {
		// For streaming requests, send an SSE error
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		
		// Set rate limit headers
		w.Header().Set("X-RateLimit-Exceeded", "true")
		w.Header().Set("X-RateLimit-Reason", decision.Reason)
		
		if decision.RetryAfter > 0 {
			w.Header().Set("Retry-After", strconv.FormatInt(int64(decision.RetryAfter.Seconds()), 10))
		}
		
		w.WriteHeader(http.StatusTooManyRequests)
		
		// Send SSE error event
		errorEvent := map[string]interface{}{
			"error": map[string]interface{}{
				"type":    "rate_limit_exceeded",
				"message": decision.Reason,
				"code":    "rate_limit_exceeded",
			},
			"retry_after": decision.RetryAfter.Seconds(),
		}
		
		errorData, _ := json.Marshal(errorEvent)
		fmt.Fprintf(w, "data: %s\n\n", errorData)
		fmt.Fprintf(w, "data: [DONE]\n\n")
		
		// Flush the response
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
	} else {
		// For non-streaming requests, use the default handler
		h.defaultHandler.HandleRateLimitExceeded(w, r, decision)
	}
} 
