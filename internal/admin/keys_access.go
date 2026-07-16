package admin

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/Instawork/llm-proxy/internal/admin/permissions"
	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
)

var viewerPersonalProviders = map[string]struct{}{
	"openai":    {},
	"anthropic": {},
	"gemini":    {},
	"bedrock":   {},
}

func isViewerPersonalProvider(provider string) bool {
	_, ok := viewerPersonalProviders[strings.ToLower(strings.TrimSpace(provider))]
	return ok
}

func canAccessKey(role adminusers.Role, userEmail string, key *apikeys.APIKey) bool {
	return permissions.CanAccessKey(role, userEmail, key)
}

func filterKeysForUser(role adminusers.Role, email string, keys []*apikeys.APIKey) []*apikeys.APIKey {
	if role != adminusers.RoleViewer {
		return keys
	}
	filtered := make([]*apikeys.APIKey, 0, len(keys))
	for _, k := range keys {
		if canAccessKey(role, email, k) {
			filtered = append(filtered, k)
		}
	}
	return filtered
}

func viewerPersonalMonthlyLimitCents(h *handler) int64 {
	return h.viewerPersonalMonthlyLimit()
}

func requiresProvisionedKey(role adminusers.Role) bool {
	return permissions.RequiresAutoProvision(role)
}

func validateProvisionedKeyOnly(role adminusers.Role, req *CreateKeyRequest) error {
	if !requiresProvisionedKey(role) {
		return nil
	}
	// Bedrock personal/org keys have no upstream provider secret — outbound
	// auth is AWS SigV4 on the proxy.
	if apikeys.ProviderUsesAWSAuth(req.Provider) {
		return nil
	}
	if strings.TrimSpace(req.ActualKey) != "" {
		return fmt.Errorf("manual provider keys are not allowed for your role; use auto_provision")
	}
	if !req.AutoProvision {
		return fmt.Errorf("auto_provision is required for your role")
	}
	return nil
}

func (h *handler) validatePersonalCreate(r *http.Request, req *CreateKeyRequest, userEmail string) error {
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
	if !isViewerPersonalProvider(req.Provider) {
		return fmt.Errorf("personal keys may only be created for openai, anthropic, gemini, or bedrock")
	}
	if err := validateProvisionedKeyOnly(adminusers.RoleViewer, req); err != nil {
		return err
	}
	if req.DailyCostLimit != 0 {
		return fmt.Errorf("daily_cost_limit cannot be set on personal keys")
	}
	if req.MonthlyCostLimit != 0 {
		return fmt.Errorf("monthly_cost_limit cannot be set on personal keys")
	}
	if req.Tags != nil {
		return fmt.Errorf("tags cannot be set on personal keys")
	}
	if req.RedactPII != nil {
		return fmt.Errorf("redact_pii cannot be set on personal keys")
	}
	if req.RateLimitRPM != 0 || req.RateLimitTPM != 0 || req.RateLimitRPD != 0 || req.RateLimitTPD != 0 {
		return fmt.Errorf("rate limits cannot be set on personal keys")
	}

	existing, err := h.deps.APIKeyStore.GetOwnerKeyByProvider(r.Context(), userEmail, req.Provider)
	if err != nil {
		return fmt.Errorf("failed to check existing personal key: %w", err)
	}
	if existing != nil {
		return apikeys.ErrOwnerKeyExists
	}
	return nil
}
