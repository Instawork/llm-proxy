package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Instawork/llm-proxy/internal/adminusers"
	"github.com/Instawork/llm-proxy/internal/config"
	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
)

func TestPprof_RequiresAdminSession(t *testing.T) {
	t.Setenv("LLM_PROXY_ADMIN_SESSION_SECRET", "test-secret-at-least-32-bytes-long")

	yamlCfg := config.GetDefaultYAMLConfig()
	yamlCfg.Features.AdminDashboard.DevBypassLogin = true

	userStore := testAdminUserStore(t)
	_, err := userStore.CreateUser(context.Background(), "admin@example.com", adminusers.RoleAdmin)
	require.NoError(t, err)

	r := mux.NewRouter()
	RegisterRoutes(r, Deps{Logger: testLogger(), YAMLConfig: yamlCfg, UserStore: userStore})

	// Unauthenticated: rejected before any profile data is produced.
	anon := httptest.NewRecorder()
	r.ServeHTTP(anon, httptest.NewRequest(http.MethodGet, "/admin/debug/pprof/goroutine", nil))
	require.Equal(t, http.StatusUnauthorized, anon.Code)

	loginRec := httptest.NewRecorder()
	loginBody, _ := json.Marshal(map[string]string{"redirect": "http://localhost:9002/admin/"})
	loginReq := httptest.NewRequest(http.MethodPost, "/admin/auth/dev-login", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(loginRec, loginReq)
	require.Equal(t, http.StatusOK, loginRec.Code)

	for _, path := range []string{"/admin/debug/pprof/", "/admin/debug/pprof/goroutine?debug=1", "/admin/debug/pprof/goroutineleak?debug=1"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		for _, c := range loginRec.Result().Cookies() {
			req.AddCookie(c)
		}
		r.ServeHTTP(rec, req)
		require.Equalf(t, http.StatusOK, rec.Code, "path %s body %s", path, rec.Body.String())
		require.NotEmpty(t, rec.Body.Bytes(), path)
	}
}
