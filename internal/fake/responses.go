package fake

import (
	"encoding/json"
	"fmt"
	"time"
)

func successBody(provider, model string, inputTokens, outputTokens, cachedTokens int, assistantContent string) []byte {
	if assistantContent == "" {
		assistantContent = "fake response"
	}
	switch provider {
	case "anthropic":
		return anthropicSuccess(model, inputTokens, outputTokens, assistantContent)
	case "gemini":
		return geminiSuccess(model, inputTokens, outputTokens, cachedTokens, assistantContent)
	default:
		return openAISuccess(model, inputTokens, outputTokens, cachedTokens, assistantContent)
	}
}

func failureBody(provider string, status int) []byte {
	switch provider {
	case "anthropic":
		return anthropicError(status)
	case "gemini":
		return geminiError(status)
	default:
		return openAIError(status)
	}
}

func openAISuccess(model string, inTok, outTok, cachedTok int, content string) []byte {
	usage := map[string]any{
		"prompt_tokens":     inTok,
		"completion_tokens": outTok,
		"total_tokens":      inTok + outTok,
	}
	if cachedTok > 0 {
		// OpenAI reports cached tokens as a SUBSET of prompt_tokens (inclusive):
		// prompt_tokens is unchanged; cached_tokens just describes how many of
		// them were a cache hit. A correct proxy must not add this to input.
		usage["prompt_tokens_details"] = map[string]int{"cached_tokens": cachedTok}
	}
	body := map[string]any{
		"id":      "chatcmpl-fake",
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   model,
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]string{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
		"usage": usage,
	}
	b, _ := json.Marshal(body)
	return b
}

func openAIError(status int) []byte {
	msg := "fake upstream error"
	typ := "server_error"
	code := "server_error"
	switch status {
	case 503:
		msg = "Service unavailable"
		typ = "server_error"
	case 429:
		msg = "Rate limit exceeded"
		typ = "rate_limit_error"
		code = "rate_limit_exceeded"
	}
	body := map[string]any{
		"error": map[string]any{
			"message": msg,
			"type":    typ,
			"code":    code,
		},
	}
	b, _ := json.Marshal(body)
	return b
}

func anthropicSuccess(model string, inTok, outTok int, content string) []byte {
	body := map[string]any{
		"id":    "msg_fake",
		"type":  "message",
		"role":  "assistant",
		"model": model,
		"content": []map[string]string{
			{"type": "text", "text": content},
		},
		"stop_reason": "end_turn",
		"usage": map[string]int{
			"input_tokens":  inTok,
			"output_tokens": outTok,
		},
	}
	b, _ := json.Marshal(body)
	return b
}

func anthropicError(status int) []byte {
	typ := "api_error"
	switch status {
	case 429:
		typ = "rate_limit_error"
	case 503:
		typ = "overloaded_error"
	}
	body := map[string]any{
		"type": "error",
		"error": map[string]string{
			"type":    typ,
			"message": fmt.Sprintf("fake anthropic %d", status),
		},
	}
	b, _ := json.Marshal(body)
	return b
}

func geminiSuccess(model string, inTok, outTok, cachedTok int, content string) []byte {
	usage := map[string]int{
		"promptTokenCount":     inTok,
		"candidatesTokenCount": outTok,
		"totalTokenCount":      inTok + outTok,
	}
	if cachedTok > 0 {
		// Gemini's cachedContentTokenCount is a subset of promptTokenCount.
		usage["cachedContentTokenCount"] = cachedTok
	}
	body := map[string]any{
		"candidates": []map[string]any{
			{
				"content": map[string]any{
					"parts": []map[string]string{{"text": content}},
					"role":  "model",
				},
				"finishReason": "STOP",
			},
		},
		"usageMetadata": usage,
		"modelVersion":  model,
	}
	b, _ := json.Marshal(body)
	return b
}

func geminiError(status int) []byte {
	body := map[string]any{
		"error": map[string]any{
			"code":    status,
			"message": fmt.Sprintf("fake gemini %d", status),
			"status":  "UNAVAILABLE",
		},
	}
	b, _ := json.Marshal(body)
	return b
}
