package main

import (
	"context"
	"fmt"
	"os"

	"google.golang.org/genai"
)

func main() {
	baseURL := os.Getenv("PROXY_BASE_URL")
	key := os.Getenv("PROXY_API_KEY")
	if baseURL == "" || key == "" {
		fmt.Fprintln(os.Stderr, "PROXY_BASE_URL and PROXY_API_KEY required")
		os.Exit(1)
	}

	ctx := context.Background()
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		APIKey:      key,
		HTTPOptions: genai.HTTPOptions{BaseURL: baseURL},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	resp, err := client.Models.GenerateContent(ctx, "gemini-2.5-flash",
		genai.Text("Hello from the proxy!"), nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(resp.Text())
}
