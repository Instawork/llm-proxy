package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/providers"
)

func TestMetaURLRewritingMiddleware_BasicRewriting(t *testing.T) {
	// Create provider manager and register providers
	manager := providers.NewProviderManager()
	openAIProvider := providers.NewOpenAIProxy()
	anthropicProvider := providers.NewAnthropicProxy()
	geminiProvider := providers.NewGeminiProxy()
	manager.RegisterProvider(openAIProvider)
	manager.RegisterProvider(anthropicProvider)
	manager.RegisterProvider(geminiProvider)

	// Create test handler that captures the final URL
	var finalURL string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalURL = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	})

	// Wrap with meta URL rewriting middleware
	rewritingHandler := MetaURLRewritingMiddleware(manager)(handler)

	tests := []struct {
		name        string
		requestURL  string
		expectedURL string
	}{
		{
			name:        "OpenAI with path",
			requestURL:  "/meta/user123/openai/v1/chat/completions",
			expectedURL: "/openai/v1/chat/completions",
		},
		{
			name:        "Anthropic with path",
			requestURL:  "/meta/user456/anthropic/v1/messages",
			expectedURL: "/anthropic/v1/messages",
		},
		{
			name:        "Gemini with path",
			requestURL:  "/meta/user789/gemini/v1/models/gemini-pro:generateContent",
			expectedURL: "/gemini/v1/models/gemini-pro:generateContent",
		},
		{
			name:        "OpenAI without additional path",
			requestURL:  "/meta/user123/openai/",
			expectedURL: "/openai/",
		},
		{
			name:        "OpenAI with trailing slash only",
			requestURL:  "/meta/user123/openai",
			expectedURL: "/openai/",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tt.requestURL, nil)
			recorder := httptest.NewRecorder()

			rewritingHandler.ServeHTTP(recorder, req)

			if finalURL != tt.expectedURL {
				t.Errorf("Expected URL '%s', got '%s'", tt.expectedURL, finalURL)
			}

			if recorder.Code != http.StatusOK {
				t.Errorf("Expected status 200, got %d", recorder.Code)
			}
		})
	}
}

func TestMetaURLRewritingMiddleware_InvalidProvider(t *testing.T) {
	// Create provider manager with only OpenAI registered
	manager := providers.NewProviderManager()
	openAIProvider := providers.NewOpenAIProxy()
	manager.RegisterProvider(openAIProvider)

	// Create test handler that captures the final URL
	var finalURL string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalURL = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	})

	// Wrap with meta URL rewriting middleware
	rewritingHandler := MetaURLRewritingMiddleware(manager)(handler)

	tests := []struct {
		name        string
		requestURL  string
		expectedURL string // Should remain unchanged for invalid providers
	}{
		{
			name:        "Unregistered provider - anthropic",
			requestURL:  "/meta/user123/anthropic/v1/messages",
			expectedURL: "/meta/user123/anthropic/v1/messages",
		},
		{
			name:        "Unregistered provider - gemini",
			requestURL:  "/meta/user456/gemini/v1/models/gemini-pro:generateContent",
			expectedURL: "/meta/user456/gemini/v1/models/gemini-pro:generateContent",
		},
		{
			name:        "Unknown provider",
			requestURL:  "/meta/user789/unknown/v1/test",
			expectedURL: "/meta/user789/unknown/v1/test",
		},
		{
			name:        "Valid provider - openai",
			requestURL:  "/meta/user123/openai/v1/chat/completions",
			expectedURL: "/openai/v1/chat/completions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tt.requestURL, nil)
			recorder := httptest.NewRecorder()

			rewritingHandler.ServeHTTP(recorder, req)

			if finalURL != tt.expectedURL {
				t.Errorf("Expected URL '%s', got '%s'", tt.expectedURL, finalURL)
			}

			if recorder.Code != http.StatusOK {
				t.Errorf("Expected status 200, got %d", recorder.Code)
			}
		})
	}
}

func TestMetaURLRewritingMiddleware_NonMetaPaths(t *testing.T) {
	// Create provider manager
	manager := providers.NewProviderManager()
	openAIProvider := providers.NewOpenAIProxy()
	manager.RegisterProvider(openAIProvider)

	// Create test handler that captures the final URL
	var finalURL string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalURL = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	})

	// Wrap with meta URL rewriting middleware
	rewritingHandler := MetaURLRewritingMiddleware(manager)(handler)

	tests := []struct {
		name        string
		requestURL  string
		expectedURL string // Should remain unchanged for non-meta paths
	}{
		{
			name:        "Direct OpenAI path",
			requestURL:  "/openai/v1/chat/completions",
			expectedURL: "/openai/v1/chat/completions",
		},
		{
			name:        "Health endpoint",
			requestURL:  "/health",
			expectedURL: "/health",
		},
		{
			name:        "Root path",
			requestURL:  "/",
			expectedURL: "/",
		},
		{
			name:        "Random path",
			requestURL:  "/some/random/path",
			expectedURL: "/some/random/path",
		},
		{
			name:        "Meta-like but incomplete path",
			requestURL:  "/meta/user123",
			expectedURL: "/meta/user123",
		},
		{
			name:        "Meta prefix but different structure",
			requestURL:  "/metadata/user123/openai/test",
			expectedURL: "/metadata/user123/openai/test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.requestURL, nil)
			recorder := httptest.NewRecorder()

			rewritingHandler.ServeHTTP(recorder, req)

			if finalURL != tt.expectedURL {
				t.Errorf("Expected URL '%s', got '%s'", tt.expectedURL, finalURL)
			}

			if recorder.Code != http.StatusOK {
				t.Errorf("Expected status 200, got %d", recorder.Code)
			}
		})
	}
}

func TestMetaURLRewritingMiddleware_MalformedPaths(t *testing.T) {
	// Create provider manager
	manager := providers.NewProviderManager()
	openAIProvider := providers.NewOpenAIProxy()
	manager.RegisterProvider(openAIProvider)

	// Create test handler that captures the final URL
	var finalURL string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalURL = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	})

	// Wrap with meta URL rewriting middleware
	rewritingHandler := MetaURLRewritingMiddleware(manager)(handler)

	tests := []struct {
		name        string
		requestURL  string
		expectedURL string // Should remain unchanged for malformed paths
	}{
		{
			name:        "Meta with empty userID",
			requestURL:  "/meta//openai/v1/chat/completions",
			expectedURL: "/openai/v1/chat/completions", // Empty userID still allows rewriting
		},
		{
			name:        "Meta with missing provider",
			requestURL:  "/meta/user123/",
			expectedURL: "/meta/user123/",
		},
		{
			name:        "Meta with only userID",
			requestURL:  "/meta/user123",
			expectedURL: "/meta/user123",
		},
		{
			name:        "Meta root",
			requestURL:  "/meta/",
			expectedURL: "/meta/",
		},
		{
			name:        "Just meta",
			requestURL:  "/meta",
			expectedURL: "/meta",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tt.requestURL, nil)
			recorder := httptest.NewRecorder()

			rewritingHandler.ServeHTTP(recorder, req)

			if finalURL != tt.expectedURL {
				t.Errorf("Expected URL '%s', got '%s'", tt.expectedURL, finalURL)
			}

			if recorder.Code != http.StatusOK {
				t.Errorf("Expected status 200, got %d", recorder.Code)
			}
		})
	}
}

func TestMetaURLRewritingMiddleware_HTTPMethods(t *testing.T) {
	// Create provider manager
	manager := providers.NewProviderManager()
	openAIProvider := providers.NewOpenAIProxy()
	manager.RegisterProvider(openAIProvider)

	// Create test handler that captures the final URL and method
	var finalURL string
	var finalMethod string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalURL = r.URL.Path
		finalMethod = r.Method
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	})

	// Wrap with meta URL rewriting middleware
	rewritingHandler := MetaURLRewritingMiddleware(manager)(handler)

	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH", "OPTIONS"}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/meta/user123/openai/v1/chat/completions", nil)
			recorder := httptest.NewRecorder()

			rewritingHandler.ServeHTTP(recorder, req)

			expectedURL := "/openai/v1/chat/completions"
			if finalURL != expectedURL {
				t.Errorf("Expected URL '%s', got '%s'", expectedURL, finalURL)
			}

			if finalMethod != method {
				t.Errorf("Expected method '%s', got '%s'", method, finalMethod)
			}

			if recorder.Code != http.StatusOK {
				t.Errorf("Expected status 200, got %d", recorder.Code)
			}
		})
	}
}

func TestMetaURLRewritingMiddleware_EmptyProviderManager(t *testing.T) {
	// Create empty provider manager (no providers registered)
	manager := providers.NewProviderManager()

	// Create test handler that captures the final URL
	var finalURL string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalURL = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	})

	// Wrap with meta URL rewriting middleware
	rewritingHandler := MetaURLRewritingMiddleware(manager)(handler)

	// Test that nothing gets rewritten when no providers are registered
	req := httptest.NewRequest("POST", "/meta/user123/openai/v1/chat/completions", nil)
	recorder := httptest.NewRecorder()

	rewritingHandler.ServeHTTP(recorder, req)

	expectedURL := "/meta/user123/openai/v1/chat/completions" // Should remain unchanged
	if finalURL != expectedURL {
		t.Errorf("Expected URL '%s', got '%s'", expectedURL, finalURL)
	}

	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}
}

func TestMetaURLRewritingMiddleware_UserIDContext(t *testing.T) {
	// Create provider manager
	manager := providers.NewProviderManager()
	openAIProvider := providers.NewOpenAIProxy()
	manager.RegisterProvider(openAIProvider)

	// Create test handler that extracts user ID using ExtractUserIDFromRequest
	var extractedUserID string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// This simulates what the cost tracking callback does
		provider := GetProviderFromRequest(manager, r)
		extractedUserID = ExtractUserIDFromRequest(r, provider)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	})

	// Wrap with meta URL rewriting middleware
	rewritingHandler := MetaURLRewritingMiddleware(manager)(handler)

	// Test with meta URL that should extract user ID
	req := httptest.NewRequest("POST", "/meta/test-user-456/openai/v1/chat/completions", nil)
	recorder := httptest.NewRecorder()

	rewritingHandler.ServeHTTP(recorder, req)

	// Verify URL was rewritten correctly
	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}

	// Verify user ID was extracted and available via ExtractUserIDFromRequest
	if extractedUserID != "test-user-456" {
		t.Errorf("Expected user ID 'test-user-456' to be extracted from context, got '%s'", extractedUserID)
	}
}

func TestMetaURLRewritingMiddleware_HandlerError(t *testing.T) {
	// Create provider manager
	manager := providers.NewProviderManager()
	openAIProvider := providers.NewOpenAIProxy()
	manager.RegisterProvider(openAIProvider)

	// Create test handler that returns an error
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify URL was rewritten correctly before error
		expectedURL := "/openai/v1/chat/completions"
		if r.URL.Path != expectedURL {
			t.Errorf("Expected URL '%s', got '%s'", expectedURL, r.URL.Path)
		}
		
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	})

	// Wrap with meta URL rewriting middleware
	rewritingHandler := MetaURLRewritingMiddleware(manager)(handler)

	req := httptest.NewRequest("POST", "/meta/user123/openai/v1/chat/completions", nil)
	recorder := httptest.NewRecorder()

	rewritingHandler.ServeHTTP(recorder, req)

	// Should still handle the error correctly after rewriting
	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", recorder.Code)
	}

	if recorder.Body.String() != "internal error" {
		t.Errorf("Expected body 'internal error', got '%s'", recorder.Body.String())
	}
}

func TestMetaURLRewritingMiddleware_ComplexPaths(t *testing.T) {
	// Create provider manager
	manager := providers.NewProviderManager()
	openAIProvider := providers.NewOpenAIProxy()
	geminiProvider := providers.NewGeminiProxy()
	manager.RegisterProvider(openAIProvider)
	manager.RegisterProvider(geminiProvider)

	// Create test handler that captures the final URL
	var finalURL string
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalURL = r.URL.Path
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	})

	// Wrap with meta URL rewriting middleware
	rewritingHandler := MetaURLRewritingMiddleware(manager)(handler)

	tests := []struct {
		name        string
		requestURL  string
		expectedURL string
	}{
		{
			name:        "Complex Gemini path",
			requestURL:  "/meta/user123/gemini/v1/models/gemini-pro:generateContent",
			expectedURL: "/gemini/v1/models/gemini-pro:generateContent",
		},
		{
			name:        "Path with query parameters", 
			requestURL:  "/meta/user456/openai/v1/chat/completions?stream=true",
			expectedURL: "/openai/v1/chat/completions",
		},
		{
			name:        "Deep nested path",
			requestURL:  "/meta/user789/openai/v1/engines/davinci/completions",
			expectedURL: "/openai/v1/engines/davinci/completions",
		},
		{
			name:        "Path with special characters",
			requestURL:  "/meta/user123/gemini/v1/models/gemini-1.5-pro:generateContent",
			expectedURL: "/gemini/v1/models/gemini-1.5-pro:generateContent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", tt.requestURL, nil)
			recorder := httptest.NewRecorder()

			rewritingHandler.ServeHTTP(recorder, req)

			if finalURL != tt.expectedURL {
				t.Errorf("Expected URL '%s', got '%s'", tt.expectedURL, finalURL)
			}

			if recorder.Code != http.StatusOK {
				t.Errorf("Expected status 200, got %d", recorder.Code)
			}
		})
	}
} 
