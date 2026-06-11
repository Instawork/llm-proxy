package main

import (
	"context"
	"fmt"
	"os"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

func main() {
	baseURL := os.Getenv("PROXY_BASE_URL")
	key := os.Getenv("PROXY_API_KEY")
	if baseURL == "" || key == "" {
		fmt.Fprintln(os.Stderr, "PROXY_BASE_URL and PROXY_API_KEY required")
		os.Exit(1)
	}

	client := openai.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(key),
	)
	resp, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model: openai.ChatModelGPT4o,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Hello from the proxy!"),
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(resp.Choices[0].Message.Content)
}
