package admin

import (
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/Instawork/llm-proxy/web"
	"github.com/gorilla/mux"
)

func mountSPA(adminRouter *mux.Router) {
	uiFS := web.FS()
	if uiFS == nil {
		return
	}
	dist, err := fs.Sub(uiFS, "dist")
	if err != nil {
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
		if _, err := fs.Stat(dist, strings.TrimPrefix(path, "/")); err != nil {
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
