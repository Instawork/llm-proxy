package admin

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/gorilla/mux"
)

func (h *handler) writeUserStoreUnavailable(w http.ResponseWriter) {
	msg := "admin user store unavailable"
	if h.deps.UserStoreError != nil {
		msg = "admin user store unavailable: " + h.deps.UserStoreError.Error()
	}
	writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": msg})
}

func (h *handler) allowedDomain() string {
	if h.deps.YAMLConfig == nil {
		return "example.com"
	}
	return h.deps.YAMLConfig.Features.AdminDashboard.AllowedDomain
}

func (h *handler) handleListUsers(w http.ResponseWriter, r *http.Request) {
	if h.deps.UserStore == nil {
		h.writeUserStoreUnavailable(w)
		return
	}
	users, err := h.deps.UserStore.ListUsers(r.Context())
	if err != nil {
		h.deps.Logger.Error("admin: list users failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list users"})
		return
	}
	resp := make([]AdminUserRecord, 0, len(users))
	for _, u := range users {
		resp = append(resp, userRecordFromStore(u))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *handler) handleGetUser(w http.ResponseWriter, r *http.Request) {
	if h.deps.UserStore == nil {
		h.writeUserStoreUnavailable(w)
		return
	}
	email := mux.Vars(r)["email"]
	if strings.TrimSpace(email) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email is required"})
		return
	}
	u, err := h.deps.UserStore.GetUser(r.Context(), email)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
			return
		}
		h.deps.Logger.Error("admin: get user failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to get user"})
		return
	}
	writeJSON(w, http.StatusOK, userRecordFromStore(u))
}

func (h *handler) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if h.deps.UserStore == nil {
		h.writeUserStoreUnavailable(w)
		return
	}
	var req CreateUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.Email == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email is required"})
		return
	}
	if err := validateAllowedEmail(req.Email, h.allowedDomain()); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	role := adminusers.RoleViewer
	if req.Role != "" {
		parsed, err := adminusers.ParseRole(req.Role)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		role = parsed
	}
	u, err := h.deps.UserStore.CreateUser(r.Context(), req.Email, role)
	if err != nil {
		if strings.Contains(err.Error(), "already exists") {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "user already exists"})
			return
		}
		h.deps.Logger.Error("admin: create user failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create user"})
		return
	}
	writeJSON(w, http.StatusCreated, userRecordFromStore(u))
}

func (h *handler) handleUpdateUserRole(w http.ResponseWriter, r *http.Request) {
	if h.deps.UserStore == nil {
		h.writeUserStoreUnavailable(w)
		return
	}
	email := mux.Vars(r)["email"]
	if strings.TrimSpace(email) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email is required"})
		return
	}
	var req UpdateUserRoleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	role, err := adminusers.ParseRole(req.Role)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	actor, err := h.auth.currentUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if strings.EqualFold(actor.Email, email) {
		target, getErr := h.deps.UserStore.GetUser(r.Context(), email)
		if getErr == nil && target.Role == adminusers.RoleAdmin && role != adminusers.RoleAdmin {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cannot demote your own admin account"})
			return
		}
	}

	if err := h.guardLastAdminDemotion(r, email, role); err != nil {
		writeLastAdminGuardError(w, h, err, "update user")
		return
	}

	if err := h.deps.UserStore.SetRole(r.Context(), email, role); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
			return
		}
		h.deps.Logger.Error("admin: update user role failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update user"})
		return
	}
	u, err := h.deps.UserStore.GetUser(r.Context(), email)
	if err != nil {
		writeJSON(w, http.StatusOK, AdminUserRecord{Email: email, Role: string(role)})
		return
	}
	writeJSON(w, http.StatusOK, userRecordFromStore(u))
}

func (h *handler) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if h.deps.UserStore == nil {
		h.writeUserStoreUnavailable(w)
		return
	}
	email := mux.Vars(r)["email"]
	if strings.TrimSpace(email) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "email is required"})
		return
	}

	actor, err := h.auth.currentUser(r)
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if strings.EqualFold(actor.Email, email) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cannot delete your own account"})
		return
	}

	if err := h.guardLastAdminRemoval(r, email); err != nil {
		writeLastAdminGuardError(w, h, err, "delete user")
		return
	}

	if err := h.deps.UserStore.DeleteUser(r.Context(), email); err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
			return
		}
		h.deps.Logger.Error("admin: delete user failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to delete user"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) guardLastAdminDemotion(r *http.Request, email string, newRole adminusers.Role) error {
	if newRole == adminusers.RoleAdmin {
		return nil
	}
	return h.guardLastAdminRemoval(r, email)
}

func (h *handler) guardLastAdminRemoval(r *http.Request, email string) error {
	target, err := h.deps.UserStore.GetUser(r.Context(), email)
	if err != nil {
		if errors.Is(err, adminusers.ErrUserNotFound) {
			return nil
		}
		return err
	}
	if target.Role != adminusers.RoleAdmin {
		return nil
	}
	count, err := h.deps.UserStore.CountAdmins(r.Context())
	if err != nil {
		return err
	}
	if count <= 1 {
		return errLastAdmin
	}
	return nil
}

var errLastAdmin = &lastAdminError{}

type lastAdminError struct{}

func (e *lastAdminError) Error() string {
	return "cannot remove the last admin"
}

func writeLastAdminGuardError(w http.ResponseWriter, h *handler, err error, action string) {
	if errors.Is(err, errLastAdmin) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	h.deps.Logger.Error("admin: guard last admin failed", "action", action, "error", err)
	writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to " + action})
}
