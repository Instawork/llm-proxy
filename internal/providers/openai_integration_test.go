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

// openaiTestModel represents a model configuration for testing
type openaiTestModel struct {
	name        string
	modelID     string
	endpoint    string // "chat" or "completions"
	maxTokens   int
	testPrompt  string
	temperature float64
}

// Test models to use in parameterized tests
var openaiTestModels = []openaiTestModel{
	{
		name:        "GPT-3.5-Turbo",
		modelID:     "gpt-3.5-turbo",
		endpoint:    "chat",
		maxTokens:   100,
		testPrompt:  "Hello! Can you tell me a short joke?",
		temperature: 0.7,
	},
	{
		name:        "GPT-4o-mini",
		modelID:     "gpt-4o-mini",
		endpoint:    "chat",
		maxTokens:   100,
		testPrompt:  "What is 2+2?",
		temperature: 0.0,
	},
	// Legacy completions model
	{
		name:        "GPT-3.5-Turbo-Instruct",
		modelID:     "gpt-3.5-turbo-instruct",
		endpoint:    "completions",
		maxTokens:   50,
		testPrompt:  "Hello, world!",
		temperature: 0.7,
	},
}

// TestOpenAIIntegration_ChatCompletions_Models tests multiple OpenAI models using subtests
func TestOpenAIIntegration_ChatCompletions_Models(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY environment variable is not set")
	}

	server, providerManager := setupTestServer(t)
	defer server.Close()

	// Run model tests in parallel
	for _, model := range openaiTestModels {
		model := model // capture range variable

		t.Run(model.name, func(t *testing.T) {
			// Test non-streaming
			t.Run("NonStreaming", func(t *testing.T) {
				testOpenAIChatCompletionsNonStreaming(t, server, providerManager, apiKey, model)
			})

			// Test streaming (chat endpoint only)
			if model.endpoint == "chat" {
				t.Run("Streaming", func(t *testing.T) {
					testOpenAIChatCompletionsStreaming(t, server, providerManager, apiKey, model)
				})
			}
		})
	}
}

// TestOpenAIIntegration_ChatCompletions_AdvancedScenarios tests various advanced scenarios with different models
func TestOpenAIIntegration_ChatCompletions_AdvancedScenarios(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY environment variable is not set")
	}

	server, _ := setupTestServer(t)
	defer server.Close()

	// Define test scenarios
	scenarios := []struct {
		name          string
		model         openaiTestModel
		messages      []map[string]string // for chat endpoint
		prompt        string              // for completions endpoint
		maxTokens     int
		temperature   float64
		stream        bool
		includeUsage  bool // for streaming with usage
		systemMessage string
	}{
		{
			name:  "SimpleQuestion",
			model: openaiTestModels[0], // GPT-3.5-Turbo
			messages: []map[string]string{
				{"role": "user", "content": "What is the capital of France?"},
			},
			maxTokens:   50,
			temperature: 0.0,
			stream:      false,
		},
		{
			name:  "WithSystemMessage",
			model: openaiTestModels[0], // GPT-3.5-Turbo
			messages: []map[string]string{
				{"role": "system", "content": "You are a helpful assistant that speaks like a pirate."},
				{"role": "user", "content": "Tell me about the weather."},
			},
			maxTokens:   100,
			temperature: 0.7,
			stream:      false,
		},
		{
			name:  "Conversation",
			model: openaiTestModels[0], // GPT-3.5-Turbo
			messages: []map[string]string{
				{"role": "user", "content": "Hello, I'm learning Go."},
				{"role": "assistant", "content": "That's great! Go is an excellent language for building concurrent applications. What would you like to know about Go?"},
				{"role": "user", "content": "What are goroutines?"},
			},
			maxTokens:   150,
			temperature: 0.7,
			stream:      false,
		},
		{
			name:  "StreamingWithUsage",
			model: openaiTestModels[0], // GPT-3.5-Turbo
			messages: []map[string]string{
				{"role": "user", "content": "Explain concurrency in one sentence."},
			},
			maxTokens:    100,
			temperature:  0.5,
			stream:       true,
			includeUsage: true,
		},
		{
			name:        "LegacyCompletion",
			model:       openaiTestModels[2], // GPT-3.5-Turbo-Instruct
			prompt:      "Once upon a time",
			maxTokens:   50,
			temperature: 0.8,
			stream:      false,
		},
	}

	// Run each scenario as a subtest
	for _, scenario := range scenarios {
		scenario := scenario // capture range variable

		t.Run(fmt.Sprintf("%s_%s", scenario.model.name, scenario.name), func(t *testing.T) {
			var requestBody map[string]interface{}
			var url string

			if scenario.model.endpoint == "chat" {
				requestBody = map[string]interface{}{
					"model":       scenario.model.modelID,
					"max_tokens":  scenario.maxTokens,
					"messages":    scenario.messages,
					"temperature": scenario.temperature,
				}

				if scenario.stream {
					requestBody["stream"] = true
					if scenario.includeUsage {
						requestBody["stream_options"] = map[string]bool{
							"include_usage": true,
						}
					}
				}

				url = server.URL + "/openai/v1/chat/completions"
			} else {
				// completions endpoint
				requestBody = map[string]interface{}{
					"model":       scenario.model.modelID,
					"prompt":      scenario.prompt,
					"max_tokens":  scenario.maxTokens,
					"temperature": scenario.temperature,
				}

				if scenario.stream {
					requestBody["stream"] = true
				}

				url = server.URL + "/openai/v1/completions"
			}

			jsonData, err := json.Marshal(requestBody)
			if err != nil {
				t.Fatalf("Failed to marshal request body: %v", err)
			}

			req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+apiKey)

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
				if _, ok := response["choices"]; !ok {
					t.Error("Response missing 'choices' field")
				}
				if _, ok := response["usage"]; !ok {
					t.Error("Response missing 'usage' field")
				}
			}

			t.Logf("Scenario %s completed successfully", scenario.name)
		})
	}
}

// TestOpenAIIntegration_Responses_Models tests OpenAI Responses API with different models
func TestOpenAIIntegration_Responses_Models(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY environment variable is not set")
	}

	server, providerManager := setupTestServer(t)
	defer server.Close()

	// Test models for Responses API
	responsesTestModels := []openaiTestModel{
		{
			name:        "GPT-4o-mini-Responses",
			modelID:     "gpt-4o-mini",
			endpoint:    "responses",
			maxTokens:   100,
			testPrompt:  "What is 2+2?",
			temperature: 0.0,
		},
		{
			name:        "GPT-4o-Responses",
			modelID:     "gpt-4o",
			endpoint:    "responses",
			maxTokens:   150,
			testPrompt:  "Explain quantum computing in simple terms.",
			temperature: 0.7,
		},
	}

	// Run model tests in parallel
	for _, model := range responsesTestModels {
		model := model // capture range variable

		t.Run(model.name, func(t *testing.T) {
			// Test non-streaming
			t.Run("NonStreaming", func(t *testing.T) {
				testOpenAIResponsesNonStreaming(t, server, providerManager, apiKey, model)
			})

			// Test streaming
			t.Run("Streaming", func(t *testing.T) {
				testOpenAIResponsesStreaming(t, server, providerManager, apiKey, model)
			})
		})
	}
}

// TestOpenAIIntegration_Responses_AdvancedScenarios tests various advanced scenarios with Responses API
func TestOpenAIIntegration_Responses_AdvancedScenarios(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY environment variable is not set")
	}

	server, _ := setupTestServer(t)
	defer server.Close()

	// Define test scenarios for Responses API
	scenarios := []struct {
		name        string
		model       string
		prompt      string
		maxTokens   int
		temperature float64
		stream      bool
		store       bool
	}{
		{
			name:        "SimpleQuestion",
			model:       "gpt-4o-mini",
			prompt:      "What is the capital of France?",
			maxTokens:   50,
			temperature: 0.0,
			stream:      false,
			store:       false,
		},
		{
			name:        "CreativeWriting",
			model:       "gpt-4o",
			prompt:      "Write a short story about a robot learning to paint.",
			maxTokens:   200,
			temperature: 0.8,
			stream:      false,
			store:       true,
		},
		{
			name:        "StreamingResponse",
			model:       "gpt-4o-mini",
			prompt:      "Explain the benefits of renewable energy.",
			maxTokens:   150,
			temperature: 0.5,
			stream:      true,
			store:       false,
		},
		{
			name:        "TechnicalExplanation",
			model:       "gpt-4o",
			prompt:      "How does a neural network work?",
			maxTokens:   300,
			temperature: 0.3,
			stream:      false,
			store:       false,
		},
	}

	// Run each scenario as a subtest
	for _, scenario := range scenarios {
		scenario := scenario // capture range variable

		t.Run(scenario.name, func(t *testing.T) {
			requestBody := map[string]interface{}{
				"model":             scenario.model,
				"input":             scenario.prompt,
				"max_output_tokens": scenario.maxTokens,
				"temperature":       scenario.temperature,
				"store":             scenario.store,
			}

			if scenario.stream {
				requestBody["stream"] = true
			}

			jsonData, err := json.Marshal(requestBody)
			if err != nil {
				t.Fatalf("Failed to marshal request body: %v", err)
			}

			url := server.URL + "/openai/v1/responses"
			req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
			if err != nil {
				t.Fatalf("Failed to create request: %v", err)
			}

			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+apiKey)

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

				// Verify Responses API specific structure
				if _, ok := response["output"]; !ok {
					t.Error("Response missing 'output' field")
				}
				if _, ok := response["usage"]; !ok {
					t.Error("Response missing 'usage' field")
				}
				if _, ok := response["status"]; !ok {
					t.Error("Response missing 'status' field")
				}
			}

			t.Logf("Scenario %s completed successfully", scenario.name)
		})
	}
}

// Helper function for non-streaming tests
func testOpenAIChatCompletionsNonStreaming(t *testing.T, server *httptest.Server, providerManager *ProviderManager, apiKey string, model openaiTestModel) {
	var requestBody map[string]interface{}
	var url string

	if model.endpoint == "chat" {
		requestBody = map[string]interface{}{
			"model": model.modelID,
			"messages": []map[string]string{
				{
					"role":    "user",
					"content": model.testPrompt,
				},
			},
			"max_tokens":  model.maxTokens,
			"temperature": model.temperature,
		}
		url = server.URL + "/openai/v1/chat/completions"
	} else {
		// completions endpoint
		requestBody = map[string]interface{}{
			"model":       model.modelID,
			"prompt":      model.testPrompt,
			"max_tokens":  model.maxTokens,
			"temperature": model.temperature,
		}
		url = server.URL + "/openai/v1/completions"
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

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
	if _, ok := response["choices"]; !ok {
		t.Error("Response missing 'choices' field")
	}
	if _, ok := response["usage"]; !ok {
		t.Error("Response missing 'usage' field")
	}
	if _, ok := response["model"]; !ok {
		t.Error("Response missing 'model' field")
	}

	// Test metadata parsing
	openAIProvider := providerManager.GetProvider("openai")
	if openAIProvider == nil {
		t.Fatal("OpenAI provider not found")
	}

	metadata, err := openAIProvider.ParseResponseMetadata(bytes.NewReader(bodyBytes), false)
	if err != nil {
		t.Fatalf("Failed to parse metadata: %v", err)
	}

	validateOpenAIMetadata(t, metadata, "openai", false)

	t.Logf("Non-streaming test passed. Model: %v, Endpoint: %s", response["model"], model.endpoint)
}

// Helper function for streaming tests
func testOpenAIChatCompletionsStreaming(t *testing.T, server *httptest.Server, providerManager *ProviderManager, apiKey string, model openaiTestModel) {
	if model.endpoint != "chat" {
		t.Skip("Streaming only supported for chat endpoint")
	}

	requestBody := map[string]interface{}{
		"model": model.modelID,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": "Hello!",
			},
		},
		"stream": true,
		"stream_options": map[string]bool{
			"include_usage": true,
		},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}

	req, err := http.NewRequest("POST", server.URL+"/openai/v1/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

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
	openAIProvider := providerManager.GetProvider("openai")
	if openAIProvider == nil {
		t.Fatal("OpenAI provider not found")
	}

	metadata, err := openAIProvider.ParseResponseMetadata(bytes.NewReader(streamData.Bytes()), true)
	if err != nil {
		t.Fatalf("Failed to parse streaming metadata: %v", err)
	}

	validateOpenAIMetadata(t, metadata, "openai", true)

	t.Logf("Streaming test passed. Model: %s, Received %d chunks, usage included: %v", model.modelID, chunkCount, hasUsage)
}

// Helper function for Responses API non-streaming tests
func testOpenAIResponsesNonStreaming(t *testing.T, server *httptest.Server, providerManager *ProviderManager, apiKey string, model openaiTestModel) {
	requestBody := map[string]interface{}{
		"model":             model.modelID,
		"input":             model.testPrompt,
		"max_output_tokens": model.maxTokens,
		"temperature":       model.temperature,
		"store":             false,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}

	req, err := http.NewRequest("POST", server.URL+"/openai/v1/responses", bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

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

	// Verify Responses API specific response structure
	if _, ok := response["output"]; !ok {
		t.Error("Response missing 'output' field")
	}
	if _, ok := response["usage"]; !ok {
		t.Error("Response missing 'usage' field")
	}
	if _, ok := response["status"]; !ok {
		t.Error("Response missing 'status' field")
	}
	if _, ok := response["model"]; !ok {
		t.Error("Response missing 'model' field")
	}

	// Test metadata parsing
	openAIProvider := providerManager.GetProvider("openai")
	if openAIProvider == nil {
		t.Fatal("OpenAI provider not found")
	}

	metadata, err := openAIProvider.ParseResponseMetadata(bytes.NewReader(bodyBytes), false)
	if err != nil {
		t.Fatalf("Failed to parse metadata: %v", err)
	}

	validateOpenAIMetadata(t, metadata, "openai", false)

	t.Logf("Responses API non-streaming test passed. Model: %v", response["model"])
}

// Helper function for Responses API streaming tests
func testOpenAIResponsesStreaming(t *testing.T, server *httptest.Server, providerManager *ProviderManager, apiKey string, model openaiTestModel) {
	requestBody := map[string]interface{}{
		"model":             model.modelID,
		"input":             model.testPrompt,
		"max_output_tokens": model.maxTokens,
		"temperature":       model.temperature,
		"stream":            true,
		"store":             false,
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}

	req, err := http.NewRequest("POST", server.URL+"/openai/v1/responses", bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

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

		// Check for usage information in Responses API streaming
		if event, ok := chunk["event"].(map[string]interface{}); ok {
			if usage, ok := event["usage"]; ok && usage != nil {
				hasUsage = true
			}
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
	openAIProvider := providerManager.GetProvider("openai")
	if openAIProvider == nil {
		t.Fatal("OpenAI provider not found")
	}

	metadata, err := openAIProvider.ParseResponseMetadata(bytes.NewReader(streamData.Bytes()), true)
	if err != nil {
		t.Fatalf("Failed to parse streaming metadata: %v", err)
	}

	validateOpenAIMetadata(t, metadata, "openai", true)

	t.Logf("Responses API streaming test passed. Model: %s, Received %d chunks, usage included: %v", model.modelID, chunkCount, hasUsage)
}
