package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/gorilla/mux"
)

// KeyViewersResponse lists who can read a key. RequesterEmail is the implicit
// viewer from the key-request flow; it is reported for context but is not
// removable, because it is not part of the explicit grant list.
type KeyViewersResponse struct {
	Viewers        []string `json:"viewers"`
	RequesterEmail string   `json:"requester_email,omitempty"`
}

// AddKeyViewersRequest grants read-only access to one or more users.
type AddKeyViewersRequest struct {
	Emails []string `json:"emails"`
}

// keyViewersResponse builds the sorted viewer list returned by every viewers endpoint.
func keyViewersResponse(k *apikeys.APIKey) KeyViewersResponse {
	viewers := append([]string{}, k.ViewerEmails...)
	sort.Strings(viewers)
	return KeyViewersResponse{Viewers: viewers, RequesterEmail: k.RequesterEmail}
}

// handleListKeyViewers returns the explicit viewers of a key (editor/admin only).
func (h *handler) handleListKeyViewers(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}
	record, _, _, ok := h.loadAccessibleKey(w, r, mux.Vars(r)["key"], keyActionWrite)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, keyViewersResponse(record))
}

// handleAddKeyViewers grants read-only access to emails in the allowed domain.
func (h *handler) handleAddKeyViewers(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}
	var req AddKeyViewersRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if len(req.Emails) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "emails is required"})
		return
	}
	if len(req.Emails) > apikeys.MaxKeyViewers {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": apikeys.ErrTooManyViewers.Error()})
		return
	}
	emails := make([]string, 0, len(req.Emails))
	for _, e := range req.Emails {
		e = apikeys.NormalizeViewerEmail(e)
		if err := validateAllowedEmail(e, h.allowedDomain()); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		emails = append(emails, e)
	}

	record, user, _, ok := h.loadAccessibleKey(w, r, mux.Vars(r)["key"], keyActionWrite)
	if !ok {
		return
	}
	updated, err := h.deps.APIKeyStore.AddKeyViewers(r.Context(), record.PK, emails)
	if err != nil {
		h.writeKeyViewersError(w, err)
		return
	}
	h.deps.Logger.Info("admin: key viewers granted",
		"key", apikeys.MaskKeyID(record.PK), "actor", user.Email, "requested", len(emails), "total", len(updated.ViewerEmails))
	writeJSON(w, http.StatusOK, keyViewersResponse(updated))
}

// handleRemoveKeyViewer revokes one explicit viewer; the implicit requester grant is unaffected.
func (h *handler) handleRemoveKeyViewer(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}
	email := apikeys.NormalizeViewerEmail(mux.Vars(r)["email"])
	if email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email is required"})
		return
	}
	record, user, _, ok := h.loadAccessibleKey(w, r, mux.Vars(r)["key"], keyActionWrite)
	if !ok {
		return
	}
	updated, err := h.deps.APIKeyStore.RemoveKeyViewer(r.Context(), record.PK, email)
	if err != nil {
		h.writeKeyViewersError(w, err)
		return
	}
	h.deps.Logger.Info("admin: key viewer revoked",
		"key", apikeys.MaskKeyID(record.PK), "actor", user.Email, "total", len(updated.ViewerEmails))
	writeJSON(w, http.StatusOK, keyViewersResponse(updated))
}

// writeKeyViewersError maps store errors to client-safe responses.
func (h *handler) writeKeyViewersError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, apikeys.ErrViewersNotAllowed), errors.Is(err, apikeys.ErrTooManyViewers):
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
	case strings.Contains(err.Error(), "not found"):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "key not found"})
	default:
		h.deps.Logger.Error("admin: update key viewers failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update viewers"})
	}
}
