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
	if uiFS := web.FS(); uiFS != nil {
		if dist, err := fs.Sub(uiFS, "dist"); err == nil {
			return dist
		}
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
	w.Write(data) //nolint:errcheck
}
