package admin

import (
	"log/slog"
	"net/http"

	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/gorilla/mux"
)

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
	auth, err := newAuthenticator(logger, adminCfg)
	if err != nil {
		logger.Error("admin dashboard disabled: auth setup failed", "error", err)
		return
	}

	h := newHandler(deps, auth)

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
	publicAPI.HandleFunc("/share/{id}", h.handleGetShare).Methods(http.MethodGet, http.MethodOptions)

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
	api.HandleFunc("/usage", h.handleUsage).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/pii", h.handlePII).Methods(http.MethodGet, http.MethodOptions)
	api.HandleFunc("/share", h.handleCreateShare).Methods(http.MethodPost, http.MethodOptions)
	api.HandleFunc("/share/{id}", h.handleDeleteShare).Methods(http.MethodDelete, http.MethodOptions)

	mountSPA(adminRouter)

	logger.Info("Admin dashboard routes registered", "prefix", "/admin")
}
