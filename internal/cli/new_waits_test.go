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
	f.json("GET /api/v1/projects/{id}/providers", map[string]any{"demo": false, "providers": []any{}})
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
	f.json("GET /api/v1/projects/{id}/providers", map[string]any{"demo": false, "providers": []any{}})
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
	f.json("GET /api/v1/projects/{id}/providers", map[string]any{"demo": false, "providers": []any{}})
	f.json("GET /api/v1/operations/op-1", map[string]string{"status": "failed", "type": "run_initiative"})

	if _, _, err := run(t, "new", "Build usage-based billing"); err == nil {
		t.Fatal("a failed run reported success")
	}
}
