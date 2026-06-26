package live

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/openai/openai-go"
	openaiopt "github.com/openai/openai-go/option"
	"google.golang.org/genai"
)

type ProxyResponse struct {
	Status    int
	Headers   http.Header
	Trailer   http.Header
	InputTok  string
	OutputTok string
	Provider  string
	Model     string
}

type ProxyClient struct {
	base      string
	timeout   time.Duration
	transport *capturingTransport
	http      *HTTPClient
}

func NewProxyClient(base string, timeout time.Duration) (*ProxyClient, error) {
	httpClient, err := NewHTTPClient(base, timeout)
	if err != nil {
		return nil, err
	}
	return &ProxyClient{
		base:      base,
		timeout:   timeout,
		transport: newCapturingTransport(),
		http:      httpClient,
	}, nil
}

func (p *ProxyClient) sdkHTTPClient() *http.Client {
	return &http.Client{
		Timeout:   p.timeout,
		Transport: p.transport,
	}
}

func (p *ProxyClient) lastResponse() ProxyResponse {
	return responseFromCapture(p.transport.snapshot())
}

func (p *ProxyClient) lastResponseBody() []byte {
	return p.transport.snapshotBody()
}

func (p *ProxyClient) OpenAIChat(ctx context.Context, apiKey, model string, maxTokens int) (*ProxyResponse, error) {
	if model == "" {
		model = "gpt-4o-mini"
	}
	client := openai.NewClient(
		openaiopt.WithBaseURL(p.base+"/openai/v1"),
		openaiopt.WithAPIKey(apiKey),
		openaiopt.WithHTTPClient(p.sdkHTTPClient()),
	)
	_, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(model),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Reply with exactly: ok"),
		},
		MaxTokens: openai.Int(int64(maxTokens)),
	})
	pr := p.lastResponse()
	if err != nil {
		return &pr, fmt.Errorf("openai-go: %w", err)
	}
	return &pr, nil
}

func (p *ProxyClient) AnthropicMessage(ctx context.Context, apiKey, model string, maxTokens int) (*ProxyResponse, error) {
	if model == "" {
		model = "claude-haiku-4-5"
	}
	client := anthropic.NewClient(
		option.WithBaseURL(p.base+"/anthropic"),
		option.WithAPIKey(apiKey),
		option.WithHTTPClient(p.sdkHTTPClient()),
	)
	_, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: int64(maxTokens),
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Reply with exactly: ok")),
		},
	})
	pr := p.lastResponse()
	if err != nil {
		return &pr, fmt.Errorf("anthropic-sdk-go: %w", err)
	}
	return &pr, nil
}

func (p *ProxyClient) GeminiGenerate(ctx context.Context, apiKey, model string) (*ProxyResponse, error) {
	if model == "" {
		model = "gemini-2.5-flash"
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey: apiKey,
		HTTPOptions: genai.HTTPOptions{
			BaseURL: p.base + "/gemini",
		},
		HTTPClient: p.sdkHTTPClient(),
	})
	if err != nil {
		return nil, fmt.Errorf("genai.NewClient: %w", err)
	}
	_, err = client.Models.GenerateContent(ctx, model, genai.Text("Reply with exactly: ok"), nil)
	pr := p.lastResponse()
	if err != nil {
		return &pr, fmt.Errorf("google.golang.org/genai: %w", err)
	}
	return &pr, nil
}

func (p *ProxyClient) OpenAIChatWithPII(ctx context.Context, apiKey string) (*ProxyResponse, error) {
	client := openai.NewClient(
		openaiopt.WithBaseURL(p.base+"/openai/v1"),
		openaiopt.WithAPIKey(apiKey),
		openaiopt.WithHTTPClient(p.sdkHTTPClient()),
	)
	_, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModelGPT4oMini,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("my ssn is 222-33-4444 and email alice@example.com — say ok"),
		},
		MaxTokens: openai.Int(5),
	})
	pr := p.lastResponse()
	if err != nil {
		return &pr, fmt.Errorf("openai-go: %w", err)
	}
	return &pr, nil
}

func (p *ProxyClient) Health(ctx context.Context) (*ProxyResponse, error) {
	resp, _, err := p.http.DoRaw(ctx, http.MethodGet, "/health", nil, nil)
	if err != nil {
		return nil, err
	}
	return &ProxyResponse{Status: resp.StatusCode, Headers: resp.Header.Clone(), Trailer: resp.Trailer.Clone()}, nil
}

func proxyOK(pr *ProxyResponse) error {
	if pr == nil {
		return fmt.Errorf("nil response")
	}
	if pr.Status < 200 || pr.Status >= 300 {
		return fmt.Errorf("status %d", pr.Status)
	}
	return nil
}

func hasTokenHeaders(pr *ProxyResponse) bool {
	if pr == nil {
		return false
	}
	return pr.InputTok != "" || pr.OutputTok != ""
}

// Redact posts plain text to POST /redact?mode=text. apiKey may be empty when the
// proxy runs with redact_api.dev_allow_unauthenticated in dev.
func (p *ProxyClient) Redact(ctx context.Context, apiKey, text string) (int, string, error) {
	headers := map[string]string{
		"Content-Type": "text/plain; charset=utf-8",
	}
	if strings.TrimSpace(apiKey) != "" {
		headers["Authorization"] = "Bearer " + apiKey
	}
	resp, data, err := p.http.DoRaw(ctx, http.MethodPost, "/redact?mode=text", []byte(text), headers)
	if err != nil {
		return 0, "", err
	}
	return resp.StatusCode, string(data), nil
}
