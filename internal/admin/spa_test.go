package admin

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"testing/fstest"

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

func TestSPAIndex_ReadsOnceAtMountAndServesCachedBytes(t *testing.T) {
	dist := fstest.MapFS{
		"index.html": &fstest.MapFile{Data: []byte("<!doctype html><title>v1</title>")},
	}
	index := newSPAIndex(dist)
	require.Zero(t, index.status)

	// Mutating the backing FS after mount must not change what is served:
	// the shell is immutable for the life of the process.
	dist["index.html"] = &fstest.MapFile{Data: []byte("<!doctype html><title>v2</title>")}

	for range 3 {
		rec := httptest.NewRecorder()
		index.serve(rec, httptest.NewRequest(http.MethodGet, "/admin/anything", nil))
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "<!doctype html><title>v1</title>", rec.Body.String())
		assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
		assert.Equal(t, "no-cache, no-store, must-revalidate", rec.Header().Get("Cache-Control"))
	}
}

func TestSPAIndex_MissingIndexIs404(t *testing.T) {
	index := newSPAIndex(fstest.MapFS{"assets/app.js": &fstest.MapFile{Data: []byte("x")}})
	require.Equal(t, http.StatusNotFound, index.status)
	rec := httptest.NewRecorder()
	index.serve(rec, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	assert.Equal(t, http.StatusNotFound, rec.Code)
}

// unreadableFS opens index.html successfully but fails on Read, mirroring an
// embedded or on-disk file that exists yet cannot be loaded.
type unreadableFS struct{ fs.FS }

type unreadableFile struct{ fs.File }

func (unreadableFile) Read([]byte) (int, error) { return 0, errors.New("disk read error") }

func (u unreadableFS) Open(name string) (fs.File, error) {
	f, err := u.FS.Open(name)
	if err != nil {
		return nil, err
	}
	return unreadableFile{f}, nil
}

func TestSPAIndex_UnreadableIndexIs500(t *testing.T) {
	dist := unreadableFS{fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<!doctype html>")}}}
	index := newSPAIndex(dist)
	require.Equal(t, http.StatusInternalServerError, index.status)
	rec := httptest.NewRecorder()
	index.serve(rec, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Contains(t, rec.Body.String(), "failed to load admin UI")
}
