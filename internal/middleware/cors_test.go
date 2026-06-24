package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Instawork/llm-proxy/internal/providers"
)

func TestCORSMiddleware_BasicHeaders(t *testing.T) {
	// Create a mock provider manager
	manager := providers.NewProviderManager()

	// Create a test handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("test response"))
	})

	// Wrap with CORS middleware
	corsHandler := CORSMiddleware(manager)(handler)

	// Create test request
	req := httptest.NewRequest("GET", "/test", nil)
	recorder := httptest.NewRecorder()

	// Execute request
	corsHandler.ServeHTTP(recorder, req)

	// Check CORS headers
	if recorder.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("Expected Access-Control-Allow-Origin to be '*', got '%s'", recorder.Header().Get("Access-Control-Allow-Origin"))
	}

	expectedMethods := "GET, POST, PUT, DELETE, OPTIONS"
	if recorder.Header().Get("Access-Control-Allow-Methods") != expectedMethods {
		t.Errorf("Expected Access-Control-Allow-Methods to be '%s', got '%s'", expectedMethods, recorder.Header().Get("Access-Control-Allow-Methods"))
	}

	// Allow-Headers must include every provider auth header browsers might
	// send. We assert each token individually because the exact concatenation
	// order isn't part of the contract; a missing token would silently break
	// browser-based clients.
	gotHeaders := recorder.Header().Get("Access-Control-Allow-Headers")
	required := []string{
		"Content-Type", "Authorization", "Accept", "Cache-Control",
		"x-api-key", "anthropic-version", "x-goog-api-key",
		"X-LLM-Proxy-Test-Mode",
	}
	for _, h := range required {
		if !strings.Contains(gotHeaders, h) {
			t.Errorf("Access-Control-Allow-Headers %q missing required token %q", gotHeaders, h)
		}
	}

	// Check response status and body
	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}

	if recorder.Body.String() != "test response" {
		t.Errorf("Expected body 'test response', got '%s'", recorder.Body.String())
	}
}

func TestCORSMiddleware_OptionsRequest(t *testing.T) {
	// Create a mock provider manager
	manager := providers.NewProviderManager()

	// Create a test handler that should not be called for OPTIONS
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("Handler should not be called for OPTIONS request")
	})

	// Wrap with CORS middleware
	corsHandler := CORSMiddleware(manager)(handler)

	// Create OPTIONS request
	req := httptest.NewRequest("OPTIONS", "/test", nil)
	recorder := httptest.NewRecorder()

	// Execute request
	corsHandler.ServeHTTP(recorder, req)

	// Check that OPTIONS returns 200 and doesn't call next handler
	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status 200 for OPTIONS, got %d", recorder.Code)
	}

	// Check CORS headers are still set
	if recorder.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("CORS headers should be set for OPTIONS request")
	}

	// Response body should be empty for OPTIONS
	if recorder.Body.String() != "" {
		t.Errorf("Expected empty body for OPTIONS, got '%s'", recorder.Body.String())
	}
}

func TestCORSMiddleware_DifferentMethods(t *testing.T) {
	// Create a mock provider manager
	manager := providers.NewProviderManager()

	// Create a simple test handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	})

	// Wrap with CORS middleware
	corsHandler := CORSMiddleware(manager)(handler)

	// Test different HTTP methods
	methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH"}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/api/test", nil)
			recorder := httptest.NewRecorder()

			corsHandler.ServeHTTP(recorder, req)

			// Check that CORS headers are always set
			if recorder.Header().Get("Access-Control-Allow-Origin") != "*" {
				t.Errorf("CORS headers should be set for %s method", method)
			}

			// Check response
			if recorder.Code != http.StatusOK {
				t.Errorf("Expected status 200 for %s, got %d", method, recorder.Code)
			}
		})
	}
}

func TestCORSMiddleware_HandlerError(t *testing.T) {
	// Create a mock provider manager
	manager := providers.NewProviderManager()

	// Create a handler that returns an error
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	})

	// Wrap with CORS middleware
	corsHandler := CORSMiddleware(manager)(handler)

	// Create test request
	req := httptest.NewRequest("POST", "/error", nil)
	recorder := httptest.NewRecorder()

	// Execute request
	corsHandler.ServeHTTP(recorder, req)

	// CORS headers should still be set even on error
	if recorder.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("CORS headers should be set even on error responses")
	}

	// Check error response
	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", recorder.Code)
	}

	if recorder.Body.String() != "internal error" {
		t.Errorf("Expected body 'internal error', got '%s'", recorder.Body.String())
	}
}

// TestCORSMiddleware_StreamingRequest_ExposesContentTypeAndCacheControl
// verifies that streaming requests get an Access-Control-Expose-Headers
// response header so browsers can read the streaming-related response
// headers. Without it, EventSource/fetch-stream consumers can't see the
// SSE Content-Type and Cache-Control values.
func TestCORSMiddleware_StreamingRequest_ExposesContentTypeAndCacheControl(t *testing.T) {
	pm := providers.NewProviderManager()
	pm.RegisterProvider(providers.NewAnthropicProxy())

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	chain := CORSMiddleware(pm)(handler)

	req := httptest.NewRequest("POST", "/anthropic/v1/messages", nil)
	req.Header.Set("Accept", "text/event-stream")
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, req)

	exposed := rr.Header().Get("Access-Control-Expose-Headers")
	if !strings.Contains(exposed, "Content-Type") || !strings.Contains(exposed, "Cache-Control") {
		t.Fatalf("streaming request must expose Content-Type and Cache-Control; got %q", exposed)
	}
	if !strings.Contains(exposed, "X-LLM-PII-Outcome") {
		t.Fatalf("must expose PII headers; got %q", exposed)
	}
}

func TestCORSMiddleware_NonStreamingRequest_ExposesPIIHeaders(t *testing.T) {
	pm := providers.NewProviderManager()
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	chain := CORSMiddleware(pm)(handler)

	req := httptest.NewRequest("POST", "/openai/v1/chat/completions", nil)
	rr := httptest.NewRecorder()
	chain.ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Expose-Headers"); !strings.Contains(got, "X-LLM-PII-Outcome") {
		t.Fatalf("non-streaming request must expose PII headers; got %q", got)
	}
}

func TestCORSMiddleware_EmptyProviderManager(t *testing.T) {
	// Test with an empty provider manager (no providers registered)
	manager := providers.NewProviderManager()

	// Create a test handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("no providers"))
	})

	// Wrap with CORS middleware
	corsHandler := CORSMiddleware(manager)(handler)

	// Create test request
	req := httptest.NewRequest("GET", "/test", nil)
	recorder := httptest.NewRecorder()

	// Execute request
	corsHandler.ServeHTTP(recorder, req)

	// CORS headers should still be set
	if recorder.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("CORS headers should be set even with empty provider manager")
	}

	// Check response
	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}
}
