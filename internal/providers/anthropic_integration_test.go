package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// testModel represents a model configuration for testing
type anthropicTestModel struct {
	name       string
	modelID    string
	maxTokens  int
	testPrompt string
}

// Test models to use in parameterized tests
var anthropicTestModels = []anthropicTestModel{
	{
		name:       "Claude-3.5-Haiku",
		modelID:    "claude-3-5-haiku-latest",
		maxTokens:  50,
		testPrompt: "What is 2+2?",
	},
	{
		name:       "Claude-3.5-Sonnet",
		modelID:    "claude-3-5-sonnet-latest",
		maxTokens:  100,
		testPrompt: "Hello! Can you tell me a short joke?",
	},
}

// TestAnthropic_ParallelModels tests multiple models in parallel
func TestAnthropicIntegration_Models(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY environment variable is not set")
	}

	server, providerManager := setupTestServer(t)
	defer server.Close()

	// Run model tests in parallel
	for _, model := range anthropicTestModels {
		model := model // capture range variable

		t.Run(model.name, func(t *testing.T) {
			// Test both streaming and non-streaming in parallel
			t.Run("NonStreaming", func(t *testing.T) {
				testAnthropicNonStreaming(t, server, providerManager, apiKey, model)
			})

			t.Run("Streaming", func(t *testing.T) {
				testAnthropicStreaming(t, server, providerManager, apiKey, model)
			})
		})
	}
}

// TestAnthropic_AdvancedScenarios tests various advanced scenarios with different models
func TestAnthropicIntegration_AdvancedScenarios(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY environment variable is not set")
	}

	server, _ := setupTestServer(t)
	defer server.Close()

	// Define test scenarios
	scenarios := []struct {
		name        string
		model       anthropicTestModel
		messages    []map[string]string
		maxTokens   int
		temperature float64
		stream      bool
	}{
		{
			name:  "SimpleQuestion",
			model: anthropicTestModels[0],
			messages: []map[string]string{
				{"role": "user", "content": "What is the capital of France?"},
			},
			maxTokens:   50,
			temperature: 0.0,
			stream:      false,
		},
		{
			name:  "Conversation",
			model: anthropicTestModels[0],
			messages: []map[string]string{
				{"role": "user", "content": "Hello, I'm learning Go."},
				{"role": "assistant", "content": "That's great! Go is an excellent language for building concurrent applications. What would you like to know about Go?"},
				{"role": "user", "content": "What are goroutines?"},
			},
			maxTokens:   100,
			temperature: 0.7,
			stream:      false,
		},
		{
			name:  "StreamingConversation",
			model: anthropicTestModels[0],
			messages: []map[string]string{
				{"role": "user", "content": "Explain concurrency in one sentence."},
			},
			maxTokens:   100,
			temperature: 0.5,
			stream:      true,
		},
	}

	// Run each scenario as a subtest
	for _, scenario := range scenarios {
		scenario := scenario // capture range variable

		t.Run(fmt.Sprintf("%s_%s", scenario.model.name, scenario.name), func(t *testing.T) {
			requestBody := map[string]interface{}{
				"model":      scenario.model.modelID,
				"max_tokens": scenario.maxTokens,
				"messages":   scenario.messages,
			}

			if scenario.stream {
				requestBody["stream"] = true
			}

			jsonData, err := json.Marshal(requestBody)
			if err != nil {
				t.Fatalf("Failed to marshal request body: %v", err)
			}

			req, err := http.NewRequest("POST", server.URL+"/anthropic/v1/messages", bytes.NewBuffer(jsonData))
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("x-api-key", apiKey)
			req.Header.Set("anthropic-version", "2023-06-01")

			client := &http.Client{Timeout: 30 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				t.Fatalf("Request failed: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("Expected status 200, got %d. Response: %s", resp.StatusCode, string(body))
			}

			if scenario.stream {
				// Verify streaming response
				contentType := resp.Header.Get("Content-Type")
				if !strings.Contains(contentType, "text/event-stream") {
					t.Errorf("Expected text/event-stream content type, got: %s", contentType)
				}
			} else {
				// Verify non-streaming response
				bodyBytes, err := io.ReadAll(resp.Body)
				if err != nil {
					t.Fatalf("Failed to read response body: %v", err)
				}

				var response map[string]interface{}
				if err := json.Unmarshal(bodyBytes, &response); err != nil {
					t.Fatalf("Failed to decode response: %v", err)
				}

				// Verify response structure
				if _, ok := response["content"]; !ok {
					t.Error("Response missing 'content' field")
				}
				if _, ok := response["usage"]; !ok {
					t.Error("Response missing 'usage' field")
				}
			}

			t.Logf("Scenario %s completed successfully", scenario.name)
		})
	}
}

// Helper function for non-streaming tests
func testAnthropicNonStreaming(t *testing.T, server *httptest.Server, providerManager *ProviderManager, apiKey string, model anthropicTestModel) {
	requestBody := map[string]interface{}{
		"model":      model.modelID,
		"max_tokens": model.maxTokens,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": model.testPrompt,
			},
		},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}

	req, err := http.NewRequest("POST", server.URL+"/anthropic/v1/messages", bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Expected status 200, got %d. Response: %s", resp.StatusCode, string(body))
	}

	// Read the response body
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	// Parse JSON for basic validation
	var response map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify response structure
	if _, ok := response["content"]; !ok {
		t.Error("Response missing 'content' field")
	}
	if _, ok := response["usage"]; !ok {
		t.Error("Response missing 'usage' field")
	}
	if _, ok := response["model"]; !ok {
		t.Error("Response missing 'model' field")
	}

	// Test metadata parsing
	anthropicProvider := providerManager.GetProvider("anthropic")
	if anthropicProvider == nil {
		t.Fatal("Anthropic provider not found")
	}

	metadata, err := anthropicProvider.ParseResponseMetadata(bytes.NewReader(bodyBytes), false)
	if err != nil {
		t.Fatalf("Failed to parse metadata: %v", err)
	}

	validateAnthropicMetadata(t, metadata, "anthropic", false)

	t.Logf("Non-streaming test passed. Model: %v", response["model"])
}

// Helper function for streaming tests
func testAnthropicStreaming(t *testing.T, server *httptest.Server, providerManager *ProviderManager, apiKey string, model anthropicTestModel) {
	requestBody := map[string]interface{}{
		"model":      model.modelID,
		"max_tokens": model.maxTokens,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": "Hello!",
			},
		},
		"stream": true,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}

	req, err := http.NewRequest("POST", server.URL+"/anthropic/v1/messages", bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("Expected status 200, got %d. Response: %s", resp.StatusCode, string(body))
	}

	// Verify it's a streaming response
	contentType := resp.Header.Get("Content-Type")
	if !strings.Contains(contentType, "text/event-stream") {
		t.Errorf("Expected text/event-stream content type, got: %s", contentType)
	}

	// Read and capture all streaming data
	var streamData bytes.Buffer
	scanner := bufio.NewScanner(resp.Body)
	chunkCount := 0
	hasUsage := false

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			t.Fatal("Streaming test timed out")
		default:
		}

		line := scanner.Text()
		streamData.WriteString(line + "\n")

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		jsonData := strings.TrimPrefix(line, "data: ")
		if strings.TrimSpace(jsonData) == "[DONE]" {
			break
		}

		chunkCount++
		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &chunk); err != nil {
			t.Logf("Warning: failed to parse chunk: %v", err)
			continue
		}

		// Check for usage information
		if usage, ok := chunk["usage"]; ok && usage != nil {
			hasUsage = true
		}

		// Limit the number of chunks we process for testing
		if chunkCount > 50 {
			break
		}
	}

	if chunkCount == 0 {
		t.Error("No streaming chunks received")
	}

	// Test metadata parsing on the streaming response
	anthropicProvider := providerManager.GetProvider("anthropic")
	if anthropicProvider == nil {
		t.Fatal("Anthropic provider not found")
	}

	metadata, err := anthropicProvider.ParseResponseMetadata(bytes.NewReader(streamData.Bytes()), true)
	if err != nil {
		t.Fatalf("Failed to parse streaming metadata: %v", err)
	}

	validateAnthropicMetadata(t, metadata, "anthropic", true)

	t.Logf("Streaming test passed. Model: %s, Received %d chunks, usage included: %v", model.modelID, chunkCount, hasUsage)
}
