package live

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
	openaiopt "github.com/openai/openai-go/option"
)

func openAIAssistantContent(body []byte) (string, error) {
	var root struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return "", err
	}
	if len(root.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	return root.Choices[0].Message.Content, nil
}

func anthropicAssistantContent(body []byte) (string, error) {
	var root struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return "", err
	}
	for _, block := range root.Content {
		if block.Type == "text" && block.Text != "" {
			return block.Text, nil
		}
	}
	return "", fmt.Errorf("no text content in anthropic response")
}

// OpenAIChatWithPIIScrub sends a PII-bearing prompt and returns the raw
// HTTP response body from the proxy. Used to assert wire-mode scrub +
// restore behaviour through a running llm-proxy instance.
func (p *ProxyClient) OpenAIChatWithPIIScrub(ctx context.Context, apiKey, userMessage string, maxTokens int64) (*ProxyResponse, []byte, error) {
	if maxTokens <= 0 {
		maxTokens = 64
	}
	client := openai.NewClient(
		openaiopt.WithBaseURL(p.base+"/openai/v1"),
		openaiopt.WithAPIKey(apiKey),
		openaiopt.WithHTTPClient(p.sdkHTTPClient()),
	)
	_, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModelGPT4oMini,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(userMessage),
		},
		MaxTokens: openai.Int(maxTokens),
	})
	pr := p.lastResponse()
	body := p.lastResponseBody()
	if err != nil {
		return &pr, body, fmt.Errorf("openai-go: %w", err)
	}
	return &pr, body, nil
}

// AnthropicChatWithPIIScrub posts to /anthropic/v1/messages and returns the
// raw response body (non-streaming JSON or SSE when stream=true).
func (p *ProxyClient) AnthropicChatWithPIIScrub(ctx context.Context, apiKey, userMessage string, maxTokens int64, stream bool) (*ProxyResponse, []byte, error) {
	if maxTokens <= 0 {
		maxTokens = 64
	}
	payload := map[string]any{
		"model":       "claude-haiku-4-5",
		"max_tokens":  maxTokens,
		"messages":    []map[string]any{{"role": "user", "content": userMessage}},
		"stream":      stream,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	resp, data, err := p.http.DoRaw(ctx, "POST", "/anthropic/v1/messages", body, map[string]string{
		"x-api-key":         apiKey,
		"anthropic-version": "2023-06-01",
	})
	if err != nil {
		return nil, nil, err
	}
	pr := ProxyResponse{Status: resp.StatusCode, Headers: resp.Header.Clone()}
	return &pr, data, nil
}

const repeatEmailPrompt = "My email is %s. Reply with ONLY that email address and nothing else."

func repeatEmailPromptFor(email string) string {
	return fmt.Sprintf(repeatEmailPrompt, email)
}

type streamScrubFunc func(ctx context.Context, apiKey, userMessage string, maxTokens int64, stream bool) (*ProxyResponse, []byte, error)
type bodyParseFunc func(body []byte) (string, error)

func (p *ProxyClient) chatRepeatEmail(ctx context.Context, apiKey, email string, stream bool, scrub streamScrubFunc, parse bodyParseFunc) (*ProxyResponse, string, error) {
	pr, body, err := scrub(ctx, apiKey, repeatEmailPromptFor(email), 40, stream)
	if err != nil {
		return pr, "", err
	}
	if stream {
		return pr, string(body), nil
	}
	content, parseErr := parse(body)
	if parseErr != nil {
		return pr, "", parseErr
	}
	return pr, content, nil
}

// OpenAIChatRepeatEmail asks the model to echo a unique email address from
// the user message. When wire-mode restore works, the client sees the raw
// email even though the upstream model only saw a MASK placeholder.
func (p *ProxyClient) OpenAIChatRepeatEmail(ctx context.Context, apiKey, email string) (*ProxyResponse, string, error) {
	pr, body, err := p.OpenAIChatWithPIIScrub(ctx, apiKey, repeatEmailPromptFor(email), 40)
	if err != nil {
		return pr, "", err
	}
	content, parseErr := openAIAssistantContent(body)
	if parseErr != nil {
		return pr, "", parseErr
	}
	return pr, content, nil
}

// AnthropicChatRepeatEmail is the Anthropic counterpart to OpenAIChatRepeatEmail.
func (p *ProxyClient) AnthropicChatRepeatEmail(ctx context.Context, apiKey, email string, stream bool) (*ProxyResponse, string, error) {
	return p.chatRepeatEmail(ctx, apiKey, email, stream, p.AnthropicChatWithPIIScrub, anthropicAssistantContent)
}

// OpenAIChatRepeatSSN asks the model to echo an SSN from the user message.
// With wire-mode scrubbing the upstream sees a SEAL placeholder; the client
// should never receive the raw SSN back.
func (p *ProxyClient) OpenAIChatRepeatSSN(ctx context.Context, apiKey, ssn string) (*ProxyResponse, string, error) {
	prompt := fmt.Sprintf(
		"My SSN is %s. Reply with ONLY that SSN including dashes and nothing else.",
		ssn,
	)
	pr, body, err := p.OpenAIChatWithPIIScrub(ctx, apiKey, prompt, 40)
	if err != nil {
		return pr, "", err
	}
	content, parseErr := openAIAssistantContent(body)
	if parseErr != nil {
		return pr, "", parseErr
	}
	return pr, content, nil
}

func normalizeEcho(s string) string {
	return strings.TrimSpace(strings.Trim(s, `"`))
}

func geminiAssistantContent(body []byte) (string, error) {
	var root struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return "", err
	}
	if len(root.Candidates) == 0 || len(root.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no text in gemini response")
	}
	return root.Candidates[0].Content.Parts[0].Text, nil
}

const geminiWireRestoreModel = "gemini-2.5-flash"

// GeminiChatWithPIIScrub posts to Gemini generateContent (or streamGenerateContent)
// and returns the raw response body.
func (p *ProxyClient) GeminiChatWithPIIScrub(ctx context.Context, apiKey, userMessage string, stream bool) (*ProxyResponse, []byte, error) {
	payload := map[string]any{
		"contents": []map[string]any{
			{"parts": []map[string]any{{"text": userMessage}}},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
	var path string
	if stream {
		path = fmt.Sprintf("/gemini/v1beta/models/%s:streamGenerateContent?alt=sse", geminiWireRestoreModel)
	} else {
		path = fmt.Sprintf("/gemini/v1beta/models/%s:generateContent", geminiWireRestoreModel)
	}
	resp, data, err := p.http.DoRaw(ctx, "POST", path, body, map[string]string{
		"x-goog-api-key": apiKey,
	})
	if err != nil {
		return nil, nil, err
	}
	pr := ProxyResponse{Status: resp.StatusCode, Headers: resp.Header.Clone()}
	return &pr, data, nil
}

// GeminiChatRepeatEmail is the Gemini counterpart to OpenAIChatRepeatEmail.
func (p *ProxyClient) GeminiChatRepeatEmail(ctx context.Context, apiKey, email string, stream bool) (*ProxyResponse, string, error) {
	return p.chatRepeatEmail(ctx, apiKey, email, stream, func(ctx context.Context, apiKey, userMessage string, _ int64, stream bool) (*ProxyResponse, []byte, error) {
		return p.GeminiChatWithPIIScrub(ctx, apiKey, userMessage, stream)
	}, geminiAssistantContent)
}
