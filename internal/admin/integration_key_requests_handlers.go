package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/gorilla/mux"
)

const (
	integrationMaxBodyBytes        = 16 << 10
	integrationMaxDescriptionLen   = 500
	integrationMaxJustificationLen = 2000
)

var externalTicketIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

func (h *handler) integrationsConfig() config.IntegrationsConfig {
	if h.deps.YAMLConfig == nil {
		return config.IntegrationsConfig{}
	}
	return h.deps.YAMLConfig.Features.AdminDashboard.Integrations
}

// integrationDefaultDailyCostCents is the daily limit an integration request
// gets when it asks for 0: the key-request default, clamped to the
// integration maximum.
func (h *handler) integrationDefaultDailyCostCents() int64 {
	daily := h.keyRequestDefaultDailyCostCents()
	if m := h.integrationsConfig().MaxDailyCostLimitCents; m > 0 && daily > m {
		return m
	}
	return daily
}

// validateIntegrationCostLimits applies the integration maxima to the
// limits of a request filed by an integration. When a monthly maximum is
// configured, a monthly limit of 0 ("no monthly cap") is out of range too.
func (h *handler) validateIntegrationCostLimits(daily, monthly int64) error {
	ic := h.integrationsConfig()
	if ic.MaxDailyCostLimitCents > 0 && daily > ic.MaxDailyCostLimitCents {
		return fmt.Errorf("daily_cost_limit exceeds integration maximum of %d cents", ic.MaxDailyCostLimitCents)
	}
	if ic.MaxMonthlyCostLimitCents > 0 && (monthly <= 0 || monthly > ic.MaxMonthlyCostLimitCents) {
		return fmt.Errorf("monthly_cost_limit must be between 1 and the integration maximum of %d cents", ic.MaxMonthlyCostLimitCents)
	}
	return nil
}

// ticketURLAllowed reports whether raw is an https URL on one of the
// configured ticket_url_hosts.
func (h *handler) ticketURLAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for allowed := range strings.SplitSeq(os.ExpandEnv(h.integrationsConfig().TicketURLHosts), ",") {
		if allowed = strings.ToLower(strings.TrimSpace(allowed)); allowed != "" && host == allowed {
			return true
		}
	}
	return false
}

// normalizeIntegrationKeyRequest validates body in the order the API
// documents and fills defaulted limits. Error messages never echo the
// requester email or free-text fields.
func (h *handler) normalizeIntegrationKeyRequest(body *IntegrationCreateKeyRequestBody) (status int, err error) {
	if err := validateAllowedEmail(body.RequesterEmail, h.auth.allowedDomain); err != nil {
		return http.StatusBadRequest, fmt.Errorf("requester_email: %w", err)
	}
	body.Provider = strings.TrimSpace(strings.ToLower(body.Provider))
	if body.Provider == "" {
		return http.StatusBadRequest, fmt.Errorf("provider is required")
	}
	if h.deps.KeyProvisioner == nil {
		return http.StatusServiceUnavailable, fmt.Errorf("key provisioning is not configured")
	}
	if _, ok := h.deps.KeyProvisioner.ForProvider(body.Provider); !ok {
		return http.StatusBadRequest, fmt.Errorf("provider %s is not available for key requests", body.Provider)
	}
	if apikeys.SlugifyKeyName(body.Name) == "" {
		return http.StatusBadRequest, fmt.Errorf("name must contain at least one letter or number")
	}
	body.Description = strings.TrimSpace(body.Description)
	if body.Description == "" {
		return http.StatusBadRequest, fmt.Errorf("description is required")
	}
	if utf8.RuneCountInString(body.Description) > integrationMaxDescriptionLen {
		return http.StatusBadRequest, fmt.Errorf("description exceeds %d characters", integrationMaxDescriptionLen)
	}
	body.Justification = strings.TrimSpace(body.Justification)
	if utf8.RuneCountInString(body.Justification) > integrationMaxJustificationLen {
		return http.StatusBadRequest, fmt.Errorf("justification exceeds %d characters", integrationMaxJustificationLen)
	}

	if body.DailyCostLimit < 0 || body.MonthlyCostLimit < 0 {
		return http.StatusBadRequest, fmt.Errorf("cost limits cannot be negative")
	}
	if body.DailyCostLimit == 0 {
		body.DailyCostLimit = h.integrationDefaultDailyCostCents()
	}
	if body.MonthlyCostLimit == 0 {
		body.MonthlyCostLimit = h.integrationsConfig().MaxMonthlyCostLimitCents
	}
	if err := h.validateKeyRequestDailyCostLimit(body.DailyCostLimit); err != nil {
		return http.StatusBadRequest, err
	}
	if err := h.validateIntegrationCostLimits(body.DailyCostLimit, body.MonthlyCostLimit); err != nil {
		return http.StatusBadRequest, err
	}

	body.ExternalTicketID = strings.TrimSpace(body.ExternalTicketID)
	if !externalTicketIDPattern.MatchString(body.ExternalTicketID) {
		return http.StatusBadRequest, fmt.Errorf("external_ticket_id must match %s", externalTicketIDPattern)
	}
	body.ExternalTicketURL = strings.TrimSpace(body.ExternalTicketURL)
	if body.ExternalTicketURL != "" && !h.ticketURLAllowed(body.ExternalTicketURL) {
		return http.StatusBadRequest, fmt.Errorf("external_ticket_url must be an https URL on an allowed ticket host")
	}
	return 0, nil
}

func (h *handler) handleIntegrationCreateKeyRequest(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}
	caller := integrationCallerFrom(r.Context())

	r.Body = http.MaxBytesReader(w, r.Body, integrationMaxBodyBytes)
	var body IntegrationCreateKeyRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if status, err := h.normalizeIntegrationKeyRequest(&body); err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	keyReq, err := h.deps.APIKeyStore.CreateKeyRequest(r.Context(), apikeys.CreateKeyRequestInput{
		RequesterEmail:    body.RequesterEmail,
		Provider:          body.Provider,
		Name:              body.Name,
		Description:       body.Description,
		DailyCostLimit:    body.DailyCostLimit,
		MonthlyCostLimit:  body.MonthlyCostLimit,
		Source:            caller.id,
		ExternalTicketID:  body.ExternalTicketID,
		ExternalTicketURL: body.ExternalTicketURL,
		Justification:     body.Justification,
	})
	switch {
	case errors.Is(err, apikeys.ErrKeyRequestTicketExists):
		writeJSON(w, http.StatusOK, h.integrationKeyRequestResponse(r, keyReq))
		return
	case errors.Is(err, apikeys.ErrPendingKeyRequestExists):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	case err != nil:
		h.deps.Logger.Error("admin: integration create key request failed", "caller", caller.id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create key request"})
		return
	}

	h.deps.Logger.Info("admin: integration key request created", "caller", caller.id, "request_id", keyReq.ID(), "provider", keyReq.Provider)
	h.deps.Notifier.KeyRequested(r.Context(), *keyReq)
	writeJSON(w, http.StatusCreated, h.integrationKeyRequestResponse(r, keyReq))
}

// loadCallerKeyRequest returns the request only when the calling
// integration filed it; anything else is a 404 so callers can't read each
// other's requests or dashboard requests.
func (h *handler) loadCallerKeyRequest(w http.ResponseWriter, r *http.Request) (*apikeys.KeyRequest, bool) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return nil, false
	}
	caller := integrationCallerFrom(r.Context())
	req, err := h.deps.APIKeyStore.GetKeyRequest(r.Context(), mux.Vars(r)["id"])
	if errors.Is(err, apikeys.ErrKeyRequestNotFound) || (err == nil && req.Source != caller.id) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "key request not found"})
		return nil, false
	}
	if err != nil {
		h.deps.Logger.Error("admin: integration get key request failed", "caller", caller.id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load key request"})
		return nil, false
	}
	return req, true
}

func (h *handler) handleIntegrationGetKeyRequest(w http.ResponseWriter, r *http.Request) {
	req, ok := h.loadCallerKeyRequest(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, h.integrationKeyRequestResponse(r, req))
}

func (h *handler) handleIntegrationCancelKeyRequest(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}
	caller := integrationCallerFrom(r.Context())
	req, err := h.deps.APIKeyStore.CancelKeyRequest(r.Context(), mux.Vars(r)["id"], caller.id)
	switch {
	case errors.Is(err, apikeys.ErrKeyRequestNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "key request not found"})
		return
	case errors.Is(err, apikeys.ErrKeyRequestNotPending):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	case err != nil:
		h.deps.Logger.Error("admin: integration cancel key request failed", "caller", caller.id, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to cancel key request"})
		return
	}
	h.deps.Logger.Info("admin: integration key request cancelled", "caller", caller.id, "request_id", req.ID())
	writeJSON(w, http.StatusOK, h.integrationKeyRequestResponse(r, req))
}

func (h *handler) integrationKeyRequestResponse(r *http.Request, req *apikeys.KeyRequest) IntegrationKeyRequestResponse {
	dashboard := h.publicBaseURL(r) + "/admin"
	id := url.QueryEscape(req.ID())
	resp := IntegrationKeyRequestResponse{
		ID:               req.ID(),
		Status:           req.Status,
		Provider:         req.Provider,
		Name:             req.Name,
		DailyCostLimit:   req.DailyCostLimit,
		MonthlyCostLimit: req.MonthlyCostLimit,
		ExternalTicketID: req.ExternalTicketID,
		CreatedAt:        req.CreatedAt,
		ReviewedAt:       req.ReviewedAt,
		RejectionReason:  req.RejectionReason,
		ReviewURL:        dashboard + "/keys?tab=requests&request_id=" + id,
		Claimed:          req.ClaimedAt != nil,
		ClaimedAt:        req.ClaimedAt,
	}
	if req.Status == apikeys.KeyRequestStatusApproved {
		resp.ClaimURL = dashboard + "/keys?claim=" + id
	}
	return resp
}
