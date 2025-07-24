package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/providers"
)

func TestStreamingMiddleware_NonStreamingRequest(t *testing.T) {
	// Create a mock provider manager
	manager := providers.NewProviderManager()
	
	// Create a test handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("regular response"))
	})
	
	// Wrap with streaming middleware
	streamingHandler := StreamingMiddleware(manager)(handler)
	
	// Create non-streaming request
	req := httptest.NewRequest("GET", "/test", nil)
	recorder := httptest.NewRecorder()
	
	// Execute request
	streamingHandler.ServeHTTP(recorder, req)
	
	// Check response (should pass through normally for non-streaming)
	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}
	
	if recorder.Body.String() != "regular response" {
		t.Errorf("Expected body 'regular response', got '%s'", recorder.Body.String())
	}
}

func TestStreamingMiddleware_BasicHandlerExecution(t *testing.T) {
	// Create a mock provider manager
	manager := providers.NewProviderManager()
	
	// Create a test handler that writes multiple chunks
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("chunk 1\n"))
		w.Write([]byte("chunk 2\n"))
		w.Write([]byte("chunk 3\n"))
	})
	
	// Wrap with streaming middleware
	streamingHandler := StreamingMiddleware(manager)(handler)
	
	// Create request
	req := httptest.NewRequest("POST", "/api/test", nil)
	recorder := httptest.NewRecorder()
	
	// Execute request
	streamingHandler.ServeHTTP(recorder, req)
	
	// Check response
	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}
	
	expectedBody := "chunk 1\nchunk 2\nchunk 3\n"
	if recorder.Body.String() != expectedBody {
		t.Errorf("Expected body '%s', got '%s'", expectedBody, recorder.Body.String())
	}
}

func TestStreamingResponseWriter_Write(t *testing.T) {
	// Create a mock response recorder that implements Flusher
	recorder := &MockFlushableRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		flushCalled:      false,
	}
	
	// Create streaming response writer
	streamingWriter := &streamingResponseWriter{
		ResponseWriter: recorder,
		flusher:        recorder,
	}
	
	// Write data
	testData := []byte("test streaming data")
	n, err := streamingWriter.Write(testData)
	
	// Check write result
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	
	if n != len(testData) {
		t.Errorf("Expected %d bytes written, got %d", len(testData), n)
	}
	
	// Check that flush was called
	if !recorder.flushCalled {
		t.Error("Expected flush to be called after write")
	}
	
	// Check that data was written
	if recorder.Body.String() != string(testData) {
		t.Errorf("Expected body '%s', got '%s'", string(testData), recorder.Body.String())
	}
}

func TestStreamingResponseWriter_MultipleWrites(t *testing.T) {
	// Create a mock response recorder that implements Flusher
	recorder := &MockFlushableRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		flushCallCount:   0,
	}
	
	// Create streaming response writer
	streamingWriter := &streamingResponseWriter{
		ResponseWriter: recorder,
		flusher:        recorder,
	}
	
	// Write multiple pieces of data
	data1 := []byte("first chunk")
	data2 := []byte(" second chunk")
	data3 := []byte(" third chunk")
	
	streamingWriter.Write(data1)
	streamingWriter.Write(data2)
	streamingWriter.Write(data3)
	
	// Check that flush was called for each write
	if recorder.flushCallCount != 3 {
		t.Errorf("Expected flush to be called 3 times, got %d", recorder.flushCallCount)
	}
	
	// Check final data
	expectedData := "first chunk second chunk third chunk"
	if recorder.Body.String() != expectedData {
		t.Errorf("Expected body '%s', got '%s'", expectedData, recorder.Body.String())
	}
}

func TestStreamingMiddleware_HandlerPanic(t *testing.T) {
	// Create a mock provider manager
	manager := providers.NewProviderManager()
	
	// Create a handler that panics
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		panic("test panic")
	})
	
	// Wrap with streaming middleware
	streamingHandler := StreamingMiddleware(manager)(handler)
	
	// Create test request
	req := httptest.NewRequest("POST", "/panic", nil)
	recorder := httptest.NewRecorder()
	
	// The middleware doesn't handle panics, so this will panic
	var panicked bool
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicked = true
			}
		}()
		
		streamingHandler.ServeHTTP(recorder, req)
	}()
	
	if !panicked {
		t.Error("Expected handler to panic")
	}
	
	// The middleware doesn't change panic behavior
	// This test verifies that the middleware doesn't interfere with panic propagation
}

// Mock flushable recorder for testing
type MockFlushableRecorder struct {
	*httptest.ResponseRecorder
	flushCalled    bool
	flushCallCount int
}

func (m *MockFlushableRecorder) Flush() {
	m.flushCalled = true
	m.flushCallCount++
} 
