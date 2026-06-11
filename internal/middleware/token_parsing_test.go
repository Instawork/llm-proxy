package middleware

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/gorilla/mux"
)

// configurableProvider is a fully-mockable Provider whose ParseResponseMetadata
// returns a pre-configured LLMResponseMetadata (or err / nil), letting tests
// drive every metadata-decoration branch in TokenParsingMiddleware
// deterministically.  Distinct from FailingMockProvider because we need to
// vary thought/cache/RequestID fields independently.
type configurableProvider struct {
	streaming bool
	metadata  *providers.LLMResponseMetadata
	parseErr  error
	userID    string
}

func (m *configurableProvider) GetName() string                           { return "openai" }
func (m *configurableProvider) IsStreamingRequest(req *http.Request) bool { return m.streaming }
func (m *configurableProvider) Proxy() http.Handler                       { return http.NotFoundHandler() }

func (m *configurableProvider) GetHealthStatus() map[string]interface{} {
	return map[string]interface{}{}
}
func (m *configurableProvider) UserIDFromRequest(req *http.Request) string { return m.userID }
func (m *configurableProvider) RegisterExtraRoutes(r *mux.Router)          {}
func (m *configurableProvider) ValidateAPIKey(req *http.Request, ks providers.APIKeyStore) error {
	return nil
}

func (m *configurableProvider) ExtractRequestModelAndMessages(req *http.Request) (string, []string) {
	return "", nil
}

func (m *configurableProvider) ParseResponseMetadata(body io.Reader, isStreaming bool) (*providers.LLMResponseMetadata, error) {
	return m.metadata, m.parseErr
}

// driveTokenParsing wraps handler in TokenParsingMiddleware (with the given
// configurableProvider registered), runs a JSON request against path, and
// returns captured stdlib-log output and the response recorder.
func driveTokenParsing(t *testing.T, p *configurableProvider, path string, handler http.HandlerFunc, callbacks ...MetadataCallback) (string, *httptest.ResponseRecorder) {
	t.Helper()
	pm := providers.NewProviderManager()
	pm.RegisterProvider(p)
	chain := TokenParsingMiddleware(pm, callbacks...)(handler)

	req := httptest.NewRequest("POST", path, bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	logOut := captureLogOutput(func() { chain.ServeHTTP(rr, req) })
	return logOut, rr
}

// mustGzip builds a gzip-compressed payload for tests that need real gzip
// magic-byte detection or decompression.
func mustGzip(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write(b); err != nil {
		t.Fatalf("gzip Write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip Close: %v", err)
	}
	return buf.Bytes()
}

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

func TestGetProviderFromRequest_Bedrock(t *testing.T) {
	manager := providers.NewProviderManager()
	bedrockProvider := providers.NewBedrockProxy()
	manager.RegisterProvider(bedrockProvider)

	req := httptest.NewRequest(
		"POST",
		"/bedrock/model/us.anthropic.claude-sonnet-4-5-20250929-v1:0/converse",
		nil,
	)
	provider := GetProviderFromRequest(manager, req)

	if provider == nil {
		t.Fatal("Expected provider to be found for Bedrock path")
	}
	if provider.GetName() != "bedrock" {
		t.Errorf("Expected provider name 'bedrock', got '%s'", provider.GetName())
	}
}

func TestGetProviderFromRequest_BedrockMetaURL(t *testing.T) {
	manager := providers.NewProviderManager()
	bedrockProvider := providers.NewBedrockProxy()
	manager.RegisterProvider(bedrockProvider)

	req := httptest.NewRequest(
		"POST",
		"/meta/user-123/bedrock/model/us.anthropic.claude-sonnet-4-5-20250929-v1:0/converse",
		nil,
	)
	provider := GetProviderFromRequest(manager, req)

	if provider == nil {
		t.Fatal("Expected provider to be found for Bedrock meta-URL path")
	}
	if provider.GetName() != "bedrock" {
		t.Errorf("Expected provider name 'bedrock', got '%s'", provider.GetName())
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

// ─── ExtractUserIDFromRequest: meta-path priority ──────────────────────

// TestExtractUserIDFromRequest_MetaPath_ExtractsUserSegment covers the
// /meta/<userID>/<provider>/... path branch (priority 1) — the canonical
// way callers identify a user when they can't set headers (e.g. SDK clients
// using the meta proxy URL).
func TestExtractUserIDFromRequest_MetaPath_ExtractsUserSegment(t *testing.T) {
	req := httptest.NewRequest("POST", "/meta/path-user-42/openai/v1/chat/completions", nil)
	if got := ExtractUserIDFromRequest(req, nil); got != "path-user-42" {
		t.Errorf("expected path-user-42, got %q", got)
	}
}

// TestExtractUserIDFromRequest_MetaPath_EmptySegmentFallsThrough verifies
// that an empty userID segment in /meta//provider/... does NOT short-circuit
// to "" but falls through to the next priority (e.g. X-User-ID header).
func TestExtractUserIDFromRequest_MetaPath_EmptySegmentFallsThrough(t *testing.T) {
	req := httptest.NewRequest("POST", "/meta//openai/v1/chat/completions", nil)
	req.Header.Set("X-User-ID", "header-user")
	if got := ExtractUserIDFromRequest(req, nil); got != "header-user" {
		t.Errorf("empty userID segment should fall through to header; got %q", got)
	}
}

// TestExtractUserIDFromRequest_ProviderSpecificExtraction covers priority 3:
// when context, /meta path, and X-User-ID header are all empty, the provider's
// UserIDFromRequest method is consulted.
func TestExtractUserIDFromRequest_ProviderSpecificExtraction(t *testing.T) {
	p := &configurableProvider{userID: "from-provider-body"}
	req := httptest.NewRequest("POST", "/openai/v1/chat/completions", nil)
	if got := ExtractUserIDFromRequest(req, p); got != "from-provider-body" {
		t.Errorf("expected provider-extracted user, got %q", got)
	}
}

// TestExtractUserIDFromRequest_QueryParamPriority covers priority 4: the
// llm_user_id query param is the last resort before Authorization-derived
// IDs and IP fallback.
func TestExtractUserIDFromRequest_QueryParamPriority(t *testing.T) {
	p := &configurableProvider{userID: ""}
	req := httptest.NewRequest("POST", "/openai/v1/chat/completions?llm_user_id=q-user", nil)
	if got := ExtractUserIDFromRequest(req, p); got != "q-user" {
		t.Errorf("expected q-user from query, got %q", got)
	}
}

// ─── GetProviderFromRequest: meta-path dispatch ────────────────────────

// TestGetProviderFromRequest_MetaPath_DispatchesByProviderSegment covers the
// /meta/<userID>/<provider>/... dispatch branch.  An unrecognised provider
// segment must return nil so the caller can short-circuit.
func TestGetProviderFromRequest_MetaPath_DispatchesByProviderSegment(t *testing.T) {
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewOpenAIProxy())
	pm.RegisterProvider(providers.NewAnthropicProxy())
	pm.RegisterProvider(providers.NewGeminiProxy())

	cases := []struct {
		path string
		want string
	}{
		{"/meta/u1/openai/v1/chat/completions", "openai"},
		{"/meta/u2/anthropic/v1/messages", "anthropic"},
		{"/meta/u3/gemini/v1/models/gemini-pro:generateContent", "gemini"},
		{"/meta/u4/unknown/whatever", ""},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			req := httptest.NewRequest("POST", tc.path, nil)
			got := GetProviderFromRequest(pm, req)
			if tc.want == "" {
				if got != nil {
					t.Errorf("expected nil provider for %q, got %s", tc.path, got.GetName())
				}
				return
			}
			if got == nil {
				t.Fatalf("expected provider %s for %q, got nil", tc.want, tc.path)
			}
			if got.GetName() != tc.want {
				t.Errorf("expected %s, got %s", tc.want, got.GetName())
			}
		})
	}
}

// ─── TokenParsingMiddleware: provider/endpoint short-circuit ───────────

// TestTokenParsingMiddleware_NonProviderRoute_PassesThrough verifies that
// requests for routes not handled by any provider (e.g. /health) skip
// metadata parsing entirely, returning the handler's body unmodified.
func TestTokenParsingMiddleware_NonProviderRoute_PassesThrough(t *testing.T) {
	pm := providers.NewProviderManager() // no providers
	called := 0
	chain := TokenParsingMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called++
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("plain"))
	}))

	req := httptest.NewRequest("GET", "/health", nil)
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, req)

	if called != 1 {
		t.Fatalf("inner handler must run; got %d calls", called)
	}
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if rr.Body.String() != "plain" {
		t.Fatalf("expected body 'plain', got %q", rr.Body.String())
	}
}

// ─── TokenParsingMiddleware: metadata-decoration branches ──────────────

// TestTokenParsingMiddleware_NilMetadata_DoesNotWriteHeaders covers the
// short-circuit when ParseResponseMetadata returns (nil, nil): the
// middleware must not set any X-LLM-* headers nor invoke callbacks.
func TestTokenParsingMiddleware_NilMetadata_DoesNotWriteHeaders(t *testing.T) {
	p := &configurableProvider{metadata: nil, parseErr: nil}
	_, rr := driveTokenParsing(t, p, "/openai/v1/chat/completions",
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		})
	if rr.Header().Get("X-LLM-Provider") != "" {
		t.Errorf("X-LLM-Provider must not be set when metadata is nil; got %q", rr.Header().Get("X-LLM-Provider"))
	}
}

// TestTokenParsingMiddleware_BasicTokenHeaders_AlwaysSet asserts the
// always-set X-LLM-* token headers (provider, model, input/output/total)
// are populated whenever metadata is non-nil.
func TestTokenParsingMiddleware_BasicTokenHeaders_AlwaysSet(t *testing.T) {
	p := &configurableProvider{
		metadata: &providers.LLMResponseMetadata{
			Provider: "openai", Model: "gpt-4o",
			InputTokens: 10, OutputTokens: 5, TotalTokens: 15,
		},
	}
	_, rr := driveTokenParsing(t, p, "/openai/v1/chat/completions",
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	wantHeaders := map[string]string{
		"X-LLM-Provider":      "openai",
		"X-LLM-Model":         "gpt-4o",
		"X-LLM-Input-Tokens":  "10",
		"X-LLM-Output-Tokens": "5",
		"X-LLM-Total-Tokens":  "15",
	}
	for k, want := range wantHeaders {
		if got := rr.Header().Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
}

// TestTokenParsingMiddleware_CacheTokenHeaders_LogNoteWhenNonZero verifies
// the Anthropic-specific cache_read/cache_write log note fires when at least
// one cache field is > 0, and the X-LLM-Cache-* headers carry the values.
func TestTokenParsingMiddleware_CacheTokenHeaders_LogNoteWhenNonZero(t *testing.T) {
	p := &configurableProvider{
		metadata: &providers.LLMResponseMetadata{
			Provider: "openai", Model: "gpt-4o",
			CacheReadInputTokens:     7,
			CacheCreationInputTokens: 2,
		},
	}
	logOut, rr := driveTokenParsing(t, p, "/openai/v1/chat/completions",
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	if got := rr.Header().Get("X-LLM-Cache-Read-Tokens"); got != "7" {
		t.Errorf("X-LLM-Cache-Read-Tokens = %q, want 7", got)
	}
	if got := rr.Header().Get("X-LLM-Cache-Write-Tokens"); got != "2" {
		t.Errorf("X-LLM-Cache-Write-Tokens = %q, want 2", got)
	}
	if !strings.Contains(logOut, "cache_read=7 cache_write=2") {
		t.Errorf("cache log note missing; got: %s", logOut)
	}
}

// TestTokenParsingMiddleware_ThoughtTokenHeader_LogNoteWhenNonZero verifies
// the Gemini-specific "thought tokens" log note fires when > 0, and the
// header is set to the metadata value.
func TestTokenParsingMiddleware_ThoughtTokenHeader_LogNoteWhenNonZero(t *testing.T) {
	p := &configurableProvider{
		metadata: &providers.LLMResponseMetadata{
			Provider: "openai", Model: "gpt-4o", ThoughtTokens: 3,
		},
	}
	logOut, rr := driveTokenParsing(t, p, "/openai/v1/chat/completions",
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })

	if got := rr.Header().Get("X-LLM-Thought-Tokens"); got != "3" {
		t.Errorf("X-LLM-Thought-Tokens = %q, want 3", got)
	}
	if !strings.Contains(logOut, "thought=3") {
		t.Errorf("thought log note missing; got: %s", logOut)
	}
}

// TestTokenParsingMiddleware_RequestIDHeader_OnlyWhenSet verifies that the
// X-LLM-Request-ID header is only emitted when metadata.RequestID is non-empty
// — important for distributed-trace correlation without leaking blank IDs.
func TestTokenParsingMiddleware_RequestIDHeader_OnlyWhenSet(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		p := &configurableProvider{
			metadata: &providers.LLMResponseMetadata{Provider: "openai", RequestID: "req-abc"},
		}
		_, rr := driveTokenParsing(t, p, "/openai/v1/chat/completions",
			func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
		if got := rr.Header().Get("X-LLM-Request-ID"); got != "req-abc" {
			t.Errorf("X-LLM-Request-ID = %q, want req-abc", got)
		}
	})
	t.Run("empty", func(t *testing.T) {
		p := &configurableProvider{
			metadata: &providers.LLMResponseMetadata{Provider: "openai", RequestID: ""},
		}
		_, rr := driveTokenParsing(t, p, "/openai/v1/chat/completions",
			func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
		if got := rr.Header().Get("X-LLM-Request-ID"); got != "" {
			t.Errorf("X-LLM-Request-ID must be empty when metadata.RequestID is empty; got %q", got)
		}
	})
}

// TestTokenParsingMiddleware_RecordsTTFB_OnFirstWrite verifies that
// responseCapture records TTFB on the first non-empty Write and surfaces
// it in the diagnostic log line.
func TestTokenParsingMiddleware_RecordsTTFB_OnFirstWrite(t *testing.T) {
	p := &configurableProvider{
		metadata: &providers.LLMResponseMetadata{Provider: "openai", Model: "gpt-4o"},
	}
	logOut, _ := driveTokenParsing(t, p, "/openai/v1/chat/completions",
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`some-bytes-to-trigger-ttfb`))
		})
	if !strings.Contains(logOut, "TTFB:") {
		t.Errorf("TTFB log note missing; got: %s", logOut)
	}
}

// TestTokenParsingMiddleware_InvokesCallbacks_SkipsNil verifies that
// non-nil callbacks fire exactly once per request and nil entries in the
// callback list are silently skipped (no panic).
func TestTokenParsingMiddleware_InvokesCallbacks_SkipsNil(t *testing.T) {
	p := &configurableProvider{
		metadata: &providers.LLMResponseMetadata{Provider: "openai", Model: "gpt-4o", RequestID: "r1"},
	}
	var hits int
	var seen *providers.LLMResponseMetadata
	cb := func(_ *http.Request, md *providers.LLMResponseMetadata) {
		hits++
		seen = md
	}
	_, _ = driveTokenParsing(
		t, p, "/openai/v1/chat/completions",
		func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) },
		cb,
		nil, // nil-callback guard branch must not panic
	)

	if hits != 1 {
		t.Errorf("callback must run exactly once; got %d", hits)
	}
	if seen == nil || seen.RequestID != "r1" {
		t.Errorf("callback must receive populated metadata; got %+v", seen)
	}
}

// ─── TokenParsingMiddleware: parse-error preview branches ──────────────

// TestTokenParsingMiddleware_ParseErrorWithGzipBody_LogsDecompressedPreview
// covers the operator-debugging helper: when ParseResponseMetadata errors and
// the captured body starts with the gzip magic, the middleware decompresses
// a short preview into the log.
//
// Since the audit fix that routed previews through redact.LogPreview, the
// expectation is that:
//
//   - The "gzip-decompressed" log header still fires (so operators still
//     see that the path was exercised), AND
//   - The raw body content NEVER appears in the log when no global
//     Redactor is configured. We assert both halves: header present,
//     payload absent.
func TestTokenParsingMiddleware_ParseErrorWithGzipBody_LogsDecompressedPreview(t *testing.T) {
	gz := mustGzip(t, []byte("hello-from-gzip-preview-payload"))
	p := &configurableProvider{parseErr: errors.New("boom")}

	logOut, _ := driveTokenParsing(t, p, "/openai/v1/chat/completions",
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(gz)
		})

	if !strings.Contains(logOut, "gzip-decompressed") {
		t.Errorf("expected gzip-decompressed preview log; got %s", logOut)
	}
	if strings.Contains(logOut, "hello-from-gzip-preview-payload") {
		t.Errorf("body content leaked into log without a Redactor configured; got %s", logOut)
	}
	if !strings.Contains(logOut, "pii_redact disabled") {
		t.Errorf("expected length-only summary marker; got %s", logOut)
	}
}

// TestTokenParsingMiddleware_ParseErrorWithPlainBody_LogsRawPreview is the
// non-gzip companion of the test above. Same audit invariant applies: the
// preview log line fires, but body bytes do not appear when the global
// Redactor is unset.
func TestTokenParsingMiddleware_ParseErrorWithPlainBody_LogsRawPreview(t *testing.T) {
	p := &configurableProvider{parseErr: errors.New("boom")}
	logOut, _ := driveTokenParsing(t, p, "/openai/v1/chat/completions",
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("plain-text-error-body"))
		})
	if !strings.Contains(logOut, "Response body preview") {
		t.Errorf("expected preview log header; got %s", logOut)
	}
	if strings.Contains(logOut, "plain-text-error-body") {
		t.Errorf("body content leaked into log without a Redactor configured; got %s", logOut)
	}
	if !strings.Contains(logOut, "pii_redact disabled") {
		t.Errorf("expected length-only summary marker; got %s", logOut)
	}
}

// ─── TokenParsingMiddleware: streaming pacing diagnostics ──────────────

// TestTokenParsingMiddleware_StreamingResponse_LogsChunkAndEventSummary
// exercises the streaming-only branch of the middleware: per-chunk pacing
// logs (#N), the SSE event-type histogram (events=...), and the
// chunks=N bytes=N summary added to the final summary line.  We use an
// SSE-shaped body so sniffSSEEvents populates the event-type counters.
func TestTokenParsingMiddleware_StreamingResponse_LogsChunkAndEventSummary(t *testing.T) {
	p := &configurableProvider{
		streaming: true,
		metadata: &providers.LLMResponseMetadata{
			Model: "claude-3", Provider: "anthropic",
			InputTokens: 1, OutputTokens: 1, TotalTokens: 2, IsStreaming: true,
		},
	}
	pm := providers.NewProviderManager()
	pm.RegisterProvider(p)

	chain := TokenParsingMiddleware(pm)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\"}\n\n"))
		_, _ = w.Write([]byte("event: ping\ndata: {\"type\":\"ping\"}\n\n"))
	}))

	// configurableProvider's GetName() returns "openai", so the path must route
	// to the openai provider for GetProviderFromRequest to find it.
	req := httptest.NewRequest("POST", "/openai/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Accept", "text/event-stream")
	rr := httptest.NewRecorder()
	logOut := captureLogOutput(func() { chain.ServeHTTP(rr, req) })

	if !strings.Contains(logOut, "chunks=2") {
		t.Errorf("expected chunks=2 in summary; got %s", logOut)
	}
	if !strings.Contains(logOut, "events=") {
		t.Errorf("expected events=… in summary; got %s", logOut)
	}
	if !strings.Contains(logOut, "#1") || !strings.Contains(logOut, "#2") {
		t.Errorf("expected per-chunk pacing logs; got %s", logOut)
	}
}
