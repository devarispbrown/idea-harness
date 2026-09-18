package cli

import (
	"strings"
	"testing"
)

// TestStopHintNamesThePortNotAGlobalPattern is L: `idea daemon stop`'s
// no-pidfile message used to suggest `pkill -f 'idea daemon'`, which kills
// every idea-harness daemon on the machine matching that command-line
// pattern -- every project, every worktree -- not just the one this CLI is
// even pointed at. The hint must instead target the one process actually
// listening on this daemon's own port.
func TestStopHintNamesThePortNotAGlobalPattern(t *testing.T) {
	t.Setenv("IDEA_HARNESS_ADDR", "127.0.0.1:54321")
	hint := stopHint()
	if !strings.Contains(hint, "54321") {
		t.Errorf("stopHint() = %q, does not name the port", hint)
	}
	if strings.Contains(hint, "pkill") {
		t.Errorf("stopHint() = %q, still suggests a pattern-matching pkill", hint)
	}
}

// TestDaemonStopWithNoPidfileNamesThePreciseProcess is the same defect at the
// command level: a daemon answering with no pidfile (the state directory was
// removed while it ran) must refuse with the precise hint, not the old
// blanket pkill suggestion.
func TestDaemonStopWithNoPidfileNamesThePreciseProcess(t *testing.T) {
	newFakeDaemon(t) // healthy daemon; this fixture's state dir carries no pidfile

	_, _, err := run(t, "daemon", "stop")
	if err == nil {
		t.Fatal("daemon stop with no pidfile succeeded; want the descriptive refusal")
	}
	if strings.Contains(err.Error(), "pkill") {
		t.Errorf("error still suggests pkill -f 'idea daemon': %v", err)
	}
	if !strings.Contains(err.Error(), "lsof") {
		t.Errorf("error does not name a precise way to find the process: %v", err)
	}
}
