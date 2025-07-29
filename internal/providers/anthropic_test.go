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
func validateAnthropicMetadata(t *testing.T, metadata *LLMResponseMetadata, expectedProvider string, isStreaming bool) {
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

	// Verify total tokens calculation (for Anthropic, TotalTokens = InputTokens + OutputTokens)
	if metadata.TotalTokens > 0 && metadata.InputTokens > 0 && metadata.OutputTokens > 0 {
		expectedTotal := metadata.InputTokens + metadata.OutputTokens
		if metadata.TotalTokens != expectedTotal {
			t.Errorf("Total tokens mismatch: expected %d, got %d", expectedTotal, metadata.TotalTokens)
		}
	}

	t.Logf("Metadata validation passed: Model=%s, InputTokens=%d, OutputTokens=%d, TotalTokens=%d, IsStreaming=%v",
		metadata.Model, metadata.InputTokens, metadata.OutputTokens, metadata.TotalTokens, metadata.IsStreaming)
}

// Test Anthropic non-streaming endpoint
func TestAnthropic_NonStreaming(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY environment variable is not set")
	}

	server, providerManager := setupTestServer(t)
	defer server.Close()

	requestBody := map[string]interface{}{
		"model":      "claude-3-sonnet-20240229",
		"max_tokens": 100,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": "Hello! Can you tell me a short joke?",
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

	t.Logf("Anthropic non-streaming test passed. Model: %v", response["model"])
}

// Test Anthropic streaming endpoint
func TestAnthropic_Streaming(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY environment variable is not set")
	}

	server, providerManager := setupTestServer(t)
	defer server.Close()

	requestBody := map[string]interface{}{
		"model":      "claude-3-sonnet-20240229",
		"max_tokens": 100,
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

	t.Logf("Anthropic streaming test passed. Received %d chunks, usage included: %v", chunkCount, hasUsage)
}

// Test Anthropic with different model (Claude 3 Haiku)
func TestAnthropic_HaikuModel(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY environment variable is not set")
	}

	server, providerManager := setupTestServer(t)
	defer server.Close()

	requestBody := map[string]interface{}{
		"model":      "claude-3-haiku-20240307",
		"max_tokens": 50,
		"messages": []map[string]string{
			{
				"role":    "user",
				"content": "What is 2+2?",
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

	t.Logf("Anthropic Haiku model test passed. Model: %v", response["model"])
}

// Test Anthropic streaming token parsing with real response format
func TestAnthropicStreamingTokenParsing(t *testing.T) {
	anthropicProvider := NewAnthropicProxy()

	// Real Anthropic streaming response format based on user's sample
	mockStreamResponse := `event: message_start
data: {"type": "message_start", "message": {"id": "msg_01AVJ15FX8ZGY7sLCo4ajfPW", "type": "message", "role": "assistant", "content": [], "model": "claude-3-5-haiku-20241022", "stop_reason": null, "stop_sequence": null, "usage": {"input_tokens": 25, "output_tokens": 1}}}

event: content_block_start
data: {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}

event: ping
data: {"type": "ping"}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "Hello"}}

event: content_block_delta
data: {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": "!"}}

event: content_block_stop
data: {"type": "content_block_stop", "index": 0}

event: message_delta
data: {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence":null}, "usage": {"output_tokens": 15}}

event: message_stop
data: {"type": "message_stop"}
`

	metadata, err := anthropicProvider.ParseResponseMetadata(strings.NewReader(mockStreamResponse), true)
	if err != nil {
		t.Fatalf("Failed to parse Anthropic streaming metadata: %v", err)
	}

	// Validate the parsed metadata
	if metadata.Provider != "anthropic" {
		t.Errorf("Expected provider 'anthropic', got %s", metadata.Provider)
	}

	if metadata.Model != "claude-3-5-haiku-20241022" {
		t.Errorf("Expected model 'claude-3-5-haiku-20241022', got %s", metadata.Model)
	}

	if metadata.RequestID != "msg_01AVJ15FX8ZGY7sLCo4ajfPW" {
		t.Errorf("Expected request ID 'msg_01AVJ15FX8ZGY7sLCo4ajfPW', got %s", metadata.RequestID)
	}

	// Check token counts - should be 25 input + (1 + 15) output = 41 total
	if metadata.InputTokens != 25 {
		t.Errorf("Expected 25 input tokens, got %d", metadata.InputTokens)
	}

	if metadata.OutputTokens != 16 { // 1 from message_start + 15 from message_delta
		t.Errorf("Expected 16 output tokens, got %d", metadata.OutputTokens)
	}

	if metadata.TotalTokens != 41 { // 25 + 16
		t.Errorf("Expected 41 total tokens, got %d", metadata.TotalTokens)
	}

	if metadata.FinishReason != "end_turn" {
		t.Errorf("Expected finish reason 'end_turn', got %s", metadata.FinishReason)
	}

	if !metadata.IsStreaming {
		t.Errorf("Expected IsStreaming true, got %v", metadata.IsStreaming)
	}

	t.Logf("✅ Anthropic streaming token parsing test passed - Input: %d, Output: %d, Total: %d",
		metadata.InputTokens, metadata.OutputTokens, metadata.TotalTokens)
}
