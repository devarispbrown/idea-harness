// Package web serves the built React UI, injecting the per-install token so the
// UI can call the authenticated API (§15). The dist/ directory is embedded.
//
// dist/ is committed — the real Vite build, not a placeholder — so that
// go build/test/install work from a clean checkout with no Node toolchain:
// //go:embed requires the directory to exist at compile time. `make embed-web`
// rebuilds it from web/, and `make check-embed` (CI) keeps it from going stale.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// contentSecurityPolicy locks the UI origin down to its own assets. The UI
// talks only to the daemon that served it, loads no third-party script or font,
// and must never be framed -- so everything is 'self' or 'none'.
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; " + // React style attributes; the placeholder's <style>
	"img-src 'self' data:; " +
	"font-src 'self'; " +
	"connect-src 'self'; " +
	"object-src 'none'; " +
	"frame-ancestors 'none'; " +
	"base-uri 'none'; " +
	"form-action 'none'"

// Handler returns an http.Handler that serves the embedded UI. The token is
// injected into index.html so the same-origin UI can authenticate API calls.
func Handler(token string) http.Handler {
	return HandlerFor(func(*http.Request) string { return token })
}

// HandlerFor is Handler with the token decided per request. The daemon hands
// the token only to a browser provably on this machine; a remote one gets an
// empty token and signs in with a pairing code instead (internal/daemon,
// uisession.go), so the same page serves both.
func HandlerFor(tokenFor func(*http.Request) string) http.Handler {
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
			html := strings.ReplaceAll(string(data), "__IDEA_TOKEN__", tokenFor(r))
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			// This document carries the per-install API token in a meta tag, and
			// the daemon binds a fixed loopback port that other local dev servers
			// have also used -- an origin, and therefore a browser cache, that
			// idea-harness does not own. no-store keeps the token out of the
			// on-disk cache, where it would outlive both the daemon and a token
			// rotation.
			w.Header().Set("Cache-Control", "no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			// With a bearer credential in the DOM and no CSP, any script that
			// reached this origin could read the token and post it anywhere.
			// The built UI loads one same-origin module script and one
			// same-origin stylesheet, so 'self' is sufficient; inline styles are
			// allowed because React sets style attributes (and the placeholder
			// page has a <style> block).
			w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(html))
			return
		}
		// Asset filenames are content-hashed by Vite, so a long TTL is safe: a
		// new build changes the URL rather than the bytes behind it.
		if strings.HasPrefix(path, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		switch path {
		case "sw.js", "sw-push.js":
			// The service worker's scripts (sw.js and the sw-push.js it
			// imports) must be revalidated on every
			// load, or a fixed bug in it stays installed until the browser's
			// 24-hour update check. It is served with the page's CSP, which is
			// what a worker script inherits.
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		case "manifest.webmanifest":
			w.Header().Set("Content-Type", "application/manifest+json")
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
}
