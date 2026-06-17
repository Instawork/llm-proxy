package fake

import (
	"encoding/json"
	"fmt"
	"time"
)

func successBody(provider, model string, inputTokens, outputTokens int) []byte {
	switch provider {
	case "anthropic":
		return anthropicSuccess(model, inputTokens, outputTokens)
	case "gemini":
		return geminiSuccess(model, inputTokens, outputTokens)
	default:
		return openAISuccess(model, inputTokens, outputTokens)
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

func openAISuccess(model string, inTok, outTok int) []byte {
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
					"content": "fake response",
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]int{
			"prompt_tokens":     inTok,
			"completion_tokens": outTok,
			"total_tokens":      inTok + outTok,
		},
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

func anthropicSuccess(model string, inTok, outTok int) []byte {
	body := map[string]any{
		"id":   "msg_fake",
		"type": "message",
		"role": "assistant",
		"model": model,
		"content": []map[string]string{
			{"type": "text", "text": "fake response"},
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

func geminiSuccess(model string, inTok, outTok int) []byte {
	body := map[string]any{
		"candidates": []map[string]any{
			{
				"content": map[string]any{
					"parts": []map[string]string{{"text": "fake response"}},
					"role":  "model",
				},
				"finishReason": "STOP",
			},
		},
		"usageMetadata": map[string]int{
			"promptTokenCount":     inTok,
			"candidatesTokenCount": outTok,
			"totalTokenCount":      inTok + outTok,
		},
		"modelVersion": model,
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
