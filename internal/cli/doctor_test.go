package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestDoctorOutsideAProject pins both halves of a finding that has now moved
// twice, so the next change has to satisfy both at once.
//
// Audit 1 and 2 found doctor printing "✓ config valid" and "all checks passed"
// in any directory: config.Load returns a usable default when there is no
// .idea-harness, so the claim was false. Audit 3 found the fix for that -- an
// error exit -- had broken the other thing doctor is for: README puts
// `idea doctor` under "Verify:" right after the install block and before the
// quick start, so the documented post-install check runs outside a project, and
// a developer who had installed correctly was told it failed.
//
// Both properties, together: say nothing about a config that is not there, and
// do not report a healthy install as a failure.
func TestDoctorOutsideAProject(t *testing.T) {
	f := newFakeDaemon(t)
	_ = f
	// newFakeDaemon chdirs into a temp dir that has no .idea-harness.
	var out bytes.Buffer
	if err := cmdDoctor(&out); err != nil {
		t.Errorf("doctor outside a project returned %v; the documented post-install "+
			"check runs here and the install is fine", err)
	}
	got := out.String()
	for _, claim := range []string{"config valid", "all checks passed"} {
		if strings.Contains(got, claim) {
			t.Errorf("doctor claimed %q with no project present:\n%s", claim, got)
		}
	}
	if !strings.Contains(got, "no idea-harness project") {
		t.Errorf("doctor did not say there is no project here:\n%s", got)
	}
}
