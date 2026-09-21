package cli

import (
	"strings"
	"testing"
)

// TestVerifyJSONExitsNonZeroOnUnmetGate pins H10 (release-gate report): `idea
// verify --json` returned before the failOnUnmet gate at the bottom of
// runVerify, so the ONLY machine-readable form of this command always
// exited 0 -- exactly backwards from docs/demo-greenfield.md's promise that
// "an unmet gate exits non-zero, so `idea verify` is usable in a script."
// The JSON payload itself is unchanged; only the exit code is at stake here.
func TestVerifyJSONExitsNonZeroOnUnmetGate(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	// Override the default (fully-verified) coverage seedInitiative sets up:
	// REQ-1 has 0 of 1 criteria passed, which is exactly what a script needs
	// this command to fail on.
	f.verifyCoverage = map[string]any{
		"status": "in_progress",
		"coverage": []map[string]any{
			{"code": "REQ-1", "pass": 0, "total": 1, "needs_manual": 0},
		},
	}

	stdout, _, err := run(t, "verify", "--json")
	if err == nil {
		t.Fatal("idea verify --json on an unmet gate returned nil error; want a non-zero exit")
	}
	if !strings.Contains(err.Error(), "REQ-1") {
		t.Fatalf("error does not name the unmet requirement: %v", err)
	}
	// The JSON payload itself must still be printed -- the fix is the exit
	// code, not a change to what gets dumped.
	if !strings.Contains(stdout, `"status"`) || !strings.Contains(stdout, `"coverage"`) {
		t.Fatalf("expected the JSON coverage payload on stdout, got %q", stdout)
	}
}

// TestVerifyJSONExitsZeroOnMetGate is the mutation guard for the fix above: a
// gate that IS fully met must still exit 0 under --json, so the fix cannot be
// "always fail --json" instead of "apply the same gate as the human path".
func TestVerifyJSONExitsZeroOnMetGate(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	// seedInitiative's default verifyCoverage already reports REQ-1 fully
	// verified (1/1); asserted here so the intent is explicit at the call
	// site rather than relying on another test's fixture default.
	f.verifyCoverage = map[string]any{
		"status": "complete",
		"coverage": []map[string]any{
			{"code": "REQ-1", "pass": 1, "total": 1, "needs_manual": 0},
		},
	}

	stdout, _, err := run(t, "verify", "--json")
	if err != nil {
		t.Fatalf("idea verify --json on a fully met gate: %v", err)
	}
	if !strings.Contains(stdout, `"complete"`) || !strings.Contains(stdout, `"coverage"`) {
		t.Fatalf("expected the JSON coverage payload on stdout, got %q", stdout)
	}
}

// TestVerifyRunVerifyRespectsFailOnUnmetFalse guards the OTHER caller of
// runVerify: `idea next --until-human` passes failOnUnmet=false because an
// unmet gate there is the human judgment point, not a failure. The H10 fix
// must not have accidentally made --json fail unconditionally regardless of
// which caller it came from.
func TestVerifyRunVerifyRespectsFailOnUnmetFalse(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.verifyCoverage = map[string]any{
		"status": "in_progress",
		"coverage": []map[string]any{
			{"code": "REQ-1", "pass": 0, "total": 1, "needs_manual": 0},
		},
	}

	jsonArgs := cmdArgs{flags: parsedFlags{bools: map[string]bool{"--json": true}}}
	var out, errBuf strings.Builder
	if err := runVerify(jsonArgs, &out, &errBuf, false); err != nil {
		t.Fatalf("runVerify with failOnUnmet=false on an unmet gate: %v", err)
	}
	if !strings.Contains(out.String(), `"coverage"`) {
		t.Fatalf("expected the JSON coverage payload, got %q", out.String())
	}
}
