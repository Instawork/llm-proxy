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

func mountSPA(adminRouter *mux.Router) {
	dist := adminDistFS()
	if dist == nil {
		return
	}
	fileServer := http.StripPrefix("/admin", http.FileServer(http.FS(dist)))
	adminRouter.PathPrefix("/").Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/admin/api/") {
			http.NotFound(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/admin")
		if path == "" || path == "/" {
			serveSPAIndex(w, dist)
			return
		}
		rel := strings.TrimPrefix(path, "/")
		if _, err := fs.Stat(dist, rel); err != nil {
			// Missing hashed bundles must 404 — serving index.html breaks JS module loading.
			if strings.HasPrefix(rel, "assets/") {
				http.NotFound(w, r)
				return
			}
			serveSPAIndex(w, dist)
			return
		}
		if strings.HasPrefix(rel, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	}))
}

func serveSPAIndex(w http.ResponseWriter, dist fs.FS) {
	f, err := dist.Open("index.html")
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		http.Error(w, "failed to load admin UI", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Always revalidate the HTML shell so deploys don't leave browsers pointing at
	// stale hashed bundle names (Vite renames assets every build).
	w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	w.Write(data) //nolint:errcheck
}
