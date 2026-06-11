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

// OpenAIChatRepeatEmail asks the model to echo a unique email address from
// the user message. When wire-mode restore works, the client sees the raw
// email even though the upstream model only saw a MASK placeholder.
func (p *ProxyClient) OpenAIChatRepeatEmail(ctx context.Context, apiKey, email string) (*ProxyResponse, string, error) {
	prompt := fmt.Sprintf(
		"My email is %s. Reply with ONLY that email address and nothing else.",
		email,
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
