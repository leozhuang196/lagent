// Package static serves the built web UI (console and chat) embedded in the
// binary, with single-page-app fallback to index.html.
package static

import (
	"embed"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strings"
)

//go:embed all:dist
var embedded embed.FS

// Handler serves dir when set (development), otherwise the embedded build.
func Handler(dir string) http.Handler {
	var root fs.FS
	if dir != "" {
		root = os.DirFS(dir)
	} else {
		sub, err := fs.Sub(embedded, "dist")
		if err != nil {
			panic(err)
		}
		root = sub
	}
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if strings.HasPrefix(p, "v1/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":"not_found","message":"no such API route"}}`))
			return
		}
		if p == "" || p == "." {
			p = "index.html"
		}
		if _, err := fs.Stat(root, p); err != nil {
			// Client-side routes fall back to the app shell.
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}
