//go:build embed_ui

package admin

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests run only under the embed_ui build tag — the same configuration the
// production image ships with. The default (non-embed) test build exercises the
// os.DirFS("web/dist") fallback instead, which is why a double-fs.Sub("dist")
// regression in adminDistFS once shipped to prod (admin dashboard 404) without
// any test catching it.

func TestAdminDistFS_EmbeddedRootHoldsIndex(t *testing.T) {
	dist := adminDistFS()
	require.NotNil(t, dist, "embedded UI must resolve under embed_ui build")
	// index.html must sit at the FS root, not under a nested dist/ directory.
	_, err := fs.Stat(dist, "index.html")
	require.NoError(t, err, "embedded dist root must contain index.html (no double dist/ nesting)")
}

func TestMountSPA_EmbeddedServesIndex(t *testing.T) {
	rootRouter := mux.NewRouter()
	mountSPA(rootRouter.PathPrefix("/admin").Subrouter())

	req := httptest.NewRequest(http.MethodGet, "/admin/", nil)
	rec := httptest.NewRecorder()
	rootRouter.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "GET /admin/ must serve the embedded SPA index")
	assert.Contains(t, rec.Body.String(), "<!doctype html>")
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
}
