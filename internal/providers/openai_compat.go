package providers

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/Instawork/llm-proxy/internal/proxylog"
)

// This file holds the shared OpenAI-compatibility detection used by the
// Anthropic and Gemini providers. Both vendors expose an OpenAI-style
// /chat/completions endpoint (Anthropic: /v1/chat/completions, Gemini:
// /v1beta/openai/chat/completions) whose responses are OpenAI-shaped
// ("choices" + usage.prompt_tokens) rather than the vendor's native format.
// Without delegation the native parsers unmarshal those bodies into zero
// token counts, which silently disables cost tracking, admin spend stats,
// and per-key cost-limit enforcement for that traffic (this is exactly what
// happened with Opik traffic on the Anthropic compat endpoint).

// looksLikeOpenAIChatJSON reports whether a (decompressed) non-streaming JSON
// body is OpenAI-shaped: a top-level "choices" array. Native Anthropic bodies
// use "content" and native Gemini bodies use "candidates", so this cannot
// misfire on vendor-native responses.
func looksLikeOpenAIChatJSON(body []byte) bool {
	var shape struct {
		Choices json.RawMessage `json:"choices"`
	}
	return json.Unmarshal(body, &shape) == nil && shape.Choices != nil
}

// looksLikeOpenAIChatStream reports whether SSE bytes carry OpenAI-style
// chat.completion chunks. It inspects the first few data lines for a
// top-level "object" beginning with "chat.completion". Native Anthropic
// streams carry "type" events (message_start, ...) and native Gemini streams
// carry "candidates", neither of which sets "object".
func looksLikeOpenAIChatStream(data []byte) bool {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	seen := 0
	for scanner.Scan() && seen < 5 {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		jsonData := strings.TrimPrefix(line, "data: ")
		if strings.TrimSpace(jsonData) == "[DONE]" {
			break
		}
		seen++
		var shape struct {
			Object string `json:"object"`
		}
		if json.Unmarshal([]byte(jsonData), &shape) != nil {
			continue
		}
		if strings.HasPrefix(shape.Object, "chat.completion") {
			return true
		}
	}
	return false
}

// parseOpenAICompatMetadata delegates an OpenAI-shaped body to the OpenAI
// parser and re-tags the resulting metadata with the owning provider's name
// so cost records, pricing lookups, and stats stay attributed to the vendor
// that actually served the request.
func parseOpenAICompatMetadata(body []byte, isStreaming bool, provider string) (*LLMResponseMetadata, error) {
	metadata, err := (&OpenAIProxy{}).ParseResponseMetadata(bytes.NewReader(body), isStreaming)
	if metadata != nil {
		metadata.Provider = provider
	}
	return metadata, err
}

// isChatCompletionsPath reports whether the request path targets an
// OpenAI-compatibility /chat/completions endpoint.
func isChatCompletionsPath(path string) bool {
	return strings.Contains(path, "/chat/completions")
}

// requestBodyHasStreamTrue reads (and restores) the request body and reports
// whether it contains "stream": true. Used by providers whose native
// streaming detection is path-based but whose OpenAI-compatibility endpoint
// signals streaming in the JSON body, like OpenAI proper.
func requestBodyHasStreamTrue(req *http.Request, providerName string) bool {
	if req.Body == nil {
		return false
	}

	var bodyBytes []byte
	var err error

	if req.GetBody != nil {
		bodyReader, err := req.GetBody()
		if err != nil {
			proxylog.Proxy("%s streaming check: error getting cached request body: %v", providerName, err)
			return false
		}
		defer bodyReader.Close()
		bodyBytes, err = io.ReadAll(bodyReader)
		if err != nil {
			proxylog.Proxy("%s streaming check: error reading cached request body: %v", providerName, err)
			return false
		}
	} else {
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			proxylog.Proxy("%s streaming check: error reading request body: %v", providerName, err)
			return false
		}
		req.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		req.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewBuffer(bodyBytes)), nil
		}
	}

	var requestData map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &requestData); err != nil {
		return false
	}
	if streamValue, exists := requestData["stream"]; exists {
		if streamBool, ok := streamValue.(bool); ok {
			return streamBool
		}
	}
	return false
}
