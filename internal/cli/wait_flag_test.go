package cli

import (
	"strings"
	"testing"
)

// This file covers `--wait`, opt-in on advance/execute/apply/revert: the DX
// finding that `idea new` blocks and streams progress by default while these
// four return a handle instantly. Both defaults stay (waitIfRequested's own
// doc comment explains why flipping `idea new`'s default would reopen a real
// race), but every long action now understands the same --wait/--no-wait
// vocabulary.

func TestAdvanceWithoutWaitReturnsImmediately(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("POST /api/v1/projects/{projectId}/initiatives/{id}/advance",
		map[string]string{"operation_id": "op-1"})
	// No GET /operations/op-1 registered: if the command polled it anyway,
	// the fake daemon 404s and the command would fail.

	stdout, _, err := run(t, "advance")
	if err != nil {
		t.Fatalf("idea advance: %v", err)
	}
	if !strings.Contains(stdout, "advance started") {
		t.Errorf("expected the handle confirmation:\n%s", stdout)
	}
	if !strings.Contains(stdout, "idea watch") {
		t.Errorf("expected the watch hint:\n%s", stdout)
	}
}

func TestAdvanceWaitBlocksUntilSucceeded(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("POST /api/v1/projects/{projectId}/initiatives/{id}/advance",
		map[string]string{"operation_id": "op-1"})
	f.json("GET /api/v1/operations/op-1", map[string]string{"status": "succeeded", "type": "advance_initiative"})

	stdout, _, err := run(t, "advance", "--wait")
	if err != nil {
		t.Fatalf("idea advance --wait: %v", err)
	}
	if !f.asked("GET /api/v1/operations/op-1") {
		t.Errorf("--wait did not poll the operation; requests: %v", f.requests)
	}
	if !strings.Contains(stdout, "done") {
		t.Errorf("expected a completion line:\n%s", stdout)
	}
}

func TestAdvanceWaitSurfacesFailure(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("POST /api/v1/projects/{projectId}/initiatives/{id}/advance",
		map[string]string{"operation_id": "op-1"})
	f.json("GET /api/v1/operations/op-1", map[string]string{"status": "failed", "type": "advance_initiative"})

	_, _, err := run(t, "advance", "--wait")
	if err == nil {
		t.Fatal("idea advance --wait: want an error when the operation fails, got nil")
	}
}

func TestExecuteWaitBlocksUntilSucceeded(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("POST /api/v1/projects/{projectId}/initiatives/{id}/execute",
		map[string]string{"operation_id": "op-2", "executor_kind": "hermes"})
	f.json("GET /api/v1/operations/op-2", map[string]string{"status": "succeeded", "type": "execute_initiative"})

	stdout, _, err := run(t, "execute", "--wait")
	if err != nil {
		t.Fatalf("idea execute --wait: %v", err)
	}
	if !f.asked("GET /api/v1/operations/op-2") {
		t.Errorf("--wait did not poll the operation; requests: %v", f.requests)
	}
	if !strings.Contains(stdout, "done") {
		t.Errorf("expected a completion line:\n%s", stdout)
	}
}

func TestExecuteWithoutWaitReturnsImmediately(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("POST /api/v1/projects/{projectId}/initiatives/{id}/execute",
		map[string]string{"operation_id": "op-2", "executor_kind": "hermes"})

	stdout, _, err := run(t, "execute")
	if err != nil {
		t.Fatalf("idea execute: %v", err)
	}
	if !strings.Contains(stdout, "idea watch") {
		t.Errorf("expected the watch hint:\n%s", stdout)
	}
}

func TestApplyWaitBlocksUntilSucceeded(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("POST /api/v1/projects/{projectId}/change-sets/CHG-1/apply",
		map[string]string{"operation_id": "op-3"})
	f.json("GET /api/v1/operations/op-3", map[string]string{"status": "succeeded", "type": "apply_change_set"})

	stdout, _, err := run(t, "apply", "CHG-1", "--wait")
	if err != nil {
		t.Fatalf("idea apply CHG-1 --wait: %v", err)
	}
	if !f.asked("GET /api/v1/operations/op-3") {
		t.Errorf("--wait did not poll the operation; requests: %v", f.requests)
	}
	if !strings.Contains(stdout, "done") {
		t.Errorf("expected a completion line:\n%s", stdout)
	}
}

func TestApplyWithoutWaitReturnsImmediately(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("POST /api/v1/projects/{projectId}/change-sets/CHG-1/apply",
		map[string]string{"operation_id": "op-3"})

	stdout, _, err := run(t, "apply", "CHG-1")
	if err != nil {
		t.Fatalf("idea apply CHG-1: %v", err)
	}
	if !strings.Contains(stdout, "apply started") {
		t.Errorf("expected the handle confirmation:\n%s", stdout)
	}
}

// TestNewAcceptsWaitAsANoOp: `idea new` already waits by default, and --wait
// is accepted as an explicit restatement of that default rather than an
// unknown flag, so a script can spell "wait for this" consistently across
// every long action.
func TestNewAcceptsWaitAsANoOp(t *testing.T) {
	f := newFakeDaemon(t)
	f.json("POST /api/v1/projects/{projectId}/initiatives",
		map[string]string{"initiative_id": "ini-1", "operation_id": "op-4"})
	f.json("GET /api/v1/operations/op-4", map[string]string{"status": "succeeded", "type": "run_initiative"})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}", map[string]string{"status": "approved"})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/decisions", []map[string]any{})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/questions", []map[string]any{})

	_, _, err := run(t, "new", "an idea", "--wait")
	if err != nil {
		t.Fatalf("idea new \"an idea\" --wait: %v", err)
	}
}
