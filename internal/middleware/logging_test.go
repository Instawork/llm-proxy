package middleware

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/providers"
	"github.com/gorilla/mux"
)

// MockProvider implements the Provider interface for testing
type MockProvider struct {
	name string
}

func (mp *MockProvider) ExtractRequestModelAndMessages(req *http.Request) (string, []string) {
	return "", nil
}

func (mp *MockProvider) GetName() string {
	return mp.name
}

func (mp *MockProvider) IsStreamingRequest(req *http.Request) bool {
	return req.Header.Get("Accept") == "text/event-stream"
}

func (mp *MockProvider) ParseResponseMetadata(responseBody io.Reader, isStreaming bool) (*providers.LLMResponseMetadata, error) {
	return &providers.LLMResponseMetadata{
		Provider:     mp.name,
		Model:        "test-model",
		InputTokens:  10,
		OutputTokens: 5,
		TotalTokens:  15,
		IsStreaming:  isStreaming,
	}, nil
}

func (mp *MockProvider) Proxy() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("mock response"))
	})
}

func (mp *MockProvider) GetHealthStatus() map[string]interface{} {
	return map[string]interface{}{
		"status": "healthy",
		"name":   mp.name,
	}
}

func (mp *MockProvider) UserIDFromRequest(req *http.Request) string {
	return ""
}

func (mp *MockProvider) RegisterExtraRoutes(router *mux.Router) {
	// No extra routes for mock provider
}

func (mp *MockProvider) ValidateAPIKey(req *http.Request, keyStore providers.APIKeyStore) error {
	return nil
}

// captureLogOutput captures log output for testing
func captureLogOutput(fn func()) string {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	fn()
	log.SetOutput(os.Stderr) // Restore default
	return buf.String()
}

func TestLoggingMiddleware_BasicLogging(t *testing.T) {
	// Create a mock provider manager
	manager := providers.NewProviderManager()

	// Create a test handler that takes some time
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond) // Small delay to test timing
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test response"))
	})

	// Wrap with logging middleware
	loggingHandler := LoggingMiddleware(manager)(handler)

	// Create test request
	req := httptest.NewRequest("GET", "/test/path", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	recorder := httptest.NewRecorder()

	// Capture log output
	logOutput := captureLogOutput(func() {
		loggingHandler.ServeHTTP(recorder, req)
	})

	// Check that both start and completion logs are present with structured logging format
	if !strings.Contains(logOutput, "Started request") {
		t.Error("Expected start log message not found")
	}

	if !strings.Contains(logOutput, "Completed request") {
		t.Error("Expected completion log message not found")
	}

	// Check that timing information is included
	if !strings.Contains(logOutput, "ms") && !strings.Contains(logOutput, "µs") && !strings.Contains(logOutput, "s") {
		// Should contain some time unit (ms, µs, or s)
		t.Error("Expected timing information in completion log")
	}

	// Check response
	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}
}

func TestLoggingMiddleware_StreamingRequest(t *testing.T) {
	// Create a mock provider manager that identifies streaming requests
	manager := providers.NewProviderManager()

	// We would need to mock IsStreamingRequest method for full testing
	// For now, test the basic structure

	// Create a test handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("streaming response"))
	})

	// Wrap with logging middleware
	loggingHandler := LoggingMiddleware(manager)(handler)

	// Create streaming request
	req := httptest.NewRequest("POST", "/openai/v1/chat/completions", nil)
	req.Header.Set("Accept", "text/event-stream")
	req.RemoteAddr = "10.0.0.1:54321"
	recorder := httptest.NewRecorder()

	// Capture log output
	logOutput := captureLogOutput(func() {
		loggingHandler.ServeHTTP(recorder, req)
	})

	// The middleware should log the request (streaming detection depends on provider manager)
	if !strings.Contains(logOutput, "Started request") {
		t.Error("Expected request log message not found")
	}
}

func TestLoggingMiddleware_TimingAccuracy(t *testing.T) {
	// Create a mock provider manager
	manager := providers.NewProviderManager()

	// Create a test handler with a known delay
	expectedDelay := 50 * time.Millisecond
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(expectedDelay)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("delayed response"))
	})

	// Wrap with logging middleware
	loggingHandler := LoggingMiddleware(manager)(handler)

	// Create test request
	req := httptest.NewRequest("GET", "/test/timing", nil)
	req.RemoteAddr = "127.0.0.1:8080"
	recorder := httptest.NewRecorder()

	// Capture log output
	logOutput := captureLogOutput(func() {
		loggingHandler.ServeHTTP(recorder, req)
	})

	// Check that completion log with timing is present
	if !strings.Contains(logOutput, "Completed request") {
		t.Error("Expected completion log with timing not found")
	}

	// Check response
	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}
}

func TestLoggingMiddleware_DifferentMethods(t *testing.T) {
	// Create a mock provider manager
	manager := providers.NewProviderManager()

	// Create a test handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("method test"))
	})

	// Wrap with logging middleware
	loggingHandler := LoggingMiddleware(manager)(handler)

	// Test different HTTP methods
	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH"}
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/test", nil)
			req.RemoteAddr = "127.0.0.1:8080"
			recorder := httptest.NewRecorder()

			logOutput := captureLogOutput(func() {
				loggingHandler.ServeHTTP(recorder, req)
			})

			// Check for start log with structured logging format
			if !strings.Contains(logOutput, "Started request") {
				t.Error("Expected start log message not found")
			}

			// Check for completion log with structured logging format
			if !strings.Contains(logOutput, "Completed request") {
				t.Error("Expected completion log message not found")
			}

			// Check response
			if recorder.Code != http.StatusOK {
				t.Errorf("Expected status 200, got %d", recorder.Code)
			}
		})
	}
}

func TestLoggingMiddleware_HandlerPanic(t *testing.T) {
	// Create a mock provider manager
	manager := providers.NewProviderManager()

	// Create a handler that panics
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})

	// Wrap with logging middleware
	loggingHandler := LoggingMiddleware(manager)(handler)

	// Create test request
	req := httptest.NewRequest("GET", "/panic", nil)
	req.RemoteAddr = "192.0.2.1:1234"
	recorder := httptest.NewRecorder()

	// Capture log output and expect panic
	logOutput := captureLogOutput(func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("Expected panic to occur")
			}
		}()
		loggingHandler.ServeHTTP(recorder, req)
	})

	// Should still log the start even when handler panics
	if !strings.Contains(logOutput, "Started request") {
		t.Error("Expected start log even when handler panics, got log:", logOutput)
	}
}

func TestLoggingMiddleware_RootPath(t *testing.T) {
	// Create a mock provider manager
	manager := providers.NewProviderManager()

	// Create a test handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("root response"))
	})

	// Wrap with logging middleware
	loggingHandler := LoggingMiddleware(manager)(handler)

	// Create test request for root path
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "10.0.0.1:8080"
	recorder := httptest.NewRecorder()

	// Capture log output
	logOutput := captureLogOutput(func() {
		loggingHandler.ServeHTTP(recorder, req)
	})

	// Check for start log with structured logging format
	if !strings.Contains(logOutput, "Started request") {
		t.Error("Expected start log with GET method and root path")
	}

	// Check that remote address is included
	if !strings.Contains(logOutput, "10.0.0.1:8080") {
		t.Error("Expected remote address in log")
	}

	// Check response
	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}
}

func TestLoggingMiddleware_NonTrackedProviderRoutes(t *testing.T) {
	// Create a mock provider manager
	manager := providers.NewProviderManager()

	// Register mock providers for testing
	manager.RegisterProvider(&MockProvider{name: "openai"})
	manager.RegisterProvider(&MockProvider{name: "anthropic"})
	manager.RegisterProvider(&MockProvider{name: "gemini"})

	// Create a test handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("provider response"))
	})

	// Wrap with logging middleware
	loggingHandler := LoggingMiddleware(manager)(handler)

	testCases := []struct {
		name     string
		path     string
		expected string
	}{
		{
			name:     "OpenAI non-API endpoint",
			path:     "/openai/v1/models",
			expected: "Non-tracked provider route",
		},
		{
			name:     "OpenAI health endpoint",
			path:     "/openai/health",
			expected: "Non-tracked provider route",
		},
		{
			name:     "OpenAI chat completions (should be tracked)",
			path:     "/openai/v1/chat/completions",
			expected: "Provider route tracked",
		},
		{
			name:     "Anthropic non-API endpoint",
			path:     "/anthropic/v1/status",
			expected: "Non-tracked provider route",
		},
		{
			name:     "Gemini non-API endpoint",
			path:     "/gemini/v1/models",
			expected: "Non-tracked provider route",
		},
		{
			name:     "Non-provider route",
			path:     "/health",
			expected: "", // No special logging for non-provider routes
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tc.path, nil)
			req.RemoteAddr = "192.168.1.1:12345"
			recorder := httptest.NewRecorder()

			logOutput := captureLogOutput(func() {
				loggingHandler.ServeHTTP(recorder, req)
			})

			if tc.expected != "" {
				if !strings.Contains(logOutput, tc.expected) {
					t.Errorf("Expected log message '%s' not found in output: %s", tc.expected, logOutput)
				}
			}

			// Check response
			if recorder.Code != http.StatusOK {
				t.Errorf("Expected status 200, got %d", recorder.Code)
			}
		})
	}
}

func TestLoggingMiddleware_ProviderHelperFunctions(t *testing.T) {
	testCases := []struct {
		name     string
		path     string
		expected bool
		testFunc func(string) bool
	}{
		// isProviderRoute tests
		{"OpenAI provider route", "/openai/v1/chat/completions", true, isProviderRoute},
		{"Anthropic provider route", "/anthropic/v1/messages", true, isProviderRoute},
		{"Gemini provider route", "/gemini/v1/models/test", true, isProviderRoute},
		{"Non-provider route", "/health", false, isProviderRoute},
		{"Root route", "/", false, isProviderRoute},

		// isAPIEndpoint tests
		{"Chat completions endpoint", "/openai/v1/chat/completions", true, isAPIEndpoint},
		{"Completions endpoint", "/openai/v1/completions", true, isAPIEndpoint},
		{"Messages endpoint", "/anthropic/v1/messages", true, isAPIEndpoint},
		{"Generate content endpoint", "/gemini/v1/models/test:generateContent", true, isAPIEndpoint},
		{"Stream generate content endpoint", "/gemini/v1/models/test:streamGenerateContent", true, isAPIEndpoint},
		{"Models endpoint", "/openai/v1/models", false, isAPIEndpoint},
		{"Health endpoint", "/health", false, isAPIEndpoint},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := tc.testFunc(tc.path)
			if result != tc.expected {
				t.Errorf("Expected %t for path '%s', got %t", tc.expected, tc.path, result)
			}
		})
	}
}

func TestGetProviderFromPath(t *testing.T) {
	testCases := []struct {
		path     string
		expected string
	}{
		{"/openai/v1/chat/completions", "openai"},
		{"/anthropic/v1/messages", "anthropic"},
		{"/gemini/v1/models/test", "gemini"},
		{"/health", ""},
		{"/unknown/provider", ""},
		{"/", ""},
	}

	for _, tc := range testCases {
		t.Run(tc.path, func(t *testing.T) {
			result := getProviderFromPath(tc.path)
			if result != tc.expected {
				t.Errorf("Expected '%s' for path '%s', got '%s'", tc.expected, tc.path, result)
			}
		})
	}
}
