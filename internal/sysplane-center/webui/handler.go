package webui

import (
	"io/fs"
	"mime"
	"net/http"
	pathpkg "path"
	"strings"
)

func NewHandler() http.Handler {
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cleanPath := strings.TrimPrefix(pathpkg.Clean("/"+r.URL.Path), "/")
		if cleanPath == "." || cleanPath == "" || !strings.Contains(pathpkg.Base(cleanPath), ".") {
			serveEmbeddedFile(w, staticFS, "index.html")
			return
		}
		serveEmbeddedFile(w, staticFS, cleanPath)
	})
}

func serveEmbeddedFile(w http.ResponseWriter, staticFS fs.FS, name string) {
	file, err := fs.ReadFile(staticFS, name)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	contentType := mime.TypeByExtension(pathpkg.Ext(name))
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	_, _ = w.Write(file)
}
