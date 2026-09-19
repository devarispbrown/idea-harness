package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/review"
)

// TestClassifyDiffEvidencePathNotInChangeSet is review B2, adopted #5: a
// finding naming a path that is not among the change set's files is
// advisory, with a note saying so -- deterministically, regardless of
// severity or the reviewer's own in_diff self-report.
func TestClassifyDiffEvidencePathNotInChangeSet(t *testing.T) {
	files := map[string]bool{"src/main.go": true}

	f := domain.Finding{Severity: domain.SeverityCritical, Path: "src/other.go", InDiff: true}
	classifyDiffEvidence(&f, files)
	if !f.Advisory {
		t.Fatal("a path outside the change set must be advisory even at critical severity")
	}
	if f.Note == "" || f.Note != "about code this change set did not touch" {
		t.Fatalf("unexpected note: %q", f.Note)
	}

	// A path that IS one of the change set's files: left alone.
	f2 := domain.Finding{Severity: domain.SeverityCritical, Path: "src/main.go", InDiff: false}
	classifyDiffEvidence(&f2, files)
	if f2.Advisory {
		t.Fatal("a path inside the change set must not be marked advisory")
	}
	if f2.Note != "" {
		t.Fatalf("expected no note when path is verified in-diff, got %q", f2.Note)
	}
}

// TestClassifyDiffEvidenceSelfReportOnlyWithoutPath proves the fallback: no
// path means the reviewer's own in_diff self-report decides, and the note
// always says "unverified" since it is self-reported either way.
func TestClassifyDiffEvidenceSelfReportOnlyWithoutPath(t *testing.T) {
	files := map[string]bool{"src/main.go": true}

	notInDiff := domain.Finding{Severity: domain.SeverityHigh, InDiff: false}
	classifyDiffEvidence(&notInDiff, files)
	if !notInDiff.Advisory {
		t.Fatal("in_diff=false with no path should be advisory")
	}
	if notInDiff.Note != "unverified" {
		t.Fatalf("expected note 'unverified', got %q", notInDiff.Note)
	}

	inDiff := domain.Finding{Severity: domain.SeverityHigh, InDiff: true}
	classifyDiffEvidence(&inDiff, files)
	if inDiff.Advisory {
		t.Fatal("in_diff=true with no path should not be advisory")
	}
	if inDiff.Note != "unverified" {
		t.Fatalf("expected note 'unverified' even when accepted as in-diff, got %q", inDiff.Note)
	}
}

// TestParseFindingsDefaultsInDiffToTrueWhenOmitted proves a reviewer that
// never participates in the in_diff self-report (an older prompt, a
// scripted/demo response with no `in_diff` key) does not have every finding
// it raises silently downgraded to advisory.
func TestParseFindingsDefaultsInDiffToTrueWhenOmitted(t *testing.T) {
	obj := map[string]any{
		"findings": []any{
			map[string]any{
				"severity": "high", "confidence": "high", "category": "x",
				"finding": "f", "recommendation": "r",
			},
		},
	}
	findings, err := review.ParseFindings("run-1", "change_set", "cs-1", obj)
	if err != nil {
		t.Fatal(err)
	}
	if !findings[0].InDiff {
		t.Fatal("InDiff should default to true when the reviewer omits it")
	}
}

// TestChangeSetReviewMarksOutOfDiffFindingAdvisoryEndToEnd runs the real
// change-set review pipeline (runChangeSetAdversarialReview) with a scripted
// finding naming a file the change set never touched, and confirms it does
// not block approval (review B2).
func TestChangeSetReviewMarksOutOfDiffFindingAdvisoryEndToEnd(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	eng.SetWorkRoot(t.TempDir())
	eng.SetWorkspaceRoot(t.TempDir())
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}

	// A blocking (critical) finding about a file this change set never
	// touched, self-reported in_diff:true (a dishonest/mistaken self-report
	// the deterministic path check must override).
	fp.SetResponse(ChangeSetReviewSchemaName, `{"findings":[{`+
		`"severity":"critical","confidence":"high","category":"x",`+
		`"finding":"a problem elsewhere","recommendation":"fix it",`+
		`"path":"totally/unrelated/file.go","in_diff":true`+
		`}]}`)

	if err := eng.ReviewChangeSet(ctx, ini.ID, cs.ID, "approved"); err != nil {
		t.Fatalf("approval should not be blocked by an out-of-diff finding: %v", err)
	}
	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetApproved {
		t.Fatalf("expected approved, got %q", got.Status)
	}

	findings, err := db.ListFindingsByTarget(ctx, "change_set", cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	var scripted *domain.Finding
	for i := range findings {
		if findings[i].Category == "x" {
			scripted = &findings[i]
		}
	}
	if scripted == nil {
		t.Fatalf("expected the scripted out-of-diff finding to be recorded among %+v", findings)
	}
	if !scripted.Advisory {
		t.Fatalf("expected the out-of-diff finding to be marked advisory: %+v", scripted)
	}
}

// TestQuickRigorChangeSetReviewAcceptsWithAdvisories is review B1 applied to
// the M3 change-set review: quick accepts a blocking (non-system-deny)
// finding as advisory rather than refusing the approval.
func TestQuickRigorChangeSetReviewAcceptsWithAdvisories(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{
		Network: config.NetworkDisabled,
		Review:  config.ReviewConfig{MaxCycles: 3, Rigor: "quick"},
	})
	eng.SetWorkRoot(t.TempDir())
	eng.SetWorkspaceRoot(t.TempDir())
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	fp.SetResponse(ChangeSetReviewSchemaName, `{"findings":[{`+
		`"severity":"critical","confidence":"high","category":"x",`+
		`"finding":"a real problem","recommendation":"fix it"`+
		`}]}`)

	if err := eng.ReviewChangeSet(ctx, ini.ID, cs.ID, "approved"); err != nil {
		t.Fatalf("quick rigor should accept despite the blocking finding: %v", err)
	}
	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetApproved {
		t.Fatalf("expected approved, got %q", got.Status)
	}
}
