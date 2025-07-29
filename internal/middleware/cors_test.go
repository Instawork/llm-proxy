package middleware

import (
	"net/http"
	"net/http/httptest"
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

	expectedHeaders := "Content-Type, Authorization, Accept, Cache-Control"
	if recorder.Header().Get("Access-Control-Allow-Headers") != expectedHeaders {
		t.Errorf("Expected Access-Control-Allow-Headers to be '%s', got '%s'", expectedHeaders, recorder.Header().Get("Access-Control-Allow-Headers"))
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
