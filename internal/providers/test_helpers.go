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

	// Register Bedrock so smoke tests exercise its routing alongside the
	// other providers. Previously only openai/anthropic/gemini were
	// wired up here, which meant every test against /model/* or
	// /bedrock/* would 404 from the test server and silently
	// under-validate the bedrock-specific middleware paths.
	bedrockProvider := NewBedrockProxy()
	manager.RegisterProvider(bedrockProvider)

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
	// Belt-and-suspenders: every caller already `defer server.Close()`s,
	// but registering a Cleanup here guarantees that a future caller who
	// forgets the defer (or whose test panics before reaching it) does
	// not leak the listening socket past the test boundary. t.Cleanup
	// runs even on panic; defer in the caller does not.
	t.Cleanup(server.Close)
	return server, manager
}
