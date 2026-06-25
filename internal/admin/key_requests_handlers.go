package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/provision"
	"github.com/gorilla/mux"
)

func (h *handler) handleCreateKeyRequest(w http.ResponseWriter, r *http.Request) {
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
	if role == adminusers.RoleAdmin {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "admins can create keys directly"})
		return
	}

	var req CreateKeyRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.Provider == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider is required"})
		return
	}
	if strings.TrimSpace(req.Description) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "description is required"})
		return
	}

	dailyLimit := req.DailyCostLimit
	if dailyLimit <= 0 {
		dailyLimit = h.keyRequestDefaultDailyCostCents()
	}
	if err := h.validateKeyRequestDailyCostLimit(dailyLimit); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	keyReq, err := h.deps.APIKeyStore.CreateKeyRequest(r.Context(), apikeys.CreateKeyRequestInput{
		RequesterEmail: user.Email,
		Provider:       req.Provider,
		Description:    req.Description,
		DailyCostLimit: dailyLimit,
	})
	if err != nil {
		if errors.Is(err, apikeys.ErrPendingKeyRequestExists) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		h.deps.Logger.Error("admin: create key request failed", "error", err)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, keyRequestToResponse(keyReq))
}

func (h *handler) handleListKeyRequests(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}

	status := strings.TrimSpace(r.URL.Query().Get("status"))
	requests, err := h.deps.APIKeyStore.ListKeyRequests(r.Context(), status)
	if err != nil {
		h.deps.Logger.Error("admin: list key requests failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list key requests"})
		return
	}

	resp := make([]KeyRequestResponse, 0, len(requests))
	for _, req := range requests {
		resp = append(resp, keyRequestToResponse(req))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) handleListMyKeyRequests(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}

	user, err := h.auth.currentUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	requests, err := h.deps.APIKeyStore.ListKeyRequestsForRequester(r.Context(), user.Email)
	if err != nil {
		h.deps.Logger.Error("admin: list my key requests failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list key requests"})
		return
	}

	resp := make([]KeyRequestResponse, 0, len(requests))
	for _, req := range requests {
		resp = append(resp, keyRequestToResponse(req))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) handleReviewKeyRequest(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}

	actor, err := h.auth.currentUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	id := mux.Vars(r)["id"]
	if strings.TrimSpace(id) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}

	var body ReviewKeyRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	action := strings.TrimSpace(strings.ToLower(body.Action))
	switch action {
	case "approve":
		h.handleApproveKeyRequest(w, r, id, actor.Email)
	case "reject":
		h.handleRejectKeyRequest(w, r, id, actor.Email, body.RejectionReason)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "action must be approve or reject"})
	}
}

func (h *handler) handleApproveKeyRequest(w http.ResponseWriter, r *http.Request, id, reviewerEmail string) {
	claimed, err := h.deps.APIKeyStore.BeginKeyRequestApproval(r.Context(), id, reviewerEmail)
	if err != nil {
		if errors.Is(err, apikeys.ErrKeyRequestNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "key request not found"})
			return
		}
		if errors.Is(err, apikeys.ErrKeyRequestNotPending) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		h.deps.Logger.Error("admin: begin key request approval failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to approve key request"})
		return
	}

	dailyLimit := claimed.DailyCostLimit
	if dailyLimit <= 0 {
		dailyLimit = h.keyRequestDefaultDailyCostCents()
	}
	if err := h.validateKeyRequestDailyCostLimit(dailyLimit); err != nil {
		_ = h.deps.APIKeyStore.RollbackKeyRequestApproval(r.Context(), id)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	createReq := CreateKeyRequest{
		Provider:       claimed.Provider,
		AutoProvision:  true,
		Description:    claimed.Description,
		DailyCostLimit: dailyLimit,
	}

	key, err := h.createOrgKey(r, adminusers.RoleAdmin, createReq)
	if err != nil {
		_ = h.deps.APIKeyStore.RollbackKeyRequestApproval(r.Context(), id)
		var httpErr *orgKeyError
		if errors.As(err, &httpErr) {
			writeJSON(w, httpErr.status, map[string]string{"error": httpErr.message})
			return
		}
		h.deps.Logger.Error("admin: approve key request failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create key"})
		return
	}

	updated, err := h.deps.APIKeyStore.CompleteKeyRequestApproval(r.Context(), id, key.PK)
	if err != nil {
		if delErr := h.deps.APIKeyStore.DeleteKey(r.Context(), key.PK); delErr != nil {
			h.deps.Logger.Error("admin: cleanup orphaned key after approval failed", "error", delErr, "key", key.PK)
		}
		_ = h.deps.APIKeyStore.RollbackKeyRequestApproval(r.Context(), id)
		h.deps.Logger.Error("admin: complete key request approval failed", "error", err, "key", key.PK)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to finalize key request approval"})
		return
	}

	writeJSON(w, http.StatusOK, keyRequestToResponse(updated))
}

func (h *handler) handleRejectKeyRequest(w http.ResponseWriter, r *http.Request, id, reviewerEmail, reason string) {
	updated, err := h.deps.APIKeyStore.RejectKeyRequest(r.Context(), id, reviewerEmail, reason)
	if err != nil {
		if errors.Is(err, apikeys.ErrKeyRequestNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "key request not found"})
			return
		}
		if errors.Is(err, apikeys.ErrKeyRequestNotPending) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		h.deps.Logger.Error("admin: reject key request failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to reject key request"})
		return
	}
	writeJSON(w, http.StatusOK, keyRequestToResponse(updated))
}

type orgKeyError struct {
	status  int
	message string
}

func (e *orgKeyError) Error() string {
	return e.message
}

func (h *handler) createOrgKey(r *http.Request, role adminusers.Role, req CreateKeyRequest) (*apikeys.APIKey, error) {
	if err := validateProvisionedKeyOnly(role, &req); err != nil {
		return nil, &orgKeyError{status: http.StatusBadRequest, message: err.Error()}
	}

	if err := apikeys.ValidatePIIOffBedrockPolicy(
		h.globalPIIEnabled(),
		req.Provider,
		req.RedactPII,
		h.adminBypassPIIBedrockPolicy(r),
	); err != nil {
		return nil, &orgKeyError{status: http.StatusBadRequest, message: err.Error()}
	}

	actualKey := strings.TrimSpace(req.ActualKey)
	var meta apikeys.KeyCreateMeta

	if req.AutoProvision {
		if h.deps.KeyProvisioner == nil {
			return nil, &orgKeyError{status: http.StatusServiceUnavailable, message: "key provisioning is not configured"}
		}
		if _, ok := h.deps.KeyProvisioner.ForProvider(req.Provider); !ok {
			return nil, &orgKeyError{
				status:  http.StatusBadRequest,
				message: "auto_provision is not available for provider " + req.Provider,
			}
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
				return nil, &orgKeyError{status: http.StatusServiceUnavailable, message: err.Error()}
			}
			if errors.Is(err, provision.ErrInvalidTier) {
				return nil, &orgKeyError{status: http.StatusBadRequest, message: err.Error()}
			}
			h.deps.Logger.Error("admin: provision upstream key failed", "provider", req.Provider, "error", err)
			return nil, &orgKeyError{status: http.StatusServiceUnavailable, message: "failed to provision upstream key"}
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
		return nil, &orgKeyError{status: http.StatusBadRequest, message: "actual_key is required unless auto_provision is true"}
	}

	key, err := h.deps.APIKeyStore.CreateKeyWithMeta(
		r.Context(),
		req.Provider,
		actualKey,
		req.Description,
		req.DailyCostLimit,
		req.MonthlyCostLimit,
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
		h.deps.Logger.Error("admin: create org key failed", "error", err)
		return nil, &orgKeyError{status: http.StatusInternalServerError, message: "failed to create key"}
	}
	return key, nil
}
