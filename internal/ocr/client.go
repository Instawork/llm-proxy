package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"time"
)

type Client struct {
	baseURL string
	client  *http.Client
}

type extractTextResponse struct {
	Text string `json:"text"`
}

func New(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{
		baseURL: baseURL,
		client:  &http.Client{Timeout: timeout},
	}
}

func (c *Client) ExtractText(ctx context.Context, img []byte, filename string) (string, error) {
	if len(img) == 0 {
		return "", fmt.Errorf("ocr: empty image")
	}
	if filename == "" {
		filename = "image.bin"
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("image", filename)
	if err != nil {
		return "", fmt.Errorf("ocr: create form file: %w", err)
	}
	if _, err := part.Write(img); err != nil {
		return "", fmt.Errorf("ocr: write image: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("ocr: close multipart writer: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/extract-text",
		&body,
	)
	if err != nil {
		return "", fmt.Errorf("ocr: build request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("ocr: extract-text call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("ocr: extract-text returned %d: %s", resp.StatusCode, string(excerpt))
	}

	var out extractTextResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("ocr: decode response: %w", err)
	}
	return out.Text, nil
}
