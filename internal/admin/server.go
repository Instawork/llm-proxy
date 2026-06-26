package admin

import (
	"log/slog"
	"net/http"

	"github.com/Instawork/llm-proxy/internal/admin/permissions"
	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/gorilla/mux"
)

func roleHandler(auth *authenticator, min adminusers.Role, fn http.HandlerFunc) http.Handler {
	return auth.requireRole(min)(http.HandlerFunc(fn))
}

// RegisterRoutes mounts admin auth and JSON API routes on r.
func RegisterRoutes(r *mux.Router, deps Deps) {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	adminCfg := config.AdminDashboardConfig{}
	if deps.YAMLConfig != nil {
		adminCfg = deps.YAMLConfig.Features.AdminDashboard
	}
	auth, err := newAuthenticator(logger, adminCfg, deps.UserStore)
	if err != nil {
		logger.Error("admin dashboard disabled: auth setup failed", "error", err)
		return
	}

	h := newHandler(deps, auth)

	mountShareSPA(r)

	// Redirect the bare "/admin" (no trailing slash) to "/admin/". The SPA is
	// mounted on the "/admin" prefix subrouter, whose effective matcher is
	// "/admin/…", so bare "/admin" would otherwise fall through to mux's
	// default 404. Registered on the parent router because the subrouter
	// cannot match the slash-less form.
	r.HandleFunc("/admin", func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "/admin/", http.StatusMovedPermanently)
	}).Methods(http.MethodGet)

	adminRouter := r.PathPrefix("/admin").Subrouter()

	authRouter := adminRouter.PathPrefix("/auth").Subrouter()
	if adminCfg.DevCORSOrigin != "" {
		authRouter.Use(h.corsMiddleware)
	}
	authRouter.HandleFunc("/login", auth.handleLogin).Methods(http.MethodGet)
	authRouter.HandleFunc("/callback", auth.handleCallback).Methods(http.MethodGet)
	authRouter.HandleFunc("/logout", auth.handleLogout).Methods(http.MethodPost, http.MethodOptions)
	if adminCfg.DevBypassLogin {
		authRouter.HandleFunc("/dev-login", auth.handleDevLogin).Methods(http.MethodPost, http.MethodOptions)
	}

	publicAPI := adminRouter.PathPrefix("/api").Subrouter()
	publicAPI.Use(h.corsMiddleware)
	// Share read is public — the UUID in the URL is the capability token.
	// A per-client token bucket caps abuse/DoS against this unauthenticated
	// endpoint (guessing the UUID is already infeasible). Generous enough for
	// legitimate use: a 10-request burst refilling at 1/sec per client IP.
	publicAPI.Use(newShareRateLimiter(1, 10).middleware)
	publicAPI.HandleFunc("/share/{id}", h.handleGetShare).Methods(http.MethodGet, http.MethodOptions)

	api := adminRouter.PathPrefix("/api").Subrouter()
	api.Use(h.corsMiddleware)
	api.Use(auth.requireSession)

	api.HandleFunc("/me", h.handleMe).Methods(http.MethodGet, http.MethodOptions)

	api.Handle("/health", roleHandler(auth, permissions.MinRole(permissions.ViewMonitoring), h.handleHealth)).Methods(http.MethodGet, http.MethodOptions)
	api.Handle("/circuit-activity", roleHandler(auth, permissions.MinRole(permissions.ViewMonitoring), h.handleCircuitActivity)).Methods(http.MethodGet, http.MethodOptions)
	api.Handle("/rate-limits", roleHandler(auth, permissions.MinRole(permissions.ViewMonitoring), h.handleRateLimits)).Methods(http.MethodGet, http.MethodOptions)
	api.Handle("/cost", roleHandler(auth, permissions.MinRole(permissions.ViewMonitoring), h.handleCost)).Methods(http.MethodGet, http.MethodOptions)
	api.Handle("/usage", roleHandler(auth, permissions.MinRole(permissions.ViewMonitoring), h.handleUsage)).Methods(http.MethodGet, http.MethodOptions)
	api.Handle("/pii", roleHandler(auth, permissions.MinRole(permissions.ViewMonitoring), h.handlePII)).Methods(http.MethodGet, http.MethodOptions)
	api.Handle("/model-status", roleHandler(auth, permissions.MinRole(permissions.ViewMonitoring), h.handleModelStatus)).Methods(http.MethodGet, http.MethodOptions)

	api.Handle("/config", roleHandler(auth, permissions.MinRole(permissions.ViewConfig), h.handleConfig)).Methods(http.MethodGet, http.MethodOptions)
	api.Handle("/keys", roleHandler(auth, permissions.MinRole(permissions.ListKeys), h.handleListKeys)).Methods(http.MethodGet, http.MethodOptions)
	api.Handle("/keys", roleHandler(auth, permissions.MinRole(permissions.CreateKey), h.handleCreateKey)).Methods(http.MethodPost, http.MethodOptions)
	api.Handle("/provisioning", roleHandler(auth, permissions.MinRole(permissions.Provisioning), h.handleProvisioning)).Methods(http.MethodGet, http.MethodOptions)
	api.Handle("/keys/{key:.+}/stats", roleHandler(auth, permissions.MinRole(permissions.KeyStats), h.handleKeyStats)).Methods(http.MethodGet, http.MethodOptions)
	api.Handle("/keys/{key:.+}", roleHandler(auth, permissions.MinRole(permissions.GetKey), h.handleGetKey)).Methods(http.MethodGet, http.MethodOptions)
	api.Handle("/keys/{key:.+}", roleHandler(auth, permissions.MinRole(permissions.UpdateKey), h.handleUpdateKey)).Methods(http.MethodPatch, http.MethodOptions)
	api.Handle("/keys/{key:.+}", roleHandler(auth, permissions.MinRole(permissions.DeleteKey), h.handleDeleteKey)).Methods(http.MethodDelete, http.MethodOptions)
	api.Handle("/share", roleHandler(auth, permissions.MinRole(permissions.ShareKey), h.handleCreateShare)).Methods(http.MethodPost, http.MethodOptions)
	api.Handle("/share/{id}", roleHandler(auth, permissions.MinRole(permissions.ShareKey), h.handleDeleteShare)).Methods(http.MethodDelete, http.MethodOptions)

	api.Handle("/byo-keys", roleHandler(auth, permissions.MinRole(permissions.ManageBYO), h.handleListBYOKeys)).Methods(http.MethodGet, http.MethodOptions)
	api.Handle("/byo-bans", roleHandler(auth, permissions.MinRole(permissions.ManageBYO), h.handleListBYOBans)).Methods(http.MethodGet, http.MethodOptions)
	api.Handle("/byo-bans", roleHandler(auth, permissions.MinRole(permissions.ManageBYO), h.handleCreateBYOBan)).Methods(http.MethodPost, http.MethodOptions)
	api.Handle("/byo-bans/{provider}/{hash}", roleHandler(auth, permissions.MinRole(permissions.ManageBYO), h.handleDeleteBYOBan)).Methods(http.MethodDelete, http.MethodOptions)

	api.Handle("/users", roleHandler(auth, permissions.MinRole(permissions.ManageUsers), h.handleListUsers)).Methods(http.MethodGet, http.MethodOptions)
	api.Handle("/users", roleHandler(auth, permissions.MinRole(permissions.ManageUsers), h.handleCreateUser)).Methods(http.MethodPost, http.MethodOptions)
	api.Handle("/users/{email:.+}", roleHandler(auth, permissions.MinRole(permissions.ManageUsers), h.handleGetUser)).Methods(http.MethodGet, http.MethodOptions)
	api.Handle("/users/{email:.+}", roleHandler(auth, permissions.MinRole(permissions.ManageUsers), h.handleUpdateUserRole)).Methods(http.MethodPatch, http.MethodOptions)
	api.Handle("/users/{email:.+}", roleHandler(auth, permissions.MinRole(permissions.ManageUsers), h.handleDeleteUser)).Methods(http.MethodDelete, http.MethodOptions)

	api.Handle("/key-requests", roleHandler(auth, permissions.MinRole(permissions.CreateKeyRequest), h.handleCreateKeyRequest)).Methods(http.MethodPost, http.MethodOptions)
	api.Handle("/key-requests", roleHandler(auth, permissions.MinRole(permissions.ListKeyRequests), h.handleListKeyRequests)).Methods(http.MethodGet, http.MethodOptions)
	api.Handle("/key-requests/mine", roleHandler(auth, permissions.MinRole(permissions.ListMyKeyRequests), h.handleListMyKeyRequests)).Methods(http.MethodGet, http.MethodOptions)
	api.Handle("/key-requests/{id}", roleHandler(auth, permissions.MinRole(permissions.ReviewKeyRequest), h.handleReviewKeyRequest)).Methods(http.MethodPatch, http.MethodOptions)

	mountSPA(adminRouter)

	logger.Info("Admin dashboard routes registered", "prefix", "/admin")
}
