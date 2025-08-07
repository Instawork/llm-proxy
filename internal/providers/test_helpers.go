package providers

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
)

// MetadataCallback is a function type for handling parsed metadata
type MetadataCallback func(metadata *LLMResponseMetadata, err error)

// TestMetadataHook provides a way to hook into metadata parsing during tests
type TestMetadataHook struct {
	Provider         Provider
	OriginalResponse io.Reader
	IsStreaming      bool
	OnMetadataParsed MetadataCallback
}

// ParseAndValidateMetadata parses metadata and calls the callback
func (hook *TestMetadataHook) ParseAndValidateMetadata() {
	// Read the original response into a buffer so we can use it multiple times
	var responseBuffer bytes.Buffer
	_, err := responseBuffer.ReadFrom(hook.OriginalResponse)
	if err != nil {
		hook.OnMetadataParsed(nil, err)
		return
	}

	// Parse the metadata
	metadata, err := hook.Provider.ParseResponseMetadata(bytes.NewReader(responseBuffer.Bytes()), hook.IsStreaming)

	// Call the callback with the results
	hook.OnMetadataParsed(metadata, err)
}

// ValidateMetadataFields provides common validation for metadata fields
func ValidateMetadataFields(t *testing.T, metadata *LLMResponseMetadata, expectedProvider string, isStreaming bool) {
	if metadata == nil {
		t.Fatal("Metadata is nil")
		return
	}

	if metadata.Provider != expectedProvider {
		t.Errorf("Expected provider %s, got %s", expectedProvider, metadata.Provider)
	}

	if metadata.IsStreaming != isStreaming {
		t.Errorf("Expected IsStreaming %v, got %v", isStreaming, metadata.IsStreaming)
	}

	if metadata.Model == "" {
		t.Error("Model should not be empty")
	}

	// Validate token counts are not negative
	if metadata.TotalTokens < 0 {
		t.Error("Total tokens should not be negative")
	}

	if metadata.InputTokens < 0 {
		t.Error("Input tokens should not be negative")
	}

	if metadata.OutputTokens < 0 {
		t.Error("Output tokens should not be negative")
	}

	// For streaming responses, usage information might not be available in all chunks
	// So we're more lenient and only check if tokens are non-negative
	if isStreaming {
		// For streaming, we might have partial or complete usage information
		if metadata.TotalTokens > 0 {
			t.Logf("Complete usage information found in streaming response")
		} else {
			t.Logf("Partial usage information in streaming response (expected for some chunks)")
		}
	} else {
		// For non-streaming responses, we expect positive token counts
		if metadata.TotalTokens <= 0 {
			t.Error("Total tokens should be positive for non-streaming responses")
		}
		if metadata.InputTokens <= 0 {
			t.Error("Input tokens should be positive for non-streaming responses")
		}
		if metadata.OutputTokens <= 0 {
			t.Error("Output tokens should be positive for non-streaming responses")
		}
	}
}

// TestWithMetadataHook creates a test hook for metadata parsing
func TestWithMetadataHook(provider Provider, response io.Reader, isStreaming bool, callback MetadataCallback) *TestMetadataHook {
	return &TestMetadataHook{
		Provider:         provider,
		OriginalResponse: response,
		IsStreaming:      isStreaming,
		OnMetadataParsed: callback,
	}
}

// CaptureResponseAndParseMetadata captures HTTP response and parses metadata
func CaptureResponseAndParseMetadata(t *testing.T, resp *http.Response, provider Provider, isStreaming bool) *LLMResponseMetadata {
	// Read the response body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	// Parse the metadata
	metadata, err := provider.ParseResponseMetadata(bytes.NewReader(bodyBytes), isStreaming)
	if err != nil {
		t.Fatalf("Failed to parse metadata: %v", err)
	}

	return metadata
}

// Test server setup
func setupTestServer(t *testing.T) (*httptest.Server, *ProviderManager) {
	r := mux.NewRouter()

	// Create provider manager
	manager := NewProviderManager()

	// Register providers
	openAIProvider := NewOpenAIProxy()
	manager.RegisterProvider(openAIProvider)

	anthropicProvider := NewAnthropicProxy()
	manager.RegisterProvider(anthropicProvider)

	geminiProvider := NewGeminiProxy()
	manager.RegisterProvider(geminiProvider)

	// Register routes centrally
	for name, provider := range manager.GetAllProviders() {
		// Direct provider routes
		r.PathPrefix("/"+name+"/").Handler(provider.Proxy()).Methods("GET", "POST", "PUT", "DELETE", "OPTIONS")

		// Meta routes with user ID pattern: /meta/{userID}/provider/
		r.PathPrefix("/meta/{userID}/"+name+"/").Handler(provider.Proxy()).Methods("GET", "POST", "PUT", "DELETE", "OPTIONS")
	}

	// Register extra routes for all providers (e.g., compatibility routes)
	for _, provider := range manager.GetAllProviders() {
		provider.RegisterExtraRoutes(r)
	}

	// Health check endpoint
	r.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		response := map[string]interface{}{
			"status":    "healthy",
			"service":   "llm-proxy",
			"providers": manager.GetHealthStatus(),
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}).Methods("GET")

	server := httptest.NewServer(r)
	return server, manager
}
