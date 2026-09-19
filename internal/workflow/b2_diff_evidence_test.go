package workflow

import (
	"context"
	"strings"
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

	// H2: a path outside the change set no longer demotes deterministically --
	// it falls back to the reviewer's own in_diff self-report, since "no
	// normalized match" is not proof the finding is out-of-diff. Here the
	// reviewer says in_diff:true, so it stays non-advisory.
	f := domain.Finding{Severity: domain.SeverityCritical, Path: "src/other.go", InDiff: true}
	classifyDiffEvidence(&f, files, "")
	if f.Advisory {
		t.Fatal("H2: an unmatched path must fall back to self-report (in_diff:true), not demote")
	}
	if f.Note != "unverified" {
		t.Fatalf("unexpected note: %q", f.Note)
	}

	// The same unmatched path, but the reviewer self-reports NOT in-diff:
	// the self-report fallback marks it advisory.
	fNotInDiff := domain.Finding{Severity: domain.SeverityCritical, Path: "src/other.go", InDiff: false}
	classifyDiffEvidence(&fNotInDiff, files, "")
	if !fNotInDiff.Advisory {
		t.Fatal("an unmatched path with in_diff:false should fall back to advisory")
	}

	// A path that IS one of the change set's files: left alone.
	f2 := domain.Finding{Severity: domain.SeverityCritical, Path: "src/main.go", InDiff: false}
	classifyDiffEvidence(&f2, files, "")
	if f2.Advisory {
		t.Fatal("a path inside the change set must not be marked advisory")
	}
	if f2.Note != "" {
		t.Fatalf("expected no note when path is verified in-diff, got %q", f2.Note)
	}
}

// TestClassifyDiffEvidenceNormalizesPath is review H2: a reviewer-supplied
// path in a different but equivalent form ("./", "a/"/"b/" diff prefixes, a
// trailing ":LINE", a workspace-absolute path) must still match the change
// set's own (normalized) file list, deterministically -- not fall back to
// self-report just because the spelling differs.
func TestClassifyDiffEvidenceNormalizesPath(t *testing.T) {
	files := map[string]bool{"src/main.go": true}

	cases := []struct {
		name      string
		path      string
		workspace string
	}{
		{"dot-slash prefix", "./src/main.go", ""},
		{"a/ diff prefix", "a/src/main.go", ""},
		{"b/ diff prefix", "b/src/main.go", ""},
		{"trailing line number", "src/main.go:42", ""},
		{"workspace-absolute", "/repo/src/main.go", "/repo"},
		{"unclean path", "src/../src/main.go", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := domain.Finding{Severity: domain.SeverityCritical, Path: tc.path, InDiff: false}
			classifyDiffEvidence(&f, files, tc.workspace)
			if f.Advisory {
				t.Fatalf("path %q (workspace %q) should normalize to a match and stay non-advisory", tc.path, tc.workspace)
			}
			if f.Note != "" {
				t.Fatalf("expected no note for a normalized in-diff match, got %q", f.Note)
			}
		})
	}
}

// TestClassifyDiffEvidenceSelfReportOnlyWithoutPath proves the fallback: no
// path means the reviewer's own in_diff self-report decides, and the note
// always says "unverified" since it is self-reported either way.
func TestClassifyDiffEvidenceSelfReportOnlyWithoutPath(t *testing.T) {
	files := map[string]bool{"src/main.go": true}

	notInDiff := domain.Finding{Severity: domain.SeverityHigh, InDiff: false}
	classifyDiffEvidence(&notInDiff, files, "")
	if !notInDiff.Advisory {
		t.Fatal("in_diff=false with no path should be advisory")
	}
	if notInDiff.Note != "unverified" {
		t.Fatalf("expected note 'unverified', got %q", notInDiff.Note)
	}

	inDiff := domain.Finding{Severity: domain.SeverityHigh, InDiff: true}
	classifyDiffEvidence(&inDiff, files, "")
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
// finding naming a file the change set never touched, self-reported
// in_diff:false, and confirms it does not block approval (review B2).
//
// H2: an unmatched path no longer demotes deterministically regardless of
// self-report -- it falls back to the self-report rule. This case uses
// in_diff:false (the reviewer agrees it's out-of-diff), which is the case
// that stays advisory; TestChangeSetReviewUnmatchedPathWithDishonestSelfReportBlocks
// below covers the case that must now block.
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

	// A finding about a file this change set never touched, self-reported
	// in_diff:false -- the self-report and the (failed) deterministic match
	// agree, so it is advisory.
	fp.SetResponse(ChangeSetReviewSchemaName, `{"findings":[{`+
		`"severity":"critical","confidence":"high","category":"x",`+
		`"finding":"a problem elsewhere","recommendation":"fix it",`+
		`"path":"totally/unrelated/file.go","in_diff":false`+
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

// TestChangeSetReviewUnmatchedPathWithDishonestSelfReportBlocks is review H2:
// a path that does not match any file the change set touched must NOT be
// silently demoted to advisory just because it failed to match -- that
// "fail open" behaviour is exactly what let a real in-diff blocker through
// when path normalization didn't line up. When the reviewer self-reports
// in_diff:true for an unmatched path, the self-report decides, and a
// critical finding must block.
func TestChangeSetReviewUnmatchedPathWithDishonestSelfReportBlocks(t *testing.T) {
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

	fp.SetResponse(ChangeSetReviewSchemaName, `{"findings":[{`+
		`"severity":"critical","confidence":"high","category":"x",`+
		`"finding":"a problem elsewhere","recommendation":"fix it",`+
		`"path":"totally/unrelated/file.go","in_diff":true`+
		`}]}`)

	if err := eng.ReviewChangeSet(ctx, ini.ID, cs.ID, "approved"); err == nil {
		t.Fatal("expected approval to be blocked by the self-reported in-diff critical finding")
	}
	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == domain.ChangeSetApproved {
		t.Fatalf("expected the change set to remain unapproved, got %q", got.Status)
	}
}

// TestQuickRigorChangeSetReviewStillBlocksCriticalFindings is review H3: a
// change-set review calibrated to quick still refuses approval on an
// undisposed in-diff critical/high finding. Quick means one review pass and
// a smaller findings cap (findingCap), not "no gate" -- the old blanket
// "mark everything advisory at quick" let a real blocker through with no
// human step at all.
func TestQuickRigorChangeSetReviewStillBlocksCriticalFindings(t *testing.T) {
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
	// No path, in_diff omitted (defaults true, review.ParseFindings): a
	// genuine in-diff critical finding.
	fp.SetResponse(ChangeSetReviewSchemaName, `{"findings":[{`+
		`"severity":"critical","confidence":"high","category":"x",`+
		`"finding":"a real problem","recommendation":"fix it"`+
		`}]}`)

	if err := eng.ReviewChangeSet(ctx, ini.ID, cs.ID, "approved"); err == nil {
		t.Fatal("quick rigor must still block on an undisposed in-diff critical finding")
	}
	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == domain.ChangeSetApproved {
		t.Fatalf("expected the change set to remain unapproved at quick rigor, got %q", got.Status)
	}

	// The human accept path (`--accept-findings`) still works: it disposes
	// the findings explicitly rather than relying on rigor to skip them.
	if err := eng.ReviewChangeSetAccepting(ctx, ini.ID, cs.ID, "approved"); err != nil {
		t.Fatalf("accept-findings should still approve after disposing the finding: %v", err)
	}
	got2, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Status != domain.ChangeSetApproved {
		t.Fatalf("expected approved after accept-findings, got %q", got2.Status)
	}
}

// TestChangeSetReviewerPromptRequiresConcreteEvidence is review ICP re-run
// #3: a real run's only "high" change-set finding was "whitespace
// manipulation" against a value the diff already ran through TrimSpace --
// speculative, with no concrete failure input. The reviewer prompt must tell
// the model to cite a specific line/path and a concrete failure input per
// finding, and to keep speculative concerns at low/advisory.
func TestChangeSetReviewerPromptRequiresConcreteEvidence(t *testing.T) {
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
	if err := eng.ReviewChangeSet(ctx, ini.ID, cs.ID, "approved"); err != nil {
		t.Fatal(err)
	}

	found := false
	for _, call := range fp.Calls() {
		if call.JSONSchemaName != ChangeSetReviewSchemaName {
			continue
		}
		found = true
		if !strings.Contains(call.System, "specific line or path") {
			t.Errorf("change-set reviewer prompt missing the specific line/path requirement:\n%s", call.System)
		}
		if !strings.Contains(call.System, "CONCRETE failure input") {
			t.Errorf("change-set reviewer prompt missing the concrete-failure-input requirement:\n%s", call.System)
		}
		if !strings.Contains(call.System, "speculative") {
			t.Errorf("change-set reviewer prompt missing the speculative-findings-are-advisory guidance:\n%s", call.System)
		}
	}
	if !found {
		t.Fatal("expected a change-set review call")
	}
}

// TestChangeSetReviewFailsWhenPatchCannotLoad is review H2: a patch that
// fails to load must fail the review outright, not silently review against
// an empty file set (which would make EVERY path-bearing finding
// "advisory" via classifyDiffEvidence -- the unsafe direction for a real
// blocker).
func TestChangeSetReviewFailsWhenPatchCannotLoad(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
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

	// Corrupt the change set's patch_digest so loadPatch's blob lookup fails.
	if _, err := db.SQL().Exec(`UPDATE change_sets SET patch_digest = ? WHERE id = ?`, "not-a-real-digest", cs.ID); err != nil {
		t.Fatal(err)
	}

	if err := eng.ReviewChangeSet(ctx, ini.ID, cs.ID, "approved"); err == nil {
		t.Fatal("expected the review to fail when the patch cannot load, not silently succeed")
	}

	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status == domain.ChangeSetApproved {
		t.Fatalf("expected the change set to remain unapproved, got %q", got.Status)
	}
}
