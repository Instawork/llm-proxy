package admin

import (
	"log/slog"
	"net/http"

	"github.com/gorilla/mux"
)

// RegisterRoutes mounts admin auth and JSON API routes on r.
func RegisterRoutes(r *mux.Router, deps Deps) {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}

	allowedDomain := ""
	if deps.YAMLConfig != nil {
		allowedDomain = deps.YAMLConfig.Features.AdminDashboard.AllowedDomain
	}
	auth, err := newAuthenticator(logger, allowedDomain)
	if err != nil {
		logger.Error("admin dashboard disabled: auth setup failed", "error", err)
		return
	}

	h := newHandler(deps, auth)

	adminRouter := r.PathPrefix("/admin").Subrouter()

	adminRouter.HandleFunc("/auth/login", auth.handleLogin).Methods(http.MethodGet)
	adminRouter.HandleFunc("/auth/callback", auth.handleCallback).Methods(http.MethodGet)
	adminRouter.HandleFunc("/auth/logout", auth.handleLogout).Methods(http.MethodPost)

	api := adminRouter.PathPrefix("/api").Subrouter()
	api.Use(h.corsMiddleware)
	api.Use(auth.requireSession)

	api.HandleFunc("/me", h.handleMe).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/keys", h.handleListKeys).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/keys", h.handleCreateKey).Methods(http.MethodPost, http.MethodOptions)
	api.HandleFunc("/keys/{key:.+}", h.handleGetKey).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/keys/{key:.+}", h.handleUpdateKey).Methods(http.MethodPatch, http.MethodOptions)
	api.HandleFunc("/keys/{key:.+}", h.handleDeleteKey).Methods(http.MethodDelete, http.MethodOptions)
	api.HandleFunc("/config", h.handleConfig).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/health", h.handleHealth).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/rate-limits", h.handleRateLimits).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/cost", h.handleCost).Methods(http.MethodGet, http.MethodOptions)

	mountSPA(adminRouter)

	logger.Info("Admin dashboard routes registered", "prefix", "/admin")
}
