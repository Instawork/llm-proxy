package providers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Instawork/llm-proxy/internal/proxylog"
)

// Gemini Interactions API (POST /v1beta/interactions). Usage fields differ
// entirely from GenerateContent's usageMetadata, so it gets its own decoder.
// Non-streaming responses are a single Interaction object; streaming
// responses are SSE events tagged with event_type, the last of which
// (interaction.completed) wraps the final Interaction.

type interactionsUsage struct {
	TotalInputTokens   int `json:"total_input_tokens"`
	TotalOutputTokens  int `json:"total_output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	TotalThoughtTokens int `json:"total_thought_tokens"`
	TotalCachedTokens  int `json:"total_cached_tokens"`
}

type interaction struct {
	ID     string             `json:"id"`
	Object string             `json:"object"`
	Model  string             `json:"model"`
	Status string             `json:"status"`
	Usage  *interactionsUsage `json:"usage"`
}

type interactionsEvent struct {
	EventType   string             `json:"event_type"`
	Interaction *interaction       `json:"interaction"`
	Usage       *interactionsUsage `json:"usage"`
}

func looksLikeInteractionsJSON(body []byte) bool {
	var shape struct {
		Object string          `json:"object"`
		Usage  json.RawMessage `json:"usage"`
	}
	if json.Unmarshal(body, &shape) != nil {
		return false
	}
	return shape.Object == "interaction" || bytes.Contains(shape.Usage, []byte(`"total_input_tokens"`))
}

// looksLikeInteractionsStream inspects the first few SSE data lines for the
// event_type discriminator that only Interactions events carry.
func looksLikeInteractionsStream(data []byte) bool {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	seen := 0
	for scanner.Scan() && seen < 5 {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		seen++
		var shape struct {
			EventType string `json:"event_type"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &shape) == nil && shape.EventType != "" {
			return true
		}
	}
	return false
}

func interactionMetadata(in *interaction, usage *interactionsUsage, streaming bool) *LLMResponseMetadata {
	md := &LLMResponseMetadata{Model: "gemini", Provider: "gemini", IsStreaming: streaming}
	if in != nil {
		if in.Model != "" {
			md.Model = strings.TrimPrefix(in.Model, "models/")
		}
		md.RequestID = in.ID
		md.FinishReason = in.Status
	}
	if usage != nil {
		md.InputTokens = usage.TotalInputTokens
		md.OutputTokens = usage.TotalOutputTokens
		md.TotalTokens = usage.TotalTokens
		md.ThoughtTokens = usage.TotalThoughtTokens
		md.CacheReadInputTokens = usage.TotalCachedTokens
	}
	return md
}

func parseInteractionsMetadata(body []byte) (*LLMResponseMetadata, error) {
	var in interaction
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, fmt.Errorf("failed to parse Gemini interaction: %w", err)
	}
	return interactionMetadata(&in, in.Usage, false), nil
}

// parseInteractionsStream reads the whole SSE body. Usage comes from the
// interaction.completed event; if the stream ended early, the cumulative
// usage on the last step.stop event is the best available figure.
func parseInteractionsStream(data []byte) (*LLMResponseMetadata, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)

	var created *interaction
	var lastStepUsage *interactionsUsage
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev interactionsEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			proxylog.Proxy("gemini failed to parse interactions event: %v", err)
			continue
		}
		switch ev.EventType {
		case "interaction.created":
			created = ev.Interaction
		case "step.stop":
			if ev.Usage != nil {
				lastStepUsage = ev.Usage
			}
		case "interaction.completed":
			if ev.Interaction != nil {
				if ev.Interaction.Model == "" && created != nil {
					ev.Interaction.Model = created.Model
				}
				return interactionMetadata(ev.Interaction, ev.Interaction.Usage, true), nil
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("error reading interactions stream: %w", err)
	}
	if created == nil && lastStepUsage == nil {
		return nil, fmt.Errorf("no usage information found in interactions stream")
	}
	return interactionMetadata(created, lastStepUsage, true), nil
}
