package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// TestAuthorizeExecutorWriteTrustDeny: the trust_deny code is produced by the
// write_file gate. Every kind selectExecutor can build passes it; an unknown
// kind (one added without a TrustTierFor case) is refused with the code.
func TestAuthorizeExecutorWriteTrustDeny(t *testing.T) {
	for _, kind := range []string{"internal", "container", "omp", "hermes", "acp", "openhands"} {
		if err := authorizeExecutorWrite(kind); err != nil {
			t.Errorf("%s: %v", kind, err)
		}
	}
	err := authorizeExecutorWrite("acp-remote")
	var deny *SystemDenyError
	if !errors.As(err, &deny) || deny.Code != domain.DenyCodeTrustDeny {
		t.Fatalf("unknown executor kind: got %v, want a trust_deny SystemDenyError", err)
	}
	if !strings.Contains(err.Error(), "system deny trust_deny") {
		t.Errorf("message should name the code: %v", err)
	}
}

func findingsOfCategory(t *testing.T, db *store.DB, csID, category string) []domain.Finding {
	t.Helper()
	all, err := db.ListFindingsByTarget(context.Background(), "change_set", csID)
	if err != nil {
		t.Fatal(err)
	}
	var out []domain.Finding
	for _, f := range all {
		if f.Category == category {
			out = append(out, f)
		}
	}
	return out
}

// TestExecutePIIBlocksApprovalUntilDispositioned: a real-looking card number
// in produced content raises a pii-coded finding that blocks approval --
// including --accept-findings, which never signs off a coded finding -- until
// a human dispositions that one finding.
func TestExecutePIIBlocksApprovalUntilDispositioned(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()
	// The internal executor writes the task title into hello.txt.
	task := &domain.Task{InitiativeID: iniID, Title: "Seed customer 4539 1488 0343 6467", Statement: "x", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("PII must block approval, not reject: status %q", cs.Status)
	}
	pii := findingsOfCategory(t, db, cs.ID, "pii")
	if len(pii) != 1 {
		t.Fatalf("want one pii finding, got %+v", pii)
	}
	f := pii[0]
	if f.Code != domain.DenyCodePII || f.Advisory || !f.Severity.Blocking() {
		t.Fatalf("pii finding should be blocking and coded: %+v", f)
	}
	for _, s := range []string{f.Finding, f.Evidence, f.Recommendation} {
		if strings.Contains(s, "4539") || strings.Contains(s, "0343") {
			t.Fatalf("the finding carries the card number: %q", s)
		}
	}
	if !strings.Contains(f.Finding, "hello.txt:") {
		t.Errorf("finding should name path:line, got %q", f.Finding)
	}

	err = eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved")
	if err == nil || !strings.Contains(err.Error(), "pii") || !strings.Contains(err.Error(), "accepted_risk") {
		t.Fatalf("approve should be blocked by the pii finding and say how to clear it, got %v", err)
	}
	if err := eng.ReviewChangeSetAccepting(ctx, iniID, cs.ID, "approved"); err == nil {
		t.Fatal("--accept-findings must not sign off a pii finding")
	}
	if err := db.SetFindingDisposition(ctx, f.ID, domain.DispositionAcceptedRisk, store.HumanActor); err != nil {
		t.Fatal(err)
	}
	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err != nil {
		t.Fatalf("after a human accepted the pii finding, approve should pass: %v", err)
	}
}

// TestProtectedPathRequiresExplicitSignOff: a change set touching an
// auto-executing file gets one blocking protected_path finding. A plain
// approve is refused; --accept-findings (an explicit, recorded human sign-off)
// lets it through -- the false-positive policy for an initiative that really
// is about CI.
func TestProtectedPathRequiresExplicitSignOff(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()
	task := &domain.Task{InitiativeID: iniID, Title: "Add CI", Statement: "x", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if got := findingsOfCategory(t, db, cs.ID, "protected_path"); len(got) != 0 {
		t.Fatalf("hello.txt is not protected, got %+v", got)
	}
	files := []domain.FileChange{
		{Path: "hello.txt", Digest: "d1", ChangeType: domain.FileAdded},
		{Path: ".github/workflows/ci.yml", Digest: "d2", ChangeType: domain.FileAdded},
		{Path: ".husky/pre-commit", Digest: "d3", BaseDigest: "b3", ChangeType: domain.FileModified},
	}
	if err := eng.recordProtectedPathFinding(ctx, &cs, files); err != nil {
		t.Fatal(err)
	}
	got := findingsOfCategory(t, db, cs.ID, "protected_path")
	if len(got) != 1 {
		t.Fatalf("want one protected_path finding, got %+v", got)
	}
	f := got[0]
	if f.Code != "" || f.Advisory || !f.Severity.Blocking() {
		t.Fatalf("protected_path should block and carry no deny code: %+v", f)
	}
	for _, want := range []string{".github/workflows/ci.yml", "GitHub Actions workflow", ".husky/pre-commit", "--accept-findings"} {
		if !strings.Contains(f.Finding+f.Recommendation, want) {
			t.Errorf("finding should mention %q: %+v", want, f)
		}
	}
	if strings.Contains(f.Finding, "hello.txt") {
		t.Errorf("an ordinary file was listed: %q", f.Finding)
	}

	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err == nil {
		t.Fatal("a plain approve must not pass over a protected_path finding")
	}
	if err := eng.ReviewChangeSetAccepting(ctx, iniID, cs.ID, "approved"); err != nil {
		t.Fatalf("an explicit --accept-findings sign-off should approve: %v", err)
	}
	after := findingsOfCategory(t, db, cs.ID, "protected_path")[0]
	if after.Disposition != domain.DispositionAcceptedRisk || after.DispositionActor != store.HumanActor {
		t.Fatalf("the sign-off should be recorded as a human accepted_risk: %+v", after)
	}
}

// TestApplyDeniesPathEscape: between execute and apply, the workspace gained
// a symlink at the change set's path pointing outside the project. Apply
// refuses with path_escape before writing anything, records the finding,
// rejects the change set and reopens its task.
func TestApplyDeniesPathEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()
	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "x", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err != nil {
		t.Fatal(err)
	}

	outside := filepath.Join(t.TempDir(), "host-file")
	if err := os.WriteFile(outside, []byte("do not touch"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(eng.workspaceRoot, "hello.txt")); err != nil {
		t.Fatal(err)
	}

	err = eng.ApplyChangeSet(ctx, iniID, cs.ID)
	var deny *SystemDenyError
	if !errors.As(err, &deny) || deny.Code != domain.DenyCodePathEscape {
		t.Fatalf("want a path_escape SystemDenyError, got %v", err)
	}
	if b, _ := os.ReadFile(outside); string(b) != "do not touch" {
		t.Fatalf("the file outside the workspace was written: %q", b)
	}
	got := findingsOfCategory(t, db, cs.ID, "path_escape")
	if len(got) != 1 || got[0].Code != domain.DenyCodePathEscape || !strings.Contains(got[0].Finding, "hello.txt") {
		t.Fatalf("want one path_escape finding naming hello.txt, got %+v", got)
	}
	after, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Status != domain.ChangeSetRejected {
		t.Errorf("status = %q, want rejected", after.Status)
	}
	tk, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tk.Status != domain.TaskProposed {
		t.Errorf("task status = %q, want proposed so it can run again", tk.Status)
	}
}
