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
	// reason accompanies the "failed" action. It lives here rather than in a
	// second handler because net/http.ServeMux panics on a duplicate pattern,
	// so /next can only be registered once per fake daemon.
	reason string
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
			action := script.next()
			body := map[string]string{"action": action}
			if action == "failed" {
				body["failure_reason"] = script.reason
			}
			_ = json.NewEncoder(w).Encode(body)
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
	// Counted as dispatches, not as /next reads. The invariant is that the loop
	// does not run another step after one fails; how many times it *asks* what
	// is next is an implementation detail, and waitForOperation now asks once
	// more on the failure path to recover the initiative's failure_reason.
	dispatched := 0
	for _, req := range f.requests {
		if strings.HasPrefix(req, "POST ") &&
			(strings.HasSuffix(req, "/advance") || strings.HasSuffix(req, "/execute")) {
			dispatched++
		}
	}
	if dispatched != 1 {
		t.Fatalf("loop continued past a failed step (dispatched %d steps, want 1): %v", dispatched, f.requests)
	}
}

// The ordinary post-apply state: the verification gate runs clean but the
// initiative stays unverified until a human records evidence, so /next keeps
// answering "verify". Without no-progress detection the loop ran the gate 50
// times and then exited non-zero blaming the initiative -- on the README quick
// start's own path.
func TestNextUntilHumanStopsWhenAStepChangesNothing(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	script := &scriptedNext{actions: []string{"verify"}} // repeats forever
	f.seedNextSequence(script, "succeeded")
	// The gate runs clean but REQ-1 has no evidence yet, so the initiative does
	// not move -- the state a human is expected to resolve.
	f.verifyCoverage = map[string]any{
		"status": "plan_approved",
		"coverage": []map[string]any{
			{"code": "REQ-1", "pass": 0, "total": 1, "needs_manual": 1},
		},
	}

	stdout, _, err := run(t, "next", "--until-human")
	if err != nil {
		t.Fatalf("waiting on human evidence is not a failure: %v", err)
	}
	if script.served > 3 {
		t.Fatalf("loop ran %d rounds on an action that changes nothing", script.served)
	}
	if !strings.Contains(stdout, "REQ-1") || !strings.Contains(stdout, "--pass") {
		t.Fatalf("should name the requirement and the command that unblocks it, got: %s", stdout)
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

// TestNextOnAFailedInitiativeFails is the CLI half of the A3 defect: `idea next`
// printed "workflow running — try `idea watch`" over a failed initiative and
// returned nil, so it exited 0. `idea execute` points the user at `idea next`
// and `idea watch` needs a TTY, so the recovery loop was closed and reported as
// success.
func TestNextOnAFailedInitiativeFails(t *testing.T) {
	const reason = "framing: openai: status 401: invalid api key"
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.seedNextSequence(&scriptedNext{actions: []string{"failed"}, reason: reason}, "succeeded")

	_, _, err := run(t, "next")
	if err == nil {
		t.Fatal("idea next on a failed initiative returned nil; it must exit non-zero")
	}
	// Both halves matter: the cause, so the reader does not have to go find it,
	// and the way out, because naming a failure without a recovery is what
	// `idea logs` was already doing.
	if !strings.Contains(err.Error(), reason) {
		t.Errorf("error %q does not name the failure reason", err)
	}
	if !strings.Contains(err.Error(), "idea retry") {
		t.Errorf("error %q does not name the recovery command", err)
	}
}

// A failed initiative reached through --until-human must break the loop with the
// same error rather than spinning to the step budget.
func TestUntilHumanStopsOnAFailedInitiative(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	script := &scriptedNext{actions: []string{"advance", "failed"}, reason: "boom"}
	f.seedNextSequence(script, "succeeded")

	_, _, err := run(t, "next", "--until-human")
	if err == nil {
		t.Fatal("next --until-human returned nil over a failed initiative")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error %q does not name the failure reason", err)
	}
	if script.served > 3 {
		t.Errorf("loop asked for %d actions; it should stop at the failure", script.served)
	}
}

// SetInitiativeStatus does not write failure_reason, so `failed` with no reason
// is reachable. The message must still say something and still name the way out
// rather than rendering "failed: ".
func TestFailedWithNoReasonStillNamesTheRecovery(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.seedNextSequence(&scriptedNext{actions: []string{"failed"}, reason: ""}, "succeeded")

	_, _, err := run(t, "next")
	if err == nil {
		t.Fatal("idea next returned nil over a failed initiative with no reason")
	}
	if !strings.Contains(err.Error(), "no reason was recorded") {
		t.Errorf("error %q does not say the reason is missing", err)
	}
	if !strings.Contains(err.Error(), "idea retry") {
		t.Errorf("error %q does not name the recovery command", err)
	}
}

// The running branch is the only one `wait` still reaches. Its message used to
// name `idea watch` alone, which needs a TTY (cmdWatch -> Bubble Tea ->
// /dev/tty) and so named a command that cannot run from a script. Untested, this
// was a mutation that survived the whole suite.
func TestRunningNamesACommandThatWorksWithoutATTY(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.seedNextSequence(&scriptedNext{actions: []string{"wait"}}, "succeeded")

	stdout, _, err := run(t, "next")
	if err != nil {
		t.Fatalf("idea next on a running initiative: %v", err)
	}
	if !strings.Contains(stdout, "idea status") {
		t.Errorf("running message %q does not name a command that works without a TTY", stdout)
	}
}

// A failure that happens INSIDE the --until-human loop is caught by
// waitForOperation, which returns before the loop re-asks /next. Without a
// deliberate re-ask there, the fix did not fire on the path it was written for:
// the README's own documented 401 produced "advance_initiative failed — run
// `idea logs` for details" and the reason the daemon was holding went unsaid
// until a second, bare `idea next`.
func TestFailureDuringTheLoopStillNamesTheReason(t *testing.T) {
	const reason = "framing: openai: status 401: invalid api key"
	f := newFakeDaemon(t)
	f.seedInitiative()
	// advance is dispatched, its operation reports failed, and only then does
	// /next report the initiative as failed.
	f.seedNextSequence(&scriptedNext{actions: []string{"advance", "failed"}, reason: reason}, "failed")

	_, _, err := run(t, "next", "--until-human")
	if err == nil {
		t.Fatal("next --until-human returned nil after its operation failed")
	}
	if !strings.Contains(err.Error(), reason) {
		t.Errorf("error %q does not name the failure reason; it fell back to the operation-level message", err)
	}
}

// The dead end this whole change is about: `idea next` named `idea decisions`
// for every needs_human_judgment cause, and in the one that happens on the
// README's own quick start that command prints "No decisions yet."
func TestResolveNamesTheCommandThatMovesIt(t *testing.T) {
	for _, tc := range []struct {
		action   string
		wants    []string
		notWants []string
	}{
		// notWants carries the -plan pair deliberately: "idea approve" is a
		// substring of "idea approve-plan", so without these the prd arm passes
		// while printing the plan commands, which are wrong for this state.
		{"resolve prd", []string{"idea approve", "idea revise", "idea reviews"},
			[]string{"idea decisions", "idea approve-plan", "idea revise-plan"}},
		// Only regenerate. approve-plan is accepted from this state by the API
		// and would lock in a dependency graph known to be incomplete.
		{"resolve plan-dependencies", []string{"idea revise-plan"}, []string{"idea approve-plan", "idea decisions"}},
		// revise-plan is the only route that dispositions the blocking findings.
		// `idea execute` works from this state and must not be offered: it
		// leaves them open and disables the two commands that would clear them,
		// wedging the completion gate for good.
		{"resolve plan", []string{"idea revise-plan"}, []string{"idea decisions", "accept the plan as it stands", "idea execute"}},
		{"resolve verification", []string{"idea verify"}, []string{"idea approve-plan", "idea decisions"}},
		// A daemon older than the recorded reason. Point at where to look, do
		// not guess a command.
		{"resolve", []string{"idea status"}, []string{"idea decisions"}},
	} {
		t.Run(tc.action, func(t *testing.T) {
			f := newFakeDaemon(t)
			f.seedInitiative()
			f.seedNextSequence(&scriptedNext{actions: []string{tc.action}}, "succeeded")

			stdout, _, err := run(t, "next")
			if err != nil {
				t.Fatalf("idea next: %v", err)
			}
			for _, want := range tc.wants {
				if !strings.Contains(stdout, want) {
					t.Errorf("%q does not name %q", stdout, want)
				}
			}
			for _, no := range tc.notWants {
				if strings.Contains(stdout, no) {
					t.Errorf("%q names %q, which is wrong for this state", stdout, no)
				}
			}
		})
	}
}

// Each of the four verbs must reach its own endpoint. They are one letter apart
// in places (revise / revise-plan) and a mis-wired row would send the user's
// judgment to the wrong milestone.
func TestJudgmentVerbsPostTheRightPath(t *testing.T) {
	for _, tc := range []struct{ cmd, path string }{
		{"approve", "/approve"},
		{"revise", "/revise"},
		{"approve-plan", "/approve-plan"},
		{"revise-plan", "/revise-plan"},
	} {
		t.Run(tc.cmd, func(t *testing.T) {
			f := newFakeDaemon(t)
			f.seedInitiative()
			f.json("POST /api/v1/projects/{projectId}/initiatives/{id}"+tc.path, map[string]string{"status": "approved"})

			if _, _, err := run(t, tc.cmd); err != nil {
				t.Fatalf("idea %s: %v", tc.cmd, err)
			}
			found := false
			for _, req := range f.requests {
				if strings.HasPrefix(req, "POST ") && strings.HasSuffix(req, tc.path) {
					found = true
				}
			}
			if !found {
				t.Errorf("idea %s did not POST %s; sent %v", tc.cmd, tc.path, f.requests)
			}
		})
	}
}

// `idea revise` regenerates the PRD, and ReviseInitiative claims from
// plan_approved and complete too -- so on the CLI, where `idea help` offers it
// unconditionally, a typo silently discards an approved plan. The web UI only
// ever offered it from the Define page.
func TestReviseRefusesToDiscardWorkBelowThePRD(t *testing.T) {
	for _, status := range []string{"plan_approved", "complete"} {
		t.Run(status, func(t *testing.T) {
			f := newFakeDaemon(t)
			f.seedInitiative()
			f.json("GET /api/v1/projects/{projectId}/initiatives/{id}", map[string]string{
				"id": "ini-1", "status": status,
			})
			f.json("POST /api/v1/projects/{projectId}/initiatives/{id}/revise",
				map[string]string{"operation_id": "op-1"})

			_, _, err := run(t, "revise")
			if err == nil {
				t.Fatalf("idea revise on a %s initiative returned nil; it discards the plan", status)
			}
			for _, req := range f.requests {
				if strings.HasPrefix(req, "POST ") && strings.HasSuffix(req, "/revise") {
					t.Fatal("idea revise sent the request despite refusing")
				}
			}
		})
	}
}

// judgmentOperation must WAIT for the operation it starts, so the next command
// has something to act on -- the defect `idea new` was fixed for. The other
// verb tests stub a response with no operation_id and so never reach the wait.
func TestJudgmentVerbsWaitForTheirOperation(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("POST /api/v1/projects/{projectId}/initiatives/{id}/approve-plan",
		map[string]string{"operation_id": "op-7"})
	f.json("GET /api/v1/operations/{id}", map[string]string{"status": "failed", "type": "approve_plan"})

	// A failed operation must surface as an error. If the command returned
	// without polling, this would exit 0.
	if _, _, err := run(t, "approve-plan"); err == nil {
		t.Fatal("idea approve-plan returned nil over a failed operation; it did not wait")
	}
	polled := false
	for _, req := range f.requests {
		if strings.Contains(req, "/operations/op-7") {
			polled = true
		}
	}
	if !polled {
		t.Errorf("idea approve-plan never polled its operation; sent %v", f.requests)
	}
}
