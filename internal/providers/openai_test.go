package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// validateMetadata is a helper function to validate metadata parsing
func validateOpenAIMetadata(t *testing.T, metadata *LLMResponseMetadata, expectedProvider string, isStreaming bool) {
	if metadata == nil {
		t.Fatal("Metadata is nil")
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
	
	// For streaming responses, usage information might not be available in all chunks
	// So we're more lenient and only check if tokens are non-negative
	if isStreaming {
		if metadata.TotalTokens < 0 {
			t.Error("Total tokens should not be negative")
		}
		if metadata.InputTokens < 0 {
			t.Error("Input tokens should not be negative")
		}
		if metadata.OutputTokens < 0 {
			t.Error("Output tokens should not be negative")
		}
		
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
	
	// Verify total tokens calculation
	if metadata.TotalTokens > 0 && metadata.InputTokens > 0 && metadata.OutputTokens > 0 {
		expectedTotal := metadata.InputTokens + metadata.OutputTokens
		if metadata.TotalTokens != expectedTotal {
			t.Errorf("Total tokens mismatch: expected %d, got %d", expectedTotal, metadata.TotalTokens)
		}
	}
	
	t.Logf("Metadata validation passed: Model=%s, InputTokens=%d, OutputTokens=%d, TotalTokens=%d, IsStreaming=%v",
		metadata.Model, metadata.InputTokens, metadata.OutputTokens, metadata.TotalTokens, metadata.IsStreaming)
}

// Test OpenAI non-streaming endpoint
func TestOpenAI_NonStreaming(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY environment variable is not set")
	}
	
	server, providerManager := setupTestServer(t)
	defer server.Close()
	
	requestBody := map[string]interface{}{
		"model": "gpt-3.5-turbo",
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": "Hello! Can you tell me a short joke?",
			},
		},
		"max_tokens":  100,
		"temperature": 0.7,
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
	
	t.Logf("OpenAI non-streaming test passed. Model: %v", response["model"])
}

// Test OpenAI streaming endpoint
func TestOpenAI_Streaming(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY environment variable is not set")
	}
	
	server, providerManager := setupTestServer(t)
	defer server.Close()
	
	requestBody := map[string]interface{}{
		"model": "gpt-3.5-turbo",
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
	
	t.Logf("OpenAI streaming test passed. Received %d chunks, usage included: %v", chunkCount, hasUsage)
}

// Test OpenAI legacy completions endpoint
func TestOpenAI_LegacyCompletions(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY environment variable is not set")
	}
	
	server, providerManager := setupTestServer(t)
	defer server.Close()
	
	requestBody := map[string]interface{}{
		"model":       "gpt-3.5-turbo-instruct",
		"prompt":      "Hello, world!",
		"max_tokens":  50,
		"temperature": 0.7,
	}
	
	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}
	
	req, err := http.NewRequest("POST", server.URL+"/openai/v1/completions", bytes.NewBuffer(jsonData))
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
	
	t.Logf("OpenAI legacy completions test passed. Model: %v", response["model"])
}

// Test stream_options injection for streaming requests without explicit stream_options
func TestOpenAI_StreamOptionsInjection(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY environment variable is not set")
	}
	
	server, providerManager := setupTestServer(t)
	defer server.Close()
	
	// Create a streaming request WITHOUT explicit stream_options
	requestBody := map[string]interface{}{
		"model": "gpt-3.5-turbo",
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": "Hello!",
			},
		},
		"stream": true,
		// Deliberately omit stream_options to test injection
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
	
	// Read and capture all streaming data, specifically looking for usage information
	var streamData bytes.Buffer
	scanner := bufio.NewScanner(resp.Body)
	chunkCount := 0
	hasUsage := false
	var usageData map[string]interface{}
	
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			t.Fatal("Stream options injection test timed out")
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
		
		// Check for usage information (this should be present due to our injection)
		if usage, ok := chunk["usage"]; ok && usage != nil {
			hasUsage = true
			usageData = usage.(map[string]interface{})
			t.Logf("Found usage data in chunk: %+v", usageData)
		}
		
		// Limit the number of chunks we process for testing
		if chunkCount > 50 {
			break
		}
	}
	
	if chunkCount == 0 {
		t.Error("No streaming chunks received")
	}
	
	// The key test: Since we injected stream_options, we should receive usage information
	// This verifies our injection is working correctly
	if !hasUsage {
		t.Errorf("Expected usage information due to stream_options injection, but none was found")
	} else {
		t.Logf("✅ Successfully received usage information after stream_options injection")
		
		// Verify the usage data has the expected structure
		if promptTokens, ok := usageData["prompt_tokens"]; ok && promptTokens != nil {
			t.Logf("Prompt tokens: %v", promptTokens)
		} else {
			t.Error("Usage data missing prompt_tokens")
		}
		
		if completionTokens, ok := usageData["completion_tokens"]; ok && completionTokens != nil {
			t.Logf("Completion tokens: %v", completionTokens)
		} else {
			t.Error("Usage data missing completion_tokens")
		}
		
		if totalTokens, ok := usageData["total_tokens"]; ok && totalTokens != nil {
			t.Logf("Total tokens: %v", totalTokens)
		} else {
			t.Error("Usage data missing total_tokens")
		}
	}
	
	// Test metadata parsing to ensure it can extract the usage information
	openAIProvider := providerManager.GetProvider("openai")
	if openAIProvider == nil {
		t.Fatal("OpenAI provider not found")
	}
	
	metadata, err := openAIProvider.ParseResponseMetadata(bytes.NewReader(streamData.Bytes()), true)
	if err != nil {
		t.Fatalf("Failed to parse streaming metadata: %v", err)
	}
	
	// Since we injected stream_options, we should have complete usage information
	if hasUsage && metadata.TotalTokens == 0 {
		t.Error("Expected non-zero total tokens when usage information is present")
	}
	
	validateOpenAIMetadata(t, metadata, "openai", true)
	
	t.Logf("OpenAI stream_options injection test passed. Received %d chunks, usage included: %v", chunkCount, hasUsage)
} 
