package providers

import (
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
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

// TestAnthropicGzipDecompression tests the gzip decompression functionality
func TestAnthropicGzipDecompression(t *testing.T) {
	proxy := NewAnthropicProxy()

	// Create a sample JSON response
	originalResponse := `{
		"id": "msg_01XFDUDYJgAACzvnptvVoYEL",
		"type": "message",
		"role": "assistant",
		"content": [{"type": "text", "text": "Hello! How can I help you today?"}],
		"model": "claude-3-5-sonnet-20241022",
		"stop_reason": "end_turn",
		"stop_sequence": null,
		"usage": {"input_tokens": 10, "output_tokens": 25}
	}`

	// Test 1: Uncompressed response should work as before
	t.Run("uncompressed", func(t *testing.T) {
		reader := strings.NewReader(originalResponse)
		metadata, err := proxy.parseNonStreamingResponse(reader)
		if err != nil {
			t.Fatalf("Failed to parse uncompressed response: %v", err)
		}

		if metadata.Model != "claude-3-5-sonnet-20241022" {
			t.Errorf("Expected model claude-3-5-sonnet-20241022, got %s", metadata.Model)
		}
		if metadata.InputTokens != 10 {
			t.Errorf("Expected 10 input tokens, got %d", metadata.InputTokens)
		}
		if metadata.OutputTokens != 25 {
			t.Errorf("Expected 25 output tokens, got %d", metadata.OutputTokens)
		}
	})

	// Test 2: Gzip compressed response should be decompressed and parsed correctly
	t.Run("gzip_compressed", func(t *testing.T) {
		// Compress the response using gzip
		var compressedBuf bytes.Buffer
		gzipWriter := gzip.NewWriter(&compressedBuf)
		_, err := gzipWriter.Write([]byte(originalResponse))
		if err != nil {
			t.Fatalf("Failed to write to gzip writer: %v", err)
		}
		err = gzipWriter.Close()
		if err != nil {
			t.Fatalf("Failed to close gzip writer: %v", err)
		}

		// Parse the compressed response
		reader := bytes.NewReader(compressedBuf.Bytes())
		metadata, err := proxy.parseNonStreamingResponse(reader)
		if err != nil {
			t.Fatalf("Failed to parse gzip compressed response: %v", err)
		}

		if metadata.Model != "claude-3-5-sonnet-20241022" {
			t.Errorf("Expected model claude-3-5-sonnet-20241022, got %s", metadata.Model)
		}
		if metadata.InputTokens != 10 {
			t.Errorf("Expected 10 input tokens, got %d", metadata.InputTokens)
		}
		if metadata.OutputTokens != 25 {
			t.Errorf("Expected 25 output tokens, got %d", metadata.OutputTokens)
		}
	})

	// Test 3: Test the decompression helper function directly
	t.Run("decompression_helper", func(t *testing.T) {
		// Test with uncompressed data
		uncompressedReader := strings.NewReader("test data")
		result, err := DecompressResponseIfNeeded(uncompressedReader)
		if err != nil {
			t.Fatalf("Failed to process uncompressed data: %v", err)
		}

		data, err := io.ReadAll(result)
		if err != nil {
			t.Fatalf("Failed to read result: %v", err)
		}
		if string(data) != "test data" {
			t.Errorf("Expected 'test data', got '%s'", string(data))
		}

		// Test with compressed data
		var compressedBuf bytes.Buffer
		gzipWriter := gzip.NewWriter(&compressedBuf)
		_, err = gzipWriter.Write([]byte("compressed test data"))
		if err != nil {
			t.Fatalf("Failed to write compressed data: %v", err)
		}
		err = gzipWriter.Close()
		if err != nil {
			t.Fatalf("Failed to close gzip writer: %v", err)
		}

		compressedReader := bytes.NewReader(compressedBuf.Bytes())
		result, err = DecompressResponseIfNeeded(compressedReader)
		if err != nil {
			t.Fatalf("Failed to process compressed data: %v", err)
		}

		// Close the gzip reader if it was created
		if gzipReader, ok := result.(*gzip.Reader); ok {
			defer gzipReader.Close()
		}

		data, err = io.ReadAll(result)
		if err != nil {
			t.Fatalf("Failed to read decompressed result: %v", err)
		}
		if string(data) != "compressed test data" {
			t.Errorf("Expected 'compressed test data', got '%s'", string(data))
		}
	})
}
