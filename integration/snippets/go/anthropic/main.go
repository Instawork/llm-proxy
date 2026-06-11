package main

import (
	"context"
	"fmt"
	"os"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

func main() {
	baseURL := os.Getenv("PROXY_BASE_URL")
	key := os.Getenv("PROXY_API_KEY")
	if baseURL == "" || key == "" {
		fmt.Fprintln(os.Stderr, "PROXY_BASE_URL and PROXY_API_KEY required")
		os.Exit(1)
	}

	client := anthropic.NewClient(
		option.WithBaseURL(baseURL),
		option.WithAPIKey(key),
	)
	msg, err := client.Messages.New(context.Background(), anthropic.MessageNewParams{
		Model:     anthropic.ModelClaudeSonnet4_5,
		MaxTokens: 512,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Hello from the proxy!")),
		},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(msg.Content)
}
