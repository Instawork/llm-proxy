package live

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

type AdminClient struct {
	*HTTPClient
}

func NewAdminClient(base string, timeout time.Duration) (*AdminClient, error) {
	c, err := NewHTTPClient(base, timeout)
	if err != nil {
		return nil, err
	}
	return &AdminClient{HTTPClient: c}, nil
}

type KeyRecord struct {
	Key            string `json:"key"`
	Provider       string `json:"provider"`
	Description    string `json:"description"`
	Enabled        bool   `json:"enabled"`
	RedactPII      *bool  `json:"redact_pii"`
	RateLimitRPM   int    `json:"rate_limit_rpm"`
	DailyCostLimit int64  `json:"daily_cost_limit"`
}

type createKeyRequest struct {
	Provider       string            `json:"provider"`
	ActualKey      string            `json:"actual_key"`
	Description    string            `json:"description,omitempty"`
	DailyCostLimit int64             `json:"daily_cost_limit,omitempty"`
	RedactPII      *bool             `json:"redact_pii,omitempty"`
	RateLimitRPM   int               `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM   int               `json:"rate_limit_tpm,omitempty"`
	RateLimitRPD   int               `json:"rate_limit_rpd,omitempty"`
	RateLimitTPD   int               `json:"rate_limit_tpd,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
}

type updateKeyRequest struct {
	DailyCostLimit   *int64 `json:"daily_cost_limit,omitempty"`
	MonthlyCostLimit *int64 `json:"monthly_cost_limit,omitempty"`
	RateLimitRPM     *int   `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM     *int   `json:"rate_limit_tpm,omitempty"`
	RateLimitRPD     *int   `json:"rate_limit_rpd,omitempty"`
	RateLimitTPD     *int   `json:"rate_limit_tpd,omitempty"`
}

// UpdateKeyRequest is the PATCH body for /admin/api/keys/{key}.
type UpdateKeyRequest = updateKeyRequest

// FuzzCreateKeyRequest builds a key create payload for fuzz runs (dummy upstream key).
func FuzzCreateKeyRequest(description string, rpm, tpm int) createKeyRequest {
	return FuzzCreateKeyRequestWithCost(description, rpm, tpm, 0)
}

// FuzzCreateKeyRequestWithCost builds a key with optional daily_cost_limit cents (0 = unlimited).
func FuzzCreateKeyRequestWithCost(description string, rpm, tpm int, dailyCostLimitCents int64) createKeyRequest {
	return createKeyRequest{
		Provider:       "openai",
		ActualKey:      "fake-upstream-not-used",
		Description:    description,
		RateLimitRPM:   rpm,
		RateLimitTPM:   tpm,
		DailyCostLimit: dailyCostLimitCents,
	}
}

// FuzzCreateKeyRequestWithDaily builds a key with per-day request/token caps
// (rpd/tpd; 0 = unlimited) alongside the per-minute caps.
func FuzzCreateKeyRequestWithDaily(description string, rpm, tpm, rpd, tpd int) createKeyRequest {
	req := FuzzCreateKeyRequest(description, rpm, tpm)
	req.RateLimitRPD = rpd
	req.RateLimitTPD = tpd
	return req
}

// FuzzCreateKeyRequestWithPII builds a key with per-key PII redaction override.
func FuzzCreateKeyRequestWithPII(description string, rpm, tpm int, redactPII bool) createKeyRequest {
	req := FuzzCreateKeyRequest(description, rpm, tpm)
	req.RedactPII = &redactPII
	return req
}

func (a *AdminClient) Me(ctx context.Context) (map[string]any, error) {
	if err := a.DevLogin(ctx); err != nil {
		return nil, err
	}
	resp, data, err := a.Do(ctx, http.MethodGet, "/admin/api/me", nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /admin/api/me status %d: %s", resp.StatusCode, truncate(string(data), 200))
	}
	var out map[string]any
	return out, jsonDecode(data, &out)
}

func (a *AdminClient) Config(ctx context.Context) (map[string]any, error) {
	if err := a.DevLogin(ctx); err != nil {
		return nil, err
	}
	resp, data, err := a.Do(ctx, http.MethodGet, "/admin/api/config", nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /admin/api/config status %d", resp.StatusCode)
	}
	var out map[string]any
	return out, jsonDecode(data, &out)
}

func (a *AdminClient) RateLimits(ctx context.Context) (map[string]any, error) {
	if err := a.DevLogin(ctx); err != nil {
		return nil, err
	}
	resp, data, err := a.Do(ctx, http.MethodGet, "/admin/api/rate-limits", nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /admin/api/rate-limits status %d", resp.StatusCode)
	}
	var out map[string]any
	return out, jsonDecode(data, &out)
}

func (a *AdminClient) CircuitActivity(ctx context.Context) (map[string]any, error) {
	if err := a.DevLogin(ctx); err != nil {
		return nil, err
	}
	resp, data, err := a.Do(ctx, http.MethodGet, "/admin/api/circuit-activity", nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /admin/api/circuit-activity status %d", resp.StatusCode)
	}
	var out map[string]any
	return out, jsonDecode(data, &out)
}

func (a *AdminClient) CostConfig(ctx context.Context) (map[string]any, error) {
	if err := a.DevLogin(ctx); err != nil {
		return nil, err
	}
	resp, data, err := a.Do(ctx, http.MethodGet, "/admin/api/cost", nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /admin/api/cost status %d", resp.StatusCode)
	}
	var out map[string]any
	return out, jsonDecode(data, &out)
}

func (a *AdminClient) PII(ctx context.Context) (map[string]any, error) {
	if err := a.DevLogin(ctx); err != nil {
		return nil, err
	}
	resp, data, err := a.Do(ctx, http.MethodGet, "/admin/api/pii", nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET /admin/api/pii status %d", resp.StatusCode)
	}
	var out map[string]any
	return out, jsonDecode(data, &out)
}

func (a *AdminClient) CreateKey(ctx context.Context, req createKeyRequest) (*KeyRecord, error) {
	if err := a.DevLogin(ctx); err != nil {
		return nil, err
	}
	resp, data, err := a.Do(ctx, http.MethodPost, "/admin/api/keys", req, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("POST /admin/api/keys status %d: %s", resp.StatusCode, truncate(string(data), 300))
	}
	var out KeyRecord
	if err := jsonDecode(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *AdminClient) GetKey(ctx context.Context, keyID string) (*KeyRecord, error) {
	if err := a.DevLogin(ctx); err != nil {
		return nil, err
	}
	path := "/admin/api/keys/" + encodeKeyPath(keyID)
	resp, data, err := a.Do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s status %d: %s", path, resp.StatusCode, truncate(string(data), 300))
	}
	var out KeyRecord
	if err := jsonDecode(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *AdminClient) UpdateKey(ctx context.Context, keyID string, req updateKeyRequest) (*KeyRecord, error) {
	if err := a.DevLogin(ctx); err != nil {
		return nil, err
	}
	path := "/admin/api/keys/" + encodeKeyPath(keyID)
	resp, data, err := a.Do(ctx, http.MethodPatch, path, req, nil)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("PATCH %s status %d: %s", path, resp.StatusCode, truncate(string(data), 300))
	}
	var out KeyRecord
	if err := jsonDecode(data, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (a *AdminClient) DeleteKey(ctx context.Context, keyID string) error {
	if err := a.DevLogin(ctx); err != nil {
		return err
	}
	path := "/admin/api/keys/" + encodeKeyPath(keyID)
	resp, data, err := a.Do(ctx, http.MethodDelete, path, nil, nil)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("DELETE %s status %d: %s", path, resp.StatusCode, truncate(string(data), 200))
	}
	return nil
}

func piiStatsScanned(stats map[string]any) float64 {
	if stats == nil {
		return 0
	}
	switch v := stats["requests_scanned"].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	default:
		return 0
	}
}

func piiStatsWithPII(stats map[string]any) float64 {
	if stats == nil {
		return 0
	}
	switch v := stats["requests_with_pii"].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	default:
		return 0
	}
}

func piiStatsAvailable(stats map[string]any) bool {
	if stats == nil {
		return false
	}
	available, _ := stats["available"].(bool)
	return available
}

func costStatsAvailable(stats map[string]any) bool {
	if stats == nil {
		return false
	}
	available, _ := stats["available"].(bool)
	return available
}

func costStatsSpendToday(stats map[string]any) float64 {
	if stats == nil {
		return 0
	}
	switch v := stats["spend_today_usd"].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	default:
		return 0
	}
}

func piiStatsEntitiesTotal(stats map[string]any) float64 {
	if stats == nil {
		return 0
	}
	switch v := stats["entities_total"].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	default:
		return 0
	}
}

func piiStatsRecentCount(stats map[string]any) int {
	if stats == nil {
		return 0
	}
	recent, ok := stats["recent"].([]any)
	if !ok {
		return 0
	}
	return len(recent)
}

func piiLatestRecentEntry(stats map[string]any) map[string]any {
	if stats == nil {
		return nil
	}
	recent, ok := stats["recent"].([]any)
	if !ok || len(recent) == 0 {
		return nil
	}
	entry, ok := recent[0].(map[string]any)
	if !ok {
		return nil
	}
	return entry
}

func piiLatestRecentOutcome(stats map[string]any) string {
	entry := piiLatestRecentEntry(stats)
	if entry == nil {
		return ""
	}
	outcome, _ := entry["outcome"].(string)
	return outcome
}

func piiLatestRecentEntityTotal(stats map[string]any) float64 {
	entry := piiLatestRecentEntry(stats)
	if entry == nil {
		return 0
	}
	switch v := entry["entity_total"].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	default:
		return 0
	}
}

// Exported helpers for fuzz/integration callers reading /admin/api/pii stats.

func PIIStatsScanned(stats map[string]any) float64       { return piiStatsScanned(stats) }
func PIIStatsWithPII(stats map[string]any) float64       { return piiStatsWithPII(stats) }
func PIIStatsAvailable(stats map[string]any) bool        { return piiStatsAvailable(stats) }
func PIIStatsRecentCount(stats map[string]any) int       { return piiStatsRecentCount(stats) }
func PIILatestRecentOutcome(stats map[string]any) string { return piiLatestRecentOutcome(stats) }
func PIILatestRecentEntityTotal(stats map[string]any) float64 {
	return piiLatestRecentEntityTotal(stats)
}

func elapsed(start time.Time) string {
	return time.Since(start).Round(time.Millisecond).String()
}

// CostStatsAvailable is exported for fuzz/integration helpers.
func CostStatsAvailable(stats map[string]any) bool { return costStatsAvailable(stats) }

// CostStatsSpendToday is exported for fuzz/integration helpers.
func CostStatsSpendToday(stats map[string]any) float64 { return costStatsSpendToday(stats) }
