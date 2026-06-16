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

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// Gemini provisions GCP API keys restricted to the Generative Language API.
type Gemini struct {
	projectID string
	tokenSrc  oauth2.TokenSource
	client    *http.Client
	baseURL   string
}

// NewGemini returns a Gemini provisioner. baseURL is for tests only.
func NewGemini(projectID string, credsJSON []byte, baseURL string) (*Gemini, error) {
	ctx := context.Background()
	creds, err := google.CredentialsFromJSON(ctx, credsJSON, "https://www.googleapis.com/auth/cloud-platform")
	if err != nil {
		return nil, err
	}
	if baseURL == "" {
		baseURL = "https://apikeys.googleapis.com/v2"
	}
	return &Gemini{
		projectID: projectID,
		tokenSrc:  creds.TokenSource,
		client:    &http.Client{Timeout: 90 * time.Second},
		baseURL:   strings.TrimRight(baseURL, "/"),
	}, nil
}

func (g *Gemini) Provision(ctx context.Context, name string) (Result, error) {
	createBody := map[string]interface{}{
		"displayName": SanitizeName(name),
		"restrictions": map[string]interface{}{
			"apiTargets": []map[string]string{
				{"service": "generativelanguage.googleapis.com"},
			},
		},
	}
	payload, _ := json.Marshal(createBody)
	path := fmt.Sprintf("/projects/%s/locations/global/keys", g.projectID)

	opName, err := g.postJSON(ctx, path, payload)
	if err != nil {
		return Result{}, err
	}

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		keyName, keyString, done, err := g.pollOperation(ctx, opName)
		if err != nil {
			return Result{}, err
		}
		if done {
			if keyString == "" {
				return Result{}, fmt.Errorf("gemini provision: empty keyString")
			}
			return Result{
				ActualKey:    keyString,
				UpstreamID:   keyName,
				UpstreamKind: UpstreamKindGCPAPIKey,
			}, nil
		}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return Result{}, fmt.Errorf("gemini provision: operation timed out")
}

func (g *Gemini) Revoke(ctx context.Context, upstreamID, upstreamKind string) error {
	if upstreamKind != UpstreamKindGCPAPIKey || upstreamID == "" {
		return nil
	}
	path := "/" + strings.TrimPrefix(upstreamID, "/")
	req, err := g.newRequest(ctx, http.MethodDelete, path, nil)
	if err != nil {
		return err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("gemini revoke: status %d: %s", resp.StatusCode, truncate(raw, 300))
	}
	return nil
}

func (g *Gemini) PoolStatus(context.Context) (int, bool) { return 0, false }

func (g *Gemini) postJSON(ctx context.Context, path string, body []byte) (string, error) {
	req, err := g.newRequest(ctx, http.MethodPost, path, body)
	if err != nil {
		return "", err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("gemini create: status %d: %s", resp.StatusCode, truncate(raw, 300))
	}
	var op struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &op); err != nil {
		return "", err
	}
	if op.Name == "" {
		return "", fmt.Errorf("gemini create: missing operation name")
	}
	return op.Name, nil
}

func (g *Gemini) pollOperation(ctx context.Context, opName string) (keyName, keyString string, done bool, err error) {
	path := "/" + strings.TrimPrefix(opName, "/")
	req, err := g.newRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", "", false, err
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return "", "", false, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", false, fmt.Errorf("gemini operation: status %d: %s", resp.StatusCode, truncate(raw, 300))
	}
	var op struct {
		Done     bool `json:"done"`
		Response struct {
			Name      string `json:"name"`
			KeyString string `json:"keyString"`
		} `json:"response"`
		Error interface{} `json:"error"`
	}
	if err := json.Unmarshal(raw, &op); err != nil {
		return "", "", false, err
	}
	if op.Error != nil {
		return "", "", false, fmt.Errorf("gemini operation failed: %v", op.Error)
	}
	if !op.Done {
		return "", "", false, nil
	}
	return op.Response.Name, op.Response.KeyString, true, nil
}

func (g *Gemini) newRequest(ctx context.Context, method, path string, body []byte) (*http.Request, error) {
	tok, err := g.tokenSrc.Token()
	if err != nil {
		return nil, err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.baseURL+path, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}
