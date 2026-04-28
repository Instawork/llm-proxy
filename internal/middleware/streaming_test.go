package middleware

import (
	"bytes"
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

// TestResponseCapture_ImplementsFlusher guards against regression of the bug
// where TokenParsingMiddleware's responseCapture wrapper hid the http.Flusher
// interface from downstream middleware, causing SSE streams to be buffered
// instead of flushed chunk-by-chunk. See token_parsing.go Flush().
func TestResponseCapture_ImplementsFlusher(t *testing.T) {
	rc := &responseCapture{
		ResponseWriter: httptest.NewRecorder(),
		body:           &bytes.Buffer{},
	}

	if _, ok := interface{}(rc).(http.Flusher); !ok {
		t.Fatal("responseCapture must implement http.Flusher so StreamingMiddleware can detect Flusher capability")
	}
}

// TestResponseCapture_FlushDelegatesToUnderlying verifies that Flush() on
// responseCapture calls Flush on the underlying ResponseWriter when supported.
func TestResponseCapture_FlushDelegatesToUnderlying(t *testing.T) {
	recorder := &MockFlushableRecorder{
		ResponseRecorder: httptest.NewRecorder(),
	}
	rc := &responseCapture{
		ResponseWriter: recorder,
		body:           &bytes.Buffer{},
	}

	rc.Flush()
	rc.Flush()

	if recorder.flushCallCount != 2 {
		t.Errorf("Expected 2 flush calls on underlying writer, got %d", recorder.flushCallCount)
	}
}

// TestResponseCapture_FlushNoopWhenUnderlyingNotFlushable ensures Flush() is a
// safe no-op when the underlying ResponseWriter does not support flushing,
// rather than panicking with a failed type assertion.
func TestResponseCapture_FlushNoopWhenUnderlyingNotFlushable(t *testing.T) {
	rc := &responseCapture{
		ResponseWriter: httptest.NewRecorder(), // plain recorder does not implement Flusher
		body:           &bytes.Buffer{},
	}

	defer func() {
		if r := recover(); r != nil {
			t.Errorf("Flush() panicked when underlying writer does not support flushing: %v", r)
		}
	}()

	rc.Flush()
}

// TestTokenParsingThenStreaming_FlushesEachWrite verifies the production
// middleware chain order from cmd/llm-proxy/main.go:
//
//	TokenParsingMiddleware -> StreamingMiddleware -> handler
//
// In the broken state, StreamingMiddleware logged
// "Warning: ResponseWriter does not support flushing for streaming request"
// because responseCapture (from TokenParsingMiddleware) did not expose
// http.Flusher. With Flush() on responseCapture the chain flushes each chunk.
func TestTokenParsingThenStreaming_FlushesEachWrite(t *testing.T) {
	manager := providers.NewProviderManager()
	manager.RegisterProvider(providers.NewAnthropicProxy())

	// Handler emits 3 chunks like an SSE stream would.
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("data: chunk1\n\n"))
		w.Write([]byte("data: chunk2\n\n"))
		w.Write([]byte("data: chunk3\n\n"))
	})

	// Compose middleware in the same order as main.go:
	// TokenParsing wraps Streaming, which wraps the handler.
	chain := TokenParsingMiddleware(manager)(StreamingMiddleware(manager)(handler))

	// Mimic an Anthropic streaming request: matching path + Accept header so
	// IsStreamingRequest returns true via the fast-path check.
	req := httptest.NewRequest("POST", "/anthropic/v1/messages", nil)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")

	recorder := &MockFlushableRecorder{
		ResponseRecorder: httptest.NewRecorder(),
	}

	chain.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", recorder.Code)
	}

	// Critical assertion: streaming middleware must have detected Flusher
	// capability on responseCapture and called Flush after each Write.
	// Without the Flush() method on responseCapture, this would be 0.
	if recorder.flushCallCount == 0 {
		t.Fatal("Expected Flush to be called at least once; StreamingMiddleware did not detect Flusher on responseCapture")
	}
	if recorder.flushCallCount < 3 {
		t.Errorf("Expected Flush to be called for each of the 3 chunks, got %d", recorder.flushCallCount)
	}

	expectedBody := "data: chunk1\n\ndata: chunk2\n\ndata: chunk3\n\n"
	if recorder.Body.String() != expectedBody {
		t.Errorf("Expected body %q, got %q", expectedBody, recorder.Body.String())
	}
}
