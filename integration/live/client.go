package live

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"
)

type HTTPClient struct {
	Base   string
	HTTP   *http.Client
	jar    *cookiejar.Jar
	logged bool
}

func NewHTTPClient(base string, timeout time.Duration) (*HTTPClient, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &HTTPClient{
		Base: strings.TrimRight(base, "/"),
		HTTP: &http.Client{Timeout: timeout, Jar: jar},
		jar:  jar,
	}, nil
}

func (c *HTTPClient) Do(ctx context.Context, method, path string, body any, headers map[string]string) (*http.Response, []byte, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rdr)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp, nil, err
	}
	return resp, data, nil
}

func (c *HTTPClient) DoRaw(ctx context.Context, method, path string, body []byte, headers map[string]string) (*http.Response, []byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rdr)
	if err != nil {
		return nil, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return resp, nil, err
	}
	return resp, data, nil
}

func (c *HTTPClient) DevLogin(ctx context.Context) error {
	if c.logged {
		return nil
	}
	resp, data, err := c.Do(ctx, http.MethodPost, "/admin/auth/dev-login", map[string]string{}, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("dev-login returned 404 (enable features.admin_dashboard.dev_bypass_login in dev.yml)")
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("dev-login status %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	c.logged = true
	return nil
}

func jsonDecode[T any](data []byte, out *T) error {
	if len(data) == 0 {
		return fmt.Errorf("empty response body")
	}
	return json.Unmarshal(data, out)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func encodeKeyPath(keyID string) string {
	return url.PathEscape(keyID)
}
