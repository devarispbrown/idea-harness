package daemon

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/security"
	"github.com/devarispbrown/idea-harness/internal/web"
)

// DefaultStateRoot returns the global state directory (§30), overridable via
// IDEA_HARNESS_STATE for tests.
func DefaultStateRoot() string {
	if v := os.Getenv("IDEA_HARNESS_STATE"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".local", "share", "idea-harness")
}

// Run starts the daemon HTTP server on addr (loopback) and blocks until the
// server exits.
func Run(addr, stateRoot string, log *slog.Logger) error {
	rt, err := NewRuntime(stateRoot, log)
	if err != nil {
		return err
	}
	defer rt.Close()

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	// Enforce loopback-only binding: the API token is the sole credential and is
	// served to the web UI unauthenticated, so exposing the daemon beyond
	// loopback would let any LAN/remote client read the token and take over the
	// daemon (review: v1 security boundary). IDEA_HARNESS_ADDR must be loopback.
	if tcp, ok := ln.Addr().(*net.TCPAddr); ok && !tcp.IP.IsLoopback() {
		ln.Close()
		return fmt.Errorf("daemon: refusing to bind %s: must bind to a loopback address only", addr)
	}
	log.Info("idea-harness daemon listening", "addr", ln.Addr().String())
	return http.Serve(ln, NewServer(rt).Handler())
}

// Server is the localhost HTTP API.
type Server struct {
	rt  *Runtime
	mux *http.ServeMux
}

// NewServer builds the API server.
func NewServer(rt *Runtime) *Server {
	s := &Server{rt: rt, mux: http.NewServeMux()}
	s.routes()
	s.mux.Handle("/", web.Handler(rt.Token()))
	return s
}

// Handler returns the authenticated, loopback-hardened handler.
func (s *Server) Handler() http.Handler {
	return s.hostOriginGuard(s.auth(s.mux))
}

// auth enforces the per-install token on every API request (§15). Non-API
// paths (the embedded UI) are served without a token; the token is injected
// into the page and used by the UI's own fetch calls.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path == "/api/v1/health" {
			next.ServeHTTP(w, r)
			return
		}
		provided := r.Header.Get("X-Idea-Token")
		if provided == "" {
			// Also accept a Bearer token for convenience.
			if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
				provided = strings.TrimPrefix(h, "Bearer ")
			}
		}
		if provided == "" && isSSEEventsPath(r.URL.Path) {
			// EventSource cannot set headers; allow the token as a query param
			// on the SSE events stream ONLY. Every other endpoint requires the
			// header so a token never leaks via URL/logs/history/referrer.
			provided = r.URL.Query().Get("token")
		}
		if !security.TokenEqual(provided, s.rt.Token()) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostOriginGuard hardens against CSRF/DNS-rebinding (§15): exact-match
// loopback Host and Origin allowlists, Origin: null rejection, no CORS.
func (s *Server) hostOriginGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isLoopbackHost(r.Host) {
			writeError(w, http.StatusForbidden, "invalid host")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if origin == "null" || !isLoopbackOrigin(origin) {
				writeError(w, http.StatusForbidden, "invalid origin")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// isSSEEventsPath reports whether path is the project events SSE stream, the
// only endpoint allowed to accept the API token as a query parameter.
func isSSEEventsPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1/projects/") && strings.HasSuffix(path, "/events")
}

func isLoopbackHost(host string) bool {
	h := host
	if hp, _, err := net.SplitHostPort(host); err == nil {
		h = hp
	}
	h = strings.Trim(h, "[]")
	switch h {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

func isLoopbackOrigin(origin string) bool {
	// origin like http://127.0.0.1:8787
	rest := strings.TrimPrefix(origin, "http://")
	if rest == origin {
		rest = strings.TrimPrefix(origin, "https://")
	}
	if host, _, err := net.SplitHostPort(rest); err == nil {
		return isLoopbackHost(host)
	}
	return isLoopbackHost(strings.Trim(rest, "[]"))
}

// writeJSON writes a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// writeError writes a JSON error response.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
