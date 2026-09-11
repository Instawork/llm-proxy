package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultSendGridBaseURL = "https://api.sendgrid.com"

// SendGrid sends email via the SendGrid v3 Mail Send API.
type SendGrid struct {
	apiKey      string
	fromAddress string
	fromName    string
	baseURL     string
	client      *http.Client
}

// NewSendGrid builds a SendGrid mailer. baseURL overrides the API host for
// tests; pass "" in production to use the real SendGrid endpoint.
func NewSendGrid(apiKey, fromAddress, fromName, baseURL string) *SendGrid {
	if baseURL == "" {
		baseURL = defaultSendGridBaseURL
	}
	return &SendGrid{
		apiKey:      apiKey,
		fromAddress: fromAddress,
		fromName:    fromName,
		baseURL:     strings.TrimRight(baseURL, "/"),
		client:      &http.Client{Timeout: 10 * time.Second},
	}
}

type sendGridEmail struct {
	Email string `json:"email"`
	Name  string `json:"name,omitempty"`
}

type sendGridPersonalization struct {
	To []sendGridEmail `json:"to"`
}

type sendGridContent struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type sendGridRequest struct {
	Personalizations []sendGridPersonalization `json:"personalizations"`
	From             sendGridEmail             `json:"from"`
	Subject          string                    `json:"subject"`
	Content          []sendGridContent         `json:"content"`
}

// Send posts msg to SendGrid's /v3/mail/send endpoint.
func (s *SendGrid) Send(ctx context.Context, msg Message) error {
	if len(msg.To) == 0 {
		return fmt.Errorf("sendgrid: message has no recipients")
	}
	to := make([]sendGridEmail, 0, len(msg.To))
	for _, addr := range msg.To {
		to = append(to, sendGridEmail{Email: addr})
	}
	body := sendGridRequest{
		Personalizations: []sendGridPersonalization{{To: to}},
		From:             sendGridEmail{Email: s.fromAddress, Name: s.fromName},
		Subject:          msg.Subject,
		Content:          []sendGridContent{{Type: "text/plain", Value: msg.Text}},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("sendgrid: encode request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/v3/mail/send", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("sendgrid: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.apiKey)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("sendgrid: send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("sendgrid: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
