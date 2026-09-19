package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/review"
	"github.com/devarispbrown/idea-harness/internal/security"
)

// TestSplitCredentialMatchesSeparatesAdvisory pins splitCredentialMatches'
// contract (ICP re-run defect #1): an Advisory match (a generic
// "credential assignment" match in a test/fixture path) never lands in
// blocking, whatever else is in the slice.
func TestSplitCredentialMatchesSeparatesAdvisory(t *testing.T) {
	matches := []security.CredentialFinding{
		{Path: "a_test.go", Kind: "credential assignment", Advisory: true},
		{Path: "config.json", Kind: "Stripe secret key", Advisory: false},
		{Path: "b_test.go", Kind: "AWS access key ID", Advisory: false},
	}
	blocking, advisory := splitCredentialMatches(matches)
	if len(advisory) != 1 || advisory[0].Path != "a_test.go" {
		t.Fatalf("advisory = %+v, want exactly the a_test.go match", advisory)
	}
	if len(blocking) != 2 {
		t.Fatalf("blocking = %+v, want the 2 non-advisory matches", blocking)
	}
}

// TestAdvisorySecretFindingNeverBlocksApproval reproduces the effect of the
// ICP re-run's own defect #1 case 1 downstream of the sweep: a
// createAdvisorySecretFinding record is Critical-shaped (a real "looks like a
// secret" finding, still worth a human's attention) but must never block
// review.EvaluateApproval or denyReviewOpenBlocking, because Finding.Advisory
// is set (mirroring review B2's "about code this change set did not touch").
// Mutation-check: drop `Advisory: true` from createAdvisorySecretFinding, and
// this fails.
func TestAdvisorySecretFindingNeverBlocksApproval(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}

	m := security.CredentialFinding{
		Path:        "handlers_test.go",
		Line:        3,
		Kind:        "credential assignment",
		Fingerprint: "fp-advisory-test",
		Overridable: true,
		Preview:     "k3jQ****",
		Advisory:    true,
	}
	if err := eng.createAdvisorySecretFinding(ctx, iniID, cs.ID, m); err != nil {
		t.Fatal(err)
	}

	findings, err := db.ListFindingsByTarget(ctx, "change_set", cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	var f *domain.Finding
	for i := range findings {
		if findings[i].SecretFingerprint == "fp-advisory-test" {
			f = &findings[i]
		}
	}
	if f == nil {
		t.Fatal("expected the advisory secret_sweep finding to be recorded")
	}
	if !f.Advisory {
		t.Fatal("expected Finding.Advisory = true")
	}
	if f.Category != "secret_sweep" {
		t.Errorf("Category = %q, want secret_sweep", f.Category)
	}
	if f.Disposition == domain.DispositionRejected {
		t.Error("an advisory match must not be pre-dispositioned rejected -- that would read as a closed, denied finding")
	}

	if blocked, reason := review.EvaluateApproval(findings); blocked {
		t.Errorf("EvaluateApproval blocked on an advisory-only finding: %s", reason)
	}
	if err := eng.denyReviewOpenBlocking(ctx, cs.ID); err != nil {
		t.Errorf("denyReviewOpenBlocking blocked on an advisory-only finding: %v", err)
	}
}
