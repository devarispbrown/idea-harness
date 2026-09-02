package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// scriptedNext answers /next with a sequence of actions, so a test can drive the
// --until-human loop through a whole initiative.
type scriptedNext struct {
	mu      sync.Mutex
	actions []string
	served  int
}

func (s *scriptedNext) next() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.served >= len(s.actions) {
		return s.actions[len(s.actions)-1]
	}
	a := s.actions[s.served]
	s.served++
	return a
}

// seedNextSequence wires the /next script plus the endpoints the loop drives.
// opStatus is the terminal state every operation reports; net/http.ServeMux
// panics on a duplicate pattern, so it is a parameter rather than something a
// test re-registers.
func (f *fakeDaemon) seedNextSequence(script *scriptedNext, opStatus string) {
	f.mux.HandleFunc("GET /api/v1/projects/{projectId}/initiatives/{id}/next",
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"action": script.next()})
		})
	f.json("POST /api/v1/projects/{projectId}/initiatives/{id}/advance", map[string]string{"operation_id": "op-1"})
	f.json("POST /api/v1/projects/{projectId}/initiatives/{id}/execute", map[string]string{"operation_id": "op-2"})
	f.json("GET /api/v1/operations/{id}", map[string]string{"status": opStatus, "type": "advance"})
}

// Without --until-human, next runs exactly one step: the behavior every
// existing caller depends on.
func TestNextRunsOneStepByDefault(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	script := &scriptedNext{actions: []string{"advance", "execute", "done"}}
	f.seedNextSequence(script, "succeeded")

	if _, _, err := run(t, "next"); err != nil {
		t.Fatalf("idea next: %v", err)
	}
	if script.served != 1 {
		t.Fatalf("next asked for %d actions, want 1", script.served)
	}
}

// With --until-human it keeps going through the automatable steps and stops at
// the first judgment point -- the command count the README quick start depends
// on.
func TestNextUntilHumanStopsAtAJudgmentPoint(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	script := &scriptedNext{actions: []string{"advance", "execute", "review CHG-1", "done"}}
	f.seedNextSequence(script, "succeeded")

	stdout, _, err := run(t, "next", "--until-human")
	if err != nil {
		t.Fatalf("idea next --until-human: %v", err)
	}
	if script.served != 3 {
		t.Fatalf("loop consumed %d actions, want 3 (advance, execute, review)", script.served)
	}
	if !strings.Contains(stdout, "review CHG-1") {
		t.Fatalf("loop should stop by naming the judgment point, got: %s", stdout)
	}
	// It must have waited on each async step rather than firing and moving on.
	if !f.asked("/api/v1/operations/op-1") {
		t.Errorf("advance was not waited on: %v", f.requests)
	}
	if !f.asked("/api/v1/operations/op-2") {
		t.Errorf("execute was not waited on: %v", f.requests)
	}
}

// A failed step stops the loop instead of carrying on to the next action.
func TestNextUntilHumanStopsOnAFailedStep(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	script := &scriptedNext{actions: []string{"advance", "execute", "done"}}
	f.seedNextSequence(script, "failed")

	_, _, err := run(t, "next", "--until-human")
	if err == nil {
		t.Fatal("a failed operation should stop the loop with an error")
	}
	if !strings.Contains(err.Error(), "advance failed") {
		t.Fatalf("error should name the step that failed, got: %v", err)
	}
	if script.served != 1 {
		t.Fatalf("loop continued past a failed step (consumed %d actions)", script.served)
	}
}

// An unknown flag on next is rejected, not silently discarded.
func TestNextRejectsUnknownFlags(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()

	_, _, err := run(t, "next", "--nope")
	if err == nil || !strings.Contains(err.Error(), "--nope") {
		t.Fatalf("want an error naming --nope, got %v", err)
	}
	if f.asked("/next") {
		t.Fatalf("unknown flag still reached the daemon: %v", f.requests)
	}
}
