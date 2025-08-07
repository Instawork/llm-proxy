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

// geminiTestModel represents a model configuration for testing
type geminiTestModel struct {
	name       string
	modelID    string
	testPrompt string
}

// Test models to use in parameterized tests
var geminiTestModels = []geminiTestModel{
	{
		name:       "Gemini-2.0-Flash",
		modelID:    "gemini-2.0-flash",
		testPrompt: "Hello! Can you tell me a short joke?",
	},
	{
		name:       "Gemini-1.5-Flash",
		modelID:    "gemini-1.5-flash",
		testPrompt: "What is 2+2?",
	},
	{
		name:       "Gemini-1.5-Pro",
		modelID:    "gemini-1.5-pro",
		testPrompt: "Explain quantum computing in one sentence.",
	},
}

// TestGeminiIntegration_Models tests multiple Gemini models using subtests
func TestGeminiIntegration_Models(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY environment variable is not set")
	}

	server, providerManager := setupTestServer(t)
	defer server.Close()

	// Test each model with both streaming and non-streaming
	for _, model := range geminiTestModels {
		model := model // capture range variable

		// Non-streaming subtest for each model
		t.Run(fmt.Sprintf("%s/NonStreaming", model.name), func(t *testing.T) {
			testGeminiNonStreaming(t, server, providerManager, apiKey, model)
		})

		// Streaming subtest for each model
		t.Run(fmt.Sprintf("%s/Streaming", model.name), func(t *testing.T) {
			testGeminiStreaming(t, server, providerManager, apiKey, model)
		})
	}
}

// TestGeminiIntegration_AdvancedScenarios tests various advanced scenarios with different models
func TestGeminiIntegration_AdvancedScenarios(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY environment variable is not set")
	}

	server, _ := setupTestServer(t)
	defer server.Close()

	// Define test scenarios
	scenarios := []struct {
		name                 string
		model                geminiTestModel
		contents             []map[string]interface{}
		generationConfig     map[string]interface{}
		safetySettings       []map[string]string
		stream               bool
		includeCodeExecution bool
	}{
		{
			name:  "SimpleQuestion",
			model: geminiTestModels[0],
			contents: []map[string]interface{}{
				{
					"parts": []map[string]string{
						{"text": "What is the capital of France?"},
					},
				},
			},
			generationConfig: map[string]interface{}{
				"temperature":     0.0,
				"maxOutputTokens": 50,
			},
			stream: false,
		},
		{
			name:  "MultiTurnConversation",
			model: geminiTestModels[0],
			contents: []map[string]interface{}{
				{
					"role": "user",
					"parts": []map[string]string{
						{"text": "Hello, I'm learning Go."},
					},
				},
				{
					"role": "model",
					"parts": []map[string]string{
						{"text": "That's great! Go is an excellent language for building concurrent applications. What would you like to know about Go?"},
					},
				},
				{
					"role": "user",
					"parts": []map[string]string{
						{"text": "What are goroutines?"},
					},
				},
			},
			generationConfig: map[string]interface{}{
				"temperature":     0.7,
				"maxOutputTokens": 150,
			},
			stream: false,
		},
		{
			name:  "StreamingResponse",
			model: geminiTestModels[0],
			contents: []map[string]interface{}{
				{
					"parts": []map[string]string{
						{"text": "Write a haiku about programming."},
					},
				},
			},
			generationConfig: map[string]interface{}{
				"temperature":     0.9,
				"maxOutputTokens": 100,
			},
			stream: true,
		},
		{
			name:  "WithSafetySettings",
			model: geminiTestModels[0],
			contents: []map[string]interface{}{
				{
					"parts": []map[string]string{
						{"text": "Tell me a story."},
					},
				},
			},
			generationConfig: map[string]interface{}{
				"temperature":     0.7,
				"maxOutputTokens": 200,
			},
			safetySettings: []map[string]string{
				{
					"category":  "HARM_CATEGORY_HARASSMENT",
					"threshold": "BLOCK_MEDIUM_AND_ABOVE",
				},
				{
					"category":  "HARM_CATEGORY_HATE_SPEECH",
					"threshold": "BLOCK_MEDIUM_AND_ABOVE",
				},
			},
			stream: false,
		},
		{
			name:  "SystemInstruction",
			model: geminiTestModels[0],
			contents: []map[string]interface{}{
				{
					"parts": []map[string]string{
						{"text": "What's the weather like?"},
					},
				},
			},
			generationConfig: map[string]interface{}{
				"temperature":     0.5,
				"maxOutputTokens": 100,
			},
			stream: false,
		},
	}

	// Run each scenario as a subtest
	for _, scenario := range scenarios {
		scenario := scenario // capture range variable

		t.Run(fmt.Sprintf("%s_%s", scenario.model.name, scenario.name), func(t *testing.T) {
			requestBody := map[string]interface{}{
				"contents": scenario.contents,
			}

			if scenario.generationConfig != nil {
				requestBody["generationConfig"] = scenario.generationConfig
			}

			if scenario.safetySettings != nil {
				requestBody["safetySettings"] = scenario.safetySettings
			}

			jsonData, err := json.Marshal(requestBody)
			if err != nil {
				t.Fatalf("Failed to marshal request body: %v", err)
			}

			var url string
			if scenario.stream {
				url = fmt.Sprintf("%s/gemini/v1/models/%s:generateContent?key=%s&alt=sse",
					server.URL, scenario.model.modelID, apiKey)
			} else {
				url = fmt.Sprintf("%s/gemini/v1/models/%s:generateContent?key=%s",
					server.URL, scenario.model.modelID, apiKey)
			}

			req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			req.Header.Set("Content-Type", "application/json")

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
				if _, ok := response["candidates"]; !ok {
					t.Error("Response missing 'candidates' field")
				}
				if _, ok := response["usageMetadata"]; !ok {
					t.Error("Response missing 'usageMetadata' field")
				}
			}

			t.Logf("Scenario %s completed successfully", scenario.name)
		})
	}
}

// TestGeminiIntegration_CountTokens tests the count tokens endpoint with different models
func TestGeminiIntegration_CountTokens(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY environment variable is not set")
	}

	server, _ := setupTestServer(t)
	defer server.Close()

	// Test count tokens for each model
	for _, model := range geminiTestModels {
		model := model // capture range variable

		t.Run(fmt.Sprintf("%s/CountTokens", model.name), func(t *testing.T) {
			requestBody := map[string]interface{}{
				"contents": []map[string]interface{}{
					{
						"parts": []map[string]string{
							{"text": model.testPrompt},
						},
					},
				},
			}

			jsonData, err := json.Marshal(requestBody)
			if err != nil {
				t.Fatalf("Failed to marshal request body: %v", err)
			}

			url := fmt.Sprintf("%s/gemini/v1/models/%s:countTokens?key=%s", server.URL, model.modelID, apiKey)
			req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			req.Header.Set("Content-Type", "application/json")

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

			bodyBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("Failed to read response body: %v", err)
			}

			var response map[string]interface{}
			if err := json.Unmarshal(bodyBytes, &response); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			// Verify response structure
			if _, ok := response["totalTokens"]; !ok {
				t.Error("Response missing 'totalTokens' field")
			}

			t.Logf("Count tokens test passed. Model: %s, Total tokens: %v", model.modelID, response["totalTokens"])
		})
	}
}

// TestGeminiIntegration_V1BetaRoutes tests v1beta routes using subtests
func TestGeminiIntegration_V1BetaRoutes(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY environment variable is not set")
	}

	server, providerManager := setupTestServer(t)
	defer server.Close()

	// Test each model with both v1 and v1beta endpoints
	for _, model := range geminiTestModels {
		model := model // capture range variable

		// Test v1beta non-streaming
		t.Run(fmt.Sprintf("%s/V1Beta/NonStreaming", model.name), func(t *testing.T) {
			testGeminiV1BetaNonStreaming(t, server, providerManager, apiKey, model)
		})

		// Test v1beta streaming
		t.Run(fmt.Sprintf("%s/V1Beta/Streaming", model.name), func(t *testing.T) {
			testGeminiV1BetaStreaming(t, server, providerManager, apiKey, model)
		})

		// Test v1beta count tokens
		t.Run(fmt.Sprintf("%s/V1Beta/CountTokens", model.name), func(t *testing.T) {
			testGeminiV1BetaCountTokens(t, server, providerManager, apiKey, model)
		})
	}

	// Test v1beta embedding models
	embeddingModels := []struct {
		name    string
		modelID string
		text    string
	}{
		{
			name:    "TextEmbedding004",
			modelID: "text-embedding-004",
			text:    "The quick brown fox jumps over the lazy dog.",
		},
		{
			name:    "Embedding001",
			modelID: "embedding-001",
			text:    "Hello, world! This is a test of the embedding API.",
		},
	}

	for _, model := range embeddingModels {
		model := model // capture range variable

		t.Run(fmt.Sprintf("%s/V1Beta/EmbedContent", model.name), func(t *testing.T) {
			testGeminiV1BetaEmbedContent(t, server, providerManager, apiKey, model)
		})
	}
}

// TestGeminiIntegration_V1BetaAdvancedScenarios tests various advanced scenarios with v1beta endpoints
func TestGeminiIntegration_V1BetaAdvancedScenarios(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY environment variable is not set")
	}

	server, _ := setupTestServer(t)
	defer server.Close()

	// Define test scenarios for v1beta
	scenarios := []struct {
		name                 string
		model                geminiTestModel
		contents             []map[string]interface{}
		generationConfig     map[string]interface{}
		safetySettings       []map[string]string
		stream               bool
		includeCodeExecution bool
	}{
		{
			name:  "V1Beta_SimpleQuestion",
			model: geminiTestModels[0],
			contents: []map[string]interface{}{
				{
					"parts": []map[string]string{
						{"text": "What is the capital of France?"},
					},
				},
			},
			generationConfig: map[string]interface{}{
				"temperature":     0.0,
				"maxOutputTokens": 50,
			},
			stream: false,
		},
		{
			name:  "V1Beta_MultiTurnConversation",
			model: geminiTestModels[0],
			contents: []map[string]interface{}{
				{
					"role": "user",
					"parts": []map[string]string{
						{"text": "Hello, I'm learning Go."},
					},
				},
				{
					"role": "model",
					"parts": []map[string]string{
						{"text": "That's great! Go is an excellent language for building concurrent applications. What would you like to know about Go?"},
					},
				},
				{
					"role": "user",
					"parts": []map[string]string{
						{"text": "What are goroutines?"},
					},
				},
			},
			generationConfig: map[string]interface{}{
				"temperature":     0.7,
				"maxOutputTokens": 150,
			},
			stream: false,
		},
		{
			name:  "V1Beta_StreamingResponse",
			model: geminiTestModels[0],
			contents: []map[string]interface{}{
				{
					"parts": []map[string]string{
						{"text": "Write a haiku about programming."},
					},
				},
			},
			generationConfig: map[string]interface{}{
				"temperature":     0.9,
				"maxOutputTokens": 100,
			},
			stream: true,
		},
		{
			name:  "V1Beta_WithSafetySettings",
			model: geminiTestModels[0],
			contents: []map[string]interface{}{
				{
					"parts": []map[string]string{
						{"text": "Tell me a story."},
					},
				},
			},
			generationConfig: map[string]interface{}{
				"temperature":     0.7,
				"maxOutputTokens": 200,
			},
			safetySettings: []map[string]string{
				{
					"category":  "HARM_CATEGORY_HARASSMENT",
					"threshold": "BLOCK_MEDIUM_AND_ABOVE",
				},
				{
					"category":  "HARM_CATEGORY_HATE_SPEECH",
					"threshold": "BLOCK_MEDIUM_AND_ABOVE",
				},
			},
			stream: false,
		},
	}

	// Run each scenario as a subtest
	for _, scenario := range scenarios {
		scenario := scenario // capture range variable

		t.Run(fmt.Sprintf("%s_%s", scenario.model.name, scenario.name), func(t *testing.T) {
			requestBody := map[string]interface{}{
				"contents": scenario.contents,
			}

			if scenario.generationConfig != nil {
				requestBody["generationConfig"] = scenario.generationConfig
			}

			if scenario.safetySettings != nil {
				requestBody["safetySettings"] = scenario.safetySettings
			}

			jsonData, err := json.Marshal(requestBody)
			if err != nil {
				t.Fatalf("Failed to marshal request body: %v", err)
			}

			var url string
			if scenario.stream {
				url = fmt.Sprintf("%s/gemini/v1beta/models/%s:generateContent?key=%s&alt=sse",
					server.URL, scenario.model.modelID, apiKey)
			} else {
				url = fmt.Sprintf("%s/gemini/v1beta/models/%s:generateContent?key=%s",
					server.URL, scenario.model.modelID, apiKey)
			}

			req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			req.Header.Set("Content-Type", "application/json")

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
				if _, ok := response["candidates"]; !ok {
					t.Error("Response missing 'candidates' field")
				}
				if _, ok := response["usageMetadata"]; !ok {
					t.Error("Response missing 'usageMetadata' field")
				}
			}

			t.Logf("V1Beta scenario %s completed successfully", scenario.name)
		})
	}
}

// Helper function for non-streaming tests
func testGeminiNonStreaming(t *testing.T, server *httptest.Server, providerManager *ProviderManager, apiKey string, model geminiTestModel) {
	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]string{
					{
						"text": model.testPrompt,
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}

	url := fmt.Sprintf("%s/gemini/v1/models/%s:generateContent?key=%s", server.URL, model.modelID, apiKey)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")

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
	if _, ok := response["candidates"]; !ok {
		t.Error("Response missing 'candidates' field")
	}
	if _, ok := response["usageMetadata"]; !ok {
		t.Error("Response missing 'usageMetadata' field")
	}

	// Test metadata parsing
	geminiProvider := providerManager.GetProvider("gemini")
	if geminiProvider == nil {
		t.Fatal("Gemini provider not found")
	}

	metadata, err := geminiProvider.ParseResponseMetadata(bytes.NewReader(bodyBytes), false)
	if err != nil {
		t.Fatalf("Failed to parse metadata: %v", err)
	}

	validateGeminiMetadata(t, metadata, "gemini", false)

	t.Logf("Non-streaming test passed. Model: %s", model.modelID)
}

// Helper function for streaming tests
func testGeminiStreaming(t *testing.T, server *httptest.Server, providerManager *ProviderManager, apiKey string, model geminiTestModel) {
	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]string{
					{
						"text": "Hello!",
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}

	url := fmt.Sprintf("%s/gemini/v1/models/%s:generateContent?key=%s&alt=sse", server.URL, model.modelID, apiKey)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")

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
		if strings.TrimSpace(jsonData) == "[DONE]" || strings.TrimSpace(jsonData) == "" {
			break
		}

		chunkCount++
		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &chunk); err != nil {
			t.Logf("Warning: failed to parse chunk: %v", err)
			continue
		}

		// Check for usage information
		if usage, ok := chunk["usageMetadata"]; ok && usage != nil {
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
	geminiProvider := providerManager.GetProvider("gemini")
	if geminiProvider == nil {
		t.Fatal("Gemini provider not found")
	}

	metadata, err := geminiProvider.ParseResponseMetadata(bytes.NewReader(streamData.Bytes()), true)
	if err != nil {
		t.Fatalf("Failed to parse streaming metadata: %v", err)
	}

	validateGeminiMetadata(t, metadata, "gemini", true)

	t.Logf("Streaming test passed. Model: %s, Received %d chunks, usage included: %v", model.modelID, chunkCount, hasUsage)
}

// TestGeminiIntegration_EmbedContent tests the embed content endpoint with different models
func TestGeminiIntegration_EmbedContent(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY environment variable is not set")
	}

	server, providerManager := setupTestServer(t)
	defer server.Close()

	// Embedding models to test
	embeddingModels := []struct {
		name    string
		modelID string
		text    string
	}{
		{
			name:    "TextEmbedding004",
			modelID: "text-embedding-004",
			text:    "The quick brown fox jumps over the lazy dog.",
		},
		// Add more embedding models as needed
	}

	for _, model := range embeddingModels {
		model := model // capture range variable

		t.Run(model.name, func(t *testing.T) {
			requestBody := map[string]interface{}{
				"content": map[string]interface{}{
					"parts": []map[string]string{
						{"text": model.text},
					},
				},
			}

			jsonData, err := json.Marshal(requestBody)
			if err != nil {
				t.Fatalf("Failed to marshal request body: %v", err)
			}

			url := fmt.Sprintf("%s/gemini/v1/models/%s:embedContent?key=%s", server.URL, model.modelID, apiKey)
			req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			req.Header.Set("Content-Type", "application/json")

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

			bodyBytes, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("Failed to read response body: %v", err)
			}

			var response map[string]interface{}
			if err := json.Unmarshal(bodyBytes, &response); err != nil {
				t.Fatalf("Failed to decode response: %v", err)
			}

			// Verify response structure
			if _, ok := response["embedding"]; !ok {
				t.Error("Response missing 'embedding' field")
			}

			// Test metadata parsing
			geminiProvider := providerManager.GetProvider("gemini")
			if geminiProvider == nil {
				t.Fatal("Gemini provider not found")
			}

			metadata, err := geminiProvider.ParseResponseMetadata(bytes.NewReader(bodyBytes), false)
			if err != nil {
				t.Fatalf("Failed to parse metadata: %v", err)
			}

			// Validate that we got some metadata
			if metadata == nil {
				t.Error("Expected metadata to be non-nil")
			}

			t.Logf("Embed content test passed. Model: %s", model.modelID)
		})
	}
}

// Helper function for v1beta non-streaming tests
func testGeminiV1BetaNonStreaming(t *testing.T, server *httptest.Server, providerManager *ProviderManager, apiKey string, model geminiTestModel) {
	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]string{
					{
						"text": model.testPrompt,
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}

	url := fmt.Sprintf("%s/gemini/v1beta/models/%s:generateContent?key=%s", server.URL, model.modelID, apiKey)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")

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
	if _, ok := response["candidates"]; !ok {
		t.Error("Response missing 'candidates' field")
	}
	if _, ok := response["usageMetadata"]; !ok {
		t.Error("Response missing 'usageMetadata' field")
	}

	// Test metadata parsing
	geminiProvider := providerManager.GetProvider("gemini")
	if geminiProvider == nil {
		t.Fatal("Gemini provider not found")
	}

	metadata, err := geminiProvider.ParseResponseMetadata(bytes.NewReader(bodyBytes), false)
	if err != nil {
		t.Fatalf("Failed to parse metadata: %v", err)
	}

	validateGeminiMetadata(t, metadata, "gemini", false)

	t.Logf("V1Beta non-streaming test passed. Model: %s", model.modelID)
}

// Helper function for v1beta streaming tests
func testGeminiV1BetaStreaming(t *testing.T, server *httptest.Server, providerManager *ProviderManager, apiKey string, model geminiTestModel) {
	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]string{
					{
						"text": "Hello!",
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}

	url := fmt.Sprintf("%s/gemini/v1beta/models/%s:generateContent?key=%s&alt=sse", server.URL, model.modelID, apiKey)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")

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
		if strings.TrimSpace(jsonData) == "[DONE]" || strings.TrimSpace(jsonData) == "" {
			break
		}

		chunkCount++
		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &chunk); err != nil {
			t.Logf("Warning: failed to parse chunk: %v", err)
			continue
		}

		// Check for usage information
		if usage, ok := chunk["usageMetadata"]; ok && usage != nil {
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
	geminiProvider := providerManager.GetProvider("gemini")
	if geminiProvider == nil {
		t.Fatal("Gemini provider not found")
	}

	metadata, err := geminiProvider.ParseResponseMetadata(bytes.NewReader(streamData.Bytes()), true)
	if err != nil {
		t.Fatalf("Failed to parse streaming metadata: %v", err)
	}

	validateGeminiMetadata(t, metadata, "gemini", true)

	t.Logf("V1Beta streaming test passed. Model: %s, Received %d chunks, usage included: %v", model.modelID, chunkCount, hasUsage)
}

// Helper function for v1beta count tokens tests
func testGeminiV1BetaCountTokens(t *testing.T, server *httptest.Server, providerManager *ProviderManager, apiKey string, model geminiTestModel) {
	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]string{
					{"text": model.testPrompt},
				},
			},
		},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}

	url := fmt.Sprintf("%s/gemini/v1beta/models/%s:countTokens?key=%s", server.URL, model.modelID, apiKey)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")

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

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify response structure
	if _, ok := response["totalTokens"]; !ok {
		t.Error("Response missing 'totalTokens' field")
	}

	t.Logf("V1Beta count tokens test passed. Model: %s, Total tokens: %v", model.modelID, response["totalTokens"])
}

// Helper function for v1beta embed content tests
func testGeminiV1BetaEmbedContent(t *testing.T, server *httptest.Server, providerManager *ProviderManager, apiKey string, model struct {
	name    string
	modelID string
	text    string
},
) {
	requestBody := map[string]interface{}{
		"content": map[string]interface{}{
			"parts": []map[string]string{
				{"text": model.text},
			},
		},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}

	url := fmt.Sprintf("%s/gemini/v1beta/models/%s:embedContent?key=%s", server.URL, model.modelID, apiKey)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")

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

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("Failed to read response body: %v", err)
	}

	var response map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &response); err != nil {
		t.Fatalf("Failed to decode response: %v", err)
	}

	// Verify response structure
	if _, ok := response["embedding"]; !ok {
		t.Error("Response missing 'embedding' field")
	}

	// Test metadata parsing
	geminiProvider := providerManager.GetProvider("gemini")
	if geminiProvider == nil {
		t.Fatal("Gemini provider not found")
	}

	metadata, err := geminiProvider.ParseResponseMetadata(bytes.NewReader(bodyBytes), false)
	if err != nil {
		t.Fatalf("Failed to parse metadata: %v", err)
	}

	// Validate that we got some metadata
	if metadata == nil {
		t.Error("Expected metadata to be non-nil")
	}

	t.Logf("V1Beta embed content test passed. Model: %s", model.modelID)
}
