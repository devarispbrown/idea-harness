package cli

import (
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TestWatchStopsOnANotFoundInitiative pins the audit finding: watchModel.poll
// stored the error and returned, so m.done never became true and
// `idea watch CHG-1` (a change-set id, not an initiative) spun at 1 Hz forever,
// then exited 0 on Ctrl-C. A CI job invoking it never terminated.
//
// This drives the model directly rather than through tea.NewProgram, because
// Bubble Tea exits immediately with an error when there is no terminal -- so a
// test that ran the program would pass whether or not the poll loop ever
// learned to stop. (It did, which is how this test was caught being vacuous.)
func TestWatchStopsOnANotFoundInitiative(t *testing.T) {
	// Deliberately not seeded: seedInitiative registers `understanding` on a
	// wildcard that matches any id, so nothing would 404.
	f := newFakeDaemon(t)
	c, err := newClient()
	if err != nil {
		t.Fatal(err)
	}
	m := &watchModel{c: c, projectID: f.projectID, initiativeID: "CHG-1"}

	m.poll()
	if m.fatal == nil {
		t.Fatal("a 404 left fatal unset; the watcher would keep polling forever")
	}
	if _, cmd := m.Update(pollMsg{}); cmd == nil {
		t.Fatal("Update returned no command after a fatal poll; it must quit")
	} else if !isQuit(cmd) {
		t.Error("Update scheduled another poll after a fatal error instead of quitting")
	}
}

// TestWatchToleratesTransientFailures is the other half: a blip during a daemon
// restart must not end a watch, so the give-up rule is a budget, not a hair
// trigger.
func TestWatchToleratesTransientFailures(t *testing.T) {
	f := newFakeDaemon(t)
	c, err := newClient()
	if err != nil {
		t.Fatal(err)
	}
	// 500 rather than 404: retryable in principle.
	f.status("GET /api/v1/projects/{projectId}/initiatives/{id}/understanding", 500)
	m := &watchModel{c: c, projectID: f.projectID, initiativeID: "INI-1"}

	for i := 1; i < maxWatchFailures; i++ {
		m.poll()
		if m.fatal != nil {
			t.Fatalf("gave up after %d transient failures; the budget is %d", i, maxWatchFailures)
		}
	}
	m.poll()
	if m.fatal == nil {
		t.Fatalf("still polling after %d consecutive failures", maxWatchFailures)
	}
}

// isQuit reports whether cmd is tea.Quit.
func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

// TestWatchInitiativeReturnsTheFatalError drives the real program, which the
// previous round's test could not.
//
// That test called poll() on a *watchModel directly. Bubble Tea does not use
// that shape: Update has a value receiver, so the program copies the model and
// carries the returned value, leaving the outer struct untouched. `return
// m.fatal` was therefore always nil and `idea watch CHG-1` exited 0 while
// printing an error — the exact bug the round claimed to fix, shipped with a
// test that passed either way.
//
// tea.WithInput(nil) plus WithoutRenderer makes the program run headlessly, so
// this can assert what the function actually returns.
func TestWatchInitiativeReturnsTheFatalError(t *testing.T) {
	f := newFakeDaemon(t)
	// Deliberately unseeded: every poll 404s.
	c, err := newClient()
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		done <- watchInitiativeWith(c, f.projectID, "CHG-1", io.Discard,
			tea.WithInput(nil), tea.WithoutRenderer())
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("watchInitiative returned nil on a not-found initiative; main.go exits 0")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("watchInitiative did not return; it is still polling")
	}
}
