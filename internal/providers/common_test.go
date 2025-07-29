package providers

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Test health endpoint
func TestHealth(t *testing.T) {
	server, _ := setupTestServer(t)
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatalf("Failed to get health status: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("Expected status 200, got %d", resp.StatusCode)
	}

	var health map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("Failed to decode health response: %v", err)
	}

	if status, ok := health["status"]; !ok || status != "healthy" {
		t.Error("Health check failed")
	}

	if providers, ok := health["providers"]; !ok {
		t.Error("Health check missing providers")
	} else {
		providersMap := providers.(map[string]interface{})
		expectedProviders := []string{"openai", "anthropic", "gemini"}

		for _, provider := range expectedProviders {
			if _, exists := providersMap[provider]; !exists {
				t.Errorf("Provider %s not found in health check", provider)
			}
		}
	}

	t.Log("Health check test passed")
}

// Test provider manager functionality
func TestProviderManager(t *testing.T) {
	_, providerManager := setupTestServer(t)

	// Test getting providers
	openaiProvider := providerManager.GetProvider("openai")
	if openaiProvider == nil {
		t.Error("OpenAI provider not found")
	}

	anthropicProvider := providerManager.GetProvider("anthropic")
	if anthropicProvider == nil {
		t.Error("Anthropic provider not found")
	}

	geminiProvider := providerManager.GetProvider("gemini")
	if geminiProvider == nil {
		t.Error("Gemini provider not found")
	}

	// Test getting non-existent provider
	nonExistentProvider := providerManager.GetProvider("nonexistent")
	if nonExistentProvider != nil {
		t.Error("Non-existent provider should return nil")
	}

	// Test getting all providers
	allProviders := providerManager.GetAllProviders()
	if len(allProviders) != 3 {
		t.Errorf("Expected 3 providers, got %d", len(allProviders))
	}

	t.Log("Provider manager test passed")
}

// Test metadata parsing with mock responses
func TestMetadataParsing(t *testing.T) {
	_, providerManager := setupTestServer(t)

	// Test OpenAI metadata parsing
	t.Run("OpenAI_NonStreaming", func(t *testing.T) {
		openaiProvider := providerManager.GetProvider("openai")
		if openaiProvider == nil {
			t.Fatal("OpenAI provider not found")
		}

		// Mock OpenAI response
		mockResponse := `{
			"id": "test-id",
			"object": "chat.completion",
			"created": 1234567890,
			"model": "gpt-3.5-turbo",
			"usage": {
				"prompt_tokens": 10,
				"completion_tokens": 20,
				"total_tokens": 30
			},
			"choices": [
				{
					"index": 0,
					"message": {
						"role": "assistant",
						"content": "Hello!"
					},
					"finish_reason": "stop"
				}
			]
		}`

		metadata, err := openaiProvider.ParseResponseMetadata(strings.NewReader(mockResponse), false)
		if err != nil {
			t.Fatalf("Failed to parse OpenAI metadata: %v", err)
		}

		if metadata.Provider != "openai" {
			t.Errorf("Expected provider 'openai', got %s", metadata.Provider)
		}
		if metadata.Model != "gpt-3.5-turbo" {
			t.Errorf("Expected model 'gpt-3.5-turbo', got %s", metadata.Model)
		}
		if metadata.InputTokens != 10 {
			t.Errorf("Expected 10 input tokens, got %d", metadata.InputTokens)
		}
		if metadata.OutputTokens != 20 {
			t.Errorf("Expected 20 output tokens, got %d", metadata.OutputTokens)
		}
		if metadata.TotalTokens != 30 {
			t.Errorf("Expected 30 total tokens, got %d", metadata.TotalTokens)
		}
		if metadata.IsStreaming != false {
			t.Errorf("Expected IsStreaming false, got %v", metadata.IsStreaming)
		}
	})

	// Test OpenAI streaming metadata parsing
	t.Run("OpenAI_Streaming", func(t *testing.T) {
		openaiProvider := providerManager.GetProvider("openai")
		if openaiProvider == nil {
			t.Fatal("OpenAI provider not found")
		}

		// Mock OpenAI streaming response
		mockStreamResponse := `data: {"id":"test-id","object":"chat.completion.chunk","created":1234567890,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{"content":"Hello"},"finish_reason":null}]}

data: {"id":"test-id","object":"chat.completion.chunk","created":1234567890,"model":"gpt-3.5-turbo","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}

data: [DONE]
`

		metadata, err := openaiProvider.ParseResponseMetadata(strings.NewReader(mockStreamResponse), true)
		if err != nil {
			t.Fatalf("Failed to parse OpenAI streaming metadata: %v", err)
		}

		if metadata.Provider != "openai" {
			t.Errorf("Expected provider 'openai', got %s", metadata.Provider)
		}
		if metadata.Model != "gpt-3.5-turbo" {
			t.Errorf("Expected model 'gpt-3.5-turbo', got %s", metadata.Model)
		}
		if metadata.InputTokens != 10 {
			t.Errorf("Expected 10 input tokens, got %d", metadata.InputTokens)
		}
		if metadata.OutputTokens != 20 {
			t.Errorf("Expected 20 output tokens, got %d", metadata.OutputTokens)
		}
		if metadata.TotalTokens != 30 {
			t.Errorf("Expected 30 total tokens, got %d", metadata.TotalTokens)
		}
		if metadata.IsStreaming != true {
			t.Errorf("Expected IsStreaming true, got %v", metadata.IsStreaming)
		}
	})

	// Test Anthropic metadata parsing
	t.Run("Anthropic_NonStreaming", func(t *testing.T) {
		anthropicProvider := providerManager.GetProvider("anthropic")
		if anthropicProvider == nil {
			t.Fatal("Anthropic provider not found")
		}

		// Mock Anthropic response
		mockResponse := `{
			"id": "test-id",
			"type": "message",
			"role": "assistant",
			"model": "claude-3-sonnet-20240229",
			"content": [
				{
					"type": "text",
					"text": "Hello!"
				}
			],
			"stop_reason": "end_turn",
			"stop_sequence": null,
			"usage": {
				"input_tokens": 15,
				"output_tokens": 25
			}
		}`

		metadata, err := anthropicProvider.ParseResponseMetadata(strings.NewReader(mockResponse), false)
		if err != nil {
			t.Fatalf("Failed to parse Anthropic metadata: %v", err)
		}

		if metadata.Provider != "anthropic" {
			t.Errorf("Expected provider 'anthropic', got %s", metadata.Provider)
		}
		if metadata.Model != "claude-3-sonnet-20240229" {
			t.Errorf("Expected model 'claude-3-sonnet-20240229', got %s", metadata.Model)
		}
		if metadata.InputTokens != 15 {
			t.Errorf("Expected 15 input tokens, got %d", metadata.InputTokens)
		}
		if metadata.OutputTokens != 25 {
			t.Errorf("Expected 25 output tokens, got %d", metadata.OutputTokens)
		}
		if metadata.TotalTokens != 40 {
			t.Errorf("Expected 40 total tokens, got %d", metadata.TotalTokens)
		}
		if metadata.IsStreaming != false {
			t.Errorf("Expected IsStreaming false, got %v", metadata.IsStreaming)
		}
	})

	// Test Gemini metadata parsing
	t.Run("Gemini_NonStreaming", func(t *testing.T) {
		geminiProvider := providerManager.GetProvider("gemini")
		if geminiProvider == nil {
			t.Fatal("Gemini provider not found")
		}

		// Mock Gemini response
		mockResponse := `{
			"candidates": [
				{
					"content": {
						"parts": [
							{
								"text": "Hello!"
							}
						],
						"role": "model"
					},
					"finishReason": "STOP",
					"index": 0
				}
			],
			"usageMetadata": {
				"promptTokenCount": 8,
				"candidatesTokenCount": 12,
				"totalTokenCount": 20,
				"thoughtsTokenCount": 5
			},
			"modelVersion": "gemini-2.0-flash"
		}`

		metadata, err := geminiProvider.ParseResponseMetadata(strings.NewReader(mockResponse), false)
		if err != nil {
			t.Fatalf("Failed to parse Gemini metadata: %v", err)
		}

		if metadata.Provider != "gemini" {
			t.Errorf("Expected provider 'gemini', got %s", metadata.Provider)
		}
		if metadata.Model != "gemini-2.0-flash" {
			t.Errorf("Expected model 'gemini-2.0-flash', got %s", metadata.Model)
		}
		if metadata.InputTokens != 8 {
			t.Errorf("Expected 8 input tokens, got %d", metadata.InputTokens)
		}
		if metadata.OutputTokens != 12 {
			t.Errorf("Expected 12 output tokens, got %d", metadata.OutputTokens)
		}
		if metadata.TotalTokens != 20 {
			t.Errorf("Expected 20 total tokens, got %d", metadata.TotalTokens)
		}
		if metadata.ThoughtTokens != 5 {
			t.Errorf("Expected 5 thought tokens, got %d", metadata.ThoughtTokens)
		}
		if metadata.IsStreaming != false {
			t.Errorf("Expected IsStreaming false, got %v", metadata.IsStreaming)
		}
	})

	// Test Gemini streaming metadata parsing
	t.Run("Gemini_Streaming", func(t *testing.T) {
		geminiProvider := providerManager.GetProvider("gemini")
		if geminiProvider == nil {
			t.Fatal("Gemini provider not found")
		}

		// Mock Gemini streaming response
		mockStreamResponse := `data: {"candidates":[{"content":{"parts":[{"text":"Hello"}],"role":"model"},"finishReason":"STOP","index":0}],"modelVersion":"gemini-2.0-flash"}

data: {"candidates":[{"content":{"parts":[{"text":"!"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":8,"candidatesTokenCount":12,"totalTokenCount":20},"modelVersion":"gemini-2.0-flash"}

data: [DONE]
`

		metadata, err := geminiProvider.ParseResponseMetadata(strings.NewReader(mockStreamResponse), true)
		if err != nil {
			t.Fatalf("Failed to parse Gemini streaming metadata: %v", err)
		}

		if metadata.Provider != "gemini" {
			t.Errorf("Expected provider 'gemini', got %s", metadata.Provider)
		}
		if metadata.Model != "gemini-2.0-flash" {
			t.Errorf("Expected model 'gemini-2.0-flash', got %s", metadata.Model)
		}
		if metadata.InputTokens != 8 {
			t.Errorf("Expected 8 input tokens, got %d", metadata.InputTokens)
		}
		if metadata.OutputTokens != 12 {
			t.Errorf("Expected 12 output tokens, got %d", metadata.OutputTokens)
		}
		if metadata.TotalTokens != 20 {
			t.Errorf("Expected 20 total tokens, got %d", metadata.TotalTokens)
		}
		if metadata.IsStreaming != true {
			t.Errorf("Expected IsStreaming true, got %v", metadata.IsStreaming)
		}
	})

	t.Log("All metadata parsing tests passed")
}
