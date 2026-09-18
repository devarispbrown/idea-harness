package cli

import (
	"strings"
	"testing"
)

// TestDevDetectsARunningDaemonInsteadOfFailingToBind is H2.
//
// `idea dev` used to call runDaemon unconditionally, which binds daemonAddr()
// in the foreground. `idea init` already auto-starts a background daemon on
// that same address, so running `idea dev` afterwards failed with a raw
// `bind: address already in use` -- true, but useless, since the thing `idea
// dev` exists to produce (a daemon serving the UI) already existed. cmdDev
// must detect that and print the UI URL instead of trying to bind again.
func TestDevDetectsARunningDaemonInsteadOfFailingToBind(t *testing.T) {
	newFakeDaemon(t)

	// If cmdDev did not detect the live daemon, it would fall through to
	// runDaemon -> daemon.Run, which is not exercised by this harness at all
	// (it would hang trying to bind a real socket in the test process). A
	// failure here would show up as the test hanging or panicking, not as a
	// clean assertion failure -- which is itself evidence the guard matters.
	stdout, _, err := run(t, "dev")
	if err != nil {
		t.Fatalf("idea dev against a running daemon: %v", err)
	}
	if !strings.Contains(stdout, "already running") {
		t.Errorf("stdout does not say the daemon was already running: %q", stdout)
	}
	if !strings.Contains(stdout, "http://") {
		t.Errorf("stdout does not carry the UI URL: %q", stdout)
	}
}

// TestDaemonStatusPrintsTheUIURL is H2's other half: `idea daemon status`
// answers "is it running", and now also "where do I use it" -- previously
// something you had to already know the port to answer.
func TestDaemonStatusPrintsTheUIURL(t *testing.T) {
	newFakeDaemon(t)

	stdout, _, err := run(t, "daemon", "status")
	if err != nil {
		t.Fatalf("idea daemon status: %v", err)
	}
	if !strings.Contains(stdout, "running") {
		t.Errorf("stdout does not report running: %q", stdout)
	}
	if !strings.Contains(stdout, "web UI:") || !strings.Contains(stdout, "http://") {
		t.Errorf("stdout does not carry the UI URL: %q", stdout)
	}
}

// TestInitPrintsTheUIURL: the first successful `idea init` is the first
// moment the daemon (auto-started by ensureDaemon) is actually running, and
// the web UI is one of the two ways to use it from there.
func TestInitPrintsTheUIURL(t *testing.T) {
	f := newFakeDaemon(t)
	f.json("POST /api/v1/projects", map[string]string{"project_id": f.projectID})

	stdout, _, err := run(t, "init")
	if err != nil {
		t.Fatalf("idea init: %v", err)
	}
	if !strings.Contains(stdout, "web UI:") || !strings.Contains(stdout, "http://") {
		t.Errorf("stdout does not carry the UI URL: %q", stdout)
	}
}
