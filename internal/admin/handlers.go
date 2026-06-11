package admin

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

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

func (h *handler) handleListKeys(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "api key management disabled"})
		return
	}

	provider := r.URL.Query().Get("provider")
	keys, err := h.deps.APIKeyStore.ListKeys(r.Context(), provider)
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

func (h *handler) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "api key management disabled"})
		return
	}

	var req CreateKeyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.Provider == "" || req.ActualKey == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider and actual_key are required"})
		return
	}

	key, err := h.deps.APIKeyStore.CreateKey(
		r.Context(),
		req.Provider,
		req.ActualKey,
		req.Description,
		req.DailyCostLimit,
		req.Tags,
		req.RedactPII,
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
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "api key management disabled"})
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

	writeJSON(w, http.StatusOK, keyToResponse(record, true))
}

func (h *handler) handleUpdateKey(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "api key management disabled"})
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

	updates := map[string]interface{}{}
	if req.Enabled != nil {
		updates["enabled"] = *req.Enabled
	}
	if req.Description != nil {
		updates["description"] = *req.Description
	}
	if req.DailyCostLimit != nil {
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

	if len(updates) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "no fields to update"})
		return
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
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "api key management disabled"})
		return
	}

	keyID := mux.Vars(r)["key"]
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
			"admin_dashboard":    cfg.Features.AdminDashboard.Enabled,
		},
		Providers:      providers,
		AdminDashboard: cfg.Features.AdminDashboard,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) handleHealth(w http.ResponseWriter, r *http.Request) {
	if h.deps.HealthFunc == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "health unavailable"})
		return
	}
	h.deps.HealthFunc(w, r)
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
			resp.Snapshot = &snap
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) handleCost(w http.ResponseWriter, r *http.Request) {
	if h.deps.CostSummary != nil {
		writeJSON(w, http.StatusOK, h.deps.CostSummary())
		return
	}

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

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":         ct.Enabled,
		"async":           ct.Async,
		"workers":         ct.Workers,
		"queue_size":      ct.QueueSize,
		"flush_interval":  ct.FlushInterval,
		"transport_count": len(transports),
		"transports":      transports,
	})
}
