package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDoctorReportsARunningDaemonItCannotAuthenticateTo covers the state a
// wiped state directory leaves behind.
//
// healthy() calls newClient(), which fails when the token file is gone — so it
// returns false before any HTTP happens and doctor said "daemon: not running"
// while curl got a 200 from that same daemon and `idea daemon status` said
// running. Three answers to one question, and the designated diagnostic had the
// wrong one: it sent people to start a daemon that was already serving.
func TestDoctorReportsARunningDaemonItCannotAuthenticateTo(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	state := t.TempDir() // deliberately has no token file
	t.Setenv("IDEA_HARNESS_STATE", state)
	t.Setenv("OPENAI_API_KEY", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	t.Setenv("IDEA_HARNESS_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	if err := os.MkdirAll(filepath.Join(cwd, ".idea-harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".idea-harness", "harness.yaml"),
		[]byte("network: disabled\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stdout, _, err := run(t, "doctor")
	if strings.Contains(stdout, "not running") {
		t.Errorf("doctor says the daemon is not running while it answers /health:\n%s", stdout)
	}
	if !strings.Contains(stdout, "no token") {
		t.Errorf("doctor does not name the real problem (this CLI has no token):\n%s", stdout)
	}
	if err == nil {
		t.Error("doctor exited 0 over a daemon every other command gets `unauthorized` from")
	}
}

// TestDoctorTreatsAnUnreadableStoreAsAFailure keeps a read failure from being
// rendered as an absence.
//
// resolveInitiative returns a transport/store error and "no initiatives yet"
// through the same error value, so doctor printed "graph audit: no initiative
// yet — nothing to audit" over an unreadable store and reported all checks
// passed, while every other command exited 1 on the same state.
func TestDoctorTreatsAnUnreadableStoreAsAFailure(t *testing.T) {
	f := newFakeDaemon(t)
	t.Setenv("OPENAI_API_KEY", "")
	// doctor checks the directory before the graph, so the cwd has to look like
	// a project for the graph audit to be reached at all.
	cwd, _ := os.Getwd()
	if err := os.MkdirAll(filepath.Join(cwd, ".idea-harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".idea-harness", "harness.yaml"),
		[]byte("network: disabled\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.fail("GET /api/v1/projects/{projectId}/initiatives", http.StatusInternalServerError,
		"internal error (correlation id: deadbeef)")

	stdout, _, err := run(t, "doctor")
	if strings.Contains(stdout, "no initiative yet") {
		t.Errorf("an unreadable store is rendered as an absence:\n%s", stdout)
	}
	if !strings.Contains(stdout, "graph audit") {
		t.Errorf("doctor does not report the graph audit at all:\n%s", stdout)
	}
	if err == nil {
		t.Error("doctor exited 0 although listing initiatives failed")
	}
}
