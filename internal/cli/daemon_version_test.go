package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/buildinfo"
)

// staleBuild is a daemon build that is not this binary's.
var staleBuild = buildinfo.Info{Version: "v0.3.0", Commit: "deadbee1234567"}

// inProject gives the fake daemon's working directory a .idea-harness, so
// cmdDoctor runs past the no-project branch and reaches the config, graph and
// "all checks passed" tail. Without it every doctor test stops four lines in and
// the tail is untested — which is exactly what happened: a mutation dropping the
// stale-daemon guard from that condition survived the whole suite.
func inProject(t *testing.T, f *fakeDaemon) {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cwd, ".idea-harness"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cwd, ".idea-harness", "harness.yaml"), []byte("network: disabled\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The graph audit is one of doctor's checks; with no findings it is clean,
	// so the tail's condition turns only on the daemon.
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/doctor", []map[string]any{})
}

// The daemon is detached and outlives the binary that started it, so replacing
// that binary leaves the previous version serving every command. doctor printed
// "✓ daemon running" and "all checks passed" straight through it, in both
// directions, while `idea version` reported the new build.
func TestDoctorFailsOnAStaleDaemon(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	inProject(t, f)
	f.daemonBuild = staleBuild

	stdout, _, err := run(t, "doctor")
	if err == nil {
		t.Fatal("doctor passed against a daemon running a different build")
	}
	if !strings.Contains(stdout, staleBuild.String()) {
		t.Errorf("doctor did not name the daemon's build:\n%s", stdout)
	}
	if !strings.Contains(stdout, buildinfo.Get().String()) {
		t.Errorf("doctor did not name this CLI's build:\n%s", stdout)
	}
	if !strings.Contains(stdout, "idea daemon stop") {
		t.Errorf("doctor did not name the recovery:\n%s", stdout)
	}
	if strings.Contains(stdout, "all checks passed") {
		t.Errorf("doctor claimed all checks passed with a stale daemon:\n%s", stdout)
	}
}

// The README puts `idea doctor` straight after the install block, which is
// exactly where someone verifies an upgrade — and that is usually not inside a
// project. The no-project branch returned nil unconditionally, so both daemon
// failures exited 0 there.
func TestDoctorOutsideAProjectStillReportsADaemonProblem(t *testing.T) {
	f := newFakeDaemon(t)
	f.daemonBuild = staleBuild
	t.Chdir(t.TempDir()) // no .idea-harness here

	stdout, _, err := run(t, "doctor")
	if err == nil {
		t.Fatal("doctor outside a project exited 0 with a stale daemon")
	}
	if !strings.Contains(stdout, "install looks good") {
		t.Errorf("doctor stopped reporting the install:\n%s", stdout)
	}
}

// The check must be quiet when there is nothing wrong, or it is noise on every
// command in normal development.
func TestDoctorPassesWhenTheBuildsMatch(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	inProject(t, f)
	// f.daemonBuild defaults to buildinfo.Get().

	stdout, _, err := run(t, "doctor")
	if err != nil {
		t.Fatalf("doctor failed against a matching daemon: %v\n%s", err, stdout)
	}
	if strings.Contains(stdout, "✗ daemon") {
		t.Errorf("doctor reported a daemon problem against its own build:\n%s", stdout)
	}
}

// A test asserting only prose could be satisfied by a hardcoded string.
func TestDoctorAsksTheDaemonWhatBuildItIs(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	_, _, _ = run(t, "doctor")

	for _, req := range f.requests {
		if strings.Contains(req, "/api/v1/version") {
			return
		}
	}
	t.Fatalf("doctor never asked the daemon for its build; sent %v", f.requests)
}

func TestVersionNamesADifferentDaemonBuild(t *testing.T) {
	f := newFakeDaemon(t)
	f.daemonBuild = staleBuild

	stdout, _, err := run(t, "version")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, staleBuild.String()) {
		t.Errorf("version did not name the daemon's build:\n%s", stdout)
	}
}

// `idea version` is what a bug report copies and what a script may parse; it
// stays one line when there is nothing to say.
func TestVersionIsOneLineWhenTheBuildsMatch(t *testing.T) {
	f := newFakeDaemon(t)
	_ = f

	stdout, _, err := run(t, "version")
	if err != nil {
		t.Fatal(err)
	}
	lines := 0
	for _, l := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if strings.TrimSpace(l) != "" {
			lines++
		}
	}
	if lines != 1 {
		t.Errorf("version printed %d lines against a matching daemon:\n%s", lines, stdout)
	}
}

// `daemon status` answers whether it is running, not whether it is the right
// one. scripts/smoke.sh pipes it under `set -o pipefail`, so a non-zero exit
// here fails the whole smoke run.
func TestDaemonStatusExitsZeroOnAMismatch(t *testing.T) {
	f := newFakeDaemon(t)
	f.daemonBuild = staleBuild

	stdout, _, err := run(t, "daemon", "status")
	if err != nil {
		t.Fatalf("daemon status failed on a build mismatch: %v", err)
	}
	if !strings.Contains(stdout, "running") {
		t.Errorf("daemon status stopped saying it is running:\n%s", stdout)
	}
	if !strings.Contains(stdout, "idea daemon stop") {
		t.Errorf("daemon status did not hint at the mismatch:\n%s", stdout)
	}
}
