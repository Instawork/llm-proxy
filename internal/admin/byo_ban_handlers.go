package admin

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Instawork/llm-proxy/internal/apikeys"
	"github.com/gorilla/mux"
)

type BYOBanResponse struct {
	Provider  string `json:"provider"`
	MaskedID  string `json:"masked_id"`
	Hash      string `json:"hash"`
	BannedBy  string `json:"banned_by,omitempty"`
	Reason    string `json:"reason,omitempty"`
	CreatedAt string `json:"created_at"`
}

type CreateBYOBanRequest struct {
	Provider string `json:"provider"`
	MaskedID string `json:"masked_id"`
	Reason   string `json:"reason,omitempty"`
}

func byoBanToResponse(b *apikeys.BYOBan) BYOBanResponse {
	return BYOBanResponse{
		Provider:  b.Provider,
		MaskedID:  b.MaskedID,
		Hash:      b.Hash,
		BannedBy:  b.BannedBy,
		Reason:    b.Reason,
		CreatedAt: b.CreatedAt.Format(time.RFC3339),
	}
}

func (h *handler) handleListBYOBans(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}

	provider := strings.TrimSpace(r.URL.Query().Get("provider"))
	bans, err := h.deps.APIKeyStore.ListBYOBans(r.Context(), provider)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	out := make([]BYOBanResponse, 0, len(bans))
	for _, ban := range bans {
		out = append(out, byoBanToResponse(ban))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *handler) handleCreateBYOBan(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}

	user, err := h.auth.currentUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	var req CreateBYOBanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
		return
	}

	ban, err := h.deps.APIKeyStore.BanBYOCredential(
		r.Context(),
		req.Provider,
		req.MaskedID,
		user.Email,
		req.Reason,
	)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, byoBanToResponse(ban))
}

func (h *handler) handleDeleteBYOBan(w http.ResponseWriter, r *http.Request) {
	if h.deps.APIKeyStore == nil {
		h.writeAPIKeyStoreUnavailable(w)
		return
	}

	provider := strings.TrimSpace(mux.Vars(r)["provider"])
	hash := strings.TrimSpace(mux.Vars(r)["hash"])
	if provider == "" || hash == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider and hash are required"})
		return
	}

	if err := h.deps.APIKeyStore.UnbanBYOCredential(r.Context(), provider, hash); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
