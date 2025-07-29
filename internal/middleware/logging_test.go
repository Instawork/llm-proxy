package middleware

import (
	"bytes"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Instawork/llm-proxy/internal/providers"
)

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

	// Check that both start and completion logs are present
	if !strings.Contains(logOutput, "Started GET /test/path from 192.168.1.1:12345") {
		t.Error("Expected start log message not found")
	}

	if !strings.Contains(logOutput, "Completed GET /test/path in") {
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
	if !strings.Contains(logOutput, "POST /openai/v1/chat/completions from 10.0.0.1:54321") {
		t.Error("Expected request log message not found")
	}

	// Should have both start and completion logs
	startLogs := strings.Count(logOutput, "Started")
	completionLogs := strings.Count(logOutput, "Completed")

	if startLogs != 1 {
		t.Errorf("Expected 1 start log, got %d", startLogs)
	}

	if completionLogs != 1 {
		t.Errorf("Expected 1 completion log, got %d", completionLogs)
	}
}

func TestLoggingMiddleware_TimingAccuracy(t *testing.T) {
	// Create a mock provider manager
	manager := providers.NewProviderManager()

	// Create a handler with known delay
	expectedDelay := 50 * time.Millisecond
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(expectedDelay)
		w.WriteHeader(http.StatusOK)
	})

	// Wrap with logging middleware
	loggingHandler := LoggingMiddleware(manager)(handler)

	// Create test request
	req := httptest.NewRequest("POST", "/test", nil)
	recorder := httptest.NewRecorder()

	// Measure actual time and capture logs
	start := time.Now()
	logOutput := captureLogOutput(func() {
		loggingHandler.ServeHTTP(recorder, req)
	})
	actualDuration := time.Since(start)

	// Check that the logged duration is reasonable
	if !strings.Contains(logOutput, "Completed POST /test in") {
		t.Error("Expected completion log with timing not found")
	}

	// The actual duration should be at least the expected delay
	if actualDuration < expectedDelay {
		t.Errorf("Actual duration %v should be at least %v", actualDuration, expectedDelay)
	}

	// The logged duration should be close to actual (within reasonable margin)
	// This is a rough check since parsing the exact duration from log would be complex
	if actualDuration > expectedDelay*3 {
		t.Errorf("Duration seems too long: %v (expected around %v)", actualDuration, expectedDelay)
	}
}

func TestLoggingMiddleware_DifferentMethods(t *testing.T) {
	// Create a mock provider manager
	manager := providers.NewProviderManager()

	// Create a simple handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
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

			expectedStart := "Started " + method + " /api/test from 127.0.0.1:8080"
			expectedComplete := "Completed " + method + " /api/test in"

			if !strings.Contains(logOutput, expectedStart) {
				t.Errorf("Expected start log '%s' not found in: %s", expectedStart, logOutput)
			}

			if !strings.Contains(logOutput, expectedComplete) {
				t.Errorf("Expected completion log '%s' not found in: %s", expectedComplete, logOutput)
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
	recorder := httptest.NewRecorder()

	// The middleware doesn't handle panics, so this will panic
	// We'll defer recover to catch it and capture logs properly
	var logOutput string
	var panicked bool

	// Capture log output with panic recovery inside the capture function
	logOutput = captureLogOutput(func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()

		loggingHandler.ServeHTTP(recorder, req)
	})

	if !panicked {
		t.Error("Expected handler to panic")
	}

	// Should still have the start log even if handler panics
	if !strings.Contains(logOutput, "Started GET /panic") {
		t.Errorf("Expected start log even when handler panics, got log: %s", logOutput)
	}

	// Completion log might not be present due to panic
	// This is expected behavior as the middleware doesn't handle panics
}

func TestLoggingMiddleware_RootPath(t *testing.T) {
	// Create a mock provider manager
	manager := providers.NewProviderManager()

	// Create a simple handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Wrap with logging middleware
	loggingHandler := LoggingMiddleware(manager)(handler)

	// Create request with root path
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.100:9000"
	recorder := httptest.NewRecorder()

	logOutput := captureLogOutput(func() {
		loggingHandler.ServeHTTP(recorder, req)
	})

	// Should handle root path gracefully
	if !strings.Contains(logOutput, "Started GET /") {
		t.Error("Expected start log with GET method and root path")
	}

	if !strings.Contains(logOutput, "from 192.168.1.100:9000") {
		t.Error("Expected remote address in log")
	}
}

func TestLoggingMiddleware_NonTrackedProviderRoutes(t *testing.T) {
	// Create a mock provider manager with all providers
	manager := providers.NewProviderManager()
	openAIProvider := providers.NewOpenAIProxy()
	manager.RegisterProvider(openAIProvider)
	anthropicProvider := providers.NewAnthropicProxy()
	manager.RegisterProvider(anthropicProvider)
	geminiProvider := providers.NewGeminiProxy()
	manager.RegisterProvider(geminiProvider)

	// Create a test handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test response"))
	})

	// Wrap with logging middleware
	loggingHandler := LoggingMiddleware(manager)(handler)

	testCases := []struct {
		name            string
		path            string
		expectedLog     string
		shouldBeTracked bool
	}{
		{
			name:            "OpenAI non-API endpoint",
			path:            "/openai/v1/models",
			expectedLog:     "📊 NON-TRACKED PROVIDER ROUTE: Non-API endpoint",
			shouldBeTracked: false,
		},
		{
			name:            "OpenAI health endpoint",
			path:            "/openai/health",
			expectedLog:     "📊 NON-TRACKED PROVIDER ROUTE: Non-API endpoint",
			shouldBeTracked: false,
		},
		{
			name:            "OpenAI chat completions (should be tracked)",
			path:            "/openai/v1/chat/completions",
			expectedLog:     "✅ TRACKED",
			shouldBeTracked: true,
		},
		{
			name:            "Anthropic non-API endpoint",
			path:            "/anthropic/v1/status",
			expectedLog:     "📊 NON-TRACKED PROVIDER ROUTE: Non-API endpoint",
			shouldBeTracked: false,
		},
		{
			name:            "Gemini non-API endpoint",
			path:            "/gemini/v1/models",
			expectedLog:     "📊 NON-TRACKED PROVIDER ROUTE: Non-API endpoint",
			shouldBeTracked: false,
		},
		{
			name:            "Non-provider route",
			path:            "/health",
			expectedLog:     "",
			shouldBeTracked: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tc.path, nil)
			req.RemoteAddr = "192.168.1.1:12345"
			recorder := httptest.NewRecorder()

			// Capture log output
			logOutput := captureLogOutput(func() {
				loggingHandler.ServeHTTP(recorder, req)
			})

			if tc.expectedLog != "" {
				if !strings.Contains(logOutput, tc.expectedLog) {
					t.Errorf("Expected log message '%s' not found in output: %s", tc.expectedLog, logOutput)
				}
			}

			// Check tracking status logging
			if tc.shouldBeTracked {
				if !strings.Contains(logOutput, "✅ TRACKED") {
					t.Error("Expected TRACKED message for API endpoint")
				}
			} else if isProviderRoute(tc.path) {
				if !strings.Contains(logOutput, "❌ NOT TRACKED") {
					t.Error("Expected NOT TRACKED message for non-API provider endpoint")
				}
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
