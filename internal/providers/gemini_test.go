package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// validateMetadata is a helper function to validate metadata parsing
func validateGeminiMetadata(t *testing.T, metadata *LLMResponseMetadata, expectedProvider string, isStreaming bool) {
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

	// Verify total tokens calculation (for Gemini, TotalTokens = InputTokens + OutputTokens)
	if metadata.TotalTokens > 0 && metadata.InputTokens > 0 && metadata.OutputTokens > 0 {
		expectedTotal := metadata.InputTokens + metadata.OutputTokens
		if metadata.TotalTokens != expectedTotal {
			t.Errorf("Total tokens mismatch: expected %d, got %d", expectedTotal, metadata.TotalTokens)
		}
	}

	// Thought tokens are optional for Gemini
	if metadata.ThoughtTokens > 0 {
		t.Logf("Thought tokens found: %d", metadata.ThoughtTokens)
	}

	t.Logf("Metadata validation passed: Model=%s, InputTokens=%d, OutputTokens=%d, TotalTokens=%d, ThoughtTokens=%d, IsStreaming=%v",
		metadata.Model, metadata.InputTokens, metadata.OutputTokens, metadata.TotalTokens, metadata.ThoughtTokens, metadata.IsStreaming)
}

// Test Gemini non-streaming endpoint
func TestGemini_NonStreaming(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY environment variable is not set")
	}

	server, providerManager := setupTestServer(t)
	defer server.Close()

	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]string{
					{
						"text": "Hello! Can you tell me a short joke?",
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}

	url := fmt.Sprintf("%s/gemini/v1/models/gemini-2.0-flash:generateContent?key=%s", server.URL, apiKey)
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

	t.Logf("Gemini non-streaming test passed. Response: %v", response)
}

// Test Gemini streaming endpoint
func TestGemini_Streaming(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY environment variable is not set")
	}

	server, providerManager := setupTestServer(t)
	defer server.Close()

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

	url := fmt.Sprintf("%s/gemini/v1/models/gemini-2.0-flash:generateContent?key=%s&alt=sse", server.URL, apiKey)
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

	t.Logf("Gemini streaming test passed. Received %d chunks, usage included: %v", chunkCount, hasUsage)
}

// Test Gemini with explicit streaming endpoint
func TestGemini_StreamGenerateContent(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY environment variable is not set")
	}

	server, providerManager := setupTestServer(t)
	defer server.Close()

	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]string{
					{
						"text": "What is the capital of France?",
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}

	url := fmt.Sprintf("%s/gemini/v1/models/gemini-2.0-flash:streamGenerateContent?alt=sse&key=%s", server.URL, apiKey)
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

	t.Logf("Gemini streamGenerateContent test passed. Received %d chunks, usage included: %v", chunkCount, hasUsage)
}

// Test Gemini with Flash model
func TestGemini_FlashModel(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY environment variable is not set")
	}

	server, providerManager := setupTestServer(t)
	defer server.Close()

	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]string{
					{
						"text": "Count from 1 to 5",
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}

	url := fmt.Sprintf("%s/gemini/v1/models/gemini-2.0-flash:generateContent?key=%s", server.URL, apiKey)
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

	t.Logf("Gemini Flash model test passed. Response: %v", response)
}

// Test Gemini SSE streaming with alt=sse parameter
func TestGemini_SSEStreaming(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY environment variable is not set")
	}

	server, providerManager := setupTestServer(t)
	defer server.Close()

	requestBody := map[string]interface{}{
		"contents": []map[string]interface{}{
			{
				"parts": []map[string]string{
					{
						"text": "Explain how AI works in one sentence",
					},
				},
			},
		},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatalf("Failed to marshal request body: %v", err)
	}

	// Test with alt=sse parameter like the curl command
	url := fmt.Sprintf("%s/gemini/v1/models/gemini-2.5-flash:streamGenerateContent?alt=sse&key=%s", server.URL, apiKey)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		t.Fatalf("Failed to create request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	// Note: Not setting Accept header to test if alt=sse param is sufficient

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

	// Check if response is SSE format
	contentType := resp.Header.Get("Content-Type")
	t.Logf("Content-Type: %s", contentType)

	// Read and capture all SSE streaming data
	var streamData bytes.Buffer
	scanner := bufio.NewScanner(resp.Body)
	chunkCount := 0
	hasUsage := false
	var lastChunk string

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			t.Fatal("SSE streaming test timed out")
		default:
		}

		line := scanner.Text()
		streamData.WriteString(line + "\n")

		// SSE format: data: {...}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		jsonData := strings.TrimPrefix(line, "data: ")
		if strings.TrimSpace(jsonData) == "[DONE]" {
			break
		}

		chunkCount++
		lastChunk = jsonData

		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &chunk); err != nil {
			t.Logf("Warning: failed to parse SSE chunk: %v", err)
			continue
		}

		// Check for usage information
		if usage, ok := chunk["usageMetadata"]; ok && usage != nil {
			hasUsage = true
			t.Logf("Usage metadata found: %v", usage)
		}

		// Limit the number of chunks we process for testing
		if chunkCount > 50 {
			break
		}
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("Error reading SSE stream: %v", err)
	}

	if chunkCount == 0 {
		t.Error("No SSE streaming chunks received")
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

	t.Logf("Gemini SSE streaming test passed. Received %d chunks, usage included: %v", chunkCount, hasUsage)
	if lastChunk != "" {
		t.Logf("Last chunk received: %s", lastChunk)
	}
}
