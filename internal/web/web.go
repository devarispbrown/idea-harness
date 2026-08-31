// Package web serves the built React UI, injecting the per-install token so the
// UI can call the authenticated API (§15). The dist/ directory is embedded.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed dist
var distFS embed.FS

// Handler returns an http.Handler that serves the embedded UI. The token is
// injected into index.html so the same-origin UI can authenticate API calls.
func Handler(token string) http.Handler {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "favicon.ico" {
			w.Header().Set("Content-Type", "image/svg+xml")
			w.Header().Set("Cache-Control", "public, max-age=86400")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><rect width="16" height="16" rx="3" fill="#0f766e"/><text x="8" y="11.5" font-size="9" text-anchor="middle" fill="#fff" font-family="sans-serif">i</text></svg>`))
			return
		}
		if path == "" || path == "index.html" {
			data, err := distFS.ReadFile("dist/index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
			html := strings.ReplaceAll(string(data), "__IDEA_TOKEN__", token)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(html))
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}
