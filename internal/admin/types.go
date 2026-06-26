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
	Email                           string                `json:"email"`
	Name                            string                `json:"name,omitempty"`
	Picture                         string                `json:"picture,omitempty"`
	Role                            string                `json:"role"`
	CanBypassPIIOffNonBedrockPolicy bool                  `json:"can_bypass_pii_off_non_bedrock_policy"`
	EditorLimits                    *EditorLimitsResponse `json:"editor_limits,omitempty"`
	ViewerLimits                    *ViewerLimitsResponse `json:"viewer_limits,omitempty"`
}

// EditorLimitsResponse exposes editor caps to the UI.
type EditorLimitsResponse struct {
	MaxDailyCostLimitCents int64 `json:"max_daily_cost_limit_cents"`
}

// ViewerLimitsResponse exposes viewer personal-key caps to the UI.
type ViewerLimitsResponse struct {
	PersonalMonthlyCostLimitCents int64 `json:"personal_monthly_cost_limit_cents"`
}

// AdminUserRecord is a user in the admin roster.
type AdminUserRecord struct {
	Email       string    `json:"email"`
	Name        string    `json:"name,omitempty"`
	Picture     string    `json:"picture,omitempty"`
	Role        string    `json:"role"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	LastLoginAt time.Time `json:"last_login_at,omitempty"`
}

// CreateUserRequest pre-provisions a user.
type CreateUserRequest struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

// UpdateUserRoleRequest changes a user's role.
type UpdateUserRoleRequest struct {
	Role string `json:"role"`
}

// KeyRequestResponse is a user-submitted request for an org-wide service key.
type KeyRequestResponse struct {
	ID              string     `json:"id"`
	RequesterEmail  string     `json:"requester_email"`
	Provider        string     `json:"provider"`
	Description     string     `json:"description"`
	DailyCostLimit  int64      `json:"daily_cost_limit,omitempty"`
	Status          string     `json:"status"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	ReviewedBy      string     `json:"reviewed_by,omitempty"`
	ReviewedAt      *time.Time `json:"reviewed_at,omitempty"`
	CreatedKey      string     `json:"created_key,omitempty"`
	RejectionReason string     `json:"rejection_reason,omitempty"`
}

// CreateKeyRequestBody submits a new org key request.
type CreateKeyRequestBody struct {
	Provider       string `json:"provider"`
	Description    string `json:"description"`
	DailyCostLimit int64  `json:"daily_cost_limit,omitempty"`
}

// ReviewKeyRequestBody approves or rejects a pending key request.
type ReviewKeyRequestBody struct {
	Action          string `json:"action"`
	RejectionReason string `json:"rejection_reason,omitempty"`
}

func keyRequestToResponse(req *apikeys.KeyRequest) KeyRequestResponse {
	return KeyRequestResponse{
		ID:              req.ID(),
		RequesterEmail:  req.RequesterEmail,
		Provider:        req.Provider,
		Description:     req.Description,
		DailyCostLimit:  req.DailyCostLimit,
		Status:          req.Status,
		CreatedAt:       req.CreatedAt,
		UpdatedAt:       req.UpdatedAt,
		ReviewedBy:      req.ReviewedBy,
		ReviewedAt:      req.ReviewedAt,
		CreatedKey:      req.CreatedKey,
		RejectionReason: req.RejectionReason,
	}
}

// KeyResponse is a safe JSON view of an API key record.
type KeyResponse struct {
	Key              string            `json:"key"`
	MaskedKeyID      string            `json:"masked_key_id"`
	Provider         string            `json:"provider"`
	Description      string            `json:"description,omitempty"`
	DailyCostLimit   int64             `json:"daily_cost_limit"`
	MonthlyCostLimit int64             `json:"monthly_cost_limit,omitempty"`
	OwnerEmail       string            `json:"owner_email,omitempty"`
	Enabled          bool              `json:"enabled"`
	Tags             map[string]string `json:"tags,omitempty"`
	RedactPII        *bool             `json:"redact_pii,omitempty"`
	RateLimitRPM     int               `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM     int               `json:"rate_limit_tpm,omitempty"`
	RateLimitRPD     int               `json:"rate_limit_rpd,omitempty"`
	RateLimitTPD     int               `json:"rate_limit_tpd,omitempty"`
	CreatedAt        time.Time         `json:"created_at"`
	UpdatedAt        time.Time         `json:"updated_at"`
	ExpiresAt        *time.Time        `json:"expires_at,omitempty"`
	ActualKey        string            `json:"actual_key,omitempty"`
	Provisioned      bool              `json:"provisioned,omitempty"`
	ProxyBase        string            `json:"proxy_base,omitempty"`
	BaseURL          string            `json:"base_url,omitempty"`
	FirstRequestAt   *time.Time        `json:"first_request_at,omitempty"`
}

// CreateKeyRequest creates a new proxy API key.
type CreateKeyRequest struct {
	Provider         string            `json:"provider"`
	ActualKey        string            `json:"actual_key,omitempty"`
	AutoProvision    bool              `json:"auto_provision,omitempty"`
	Personal         bool              `json:"personal,omitempty"`
	Description      string            `json:"description,omitempty"`
	DailyCostLimit   int64             `json:"daily_cost_limit"`
	MonthlyCostLimit int64             `json:"monthly_cost_limit,omitempty"`
	Tags             map[string]string `json:"tags,omitempty"`
	RedactPII        *bool             `json:"redact_pii,omitempty"`
	RateLimitRPM     int               `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM     int               `json:"rate_limit_tpm,omitempty"`
	RateLimitRPD     int               `json:"rate_limit_rpd,omitempty"`
	RateLimitTPD     int               `json:"rate_limit_tpd,omitempty"`
}

// UpdateKeyRequest patches mutable key fields.
type UpdateKeyRequest struct {
	Enabled          *bool             `json:"enabled,omitempty"`
	Description      *string           `json:"description,omitempty"`
	DailyCostLimit   *int64            `json:"daily_cost_limit,omitempty"`
	MonthlyCostLimit *int64            `json:"monthly_cost_limit,omitempty"`
	Tags             map[string]string `json:"tags,omitempty"`
	RedactPII        OptionalBool      `json:"redact_pii,omitempty"`
	RateLimitRPM     *int              `json:"rate_limit_rpm,omitempty"`
	RateLimitTPM     *int              `json:"rate_limit_tpm,omitempty"`
	RateLimitRPD     *int              `json:"rate_limit_rpd,omitempty"`
	RateLimitTPD     *int              `json:"rate_limit_tpd,omitempty"`
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
	AutoProvision bool     `json:"auto_provision"`
	PoolAvailable int      `json:"pool_available,omitempty"`
	DefaultTier   string   `json:"default_tier,omitempty"`
	Tiers         []string `json:"tiers,omitempty"`
}

func keyToResponse(k *apikeys.APIKey, includeActualKey bool) KeyResponse {
	resp := KeyResponse{
		Key:              k.PK,
		MaskedKeyID:      apikeys.MaskKeyID(k.PK),
		Provider:         k.Provider,
		Description:      k.Description,
		DailyCostLimit:   k.DailyCostLimit,
		MonthlyCostLimit: k.MonthlyCostLimit,
		OwnerEmail:       k.OwnerEmail,
		Enabled:          k.Enabled,
		Tags:             k.Tags,
		RedactPII:        k.RedactPII,
		RateLimitRPM:     k.RateLimitRPM,
		RateLimitTPM:     k.RateLimitTPM,
		RateLimitRPD:     k.RateLimitRPD,
		RateLimitTPD:     k.RateLimitTPD,
		CreatedAt:        k.CreatedAt,
		UpdatedAt:        k.UpdatedAt,
		ExpiresAt:        k.ExpiresAt,
		FirstRequestAt:   k.FirstRequestAt,
	}
	if includeActualKey {
		resp.ActualKey = maskedActualKey
	}
	resp.Provisioned = k.Provisioned
	return resp
}

func keyDetailResponse(k *apikeys.APIKey, proxyBase string) KeyResponse {
	resp := keyToResponse(k, true)
	if proxyBase != "" {
		resp.ProxyBase = proxyBase
		resp.BaseURL = proxyBase + providerBasePath(k.Provider)
	}
	return resp
}
