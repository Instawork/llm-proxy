package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"time"
)

const (
	defaultOCRTimeout     = 30 * time.Second
	defaultMaxRetries     = 2
	defaultRetryBaseDelay = 150 * time.Millisecond
)

type Client struct {
	baseURL        string
	timeout        time.Duration
	maxRetries     int
	retryBaseDelay time.Duration
	client         *http.Client
}

type extractTextResponse struct {
	Text string `json:"text"`
}

func newHTTPTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   5 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

func New(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = defaultOCRTimeout
	}
	return &Client{
		baseURL:        baseURL,
		timeout:        timeout,
		maxRetries:     defaultMaxRetries,
		retryBaseDelay: defaultRetryBaseDelay,
		client:         &http.Client{Transport: newHTTPTransport()},
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

	payload := body.Bytes()
	contentType := writer.FormDataContentType()

	reqCtx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			delay := c.retryBaseDelay << (attempt - 1)
			if err := sleepWithContext(reqCtx, delay); err != nil {
				return "", fmt.Errorf("%w; retry backoff ended: %w", lastErr, err)
			}
		}

		text, retryable, err := c.doExtractOnce(reqCtx, payload, contentType)
		if err == nil {
			return text, nil
		}
		lastErr = err
		if !retryable {
			return "", err
		}
		if ctxErr := reqCtx.Err(); ctxErr != nil {
			return "", fmt.Errorf("%w; %w", err, ctxErr)
		}
	}
	return "", lastErr
}

func (c *Client) doExtractOnce(ctx context.Context, payload []byte, contentType string) (string, bool, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.baseURL+"/extract-text",
		bytes.NewReader(payload),
	)
	if err != nil {
		return "", false, fmt.Errorf("ocr: build request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)

	resp, err := c.client.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("ocr: extract-text call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		excerpt, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		retryable := isRetryableOCRStatus(resp.StatusCode)
		return "", retryable, fmt.Errorf("ocr: extract-text returned %d: %s", resp.StatusCode, string(excerpt))
	}

	var out extractTextResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", false, fmt.Errorf("ocr: decode response: %w", err)
	}
	return out.Text, false, nil
}

func isRetryableOCRStatus(code int) bool {
	return code == http.StatusBadGateway ||
		code == http.StatusServiceUnavailable
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
