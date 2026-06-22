package admin

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/Instawork/llm-proxy/internal/provision"
	"github.com/Instawork/llm-proxy/internal/ratelimit"
	"github.com/gorilla/mux"
)

type handler struct {
	deps *Deps
	auth *authenticator
}

func newHandler(deps Deps, auth *authenticator) *handler {
	return &handler{deps: &deps, auth: auth}
}

func (h *handler) corsMiddleware(next http.Handler) http.Handler {
	origin := ""
	if h.deps.YAMLConfig != nil {
		origin = h.deps.YAMLConfig.Features.AdminDashboard.DevCORSOrigin
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *handler) handleMe(w http.ResponseWriter, r *http.Request) {
	user, err := h.auth.currentUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	writeJSON(w, http.StatusOK, user)
}

func (h *handler) writeAPIKeyStoreUnavailable(w http.ResponseWriter) {
	msg := "api key management disabled"
	if h.deps.YAMLConfig != nil && h.deps.YAMLConfig.Features.APIKeyManagement.Enabled {
		if h.deps.APIKeyStoreError != nil {
			msg = "api key store unavailable: " + h.deps.APIKeyStoreError.Error()
		} else {
			msg = "api key store unavailable (check DynamoDB credentials or local dynamodb service)"
		}
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": msg})
}

func (h *handler) handleListKeys(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}

	user, err := h.auth.currentUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	role, err := adminusers.ParseRole(user.Role)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	provider := r.URL.Query().Get("provider")
	var keys []*apikeys.APIKey
	if role == adminusers.RoleViewer {
		keys, err = h.deps.APIKeyStore.ListKeysByOwner(r.Context(), user.Email, provider)
	} else {
		keys, err = h.deps.APIKeyStore.ListKeys(r.Context(), provider)
		if err == nil {
			keys = filterKeysForUser(role, user.Email, keys)
		}
	}
	if err != nil {
		h.deps.Logger.Error("admin: list keys failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list keys"})
		return
	}

	resp := make([]KeyResponse, 0, len(keys))
	for _, k := range keys {
		resp = append(resp, keyToResponse(k, false))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) globalPIIEnabled() bool {
	if h.deps.YAMLConfig == nil {
		return false
	}
	return h.deps.YAMLConfig.Features.PIIRedact.Enabled
}

func (h *handler) adminBypassPIIBedrockPolicy(r *http.Request) bool {
	user, err := h.auth.currentUser(r)
	if err != nil {
		return false
	}
	return user.CanBypassPIIOffNonBedrockPolicy
}

func (h *handler) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}

	var req CreateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.Provider == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider is required"})
		return
	}

	user, err := h.auth.currentUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	role, err := adminusers.ParseRole(user.Role)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	// Viewers always create personal keys; higher roles may opt in via the
	// "personal" flag so an admin can mint a personal testing key for themselves.
	wantsPersonal := role == adminusers.RoleViewer || req.Personal
	if wantsPersonal {
		if err := h.validatePersonalCreate(r, &req, user.Email); err != nil {
			if errors.Is(err, apikeys.ErrOwnerKeyExists) {
				writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		actualKey := strings.TrimSpace(req.ActualKey)
		var meta apikeys.KeyCreateMeta
		if req.AutoProvision {
			if h.deps.KeyProvisioner == nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "key provisioning is not configured"})
				return
			}
			if _, ok := h.deps.KeyProvisioner.ForProvider(req.Provider); !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{
					"error": "auto_provision is not available for provider " + req.Provider,
				})
				return
			}
			provName := "llm-proxy:" + provision.SanitizeName(req.Description)
			res, provErr := h.deps.KeyProvisioner.Provision(r.Context(), req.Provider, provision.ProvisionRequest{
				Name: provName,
			})
			if provErr != nil {
				if errors.Is(provErr, provision.ErrEmptyPool) {
					writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": provErr.Error()})
					return
				}
				h.deps.Logger.Error("admin: provision personal key failed", "provider", req.Provider, "error", provErr)
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "failed to provision upstream key"})
				return
			}
			actualKey = res.ActualKey
			meta = apikeys.KeyCreateMeta{
				Provisioned:   true,
				UpstreamKeyID: res.UpstreamID,
				UpstreamKind:  res.UpstreamKind,
			}
		} else if actualKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "actual_key is required unless auto_provision is true"})
			return
		}
		key, err := h.deps.APIKeyStore.CreatePersonalKey(
			r.Context(),
			user.Email,
			req.Provider,
			actualKey,
			req.Description,
			viewerPersonalMonthlyLimitCents(h),
			meta,
		)
		if err != nil {
			if errors.Is(err, apikeys.ErrOwnerKeyExists) {
				writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
				return
			}
			h.deps.Logger.Error("admin: create personal key failed", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create key"})
			return
		}
		writeJSON(w, http.StatusCreated, keyToResponse(key, false))
		return
	}

	if err := validateProvisionedKeyOnly(role, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	if err := apikeys.ValidatePIIOffBedrockPolicy(
		h.globalPIIEnabled(),
		req.Provider,
		req.RedactPII,
		h.adminBypassPIIBedrockPolicy(r),
	); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	actualKey := strings.TrimSpace(req.ActualKey)
	var meta apikeys.KeyCreateMeta

	if req.AutoProvision {
		if h.deps.KeyProvisioner == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "key provisioning is not configured"})
			return
		}
		if _, ok := h.deps.KeyProvisioner.ForProvider(req.Provider); !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{
				"error": "auto_provision is not available for provider " + req.Provider,
			})
			return
		}
		provName := "llm-proxy:" + provision.SanitizeName(req.Description)
		tier := ""
		if req.Tags != nil {
			tier = strings.TrimSpace(req.Tags["tier"])
		}
		res, err := h.deps.KeyProvisioner.Provision(r.Context(), req.Provider, provision.ProvisionRequest{
			Name: provName,
			Tier: tier,
		})
		if err != nil {
			if errors.Is(err, provision.ErrEmptyPool) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
				return
			}
			if errors.Is(err, provision.ErrInvalidTier) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			h.deps.Logger.Error("admin: provision upstream key failed", "provider", req.Provider, "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "failed to provision upstream key"})
			return
		}
		actualKey = res.ActualKey
		meta = apikeys.KeyCreateMeta{
			Provisioned:   true,
			UpstreamKeyID: res.UpstreamID,
			UpstreamKind:  res.UpstreamKind,
		}
		if req.Tags == nil {
			req.Tags = map[string]string{}
		}
		if req.Provider == "anthropic" && res.UpstreamKind == provision.UpstreamKindAnthropicTiered && res.UpstreamID != "" {
			req.Tags["tier"] = res.UpstreamID
		}
	} else if actualKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "actual_key is required unless auto_provision is true"})
		return
	}

	if err := h.validateEditorCostLimit(r, req.DailyCostLimit); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	key, err := h.deps.APIKeyStore.CreateKeyWithMeta(
		r.Context(),
		req.Provider,
		actualKey,
		req.Description,
		req.DailyCostLimit,
		req.Tags,
		req.RedactPII,
		meta,
		apikeys.KeyRateLimits{
			RPM: req.RateLimitRPM,
			TPM: req.RateLimitTPM,
			RPD: req.RateLimitRPD,
			TPD: req.RateLimitTPD,
		},
	)
	if err != nil {
		h.deps.Logger.Error("admin: create key failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create key"})
		return
	}

	writeJSON(w, http.StatusCreated, keyToResponse(key, false))
}

func (h *handler) handleGetKey(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}

	keyID := mux.Vars(r)["key"]
	record, err := h.deps.APIKeyStore.GetKeyRecord(r.Context(), keyID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "key not found"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	user, err := h.auth.currentUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	role, err := adminusers.ParseRole(user.Role)
	if err != nil || !canAccessKey(role, user.Email, record) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	writeJSON(w, http.StatusOK, keyToResponse(record, true))
}

func (h *handler) handleUpdateKey(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}

	keyID := mux.Vars(r)["key"]
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid body"})
		return
	}

	var req UpdateKeyRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	user, err := h.auth.currentUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	role, err := adminusers.ParseRole(user.Role)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	existing, err := h.deps.APIKeyStore.GetKeyRecord(r.Context(), keyID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "key not found"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !canAccessKey(role, user.Email, existing) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	if role == adminusers.RoleViewer {
		if req.Enabled != nil || req.DailyCostLimit != nil || req.Tags != nil || req.RedactPII.Defined ||
			req.RateLimitRPM != nil || req.RateLimitTPM != nil || req.RateLimitRPD != nil || req.RateLimitTPD != nil {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "viewers may only update description"})
			return
		}
		if req.Description == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no fields to update"})
			return
		}
	}

	updates := map[string]interface{}{}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.DailyCostLimit != nil {
		if err := h.validateEditorCostLimit(r, *req.DailyCostLimit); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		updates["daily_cost_limit"] = *req.DailyCostLimit
	}
	if req.Tags != nil {
		updates["tags"] = req.Tags
	}
	if req.RedactPII.Defined {
		if req.RedactPII.Value == nil {
			updates["redact_pii"] = nil
		} else {
			updates["redact_pii"] = *req.RedactPII.Value
		}
	}
	if req.RateLimitRPM != nil {
		updates["rate_limit_rpm"] = *req.RateLimitRPM
	}
	if req.RateLimitTPM != nil {
		updates["rate_limit_tpm"] = *req.RateLimitTPM
	}
	if req.RateLimitRPD != nil {
		updates["rate_limit_rpd"] = *req.RateLimitRPD
	}
	if req.RateLimitTPD != nil {
		updates["rate_limit_tpd"] = *req.RateLimitTPD
	}

	if len(updates) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no fields to update"})
		return
	}

	if req.RedactPII.Defined {
		if err := apikeys.ValidatePIIOffBedrockPolicy(
			h.globalPIIEnabled(),
			existing.Provider,
			req.RedactPII.Value,
			h.adminBypassPIIBedrockPolicy(r),
		); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}

	if err := h.deps.APIKeyStore.UpdateKey(r.Context(), keyID, updates); err != nil {
		if strings.Contains(err.Error(), "ConditionalCheckFailed") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "key not found"})
			return
		}
		h.deps.Logger.Error("admin: update key failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update key"})
		return
	}

	record, err := h.deps.APIKeyStore.GetKeyRecord(r.Context(), keyID)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
		return
	}
	writeJSON(w, http.StatusOK, keyToResponse(record, true))
}

func (h *handler) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}

	keyID := mux.Vars(r)["key"]
	record, err := h.deps.APIKeyStore.GetKeyRecord(r.Context(), keyID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "key not found"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	user, err := h.auth.currentUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	role, err := adminusers.ParseRole(user.Role)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	if role == adminusers.RoleEditor {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "editors cannot delete keys"})
		return
	}
	if !canAccessKey(role, user.Email, record) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	if record.Provisioned && h.deps.KeyProvisioner != nil {
		if revokeErr := h.deps.KeyProvisioner.Revoke(
			r.Context(),
			record.Provider,
			record.UpstreamKeyID,
			record.UpstreamKind,
		); revokeErr != nil {
			h.deps.Logger.Warn(
				"admin: upstream revoke failed",
				"key", keyID,
				"provider", record.Provider,
				"upstream_id", record.UpstreamKeyID,
				"error", revokeErr,
			)
		}
	}

	if err := h.deps.APIKeyStore.DeleteKey(r.Context(), keyID); err != nil {
		if strings.Contains(err.Error(), "ConditionalCheckFailed") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "key not found"})
			return
		}
		h.deps.Logger.Error("admin: delete key failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to delete key"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) handleProvisioning(w http.ResponseWriter, r *http.Request) {
	if h.deps.KeyProvisioner == nil {
		writeJSON(w, http.StatusOK, ProvisioningResponse{Enabled: false})
		return
	}
	raw := h.deps.KeyProvisioner.Status(r.Context())
	resp := ProvisioningResponse{Enabled: false}
	if enabled, ok := raw["enabled"].(bool); ok {
		resp.Enabled = enabled
	}
	if providers, ok := raw["providers"].(map[string]interface{}); ok {
		resp.Providers = make(map[string]ProvisioningProvider, len(providers))
		for name, entry := range providers {
			m, _ := entry.(map[string]interface{})
			p := ProvisioningProvider{}
			if v, ok := m["auto_provision"].(bool); ok {
				p.AutoProvision = v
			}
			if v, ok := m["pool_available"].(int); ok {
				p.PoolAvailable = v
			}
			if v, ok := m["default_tier"].(string); ok {
				p.DefaultTier = v
			}
			if tiers, ok := m["tiers"].([]string); ok {
				p.Tiers = tiers
			}
			resp.Providers[name] = p
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) handleConfig(w http.ResponseWriter, r *http.Request) {
	cfg := h.deps.YAMLConfig
	if cfg == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "config unavailable"})
		return
	}

	providers := make(map[string]ProviderSummary, len(cfg.Providers))
	for name, p := range cfg.Providers {
		providers[name] = ProviderSummary{
			Enabled:    p.Enabled,
			ModelCount: len(p.Models),
		}
	}

	resp := ConfigResponse{
		Enabled: cfg.Enabled,
		Features: map[string]interface{}{
			"cost_tracking":      cfg.Features.CostTracking.Enabled,
			"api_key_management": cfg.Features.APIKeyManagement.Enabled,
			"rate_limiting":      cfg.Features.RateLimiting.Enabled,
			"circuit_breaker":    cfg.Features.CircuitBreaker.Enabled,
			"pii_redact":         cfg.Features.PIIRedact.Enabled,
			"redact_api":         cfg.Features.RedactAPI.Enabled,
			"admin_dashboard":    cfg.Features.AdminDashboard.Enabled,
		},
		Providers:      providers,
		AdminDashboard: cfg.Features.AdminDashboard,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) handlePII(w http.ResponseWriter, r *http.Request) {
	cfg := h.deps.YAMLConfig
	enabled := false
	allowPerKey := false
	failMode := "open"
	if cfg != nil {
		enabled = cfg.Features.PIIRedact.Enabled
		allowPerKey = cfg.Features.PIIRedact.AllowPerKeyOverride
		if cfg.Features.PIIRedact.FailMode != "" {
			failMode = cfg.Features.PIIRedact.FailMode
		}
	}

	resp := map[string]interface{}{
		"enabled":                enabled,
		"allow_per_key_override": allowPerKey,
		"fail_mode":              failMode,
	}

	if h.deps.PIISummary != nil {
		resp["stats"] = h.deps.PIISummary()
	} else {
		resp["stats"] = map[string]interface{}{"available": false}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) handleUsage(w http.ResponseWriter, r *http.Request) {
	cfg := h.deps.YAMLConfig
	enabled := false
	if cfg != nil {
		enabled = cfg.Features.CostTracking.Enabled
	}

	resp := map[string]interface{}{
		"enabled": enabled,
		"source":  "cost_tracking",
	}

	if h.deps.UsageSummary != nil {
		resp["stats"] = h.deps.UsageSummary()
	} else {
		resp["stats"] = map[string]interface{}{"available": false}
	}

	writeJSON(w, http.StatusOK, resp)
}

// publicBaseURL returns the externally-visible base URL (scheme + host, no
// trailing slash) used to build absolute share/SDK links. Resolution order:
//  1. admin_dashboard.public_base_url from YAML config
//  2. the ADMIN_PUBLIC_BASE_URL env var
//  3. http://localhost:{PORT} when dev mode is active (DevCORSOrigin or
//     DevBypassLogin) so Docker-internal hostnames never leak into links
//  4. the inbound request (forwarding headers, then Host)
func (h *handler) publicBaseURL(r *http.Request) string {
	if h.deps.YAMLConfig != nil {
		if configured := h.deps.YAMLConfig.Features.AdminDashboard.PublicBaseURL; configured != "" {
			return strings.TrimRight(configured, "/")
		}
	}
	if env := strings.TrimSpace(os.Getenv("ADMIN_PUBLIC_BASE_URL")); env != "" {
		return strings.TrimRight(env, "/")
	}
	if h.deps.YAMLConfig != nil {
		admin := h.deps.YAMLConfig.Features.AdminDashboard
		if admin.DevCORSOrigin != "" || admin.DevBypassLogin {
			port := strings.TrimSpace(os.Getenv("PORT"))
			if port == "" {
				port = "9002"
			}
			return "http://localhost:" + port
		}
	}

	scheme := "http"
	if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if fwd := r.Header.Get("X-Forwarded-Host"); fwd != "" {
		host = fwd
	}
	return scheme + "://" + host
}

// publicAdminUIBaseURL returns the origin for browser-facing admin SPA links
// (e.g. share pages). In local dev with Vite, prefers dev_cors_origin so
// generated URLs open the dev server; SDK/proxy base URLs still use publicBaseURL.
func (h *handler) publicAdminUIBaseURL(r *http.Request) string {
	if h.deps.YAMLConfig != nil {
		admin := h.deps.YAMLConfig.Features.AdminDashboard
		if admin.DevCORSOrigin != "" && admin.DevBypassLogin {
			return strings.TrimRight(admin.DevCORSOrigin, "/")
		}
	}
	return h.publicBaseURL(r)
}

// providerBasePath maps a provider to the SDK-facing base path on the proxy.
// These mirror the provider proxy mounts (/{provider}/...). The returned
// path is what a user sets as their SDK base URL.
func providerBasePath(provider string) string {
	switch provider {
	case "openai":
		return "/openai/v1"
	case "anthropic":
		return "/anthropic"
	case "gemini":
		return "/gemini"
	default:
		return "/" + provider
	}
}

func (h *handler) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}

	var req struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.Key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "key is required"})
		return
	}

	user, err := h.auth.currentUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	role, err := adminusers.ParseRole(user.Role)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	record, err := h.deps.APIKeyStore.GetKeyRecord(r.Context(), req.Key)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "key not found"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if !canAccessKey(role, user.Email, record) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}

	createdBy := user.Email

	link, err := h.deps.APIKeyStore.CreateShareLink(r.Context(), req.Key, createdBy)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "key not found"})
			return
		}
		h.deps.Logger.Error("admin: create share link failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create share link"})
		return
	}

	resp := map[string]interface{}{
		"id":         link.ID(),
		"url":        h.publicAdminUIBaseURL(r) + "/admin/share/" + link.ID(),
		"provider":   link.Provider,
		"created_at": link.CreatedAt,
	}
	if link.ExpiresAt != nil {
		resp["expires_at"] = link.ExpiresAt
	} else {
		expires := link.EffectiveExpiresAt(time.Now())
		resp["expires_at"] = expires
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (h *handler) handleGetShare(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}

	id := mux.Vars(r)["id"]
	link, err := h.deps.APIKeyStore.GetShareLink(r.Context(), id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") || strings.Contains(err.Error(), "expired") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "share link not found or expired"})
			return
		}
		h.deps.Logger.Error("admin: get share link failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to resolve share link"})
		return
	}

	// Confirm the underlying key still exists / is usable.
	record, err := h.deps.APIKeyStore.GetKeyRecord(r.Context(), link.APIKey)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "the shared key no longer exists"})
		return
	}

	if user, err := h.auth.currentUser(r); err == nil {
		if role, roleErr := adminusers.ParseRole(user.Role); roleErr == nil && role == adminusers.RoleViewer {
			if !strings.EqualFold(record.OwnerEmail, user.Email) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
				return
			}
		}
	}

	// Audit every successful resolution of a public share link. The link hands
	// out a working credential, so we record who pulled it (client IP), which
	// link, and the redacted key — never the raw secret.
	h.deps.Logger.Info(
		"admin: share link resolved",
		"id", link.ID(),
		"key", apikeys.RedactKey(record.PK),
		"provider", record.Provider,
		"client_ip", clientIP(r),
		"created_by", link.CreatedBy,
	)

	if user, err := h.auth.currentUser(r); err == nil && h.deps.UserStore != nil {
		if recErr := h.deps.UserStore.RecordShareAwareness(r.Context(), user.Email, id); recErr != nil {
			h.deps.Logger.Warn("admin: record share awareness failed", "error", recErr, "email", user.Email, "share_id", id)
		}
	}

	// Belt-and-suspenders against the capability URL being indexed if it ever
	// leaks into a crawlable surface.
	w.Header().Set("X-Robots-Tag", "noindex, nofollow")

	base := h.publicBaseURL(r)
	resp := map[string]interface{}{
		"id":          link.ID(),
		"provider":    record.Provider,
		"key":         record.PK,
		"description": record.Description,
		"enabled":     record.Enabled,
		"proxy_base":  base,
		"base_url":    base + providerBasePath(record.Provider),
		"created_at":  link.CreatedAt,
		"created_by":  link.CreatedBy,
	}
	if link.ExpiresAt != nil {
		resp["expires_at"] = link.ExpiresAt
	} else {
		resp["expires_at"] = link.EffectiveExpiresAt(time.Now())
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}
	id := mux.Vars(r)["id"]
	if err := h.deps.APIKeyStore.DeleteShareLink(r.Context(), id); err != nil {
		if strings.Contains(err.Error(), "ConditionalCheckFailed") || strings.Contains(err.Error(), "not found") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "share link not found"})
			return
		}
		h.deps.Logger.Error("admin: delete share link failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to delete share link"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if h.deps.HealthFunc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "health unavailable"})
		return
	}
	h.deps.HealthFunc(w, r)
}

func (h *handler) handleCircuitActivity(w http.ResponseWriter, r *http.Request) {
	if h.deps.CircuitActivity == nil {
		writeJSON(w, http.StatusOK, map[string]interface{}{"available": false})
		return
	}
	writeJSON(w, http.StatusOK, h.deps.CircuitActivity())
}

func (h *handler) handleModelStatus(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"registry": modelStatusRegistry(h.deps.YAMLConfig),
	}
	if h.deps.ModelStatusSummary != nil {
		resp["stats"] = h.deps.ModelStatusSummary()
	} else {
		resp["stats"] = map[string]interface{}{"available": false}
	}
	writeJSON(w, http.StatusOK, resp)
}

func modelStatusRegistry(cfg *config.YAMLConfig) map[string]interface{} {
	retired := make([]map[string]interface{}, 0)
	deprecated := make([]map[string]interface{}, 0)
	if cfg == nil {
		return map[string]interface{}{
			"retired":    retired,
			"deprecated": deprecated,
		}
	}
	for provider, models := range cfg.RetiredModels {
		for canonical, entry := range models {
			retired = append(retired, map[string]interface{}{
				"provider":     provider,
				"model":        canonical,
				"retired_date": entry.RetiredDate,
				"replacement":  entry.Replacement,
				"aliases":      entry.Aliases,
			})
		}
	}
	sort.Slice(retired, func(i, j int) bool {
		if retired[i]["provider"] != retired[j]["provider"] {
			return retired[i]["provider"].(string) < retired[j]["provider"].(string)
		}
		return retired[i]["model"].(string) < retired[j]["model"].(string)
	})
	for provider, pcfg := range cfg.Providers {
		for canonical, mc := range pcfg.Models {
			if !mc.Deprecated {
				continue
			}
			deprecated = append(deprecated, map[string]interface{}{
				"provider":    provider,
				"model":       canonical,
				"replacement": mc.Replacement,
				"aliases":     mc.Aliases,
			})
		}
	}
	sort.Slice(deprecated, func(i, j int) bool {
		if deprecated[i]["provider"] != deprecated[j]["provider"] {
			return deprecated[i]["provider"].(string) < deprecated[j]["provider"].(string)
		}
		return deprecated[i]["model"].(string) < deprecated[j]["model"].(string)
	})
	return map[string]interface{}{
		"retired":    retired,
		"deprecated": deprecated,
	}
}

func (h *handler) handleRateLimits(w http.ResponseWriter, r *http.Request) {
	cfg := h.deps.YAMLConfig
	if cfg == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "config unavailable"})
		return
	}

	rl := cfg.Features.RateLimiting
	resp := RateLimitsResponse{
		Enabled:   rl.Enabled,
		Backend:   rl.Backend,
		Limits:    rl.Limits,
		Overrides: rl.Overrides,
	}
	if resp.Backend == "" {
		resp.Backend = "memory"
	}

	if h.deps.RateLimiter != nil {
		if snapshotter, ok := h.deps.RateLimiter.(ratelimit.Snapshotter); ok {
			snap := snapshotter.Snapshot(time.Now())
			sanitizeLimitsSnapshot(&snap)
			resp.Snapshot = &snap
		}
	}
	resp.Overrides = sanitizeRateLimitOverrides(resp.Overrides)
	if h.deps.RateLimitSummary != nil {
		resp.Stats = h.deps.RateLimitSummary()
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) handleCost(w http.ResponseWriter, r *http.Request) {
	cfg := h.deps.YAMLConfig
	if cfg == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "config unavailable"})
		return
	}

	ct := cfg.Features.CostTracking
	transports := make([]map[string]string, 0, len(ct.Transports))
	for _, t := range ct.Transports {
		entry := map[string]string{"type": t.Type}
		switch t.Type {
		case "file":
			if t.File != nil {
				entry["path"] = t.File.Path
			}
		case "dynamodb":
			if t.DynamoDB != nil {
				entry["table_name"] = t.DynamoDB.TableName
				entry["region"] = t.DynamoDB.Region
			}
		case "datadog":
			if t.Datadog != nil {
				entry["host"] = t.Datadog.Host
				entry["port"] = t.Datadog.Port
				entry["namespace"] = t.Datadog.Namespace
			}
		}
		transports = append(transports, entry)
	}

	resp := map[string]interface{}{
		"enabled":         ct.Enabled,
		"async":           ct.Async,
		"workers":         ct.Workers,
		"queue_size":      ct.QueueSize,
		"flush_interval":  ct.FlushInterval,
		"transport_count": len(transports),
		"transports":      transports,
	}

	if h.deps.CostSummary != nil {
		resp["stats"] = h.deps.CostSummary()
	} else {
		resp["stats"] = map[string]interface{}{"available": false}
	}

	writeJSON(w, http.StatusOK, resp)
}
