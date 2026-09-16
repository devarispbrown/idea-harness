package cli

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestEnsureDaemonReportsAStartFailureFast covers the defect that made `idea
// init` print nothing at all for a code-bound ~51 minutes when a non-daemon
// process held the port.
//
// Two mechanisms, and the test bounds both: healthy() polled with newClient()'s
// 60s timeout (51 probes), and the loop had no way to tell "not ready yet" from
// "already dead", so it polled a corpse. The reason was in daemon.log the whole
// time and the message only named the command that would show it.
//
// daemonExecutable is the seam: under `go test` os.Executable() is the test
// binary, which binds nothing, exits 0, and re-runs this package's tests --
// forking again. Without the seam this path is untestable.
func TestEnsureDaemonReportsAStartFailureFast(t *testing.T) {
	state := t.TempDir()
	t.Setenv("IDEA_HARNESS_STATE", state)
	if err := os.WriteFile(filepath.Join(state, "token"), []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A listener that accepts and never answers: the shape a non-daemon process
	// holding the port presents, and the one a naive probe waits out.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c // hold it open, answer nothing
		}
	}()
	t.Setenv("IDEA_HARNESS_ADDR", ln.Addr().String())

	// Stands in for a daemon that fails to bind: writes the real message to the
	// log the way the daemon's stderr does, then exits non-zero.
	helper := filepath.Join(t.TempDir(), "fake-daemon")
	script := "#!/bin/sh\n" +
		"echo 'idea: listen tcp " + ln.Addr().String() + ": bind: address already in use' >> \"$IDEA_HARNESS_STATE/daemon.log\"\n" +
		"exit 1\n"
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := daemonExecutable
	daemonExecutable = func() (string, error) { return helper, nil }
	defer func() { daemonExecutable = orig }()

	start := time.Now()
	err = ensureDaemon()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("ensureDaemon returned nil against a port held by a non-daemon")
	}
	if elapsed > 10*time.Second {
		t.Errorf("took %s; the defect was ~51 minutes of silence and the fix must be bounded", elapsed)
	}
	if !strings.Contains(err.Error(), "bind: address already in use") {
		t.Errorf("error does not name the bind failure, so the user is told nothing:\n  %v", err)
	}
	// Quoting one line must not replace the route to the rest of the output.
	if !strings.Contains(err.Error(), "idea logs") {
		t.Errorf("error drops the `idea logs` hint, removing the route to the full output:\n  %v", err)
	}
}

// TestEnsureDaemonSucceedsWhenTheDaemonAnswers guards the happy path against the
// child-watch: waiting on a Setsid child must not turn a working start into a
// failure.
func TestEnsureDaemonSucceedsWhenTheDaemonAnswers(t *testing.T) {
	newFakeDaemon(t)

	orig := daemonExecutable
	daemonExecutable = func() (string, error) {
		t.Error("ensureDaemon spawned a daemon although one was already answering")
		return "", nil
	}
	defer func() { daemonExecutable = orig }()

	if err := ensureDaemon(); err != nil {
		t.Fatalf("ensureDaemon against a live daemon: %v", err)
	}
}

// TestEnsureDaemonDoesNotQuoteAPreviousRun guards the repair against being
// worse than what it replaced.
//
// daemon.log is opened O_APPEND and never rotated, so "the last line of the
// log" is whatever failed last — possibly weeks ago, possibly a routine INFO
// line from a start that succeeded. Quoting that as the cause of *this* failure
// is a confidently wrong message, where the vague one it replaced was merely
// unhelpful.
func TestEnsureDaemonDoesNotQuoteAPreviousRun(t *testing.T) {
	state := t.TempDir()
	t.Setenv("IDEA_HARNESS_STATE", state)
	if err := os.WriteFile(filepath.Join(state, "token"), []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}
	const stale = "idea: listen tcp 127.0.0.1:9999: bind: address already in use"
	if err := os.WriteFile(filepath.Join(state, "daemon.log"), []byte(stale+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("IDEA_HARNESS_ADDR", ln.Addr().String())

	// Exits non-zero writing nothing, the way a daemon failing for a reason it
	// did not log would.
	helper := filepath.Join(t.TempDir(), "silent-daemon")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := daemonExecutable
	daemonExecutable = func() (string, error) { return helper, nil }
	defer func() { daemonExecutable = orig }()

	err = ensureDaemon()
	if err == nil {
		t.Fatal("expected a failure")
	}
	if strings.Contains(err.Error(), stale) {
		t.Errorf("the message quotes a PREVIOUS run's log line as this run's cause:\n  %v", err)
	}
	if !strings.Contains(err.Error(), "idea logs") {
		t.Errorf("the message drops the route to the real output:\n  %v", err)
	}
}

// TestEnsureDaemonSpawnsAndComesUpHealthy is the happy path of the code this
// change actually touched.
//
// TestEnsureDaemonSucceedsWhenTheDaemonAnswers returns on ensureDaemon's first
// line and reaches none of it. If cmd.Wait() misbehaved on a Setsid child, or
// the child-exit select were ordered wrongly, that test would stay green — so
// this one spawns a child, has it become healthy, and asserts success.
func TestEnsureDaemonSpawnsAndComesUpHealthy(t *testing.T) {
	state := t.TempDir()
	t.Setenv("IDEA_HARNESS_STATE", state)
	if err := os.WriteFile(filepath.Join(state, "token"), []byte("t"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Unhealthy until the spawned child signals it has "started", which is what
	// makes the poll loop actually run.
	ready := filepath.Join(t.TempDir(), "ready")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat(ready); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"ok"}`)
	}))
	defer srv.Close()
	t.Setenv("IDEA_HARNESS_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	// Comes up and stays up, like a real daemon: if it exited, the child-watch
	// would end the loop and this would prove nothing.
	helper := filepath.Join(t.TempDir(), "good-daemon")
	script := "#!/bin/sh\ntouch " + ready + "\nsleep 30\n"
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := daemonExecutable
	daemonExecutable = func() (string, error) { return helper, nil }
	defer func() { daemonExecutable = orig }()

	if err := ensureDaemon(); err != nil {
		t.Fatalf("ensureDaemon failed on a daemon that started and became healthy: %v", err)
	}
}

// TestEnsureDaemonCapturesTheLogOnAVeryFirstRun covers the one moment the
// failure message most needs to be specific.
//
// ensureDaemon redirects the daemon's stderr into daemon.log, but only if it can
// open the file — and on the very first `idea init` the state root does not
// exist yet, so OpenFile returned ENOENT, the redirect silently did not happen,
// and a start failure produced the generic message pointing at `idea logs` over
// an empty log. Measured: 0.1s with no cause, where a second run gave the full
// bind error.
func TestEnsureDaemonCapturesTheLogOnAVeryFirstRun(t *testing.T) {
	// A state root that does not exist yet — not t.TempDir(), which does.
	state := filepath.Join(t.TempDir(), "never-created")
	t.Setenv("IDEA_HARNESS_STATE", state)
	if _, err := os.Stat(state); !os.IsNotExist(err) {
		t.Fatalf("precondition: %s already exists, so this tests nothing", state)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	t.Setenv("IDEA_HARNESS_ADDR", ln.Addr().String())

	helper := filepath.Join(t.TempDir(), "failing-daemon")
	script := "#!/bin/sh\necho 'idea: listen tcp: bind: address already in use' >&2\nexit 1\n"
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := daemonExecutable
	daemonExecutable = func() (string, error) { return helper, nil }
	defer func() { daemonExecutable = orig }()

	err = ensureDaemon()
	if err == nil {
		t.Fatal("expected a start failure")
	}
	if !strings.Contains(err.Error(), "bind: address already in use") {
		t.Errorf("the very first run reports no cause, which is when the user has least to go on:\n  %v", err)
	}
}
