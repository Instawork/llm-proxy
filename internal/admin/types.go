package admin

import (
	"encoding/json"
	"time"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/ratelimit"
)

const maskedActualKey = "***HIDDEN***"

// UserResponse is the authenticated admin user.
type UserResponse struct {
	Email                           string `json:"email"`
	Name                            string `json:"name,omitempty"`
	Picture                         string `json:"picture,omitempty"`
	CanBypassPIIOffNonBedrockPolicy bool   `json:"can_bypass_pii_off_non_bedrock_policy"`
}

// KeyResponse is a safe JSON view of an API key record.
type KeyResponse struct {
	Key            string            `json:"key"`
	Provider       string            `json:"provider"`
	Description    string            `json:"description,omitempty"`
	DailyCostLimit int64             `json:"daily_cost_limit"`
	Enabled        bool              `json:"enabled"`
	Tags           map[string]string `json:"tags,omitempty"`
	RedactPII      *bool             `json:"redact_pii,omitempty"`
	RateLimitRPM   int               `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM   int               `json:"rate_limit_tpm,omitempty"`
	RateLimitRPD   int               `json:"rate_limit_rpd,omitempty"`
	RateLimitTPD   int               `json:"rate_limit_tpd,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
	ExpiresAt      *time.Time        `json:"expires_at,omitempty"`
	ActualKey      string            `json:"actual_key,omitempty"`
	Provisioned    bool              `json:"provisioned,omitempty"`
}

// CreateKeyRequest creates a new proxy API key.
type CreateKeyRequest struct {
	Provider       string            `json:"provider"`
	ActualKey      string            `json:"actual_key,omitempty"`
	AutoProvision  bool              `json:"auto_provision,omitempty"`
	Description    string            `json:"description,omitempty"`
	DailyCostLimit int64             `json:"daily_cost_limit"`
	Tags           map[string]string `json:"tags,omitempty"`
	RedactPII      *bool             `json:"redact_pii,omitempty"`
	RateLimitRPM   int               `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM   int               `json:"rate_limit_tpm,omitempty"`
	RateLimitRPD   int               `json:"rate_limit_rpd,omitempty"`
	RateLimitTPD   int               `json:"rate_limit_tpd,omitempty"`
}

// UpdateKeyRequest patches mutable key fields.
type UpdateKeyRequest struct {
	Enabled        *bool             `json:"enabled,omitempty"`
	Description    *string           `json:"description,omitempty"`
	DailyCostLimit *int64            `json:"daily_cost_limit,omitempty"`
	Tags           map[string]string `json:"tags,omitempty"`
	RedactPII      OptionalBool      `json:"redact_pii,omitempty"`
	RateLimitRPM   *int              `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM   *int              `json:"rate_limit_tpm,omitempty"`
	RateLimitRPD   *int              `json:"rate_limit_rpd,omitempty"`
	RateLimitTPD   *int              `json:"rate_limit_tpd,omitempty"`
}

// OptionalBool distinguishes omitted, null (inherit), and explicit bool values.
type OptionalBool struct {
	Defined bool
	Value   *bool
}

// UnmarshalJSON implements tri-state bool decoding for PATCH payloads.
func (o *OptionalBool) UnmarshalJSON(data []byte) error {
	o.Defined = true
	if string(data) == "null" {
		o.Value = nil
		return nil
	}
	var b bool
	if err := json.Unmarshal(data, &b); err != nil {
		return err
	}
	o.Value = &b
	return nil
}

// ConfigResponse summarizes feature flags for the dashboard.
type ConfigResponse struct {
	Enabled        bool                        `json:"enabled"`
	Features       map[string]interface{}      `json:"features"`
	Providers      map[string]ProviderSummary  `json:"providers"`
	AdminDashboard config.AdminDashboardConfig `json:"admin_dashboard"`
}

// ProviderSummary is a compact provider block for the config endpoint.
type ProviderSummary struct {
	Enabled    bool `json:"enabled"`
	ModelCount int  `json:"model_count"`
}

// RateLimitsResponse returns configured limits and optional live counters.
type RateLimitsResponse struct {
	Enabled   bool                      `json:"enabled"`
	Backend   string                    `json:"backend,omitempty"`
	Limits    config.LimitsConfig       `json:"limits,omitempty"`
	Overrides config.RateLimitOverrides `json:"overrides,omitempty"`
	Snapshot  *ratelimit.LimitsSnapshot `json:"snapshot,omitempty"`
	Stats     map[string]interface{}    `json:"stats,omitempty"`
}

// ProvisioningResponse summarizes auto-provision availability for the admin UI.
type ProvisioningResponse struct {
	Enabled   bool                            `json:"enabled"`
	Providers map[string]ProvisioningProvider `json:"providers,omitempty"`
}

// ProvisioningProvider describes per-provider auto-provision status.
type ProvisioningProvider struct {
	AutoProvision bool `json:"auto_provision"`
	PoolAvailable int  `json:"pool_available,omitempty"`
}

func keyToResponse(k *apikeys.APIKey, includeActualKey bool) KeyResponse {
	resp := KeyResponse{
		Key:            k.PK,
		Provider:       k.Provider,
		Description:    k.Description,
		DailyCostLimit: k.DailyCostLimit,
		Enabled:        k.Enabled,
		Tags:           k.Tags,
		RedactPII:      k.RedactPII,
		RateLimitRPM:   k.RateLimitRPM,
		RateLimitTPM:   k.RateLimitTPM,
		RateLimitRPD:   k.RateLimitRPD,
		RateLimitTPD:   k.RateLimitTPD,
		CreatedAt:      k.CreatedAt,
		UpdatedAt:      k.UpdatedAt,
		ExpiresAt:      k.ExpiresAt,
	}
	if includeActualKey {
		resp.ActualKey = maskedActualKey
	}
	resp.Provisioned = k.Provisioned
	return resp
}
