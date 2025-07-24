package providers

import (
	"net/http"
	"testing"
	"time"
)

// Test ProviderManager functionality
func TestNewProviderManager(t *testing.T) {
	manager := NewProviderManager()
	if manager == nil {
		t.Fatal("NewProviderManager() returned nil")
	}
	
	if manager.providers == nil {
		t.Fatal("ProviderManager.providers is nil")
	}
	
	if len(manager.providers) != 0 {
		t.Errorf("Expected empty provider map, got %d providers", len(manager.providers))
	}
}

func TestProviderManager_RegisterProvider(t *testing.T) {
	manager := NewProviderManager()
	
	// Create mock providers using the test helpers
	openAI := NewOpenAIProxy()
	anthropic := NewAnthropicProxy()
	
	// Register providers
	manager.RegisterProvider(openAI)
	manager.RegisterProvider(anthropic)
	
	// Verify registration
	if len(manager.providers) != 2 {
		t.Errorf("Expected 2 providers, got %d", len(manager.providers))
	}
	
	if manager.providers[openAI.GetName()] != openAI {
		t.Error("OpenAI provider not properly registered")
	}
	
	if manager.providers[anthropic.GetName()] != anthropic {
		t.Error("Anthropic provider not properly registered")
	}
}

func TestProviderManager_GetProvider(t *testing.T) {
	manager := NewProviderManager()
	openAI := NewOpenAIProxy()
	
	// Test getting non-existent provider
	provider := manager.GetProvider("nonexistent")
	if provider != nil {
		t.Error("Expected nil for non-existent provider")
	}
	
	// Register and test getting existing provider
	manager.RegisterProvider(openAI)
	provider = manager.GetProvider(openAI.GetName())
	if provider != openAI {
		t.Error("GetProvider returned wrong provider")
	}
}

func TestProviderManager_GetAllProviders(t *testing.T) {
	manager := NewProviderManager()
	
	// Test empty manager
	providers := manager.GetAllProviders()
	if len(providers) != 0 {
		t.Errorf("Expected 0 providers, got %d", len(providers))
	}
	
	// Add providers and test
	openAI := NewOpenAIProxy()
	anthropic := NewAnthropicProxy()
	gemini := NewGeminiProxy()
	
	manager.RegisterProvider(openAI)
	manager.RegisterProvider(anthropic)
	manager.RegisterProvider(gemini)
	
	providers = manager.GetAllProviders()
	if len(providers) != 3 {
		t.Errorf("Expected 3 providers, got %d", len(providers))
	}
	
	// Verify all providers are returned
	expectedProviders := map[string]Provider{
		openAI.GetName():    openAI,
		anthropic.GetName(): anthropic,
		gemini.GetName():    gemini,
	}
	
	for name, expectedProvider := range expectedProviders {
		if providers[name] != expectedProvider {
			t.Errorf("Provider %s not found or incorrect", name)
		}
	}
}

func TestProviderManager_IsStreamingRequest(t *testing.T) {
	manager := NewProviderManager()
	
	// Create a mock request
	req, err := http.NewRequest("POST", "/test", nil)
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}
	
	// Test with no providers
	isStreaming := manager.IsStreamingRequest(req)
	if isStreaming {
		t.Error("Expected false for empty provider manager")
	}
	
	// Add providers
	openAI := NewOpenAIProxy()
	manager.RegisterProvider(openAI)
	
	// Test with providers (this will depend on the specific provider implementation)
	// The method iterates through all providers and returns true if any says it's streaming
	isStreaming = manager.IsStreamingRequest(req)
	// We can't assert the specific value since it depends on provider implementation
	// But we can verify the method doesn't panic and returns a boolean
	_ = isStreaming
}

func TestProviderManager_GetHealthStatus(t *testing.T) {
	manager := NewProviderManager()
	
	// Test empty manager
	status := manager.GetHealthStatus()
	if len(status) != 0 {
		t.Errorf("Expected empty health status, got %d entries", len(status))
	}
	
	// Add providers
	openAI := NewOpenAIProxy()
	anthropic := NewAnthropicProxy()
	
	manager.RegisterProvider(openAI)
	manager.RegisterProvider(anthropic)
	
	status = manager.GetHealthStatus()
	if len(status) != 2 {
		t.Errorf("Expected 2 health status entries, got %d", len(status))
	}
	
	// Verify each provider's health status is included
	if _, exists := status[openAI.GetName()]; !exists {
		t.Error("OpenAI health status not found")
	}
	
	if _, exists := status[anthropic.GetName()]; !exists {
		t.Error("Anthropic health status not found")
	}
}

// Test newProxyTransport function
func TestNewProxyTransport(t *testing.T) {
	transport := newProxyTransport()
	
	if transport == nil {
		t.Fatal("newProxyTransport() returned nil")
	}
	
	// Verify key configuration settings
	if transport.MaxIdleConns != 100 {
		t.Errorf("Expected MaxIdleConns=100, got %d", transport.MaxIdleConns)
	}
	
	if transport.IdleConnTimeout != 90*time.Second {
		t.Errorf("Expected IdleConnTimeout=90s, got %v", transport.IdleConnTimeout)
	}
	
	if transport.TLSHandshakeTimeout != 10*time.Second {
		t.Errorf("Expected TLSHandshakeTimeout=10s, got %v", transport.TLSHandshakeTimeout)
	}
	
	if transport.ExpectContinueTimeout != 1*time.Second {
		t.Errorf("Expected ExpectContinueTimeout=1s, got %v", transport.ExpectContinueTimeout)
	}
	
	if transport.ResponseHeaderTimeout != 3*time.Minute {
		t.Errorf("Expected ResponseHeaderTimeout=3m, got %v", transport.ResponseHeaderTimeout)
	}
	
	if !transport.ForceAttemptHTTP2 {
		t.Error("Expected ForceAttemptHTTP2=true")
	}
	
	if !transport.DisableCompression {
		t.Error("Expected DisableCompression=true")
	}
	
	// Verify DialContext is configured
	if transport.DialContext == nil {
		t.Error("Expected DialContext to be configured")
	}
	
	// Verify Proxy is configured (should use environment)
	if transport.Proxy == nil {
		t.Error("Expected Proxy to be configured")
	}
}

// Test data structures

func TestLLMResponseMetadata_Struct(t *testing.T) {
	// Test that the struct can be created and fields are accessible
	metadata := &LLMResponseMetadata{
		Model:           "gpt-4",
		InputTokens:     100,
		OutputTokens:    50,
		TotalTokens:     150,
		ThoughtTokens:   10,
		Provider:        "openai",
		RequestID:       "req-123",
		IsStreaming:     false,
		FinishReason:    "stop",
	}
	
	// Verify all fields are set correctly
	if metadata.Model != "gpt-4" {
		t.Errorf("Expected Model='gpt-4', got '%s'", metadata.Model)
	}
	
	if metadata.TotalTokens != 150 {
		t.Errorf("Expected TotalTokens=150, got %d", metadata.TotalTokens)
	}
	
	if metadata.Provider != "openai" {
		t.Errorf("Expected Provider='openai', got '%s'", metadata.Provider)
	}
	
	if metadata.IsStreaming {
		t.Error("Expected IsStreaming=false")
	}
}
