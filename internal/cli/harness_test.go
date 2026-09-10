package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/buildinfo"
)

// A CLI command reaches the daemon through connect() -> ensureDaemon(), which
// starts os.Executable() with the "daemon" argument when no daemon answers.
// Under `go test` os.Executable() is the test binary, so a command run without
// this harness forks a copy of the test binary and then polls it for five
// seconds. The two pre-existing tests in this package avoid that only by never
// calling connect().
//
// fakeDaemon serves the endpoints connect() needs from an httptest server and
// points the CLI at it with IDEA_HARNESS_ADDR + IDEA_HARNESS_STATE, so
// ensureDaemon() finds a healthy daemon and returns without forking anything.
type fakeDaemon struct {
	t         *testing.T
	mux       *http.ServeMux
	server    *httptest.Server
	projectID string

	// requests records every path the CLI asked for, so a test can assert what
	// a command sent (or did not send) to the daemon.
	requests []string

	// daemonBuild is what GET /api/v1/version reports, so a test can make the
	// fake daemon a different build from the CLI under test.
	daemonBuild buildinfo.Info

	// providers is what GET /providers reports. Registered once here rather
	// than per-test: net/http.ServeMux panics on a duplicate pattern, and
	// three tests used to register their own under {id} while this one uses
	// {projectId} -- the two patterns match the same requests.
	providers cliProvidersView

	// verifyCoverage is what POST /verify reports. Tests that need the
	// "gate ran, nothing changed" state set it before running a command;
	// net/http.ServeMux panics on a duplicate pattern, so it is a field rather
	// than a second registration.
	verifyCoverage map[string]any

	// verifyBodies records each POST /verify request body.
	verifyBodies []string

	// initiatives is what GET /initiatives reports. Set it before seedInitiative
	// to serve a different list.
	initiatives []map[string]any
}

// seedTwoInitiatives serves an oldest-first list, which is the order
// ListInitiatives produces (ORDER BY created_at ASC). ini-2 is the newest.
func (f *fakeDaemon) seedTwoInitiatives() {
	f.initiatives = []map[string]any{
		{"id": "ini-1", "title": "The older one", "status": "approved"},
		{"id": "ini-2", "title": "The newer one", "status": "plan_approved"},
	}
	f.seedInitiative()
}

// newFakeDaemon wires a fake daemon and a cwd that resolves to projectID.
// Every command run through run() afterwards talks to it.
func newFakeDaemon(t *testing.T) *fakeDaemon {
	t.Helper()

	f := &fakeDaemon{t: t, mux: http.NewServeMux(), projectID: "p1"}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.requests = append(f.requests, r.Method+" "+r.URL.RequestURI())
		f.mux.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)

	// The CLI builds "http://" + IDEA_HARNESS_ADDR.
	t.Setenv("IDEA_HARNESS_ADDR", strings.TrimPrefix(f.server.URL, "http://"))

	// newClient() reads the token from the state root; without it every command
	// fails before reaching the server.
	state := t.TempDir()
	t.Setenv("IDEA_HARNESS_STATE", state)
	if err := os.WriteFile(filepath.Join(state, "token"), []byte("test-token"), 0o600); err != nil {
		t.Fatalf("write token: %v", err)
	}

	// projectIDForCwd() matches the registered root_path against the working
	// directory, so the test's cwd has to be a registered project.
	root := t.TempDir()
	t.Chdir(root)

	f.json("GET /api/v1/health", map[string]string{"status": "ok"})
	// Registered because authAccepted() asks for it. Without this the fake 404s
	// the request, authAccepted is false, and `tokenRejected` was silently true
	// in every test using this harness -- a whole diagnostic branch firing under
	// tests that were green. ServeMux panics on a duplicate pattern, so the
	// build is a field read at request time rather than something a test
	// re-registers; it defaults to this binary's, i.e. no mismatch.
	f.daemonBuild = buildinfo.Get()
	f.mux.HandleFunc("GET /api/v1/projects/{projectId}/providers", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.providers)
	})
	f.mux.HandleFunc("GET /api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.daemonBuild)
	})
	f.json("GET /api/v1/projects", []map[string]string{
		{"project_id": f.projectID, "root_path": root},
	})

	return f
}

// json registers a canned JSON response for a method+pattern
// (net/http.ServeMux patterns, so "GET /api/v1/projects/{id}/tasks" works).
func (f *fakeDaemon) json(pattern string, payload any) {
	f.t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		f.t.Fatalf("marshal %s: %v", pattern, err)
	}
	f.mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	})
}

// fail registers an error response, matching the daemon's {"error": …} shape.
func (f *fakeDaemon) fail(pattern string, status int, msg string) {
	f.t.Helper()
	f.mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
	})
}

// asked reports whether any request path contains substr.
func (f *fakeDaemon) asked(substr string) bool {
	for _, r := range f.requests {
		if strings.Contains(r, substr) {
			return true
		}
	}
	return false
}

// run executes a CLI command against the fake daemon and returns its stdout,
// stderr and error. Commands return error; the process exit code is applied by
// cmd/idea/main.go, so "exits non-zero" is asserted here as a non-nil error.
func run(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	// initColor() keys off stdout being a terminal; bytes.Buffer is not an
	// *os.File, so styling is off. Set it explicitly anyway: colorEnabled is a
	// package global, and a test that ever passes a real file would otherwise
	// leak color into every later test's golden output.
	colorEnabled = false
	var out, errBuf bytes.Buffer
	err = Run(args, &out, &errBuf)
	return out.String(), errBuf.String(), err
}

// seedInitiative registers the endpoints a command needs to resolve "the
// current initiative" and render a populated project.
func (f *fakeDaemon) seedInitiative() {
	// A field rather than a second registration: net/http.ServeMux panics on a
	// duplicate pattern, so a test wanting a different list sets this before
	// seeding rather than registering its own.
	if f.initiatives == nil {
		f.initiatives = []map[string]any{
			{"id": "ini-1", "title": "Build usage-based billing", "status": "plan_approved"},
		}
	}
	f.mux.HandleFunc("GET /api/v1/projects/{projectId}/initiatives",
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(f.initiatives)
		})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/tasks", []map[string]any{
		{"id": "task-1", "code": "TASK-1", "title": "Implement billing", "priority": "must"},
	})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/change-sets", []map[string]any{
		{"id": "cs-1", "human_id": "CHG-1", "status": "review_required"},
	})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/understanding", map[string]any{
		"requirements": []map[string]any{{"id": "req-1", "code": "REQ-1", "title": "Bill by usage"}},
		"decisions":    []map[string]any{},
		"questions":    []map[string]any{},
	})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/changed", map[string]any{
		"groups": []map[string]any{},
	})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/risk", map[string]any{
		"level": "low", "signals": []string{},
	})
	f.verifyCoverage = map[string]any{
		"status": "complete",
		"coverage": []map[string]any{
			{"code": "REQ-1", "pass": 1, "total": 1, "needs_manual": 0},
		},
	}
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/verify",
		func(w http.ResponseWriter, r *http.Request) {
			// The body is recorded, not just the path: `idea verify REQ-99` used
			// to send `{}` on the read path, silently discarding the argument,
			// and a path-only recorder cannot tell that apart from a correct
			// request.
			raw, _ := io.ReadAll(r.Body)
			f.verifyBodies = append(f.verifyBodies, string(raw))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(f.verifyCoverage)
		})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/diff", map[string]any{
		"requirements": map[string]any{"added": []any{}, "removed": []any{}},
		"decisions":    map[string]any{"open": []any{}, "decided": []any{}, "superseded": []any{}},
	})
}

// seedTrace registers what why/impact/trace need: the object lookup that
// resolves REQ-1, and the two edge queries.
func (f *fakeDaemon) seedTrace() {
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/requirements", []map[string]any{
		{"id": "req-1", "code": "REQ-1", "kind": "requirement", "title": "Bill by usage"},
	})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/why", []map[string]any{
		{"edge_type": "derives_from", "from_label": "EV-1", "to_label": "REQ-1"},
	})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/impact", []map[string]any{
		{"edge_type": "depends_on", "from_label": "REQ-1", "to_label": "TASK-1", "staleness": "fresh"},
	})
}

// status registers a bare status code for a method+pattern, for testing how the
// CLI reacts to a failing daemon.
func (f *fakeDaemon) status(pattern string, code int) {
	f.t.Helper()
	f.mux.HandleFunc(pattern, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
	})
}
