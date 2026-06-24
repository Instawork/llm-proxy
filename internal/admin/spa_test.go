package admin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func TestMountSPA_ServesIndexForClientRoutes(t *testing.T) {
	root := moduleRoot(t)
	distIndex := filepath.Join(root, "web", "dist", "index.html")
	if _, err := os.Stat(distIndex); err != nil {
		t.Skip("web/dist/index.html missing; run `(cd web && npm run build)` for SPA coverage")
	}

	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	rootRouter := mux.NewRouter()
	mountSPA(rootRouter.PathPrefix("/admin").Subrouter())

	req := httptest.NewRequest(http.MethodGet, "/admin/share/test-uuid", nil)
	rec := httptest.NewRecorder()
	rootRouter.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "<!doctype html>")
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache, no-store, must-revalidate", rec.Header().Get("Cache-Control"))
}

func TestMountShareSPA_ServesIndexForPublicShareRoute(t *testing.T) {
	root := moduleRoot(t)
	distIndex := filepath.Join(root, "web", "dist", "index.html")
	if _, err := os.Stat(distIndex); err != nil {
		t.Skip("web/dist/index.html missing; run `(cd web && npm run build)` for SPA coverage")
	}

	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	rootRouter := mux.NewRouter()
	mountShareSPA(rootRouter)

	req := httptest.NewRequest(http.MethodGet, "/share/test-uuid", nil)
	rec := httptest.NewRecorder()
	rootRouter.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "<!doctype html>")
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
	assert.Equal(t, "no-cache, no-store, must-revalidate", rec.Header().Get("Cache-Control"))
}

func TestMountSPA_MissingAssetReturns404(t *testing.T) {
	root := moduleRoot(t)
	distIndex := filepath.Join(root, "web", "dist", "index.html")
	if _, err := os.Stat(distIndex); err != nil {
		t.Skip("web/dist/index.html missing; run `(cd web && npm run build)` for SPA coverage")
	}

	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	rootRouter := mux.NewRouter()
	mountSPA(rootRouter.PathPrefix("/admin").Subrouter())

	req := httptest.NewRequest(http.MethodGet, "/admin/assets/missing-bundle.js", nil)
	rec := httptest.NewRecorder()
	rootRouter.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)
}

func TestAdminDistFS_FromDisk(t *testing.T) {
	root := moduleRoot(t)
	distIndex := filepath.Join(root, "web", "dist", "index.html")
	if _, err := os.Stat(distIndex); err != nil {
		t.Skip("web/dist/index.html missing; run `(cd web && npm run build)` for SPA coverage")
	}

	origWD, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(root))
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	fs := adminDistFS()
	require.NotNil(t, fs)
}
