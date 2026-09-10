package cli

import (
	"strings"
	"testing"
)

// TestVerifySendsTheRequirementOnTheReadPath pins the CLI half of audit 7's
// P0-2b.
//
// cmdVerify built its request body only for `<req> --pass|--fail|--waive`. With
// a requirement and no disposition it fell through to `{}`, so the name never
// left the machine: `idea verify REQ-99` on a project holding only REQ-1
// reported `REQ-1 0/1 verified` and never mentioned REQ-99. The write path was
// already correct, so the validation existed and was applied to the opposite
// case from the one that was reported.
//
// Asserted on the request body. A test that only checked the exit code would
// pass on the daemon's answer to a request that never carried the argument.
func TestVerifySendsTheRequirementOnTheReadPath(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()

	if _, _, err := run(t, "verify", "REQ-1"); err != nil {
		t.Fatalf("verify REQ-1: %v", err)
	}
	if len(f.verifyBodies) != 1 {
		t.Fatalf("expected one verify request, got %d", len(f.verifyBodies))
	}
	if !strings.Contains(f.verifyBodies[0], `"requirement":"REQ-1"`) {
		t.Fatalf("the read path dropped its requirement argument; body was %s", f.verifyBodies[0])
	}
}
