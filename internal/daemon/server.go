package daemon

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/devarispbrown/idea-harness/internal/executor"
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

// newHTTPServer builds the daemon's *http.Server with sensible timeouts
// (review LOW #9). Split out from Run so the timeout values themselves are
// unit-testable without spinning up a real listener.
//
// An unbounded server had no defense against a client that opens a
// connection and trickles headers/a body in slowly, or not at all -- tying
// up a goroutine and a file descriptor indefinitely. Moot against an
// attacker given loopback-only binding (enforced in Run, below), but free
// protection against a genuinely stalled local client too (a laptop that
// slept mid-request, a broken proxy).
//
// ReadHeaderTimeout/ReadTimeout/IdleTimeout are all safe to set without
// touching GET /api/v1/projects/{id}/events, the one long-lived response
// this server has (sse.go's SSE stream, open for as long as the browser tab
// is): all three bound time spent reading a REQUEST or sitting idle between
// requests on a keep-alive connection, never time spent inside a handler
// writing a response.
//
// WriteTimeout is deliberately left unset (0, unbounded): Go arms it as a
// hard wall-clock deadline covering the entire response write, with no
// per-handler override, so any nonzero value here would eventually sever
// that SSE stream out from under an open UI tab.
func newHTTPServer(handler http.Handler, baseContext func(net.Listener) context.Context) *http.Server {
	return &http.Server{
		Handler:           handler,
		BaseContext:       baseContext,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// Run starts the daemon HTTP server on addr (loopback) and blocks until the
// server exits. onReady, if set, runs once this process owns the address and
// the state directory -- the point at which it is THE daemon -- and is where
// the caller records its pid.
func Run(addr, stateRoot string, log *slog.Logger, onReady func()) error {
	return RunWith(addr, stateRoot, log, onReady, nil)
}

// RunWith is Run with the daemon config (<stateRoot>/config.yaml) adjusted by
// override first -- `idea daemon --listen ...` flags win over the file.
func RunWith(addr, stateRoot string, log *slog.Logger, onReady func(), override func(*DaemonConfig)) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stop()

	// The remote-listen policy is checked before anything is bound: a
	// refused daemon.listen must stop the daemon, not quietly leave it
	// loopback-only while the user waits for a phone that will never connect.
	dcfg, err := LoadDaemonConfig(stateRoot)
	if err != nil {
		return err
	}
	if override != nil {
		override(&dcfg)
	}
	plan, err := planRemoteListen(dcfg)
	if err != nil {
		return err
	}

	// Claim the address and the state directory FIRST, before anything that
	// assumes this process is the only daemon. The startup sweep removes agent
	// containers it believes a dead daemon left; run by a second process that
	// was about to fail on "address already in use", it removed the live
	// daemon's running agents instead.
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
	// The port is a lock only per address; two daemons on different addresses
	// can share one state directory. This covers that.
	unlock, err := lockStateDir(stateRoot)
	if err != nil {
		ln.Close()
		return err
	}
	defer unlock()

	rt, err := NewRuntime(stateRoot, log)
	if err != nil {
		ln.Close()
		return err
	}
	defer rt.Close()
	// From here on log the redacting logger NewRuntime built, and make it the
	// process default too: slog.Default() call sites (internal/projection) and
	// the standard log package (net/http's server errors) would otherwise
	// write to daemon.log around the redactor.
	log = rt.log
	slog.SetDefault(log)
	if onReady != nil {
		onReady()
	}
	rt.setIntegrationUIBase("http://" + ln.Addr().String())
	rt.configureRemote(dcfg, plan)

	// The remote listener, when configured: the same handler, which serves it
	// a token-free page and accepts only a header token or a paired session.
	var remoteLn net.Listener
	if plan != nil {
		remoteLn, err = net.Listen("tcp", plan.Addr)
		if err != nil {
			ln.Close()
			return fmt.Errorf("daemon.listen %s: %w", plan.Addr, err)
		}
		if plan.TLS != nil {
			remoteLn = tls.NewListener(remoteLn, plan.TLS)
		}
		if plan.Warning != "" {
			log.Warn("REMOTE ACCESS: " + plan.Warning)
		}
		log.Info("idea-harness daemon listening for remote browsers", "addr", remoteLn.Addr().String(), "tls", plan.TLS != nil,
			"sign_in", "run `idea ui pair` on this machine for a one-time code")
	}
	if len(dcfg.AllowedHosts) > 0 {
		log.Info("idea-harness daemon accepts proxied browsers", "allowed_hosts", strings.Join(dcfg.AllowedHosts, ","))
	}

	// Whatever an agent left under the state root now belongs to a daemon that
	// is gone. Swept in the background so a slow Docker cannot hold up serving,
	// but before any operation starts (launchOperation waits for it), so the
	// sweep can never touch an attempt of this daemon's.
	rt.holdOperationsUntilSwept()
	go func() {
		// A root daemon killed mid-run also left its work roots owned by
		// the container user. Handed back only once no leftover container
		// can still be writing to them.
		if executor.SweepAgentLeftovers(ctx, stateRoot) {
			if n, err := executor.RecoverWorkRoots(stateRoot); err != nil {
				log.Warn("could not hand every leftover work root back to the daemon", "err", err)
			} else if n > 0 {
				log.Info("handed leftover work roots back to the daemon", "count", n)
			}
		}
		rt.markSwept()
	}()

	log.Info("idea-harness daemon listening", "addr", ln.Addr().String())

	// Request contexts derive from base, which is cancelled when shutdown
	// begins. http.Server.Shutdown never cancels them itself, so an open UI
	// tab's event stream held every stop for the full grace period.
	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()
	srv := newHTTPServer(NewServer(rt).Handler(), func(net.Listener) context.Context { return base })
	srv.RegisterOnShutdown(cancelBase)
	served := make(chan error, 2)
	go func() { served <- srv.Serve(ln) }()
	if remoteLn != nil {
		go func() { served <- srv.Serve(remoteLn) }()
	}
	// Projects that configure Slack/Linear are opened now rather than on
	// their first request, so notifications and the Linear poll resume after
	// a restart without anyone touching the project.
	go rt.startConfiguredIntegrations(ctx)
	// Scheduled automations (automations.go): the projects that enable one
	// are opened now, and the scheduler ticks until shutdown.
	go rt.startConfiguredAutomations(ctx)
	go rt.runScheduler(ctx)
	select {
	case err := <-served:
		return err
	case <-ctx.Done():
	}
	// A second Ctrl-C now kills the process outright, as users expect.
	stop()

	// Cancel what is running at the same moment requests stop being taken: a
	// cancelled execute removes its agent container, and every second spent
	// first draining HTTP is a second the agent keeps running with the key.
	// Bounded, because a stop that never returns is worse than a leftover the
	// next start sweeps.
	log.Info("idea-harness daemon stopping")
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rt.beginShutdown()
	hctx, hcancel := context.WithTimeout(sctx, 5*time.Second)
	defer hcancel()
	if err := srv.Shutdown(hctx); err != nil {
		srv.Close()
	}
	if err := rt.Shutdown(sctx); err != nil {
		log.Warn("stopped with operations still running; the next start removes their containers", "err", err)
	}
	return nil
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
	// The token goes only into a page served to a browser on this machine;
	// a remote one gets the same page without it, and signs in (remote.go).
	s.mux.Handle("/", s.apiAwareNotFound(web.HandlerFor(func(r *http.Request) string {
		if isLocalRequest(r) {
			return rt.Token()
		}
		return ""
	})))
	// Integration actions (a Slack button, a Linear import) go through this
	// same handler -- auth, host guard and all -- never around it.
	rt.setIntegrationHandler(s.Handler())
	return s
}

// apiAwareNotFound answers unmatched API paths in the documented error shape,
// and hands everything else to the web UI.
//
// openapi.yaml states that "every error response in this API has this shape and
// nothing else". It did not: this catch-all is registered for "/", so it
// matched every request no pattern claimed — a wrong verb, a typo'd path — and
// they fell through to the file server's `404 page not found` as text/plain. A
// client with a token could not JSON-decode the one response it is most likely
// to hit while integrating, and the CLI surfaced it as `idea: 404 Not Found`,
// the only raw HTTP status in the product.
//
// Scoped on "/api/" rather than "/api/v1/" so a future v2 does not regress. The
// UI is unaffected: it is hash-routed (web/src/router.ts), so a fragment never
// reaches the server and there is no deep-link path to break.
func (s *Server) apiAwareNotFound(ui http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") {
			ui.ServeHTTP(w, r)
			return
		}
		// nosniff matches what http.Error set on the responses this replaces.
		w.Header().Set("X-Content-Type-Options", "nosniff")

		// Distinguish "no such path" from "wrong verb". Go synthesizes 405 only
		// when no pattern matches at all, and the UI catch-all registered for "/"
		// matches everything — so the 405 branch was dead and a wrong verb was
		// indistinguishable from a typo. Rather than restructure the mux, ask it
		// which methods this path does accept.
		if allowed := s.methodsFor(r.URL.Path); len(allowed) > 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			writeError(w, http.StatusMethodNotAllowed,
				"method not allowed: "+r.Method+" "+r.URL.Path+"; allowed: "+strings.Join(allowed, ", "))
			return
		}
		writeError(w, http.StatusNotFound, "no such endpoint: "+r.Method+" "+r.URL.Path)
	})
}

// methodsFor returns the methods a path is routed for, by probing the mux.
//
// ServeMux.Handler reports the pattern it would use, so a probe per method says
// whether that combination is registered. The catch-all's own pattern ("/") is
// excluded, since it matches everything and would make every path look routed.
//
// The probe list is the set the API actually registers; a method outside it can
// only ever be a 404, which is the right answer for it anyway.
func (s *Server) methodsFor(path string) []string {
	var allowed []string
	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		probe := &http.Request{Method: m, URL: &url.URL{Path: path}}
		if _, pattern := s.mux.Handler(probe); pattern != "" && pattern != "/" {
			allowed = append(allowed, m)
		}
	}
	// HEAD is served wherever GET is, by net/http.
	for _, m := range allowed {
		if m == http.MethodGet {
			allowed = append(allowed, http.MethodHead)
			break
		}
	}
	sort.Strings(allowed)
	return allowed
}

// Handler returns the authenticated, loopback-hardened handler.
func (s *Server) Handler() http.Handler {
	return s.hostOriginGuard(s.auth(s.mux))
}

// auth enforces authentication on every API request (§15). Non-API paths
// (the embedded UI) are served without it: a local page carries the token,
// a remote one carries none (remote.go).
//
// Two credentials are accepted, in this order:
//
//  1. the API token, in X-Idea-Token or a Bearer header (or, on the two SSE
//     streams only, a ?token= query parameter) -- the CLI, MCP, scripts and
//     the local UI. A header cannot be attached by a cross-site page, so a
//     token-authenticated request needs no CSRF defence;
//  2. a browser session cookie from a pairing-code sign-in (uisession.go). A
//     cookie IS attached by the browser on its own, so a state-changing
//     request authenticated this way must also carry the session's CSRF token
//     and an allowed Origin.
//
// A request that presents a wrong token is refused outright rather than
// falling through to the cookie: a broken client should fail loudly, not
// quietly act as whichever browser session shares its cookie jar.
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
		if r.URL.Path == "/api/v1/ui/login" && r.Method == http.MethodPost {
			// An unauthenticated write; it carries its own checks.
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodPost && isPushRenewPath(r.URL.Path) {
			// The other one: authenticated by the subscription's renewal
			// token in the body, because the service worker that calls it
			// holds no token and maybe no live session (push.go).
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
			// (A signed-in remote browser needs neither: EventSource sends the
			// same-origin session cookie.)
			provided = r.URL.Query().Get("token")
		}
		if provided != "" {
			if !security.TokenEqual(provided, s.rt.Token()) {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			next.ServeHTTP(w, withAuth(r, authInfo{kind: authToken}))
			return
		}
		if sess, secret, ok := s.sessionFromRequest(r); ok {
			if isUnsafeMethod(r.Method) {
				if err := checkSessionCSRF(r, secret); err != nil {
					writeError(w, http.StatusForbidden, err.Error())
					return
				}
			}
			next.ServeHTTP(w, withAuth(r, authInfo{kind: authSession, sessionID: sess.ID, csrf: csrfFor(secret)}))
			return
		}
		writeError(w, http.StatusUnauthorized, "unauthorized")
	})
}

// hostOriginGuard hardens against CSRF/DNS-rebinding (§15): exact-match
// Host and Origin allowlists -- loopback, plus the hostnames named in
// daemon.allowed_hosts and the remote listener's own address (remote.go) --
// Origin: null rejection, no CORS.
func (s *Server) hostOriginGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.rt.hosts.allowsHost(r.Host) {
			writeError(w, http.StatusForbidden, "invalid host")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if origin == "null" || !s.rt.hosts.allowsOrigin(origin) {
				writeError(w, http.StatusForbidden, "invalid origin")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// isSSEEventsPath reports whether path is one of the two SSE streams -- the
// project's (/api/v1/projects/{p}/events) and a run's
// (/api/v1/projects/{p}/runs/{r}/events/stream) -- the only endpoints allowed
// to accept the API token as a query parameter, because EventSource cannot
// set a header. Matched by exact shape, not suffix: GET .../runs/{r}/events
// is the run's JSON page and requires the header like everything else.
func isSSEEventsPath(path string) bool {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/projects/"), "/")
	if !strings.HasPrefix(path, "/api/v1/projects/") {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	switch len(parts) {
	case 2:
		return parts[1] == "events"
	case 5:
		return parts[1] == "runs" && parts[3] == "events" && parts[4] == "stream"
	}
	return false
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
