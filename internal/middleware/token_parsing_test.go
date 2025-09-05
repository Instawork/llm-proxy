package middleware

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/gorilla/mux"
)

func TestGetProviderFromRequest_OpenAI(t *testing.T) {
	manager := providers.NewProviderManager()
	openAIProvider := providers.NewOpenAIProxy()
	manager.RegisterProvider(openAIProvider)

	req := httptest.NewRequest("POST", "/openai/v1/chat/completions", nil)
	provider := GetProviderFromRequest(manager, req)

	if provider == nil {
		t.Fatal("Expected provider to be found for OpenAI path")
	}

	if provider.GetName() != "openai" {
		t.Errorf("Expected provider name 'openai', got '%s'", provider.GetName())
	}
}

func TestGetProviderFromRequest_Anthropic(t *testing.T) {
	manager := providers.NewProviderManager()
	anthropicProvider := providers.NewAnthropicProxy()
	manager.RegisterProvider(anthropicProvider)

	req := httptest.NewRequest("POST", "/anthropic/v1/messages", nil)
	provider := GetProviderFromRequest(manager, req)

	if provider == nil {
		t.Fatal("Expected provider to be found for Anthropic path")
	}

	if provider.GetName() != "anthropic" {
		t.Errorf("Expected provider name 'anthropic', got '%s'", provider.GetName())
	}
}

func TestGetProviderFromRequest_Gemini(t *testing.T) {
	manager := providers.NewProviderManager()
	geminiProvider := providers.NewGeminiProxy()
	manager.RegisterProvider(geminiProvider)

	req := httptest.NewRequest("POST", "/gemini/v1/models/gemini-pro:generateContent", nil)
	provider := GetProviderFromRequest(manager, req)

	if provider == nil {
		t.Fatal("Expected provider to be found for Gemini path")
	}

	if provider.GetName() != "gemini" {
		t.Errorf("Expected provider name 'gemini', got '%s'", provider.GetName())
	}
}

func TestGetProviderFromRequest_UnknownPath(t *testing.T) {
	manager := providers.NewProviderManager()

	req := httptest.NewRequest("POST", "/unknown/path", nil)
	provider := GetProviderFromRequest(manager, req)

	if provider != nil {
		t.Errorf("Expected nil provider for unknown path, got '%s'", provider.GetName())
	}
}

func TestExtractUserIDFromRequest_HeaderXUserID(t *testing.T) {
	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("X-User-ID", "user123")

	userID := ExtractUserIDFromRequest(req, nil)

	if userID != "user123" {
		t.Errorf("Expected user ID 'user123', got '%s'", userID)
	}
}

func TestExtractUserIDFromRequest_QueryParameter(t *testing.T) {
	req := httptest.NewRequest("POST", "/test?llm_user_id=queryuser456", nil)

	userID := ExtractUserIDFromRequest(req, nil)

	if userID != "queryuser456" {
		t.Errorf("Expected user ID 'queryuser456', got '%s'", userID)
	}
}

func TestExtractUserIDFromRequest_AuthorizationHeader(t *testing.T) {
	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("Authorization", "Bearer abc12345token")

	userID := ExtractUserIDFromRequest(req, nil)

	expected := "token:abc12345"
	if userID != expected {
		t.Errorf("Expected user ID '%s', got '%s'", expected, userID)
	}
}

func TestExtractUserIDFromRequest_ShortToken(t *testing.T) {
	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("Authorization", "Bearer abc")

	userID := ExtractUserIDFromRequest(req, nil)

	expected := "token:abc"
	if userID != expected {
		t.Errorf("Expected user ID '%s', got '%s'", expected, userID)
	}
}

func TestExtractUserIDFromRequest_IPFallback(t *testing.T) {
	req := httptest.NewRequest("POST", "/test", nil)
	req.RemoteAddr = "192.168.1.100:8080"

	userID := ExtractUserIDFromRequest(req, nil)

	expected := "ip:192.168.1.100:8080"
	if userID != expected {
		t.Errorf("Expected user ID '%s', got '%s'", expected, userID)
	}
}

func TestExtractUserIDFromRequest_ForwardedHeaders(t *testing.T) {
	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.45")
	req.RemoteAddr = "192.168.1.100:8080"

	userID := ExtractUserIDFromRequest(req, nil)

	expected := "ip:203.0.113.45"
	if userID != expected {
		t.Errorf("Expected user ID '%s', got '%s'", expected, userID)
	}
}

func TestExtractUserIDFromRequest_RealIPHeader(t *testing.T) {
	req := httptest.NewRequest("POST", "/test", nil)
	req.Header.Set("X-Real-IP", "198.51.100.23")
	req.RemoteAddr = "192.168.1.100:8080"

	userID := ExtractUserIDFromRequest(req, nil)

	expected := "ip:198.51.100.23"
	if userID != expected {
		t.Errorf("Expected user ID '%s', got '%s'", expected, userID)
	}
}

func TestExtractUserIDFromRequest_PriorityOrder(t *testing.T) {
	// Test that X-User-ID header takes priority over other methods
	req := httptest.NewRequest("POST", "/test?llm_user_id=queryuser", nil)
	req.Header.Set("X-User-ID", "headeruser")
	req.Header.Set("Authorization", "Bearer tokenuser")
	req.Header.Set("X-Forwarded-For", "203.0.113.45")

	userID := ExtractUserIDFromRequest(req, nil)

	if userID != "headeruser" {
		t.Errorf("Expected X-User-ID header to take priority, got '%s'", userID)
	}
}

func TestExtractUserIDFromRequest_Context(t *testing.T) {
	req := httptest.NewRequest("POST", "/openai/v1/chat/completions", nil)

	// Add user ID to context (simulating what MetaURLRewritingMiddleware does)
	ctx := context.WithValue(req.Context(), userIDContextKey, "context-user-123")
	req = req.WithContext(ctx)

	userID := ExtractUserIDFromRequest(req, nil)

	if userID != "context-user-123" {
		t.Errorf("Expected user ID 'context-user-123', got '%s'", userID)
	}
}

func TestExtractUserIDFromRequest_ContextPriority(t *testing.T) {
	// Test that context takes priority over other methods
	req := httptest.NewRequest("POST", "/meta/url-user/openai/v1/chat/completions?llm_user_id=queryuser", nil)
	req.Header.Set("X-User-ID", "headeruser")
	req.Header.Set("Authorization", "Bearer tokenuser")

	// Add user ID to context (should take highest priority)
	ctx := context.WithValue(req.Context(), userIDContextKey, "context-user-priority")
	req = req.WithContext(ctx)

	userID := ExtractUserIDFromRequest(req, nil)

	if userID != "context-user-priority" {
		t.Errorf("Expected context user ID to take priority, got '%s'", userID)
	}
}

func TestExtractIPAddressFromRequest_RemoteAddr(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	req.RemoteAddr = "192.168.1.100:8080"

	ip := ExtractIPAddressFromRequest(req)

	if ip != "192.168.1.100:8080" {
		t.Errorf("Expected IP '192.168.1.100:8080', got '%s'", ip)
	}
}

func TestExtractIPAddressFromRequest_XForwardedFor(t *testing.T) {
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.45")
	req.RemoteAddr = "192.168.1.100:8080"

	ip := ExtractIPAddressFromRequest(req)

	if ip != "203.0.113.45" {
		t.Errorf("Expected forwarded IP '203.0.113.45', got '%s'", ip)
	}
}

func TestTokenParsingMiddleware_BasicExecution(t *testing.T) {
	manager := providers.NewProviderManager()

	// Create a test handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test response"))
	})

	// Wrap with token parsing middleware
	tokenHandler := TokenParsingMiddleware(manager)(handler)

	// Create test request
	req := httptest.NewRequest("POST", "/test", nil)
	recorder := httptest.NewRecorder()

	// Execute request
	tokenHandler.ServeHTTP(recorder, req)

	// Check response
	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}

	if recorder.Body.String() != "test response" {
		t.Errorf("Expected body 'test response', got '%s'", recorder.Body.String())
	}
}

func TestTokenParsingMiddleware_WithCallback(t *testing.T) {
	manager := providers.NewProviderManager()

	// Track callback invocations
	var callbackCalled bool
	var receivedMetadata *providers.LLMResponseMetadata

	callback := func(r *http.Request, metadata *providers.LLMResponseMetadata) {
		callbackCalled = true
		receivedMetadata = metadata
	}

	// Create a test handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test response"))
	})

	// Wrap with token parsing middleware with callback
	tokenHandler := TokenParsingMiddleware(manager, callback)(handler)

	// Create test request (non-API endpoint, so callback won't be called)
	req := httptest.NewRequest("POST", "/test", nil)
	recorder := httptest.NewRecorder()

	// Execute request
	tokenHandler.ServeHTTP(recorder, req)

	// Check that callback was not called for non-API endpoint
	if callbackCalled {
		t.Error("Callback should not be called for non-API endpoints")
	}

	if receivedMetadata != nil {
		t.Error("No metadata should be received for non-API endpoints")
	}
}

func TestTokenParsingMiddleware_APIEndpointDetection(t *testing.T) {
	manager := providers.NewProviderManager()

	// Register OpenAI provider for testing
	openAIProvider := providers.NewOpenAIProxy()
	manager.RegisterProvider(openAIProvider)

	// Create a test handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Simulate an OpenAI response (simplified)
		w.Write([]byte(`{"choices":[{"message":{"content":"Hello"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	})

	// Wrap with token parsing middleware
	tokenHandler := TokenParsingMiddleware(manager)(handler)

	// Test different API endpoints
	apiEndpoints := []string{
		"/openai/v1/chat/completions",
		"/anthropic/v1/messages",
		"/gemini/v1/models/gemini-pro:generateContent",
	}

	for _, endpoint := range apiEndpoints {
		t.Run(endpoint, func(t *testing.T) {
			req := httptest.NewRequest("POST", endpoint, nil)
			recorder := httptest.NewRecorder()

			tokenHandler.ServeHTTP(recorder, req)

			// Check that request was processed
			if recorder.Code != http.StatusOK {
				t.Errorf("Expected status 200 for %s, got %d", endpoint, recorder.Code)
			}
		})
	}
}

func TestTokenParsingMiddleware_NonAPIEndpoint(t *testing.T) {
	manager := providers.NewProviderManager()

	// Create a test handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("health check"))
	})

	// Wrap with token parsing middleware
	tokenHandler := TokenParsingMiddleware(manager)(handler)

	// Test non-API endpoints
	nonAPIEndpoints := []string{
		"/health",
		"/status",
		"/metrics",
		"/openai/health", // Not a completion endpoint
	}

	for _, endpoint := range nonAPIEndpoints {
		t.Run(endpoint, func(t *testing.T) {
			req := httptest.NewRequest("GET", endpoint, nil)
			recorder := httptest.NewRecorder()

			tokenHandler.ServeHTTP(recorder, req)

			// Should pass through without token parsing
			if recorder.Code != http.StatusOK {
				t.Errorf("Expected status 200 for %s, got %d", endpoint, recorder.Code)
			}

			if recorder.Body.String() != "health check" {
				t.Errorf("Expected body 'health check' for %s, got '%s'", endpoint, recorder.Body.String())
			}
		})
	}
}

func TestTokenParsingMiddleware_MultipleCallbacks(t *testing.T) {
	manager := providers.NewProviderManager()

	// Track multiple callback invocations
	var callback1Called bool
	var callback2Called bool

	callback1 := func(r *http.Request, metadata *providers.LLMResponseMetadata) {
		callback1Called = true
	}

	callback2 := func(r *http.Request, metadata *providers.LLMResponseMetadata) {
		callback2Called = true
	}

	// Create a test handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test response"))
	})

	// Wrap with token parsing middleware with multiple callbacks
	tokenHandler := TokenParsingMiddleware(manager, callback1, callback2)(handler)

	// Create test request
	req := httptest.NewRequest("POST", "/test", nil)
	recorder := httptest.NewRecorder()

	// Execute request
	tokenHandler.ServeHTTP(recorder, req)

	// Since this is not an API endpoint, callbacks should not be called
	if callback1Called || callback2Called {
		t.Error("Callbacks should not be called for non-API endpoints")
	}
}

func TestTokenParsingMiddleware_NilCallback(t *testing.T) {
	manager := providers.NewProviderManager()

	// Create a test handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test response"))
	})

	// Wrap with token parsing middleware with nil callback
	tokenHandler := TokenParsingMiddleware(manager, nil)(handler)

	// Create test request
	req := httptest.NewRequest("POST", "/test", nil)
	recorder := httptest.NewRecorder()

	// Execute request (should not panic with nil callback)
	tokenHandler.ServeHTTP(recorder, req)

	// Check response
	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}
}

// FailingMockProvider implements the Provider interface for testing parsing failures
type FailingMockProvider struct {
	name            string
	parseShouldFail bool
	parseError      error
	isStreaming     bool
}

func NewFailingMockProvider(name string) *FailingMockProvider {
	return &FailingMockProvider{
		name: name,
	}
}

func (fmp *FailingMockProvider) GetName() string {
	return fmp.name
}

func (fmp *FailingMockProvider) IsStreamingRequest(req *http.Request) bool {
	return fmp.isStreaming
}

func (fmp *FailingMockProvider) ParseResponseMetadata(responseBody io.Reader, isStreaming bool) (*providers.LLMResponseMetadata, error) {
	if fmp.parseShouldFail {
		return nil, fmp.parseError
	}
	// Return a valid metadata for successful parsing
	return &providers.LLMResponseMetadata{
		Model:        "test-model",
		InputTokens:  10,
		OutputTokens: 5,
		TotalTokens:  15,
		Provider:     fmp.name,
		IsStreaming:  isStreaming,
	}, nil
}

func (fmp *FailingMockProvider) Proxy() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("mock proxy response"))
	})
}

func (fmp *FailingMockProvider) GetHealthStatus() map[string]interface{} {
	return map[string]interface{}{
		"status": "healthy",
	}
}

func (fmp *FailingMockProvider) UserIDFromRequest(req *http.Request) string {
	return ""
}

func (fmp *FailingMockProvider) RegisterExtraRoutes(router *mux.Router) {
	// No extra routes for mock provider
}

func (fmp *FailingMockProvider) ValidateAPIKey(req *http.Request, keyStore providers.APIKeyStore) error {
	return nil
}

func (fmp *FailingMockProvider) ExtractRequestModelAndMessages(req *http.Request) (string, []string) {
	return "", nil
}

func TestTokenParsingMiddleware_ParsingFailureContinuesProxy(t *testing.T) {
	manager := providers.NewProviderManager()

	// Create a mock provider that will fail to parse tokens
	mockProvider := NewFailingMockProvider("openai")
	mockProvider.parseShouldFail = true
	mockProvider.parseError = fmt.Errorf("simulated parsing error")
	manager.RegisterProvider(mockProvider)

	// Track if callback was called
	var callbackCalled bool
	var receivedMetadata *providers.LLMResponseMetadata

	callback := func(r *http.Request, metadata *providers.LLMResponseMetadata) {
		callbackCalled = true
		receivedMetadata = metadata
	}

	// Create a test handler that simulates an API response
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Write a response that the mock provider will fail to parse
		w.Write([]byte(`{"invalid": "json", "that": "will", "fail": "parsing"}`))
	})

	// Wrap with token parsing middleware
	tokenHandler := TokenParsingMiddleware(manager, callback)(handler)

	// Create test request for an API endpoint
	req := httptest.NewRequest("POST", "/openai/v1/chat/completions", nil)
	recorder := httptest.NewRecorder()

	// Execute request
	tokenHandler.ServeHTTP(recorder, req)

	// Verify that the request was still proxied successfully despite parsing failure
	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}

	expectedBody := `{"invalid": "json", "that": "will", "fail": "parsing"}`
	if recorder.Body.String() != expectedBody {
		t.Errorf("Expected body '%s', got '%s'", expectedBody, recorder.Body.String())
	}

	// Verify that callback was not called since parsing failed
	if callbackCalled {
		t.Error("Callback should not be called when parsing fails")
	}

	if receivedMetadata != nil {
		t.Error("No metadata should be received when parsing fails")
	}
}

func TestTokenParsingMiddleware_ParsingFailureWithStreaming(t *testing.T) {
	manager := providers.NewProviderManager()

	// Create a custom streaming mock provider that will fail to parse tokens
	streamingMockProvider := &FailingMockProvider{
		name:            "openai",
		parseShouldFail: true,
		parseError:      fmt.Errorf("simulated streaming parsing error"),
		isStreaming:     true,
	}

	manager.RegisterProvider(streamingMockProvider)

	// Track if callback was called
	var callbackCalled bool
	var receivedMetadata *providers.LLMResponseMetadata

	callback := func(r *http.Request, metadata *providers.LLMResponseMetadata) {
		callbackCalled = true
		receivedMetadata = metadata
	}

	// Create a test handler that simulates a streaming API response
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Write streaming response data that will fail to parse
		w.Write([]byte("data: {\"invalid\": \"streaming\", \"data\": \"that fails\"}\n\n"))
		w.Write([]byte("data: {\"more\": \"invalid\", \"streaming\": \"data\"}\n\n"))
		w.Write([]byte("data: [DONE]\n\n"))
	})

	// Wrap with token parsing middleware
	tokenHandler := TokenParsingMiddleware(manager, callback)(handler)

	// Create test request for a streaming API endpoint
	req := httptest.NewRequest("POST", "/openai/v1/chat/completions", nil)
	recorder := httptest.NewRecorder()

	// Execute request
	tokenHandler.ServeHTTP(recorder, req)

	// Verify that the request was still proxied successfully despite parsing failure
	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}

	expectedBody := "data: {\"invalid\": \"streaming\", \"data\": \"that fails\"}\n\ndata: {\"more\": \"invalid\", \"streaming\": \"data\"}\n\ndata: [DONE]\n\n"
	if recorder.Body.String() != expectedBody {
		t.Errorf("Expected body '%s', got '%s'", expectedBody, recorder.Body.String())
	}

	// Verify that callback was not called since parsing failed
	if callbackCalled {
		t.Error("Callback should not be called when streaming parsing fails")
	}

	if receivedMetadata != nil {
		t.Error("No metadata should be received when streaming parsing fails")
	}
}

func TestTokenParsingMiddleware_ParsingFailureWithValidResponse(t *testing.T) {
	manager := providers.NewProviderManager()

	// Create a mock provider that will fail to parse tokens
	mockProvider := NewFailingMockProvider("openai")
	mockProvider.parseShouldFail = true
	mockProvider.parseError = fmt.Errorf("simulated parsing error")
	manager.RegisterProvider(mockProvider)

	// Track if callback was called
	var callbackCalled bool
	var receivedMetadata *providers.LLMResponseMetadata

	callback := func(r *http.Request, metadata *providers.LLMResponseMetadata) {
		callbackCalled = true
		receivedMetadata = metadata
	}

	// Create a test handler that returns a valid response
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		// Write a valid JSON response (but mock provider will still fail to parse)
		w.Write([]byte(`{"choices":[{"message":{"content":"Hello"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	})

	// Wrap with token parsing middleware
	tokenHandler := TokenParsingMiddleware(manager, callback)(handler)

	// Create test request for an API endpoint
	req := httptest.NewRequest("POST", "/openai/v1/chat/completions", nil)
	recorder := httptest.NewRecorder()

	// Execute request
	tokenHandler.ServeHTTP(recorder, req)

	// Verify that the request was still proxied successfully despite parsing failure
	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}

	expectedBody := `{"choices":[{"message":{"content":"Hello"}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`
	if recorder.Body.String() != expectedBody {
		t.Errorf("Expected body '%s', got '%s'", expectedBody, recorder.Body.String())
	}

	// Verify that callback was not called since parsing failed
	if callbackCalled {
		t.Error("Callback should not be called when parsing fails")
	}

	if receivedMetadata != nil {
		t.Error("No metadata should be received when parsing fails")
	}
}
