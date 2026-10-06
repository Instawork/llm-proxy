package admin

import (
	"net/http"
	"net/http/pprof"

	"github.com/Instawork/llm-proxy/internal/admin/permissions"
	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/gorilla/mux"
)

// mountPprof exposes the runtime profiler under /admin/debug/pprof/ for
// admin-role sessions only. It is deliberately not registered on the public
// proxy router: profiles reveal goroutine stacks and heap contents. The
// goroutineleak profile (Go 1.27+) is served through Index like any other
// named runtime/pprof profile.
func mountPprof(adminRouter *mux.Router, auth *authenticator) {
	pp := adminRouter.PathPrefix("/debug/pprof").Subrouter()
	pp.Use(auth.requireSession)

	// net/http/pprof handlers key off the "/debug/pprof/" path prefix, so
	// strip the /admin mount point before delegating.
	guard := func(fn http.HandlerFunc) http.Handler {
		return auth.requireRole(adminPprofRole)(http.StripPrefix("/admin", fn))
	}
	pp.Handle("/cmdline", guard(pprof.Cmdline)).Methods(http.MethodGet)
	pp.Handle("/profile", guard(pprof.Profile)).Methods(http.MethodGet)
	pp.Handle("/symbol", guard(pprof.Symbol)).Methods(http.MethodGet, http.MethodPost)
	pp.Handle("/trace", guard(pprof.Trace)).Methods(http.MethodGet)
	pp.PathPrefix("/").Handler(guard(pprof.Index)).Methods(http.MethodGet)
}

var adminPprofRole adminusers.Role = permissions.MinRole(permissions.ManageUsers)
