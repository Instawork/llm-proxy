package admin

import (
	"errors"
	"net/http"
	"strings"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/gorilla/mux"
)

// handleClaimKeyRequest reveals an approved request's key to its requester,
// once. This is the only response that ever carries the full key of a
// requested service key.
func (h *handler) handleClaimKeyRequest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}
	// Without this a third-party page could POST here and use up the claim.
	if !h.auth.sameOriginRequest(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-site request rejected"})
		return
	}
	user, err := h.auth.currentUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	id := strings.TrimSpace(mux.Vars(r)["id"])
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}

	claimed, err := h.deps.APIKeyStore.ClaimKeyRequest(r.Context(), id, user.Email)
	if err != nil {
		switch {
		case errors.Is(err, apikeys.ErrKeyRequestNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "key request not found"})
		case errors.Is(err, apikeys.ErrKeyRequestNotApproved):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "key request is not approved yet"})
		case errors.Is(err, apikeys.ErrKeyRequestAlreadyClaimed):
			writeJSON(w, http.StatusGone, map[string]string{"error": "this key has already been claimed; ask an admin to reset the claim"})
		default:
			h.deps.Logger.Error("admin: claim key request failed", "request_id", id, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to claim key request"})
		}
		return
	}
	if claimed.CreatedKey == "" {
		h.deps.Logger.Error("admin: claimed key request has no key", "request_id", id)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to claim key request"})
		return
	}

	h.deps.Logger.Info("admin: key request claimed", "request_id", claimed.ID(), "key", apikeys.RedactKey(claimed.CreatedKey))
	resp := ClaimKeyResponse{
		Key:         claimed.CreatedKey,
		MaskedKeyID: apikeys.MaskKeyID(claimed.CreatedKey),
		Provider:    claimed.Provider,
	}
	if base := h.publicBaseURL(r); base != "" {
		resp.BaseURL = base + providerBasePath(claimed.Provider)
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleResetKeyRequestClaim lets an admin allow one more claim, for a
// requester who lost the key before storing it.
func (h *handler) handleResetKeyRequestClaim(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}
	if !h.auth.sameOriginRequest(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-site request rejected"})
		return
	}
	actor, err := h.auth.currentUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	id := strings.TrimSpace(mux.Vars(r)["id"])
	if id == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id is required"})
		return
	}

	updated, err := h.deps.APIKeyStore.ResetKeyRequestClaim(r.Context(), id, actor.Email)
	if err != nil {
		switch {
		case errors.Is(err, apikeys.ErrKeyRequestNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "key request not found"})
		case errors.Is(err, apikeys.ErrKeyRequestNotApproved), errors.Is(err, apikeys.ErrKeyRequestNotClaimed):
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		default:
			h.deps.Logger.Error("admin: reset key request claim failed", "request_id", id, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to reset key request claim"})
		}
		return
	}
	h.deps.Logger.Info("admin: key request claim reset", "request_id", updated.ID())
	writeJSON(w, http.StatusOK, keyRequestToResponse(updated))
}
