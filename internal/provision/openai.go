package provision

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

const openAIBaseURL = "https://api.openai.com/v1"

// OpenAI provisions project service accounts via the Admin API.
type OpenAI struct {
	adminKey  string
	projectID string
	baseURL   string
	client    *http.Client
}

// NewOpenAI returns an OpenAI provisioner. baseURL is for tests only.
func NewOpenAI(adminKey, projectID, baseURL string) *OpenAI {
	if baseURL == "" {
		baseURL = openAIBaseURL
	}
	return &OpenAI{
		adminKey:  adminKey,
		projectID: projectID,
		baseURL:   strings.TrimRight(baseURL, "/"),
		client:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (o *OpenAI) Provision(ctx context.Context, name string) (Result, error) {
	path := fmt.Sprintf("/organization/projects/%s/service_accounts", o.projectID)
	body, _ := json.Marshal(map[string]string{"name": SanitizeName(name)})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+o.adminKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Result{}, fmt.Errorf("openai provision: status %d: %s", resp.StatusCode, truncate(raw, 300))
	}

	var parsed struct {
		ID     string `json:"id"`
		APIKey struct {
			Value string `json:"value"`
			ID    string `json:"id"`
		} `json:"api_key"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Result{}, fmt.Errorf("openai provision: decode: %w", err)
	}
	if parsed.APIKey.Value == "" {
		return Result{}, fmt.Errorf("openai provision: empty api_key in response")
	}
	return Result{
		ActualKey:    parsed.APIKey.Value,
		UpstreamID:   parsed.ID,
		UpstreamKind: UpstreamKindOpenAIServiceAccount,
		ProviderMeta: map[string]string{"openai_key_id": parsed.APIKey.ID},
	}, nil
}

func (o *OpenAI) Revoke(ctx context.Context, upstreamID, upstreamKind string) error {
	if upstreamKind != UpstreamKindOpenAIServiceAccount || upstreamID == "" {
		return nil
	}
	path := fmt.Sprintf("/organization/projects/%s/service_accounts/%s", o.projectID, upstreamID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, o.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+o.adminKey)

	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("openai revoke: status %d: %s", resp.StatusCode, truncate(raw, 300))
	}
	return nil
}

func (o *OpenAI) PoolStatus(context.Context) (int, bool) { return 0, false }

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n])
}
