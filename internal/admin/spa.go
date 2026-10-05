package admin

import (
	"io"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"github.com/Instawork/llm-proxy/web"
	"github.com/gorilla/mux"
)

func adminDistFS() fs.FS {
	// web.FS() (embed_ui build) already returns the tree rooted at dist/, so its
	// root holds index.html and assets/. Don't fs.Sub(uiFS, "dist") again — that
	// points at a nonexistent dist/dist/ and, because fs.Sub doesn't stat, yields
	// a non-nil-but-broken FS that makes every file open 404.
	if uiFS := web.FS(); uiFS != nil {
		return uiFS
	}
	if st, err := os.Stat("web/dist/index.html"); err == nil && !st.IsDir() {
		return os.DirFS("web/dist")
	}
	return nil
}

// spaIndex serves the SPA's index.html shell. The bytes are read from the
// dist FS once at mount: every client-side route and every unknown path
// falls through to this handler, and re-opening the embedded file on each
// hit was pure overhead for content that cannot change while the process
// runs. Failures are recorded at mount and reported per request with the
// same status codes the previous per-request implementation used: a dist
// without index.html is 404, an index.html that exists but cannot be read is
// 500.
type spaIndex struct {
	data   []byte
	status int // 0 when the shell loaded; otherwise the HTTP status to report
}

func newSPAIndex(dist fs.FS) *spaIndex {
	f, err := dist.Open("index.html")
	if err != nil {
		return &spaIndex{status: http.StatusNotFound}
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return &spaIndex{status: http.StatusInternalServerError}
	}
	return &spaIndex{data: data}
}

func (s *spaIndex) serve(w http.ResponseWriter, r *http.Request) {
	switch s.status {
	case 0:
	case http.StatusNotFound:
		http.NotFound(w, r)
		return
	default:
		http.Error(w, "failed to load admin UI", s.status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Always revalidate the HTML shell so deploys don't leave browsers pointing at
	// stale hashed bundle names (Vite renames assets every build).
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Write(s.data) //nolint:errcheck
}

func mountSPA(adminRouter *mux.Router) {
	dist := adminDistFS()
	if dist == nil {
		return
	}
	index := newSPAIndex(dist)
	fileServer := http.StripPrefix("/admin", http.FileServer(http.FS(dist)))
	adminRouter.PathPrefix("/").Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/admin/api/") {
			http.NotFound(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/admin")
		if path == "" || path == "/" {
			index.serve(w, r)
			return
		}
		rel := strings.TrimPrefix(path, "/")
		if _, err := fs.Stat(dist, rel); err != nil {
			// Missing hashed bundles must 404 — serving index.html breaks JS module loading.
			if strings.HasPrefix(rel, "assets/") {
				http.NotFound(w, r)
				return
			}
			index.serve(w, r)
			return
		}
		if strings.HasPrefix(rel, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	}))
}

func mountShareSPA(rootRouter *mux.Router) {
	dist := adminDistFS()
	if dist == nil {
		return
	}
	index := newSPAIndex(dist)
	rootRouter.HandleFunc("/share/{id}", index.serve).Methods(http.MethodGet)
}
