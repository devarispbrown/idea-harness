package cli

import (
	"strings"
	"testing"
)

// TestNewWaitsForItsOwnOperation pins audit 5's A1.
//
// `idea new` received operation_id and discarded it, so it returned while the
// run was still going. That made the README's own eight-command quick start a
// race: the next command saw the initiative `running`, `idea next
// --until-human` fell through to "workflow running — try `idea watch`" without
// doing anything, and the block went on to print "✓ initiative complete" over
// an initiative with zero change sets and no file written.
//
// Measured before the fix, running the block back-to-back: 5/5 passed with
// provider keys exported and 0/5 without, because ambient keys shifted the
// timing enough to win the race. After it, 5/5 without.
//
// The assertion is that the operation was actually polled — not that some
// sequence of canned responses was consumed, which a fake will satisfy whether
// or not the CLI waits.
func TestNewWaitsForItsOwnOperation(t *testing.T) {
	f := newFakeDaemon(t)
	f.json("POST /api/v1/projects/{id}/initiatives",
		map[string]string{"initiative_id": "ini-1", "operation_id": "op-1"})
	f.json("GET /api/v1/operations/op-1", map[string]string{"status": "succeeded", "type": "run_initiative"})

	stdout, _, err := run(t, "new", "Build usage-based billing")
	if err != nil {
		t.Fatalf("idea new: %v", err)
	}
	if !f.asked("/api/v1/operations/op-1") {
		t.Fatalf("idea new returned without waiting for the run it started; "+
			"the next command in the quick start races it. Requests: %v", f.requests)
	}
	if !strings.Contains(stdout, "idea next") {
		t.Errorf("after waiting, it should name the next command; got: %s", stdout)
	}
}

// TestNewNoWaitReturnsImmediately keeps the fire-and-forget shape available —
// it is what `idea watch` in a second terminal is for, and a long real-provider
// run should not be forced to block.
func TestNewNoWaitReturnsImmediately(t *testing.T) {
	f := newFakeDaemon(t)
	f.json("POST /api/v1/projects/{id}/initiatives",
		map[string]string{"initiative_id": "ini-1", "operation_id": "op-1"})
	f.json("GET /api/v1/operations/op-1", map[string]string{"status": "running", "type": "run_initiative"})

	stdout, _, err := run(t, "new", "Build usage-based billing", "--no-wait")
	if err != nil {
		t.Fatalf("idea new --no-wait: %v", err)
	}
	if f.asked("/api/v1/operations/op-1") {
		t.Errorf("--no-wait polled the operation anyway: %v", f.requests)
	}
	if !strings.Contains(stdout, "idea watch") {
		t.Errorf("--no-wait should point at idea watch; got: %s", stdout)
	}
}

// TestNewSurfacesAFailedRun: waiting is only worth anything if a failure stops
// the command. Returning 0 here would put the old false-success back, one
// command earlier.
func TestNewSurfacesAFailedRun(t *testing.T) {
	f := newFakeDaemon(t)
	f.json("POST /api/v1/projects/{id}/initiatives",
		map[string]string{"initiative_id": "ini-1", "operation_id": "op-1"})
	f.json("GET /api/v1/operations/op-1", map[string]string{"status": "failed", "type": "run_initiative"})

	if _, _, err := run(t, "new", "Build usage-based billing"); err == nil {
		t.Fatal("a failed run reported success")
	}
}

// TestNewReportsTheTerminalState pins audit 7's P1-1.
//
// `idea new` printed `✓ reviewed PRD ready` unconditionally on operation
// success and never read the terminal status, so a PRD parked for human
// judgment was reported as ready while `idea status` said "needs your judgment"
// one command later. That is the same class audit 6 fixed one command over, on
// the path README:179 tells you to expect with a real model.
//
// Both rows also assert exit 0. `idea new` succeeded in both: it created the
// initiative and its run finished. A parked review is the workflow working, and
// a non-zero exit here would be a new defect in a command that previously
// always succeeded.
func TestNewReportsTheTerminalState(t *testing.T) {
	for _, tc := range []struct {
		name    string
		next    map[string]string
		want    string
		notWant string
	}{
		{
			name:    "review converged",
			next:    map[string]string{"action": "advance"},
			want:    "reviewed PRD ready",
			notWant: "did not converge",
		},
		{
			name: "review parked on a human",
			next: map[string]string{"action": "resolve prd"},
			// The judgment text has one source: reportJudgment, which `idea
			// next` uses too, so the two cannot drift.
			want:    "did not converge",
			notWant: "reviewed PRD ready",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDaemon(t)
			f.json("POST /api/v1/projects/{id}/initiatives",
				map[string]string{"initiative_id": "ini-1", "operation_id": "op-1"})
			f.json("GET /api/v1/operations/op-1", map[string]string{"status": "succeeded", "type": "run_initiative"})
			f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/next", tc.next)

			stdout, _, err := run(t, "new", "Build usage-based billing")
			if err != nil {
				t.Fatalf("idea new exited non-zero (%v); the initiative was created and its "+
					"run finished, so this command succeeded", err)
			}
			if !strings.Contains(stdout, tc.want) {
				t.Errorf("stdout does not contain %q:\n%s", tc.want, stdout)
			}
			if strings.Contains(stdout, tc.notWant) {
				t.Errorf("stdout still contains %q, which is not what happened:\n%s", tc.notWant, stdout)
			}
		})
	}
}

// The tail must never drive. Routing it through reportNextAction would have
// made a successful `idea new` POST /advance and start M2 -- spending model
// budget through design before anyone read the PRD, which internal/cli/next.go
// says the CLI deliberately does not do.
func TestNewDoesNotDriveTheWorkflow(t *testing.T) {
	f := newFakeDaemon(t)
	// Seeded so cmdAdvance would actually reach its POST. Without it
	// resolveInitiative fails first with "no initiatives yet" and this test
	// cannot tell driving from not driving -- it passed against a deliberately
	// broken build until the seed was added.
	f.seedInitiative()
	f.json("POST /api/v1/projects/{id}/initiatives",
		map[string]string{"initiative_id": "ini-1", "operation_id": "op-1"})
	f.json("GET /api/v1/operations/op-1", map[string]string{"status": "succeeded", "type": "run_initiative"})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/next", map[string]string{"action": "advance"})

	if _, _, err := run(t, "new", "Build usage-based billing"); err != nil {
		t.Fatalf("idea new: %v", err)
	}
	for _, r := range f.requests {
		if strings.Contains(r, "/advance") {
			t.Fatalf("idea new started the M2 pipeline on its own: %v", f.requests)
		}
	}
}
