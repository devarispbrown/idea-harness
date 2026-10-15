package workflow

import (
	"context"
	"sort"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
)

// planState is what a revision must not disturb when it changes nothing: how
// many versions the execution plan has, and the codes its tasks carry.
func planState(t *testing.T, eng *Engine, iniID string) (versions int, latest int, codes []string) {
	t.Helper()
	ctx := context.Background()
	arts, err := eng.db.ListArtifacts(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	families := map[string]bool{}
	for _, a := range arts {
		if a.Kind == domain.ArtifactExecutionPlan {
			versions++
			families[a.ArtifactID] = true
			if a.Version > latest {
				latest = a.Version
			}
		}
	}
	if len(families) > 1 {
		t.Fatalf("the execution plan has %d artifact families; a revision must add a version, not a family", len(families))
	}
	tasks, err := eng.db.ListTasks(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range tasks {
		codes = append(codes, tk.Code)
	}
	sort.Strings(codes)
	return versions, latest, codes
}

// TestARevisionThatChangesNothingKeepsTheVersionAndTaskCodes pins the plan
// half of the "Ask for a revision" defect: a revision whose regenerated plan
// was identical produced "Execution plan v1" a second time (a new artifact
// family, so the UI offered "Diff v1 → v1") and renumbered TASK-1 to TASK-2,
// because every task was deleted and recreated through a monotonic counter.
func TestARevisionThatChangesNothingKeepsTheVersionAndTaskCodes(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Run(ctx, ini.ID, ini.Description); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Advance(ctx, ini.ID); err != nil {
		t.Fatal(err)
	}
	beforeVersions, beforeLatest, beforeCodes := planState(t, eng, ini.ID)
	if beforeVersions != 1 || len(beforeCodes) == 0 {
		t.Fatalf("fixture: want one plan version and some tasks, got %d versions, tasks %v", beforeVersions, beforeCodes)
	}

	if _, err := eng.RevisePlan(ctx, ini.ID); err != nil {
		t.Fatal(err)
	}
	afterVersions, afterLatest, afterCodes := planState(t, eng, ini.ID)
	if afterVersions != beforeVersions || afterLatest != beforeLatest {
		t.Errorf("an identical plan minted a version: %d versions (latest v%d) -> %d (latest v%d)",
			beforeVersions, beforeLatest, afterVersions, afterLatest)
	}
	if strings.Join(afterCodes, ",") != strings.Join(beforeCodes, ",") {
		t.Errorf("an identical plan renumbered its tasks: %v -> %v", beforeCodes, afterCodes)
	}
}

// TestRecordArtifactVersionAppendsToTheFamily is the store contract the fix
// rests on: a changed body is the next version of the same artifact, the
// previous version is marked superseded by it, and an identical body records
// nothing when asked to skip it.
func TestRecordArtifactVersionAppendsToTheFamily(t *testing.T) {
	_, db, iniID := newExecuteFixture(t)
	ctx := context.Background()
	v1 := &domain.ArtifactVersion{BodyDigest: "digest-a"}
	if wrote, err := db.RecordArtifactVersion(ctx, iniID, domain.ArtifactTechnicalDesign, v1, true); err != nil || !wrote {
		t.Fatalf("first version: wrote=%v err=%v", wrote, err)
	}
	same := &domain.ArtifactVersion{BodyDigest: "digest-a"}
	if wrote, err := db.RecordArtifactVersion(ctx, iniID, domain.ArtifactTechnicalDesign, same, true); err != nil || wrote {
		t.Fatalf("identical body: wrote=%v err=%v, want no new version", wrote, err)
	}
	v2 := &domain.ArtifactVersion{BodyDigest: "digest-b"}
	if wrote, err := db.RecordArtifactVersion(ctx, iniID, domain.ArtifactTechnicalDesign, v2, true); err != nil || !wrote {
		t.Fatalf("changed body: wrote=%v err=%v", wrote, err)
	}
	if v2.Version != 2 || v2.ArtifactID == "" {
		t.Fatalf("changed body: version %d artifact %q, want v2 of the existing artifact", v2.Version, v2.ArtifactID)
	}
	arts, err := db.ListArtifacts(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	var mine int
	for _, a := range arts {
		if a.Kind != domain.ArtifactTechnicalDesign {
			continue
		}
		mine++
		if a.ArtifactID != v2.ArtifactID {
			t.Errorf("v%d belongs to artifact %s, want %s", a.Version, a.ArtifactID, v2.ArtifactID)
		}
	}
	if mine != 2 {
		t.Fatalf("got %d versions, want 2", mine)
	}
	var superseded string
	if err := db.SQL().QueryRow(`SELECT superseded_by FROM artifact_versions WHERE id = ?`, v1.ID).Scan(&superseded); err != nil {
		t.Fatal(err)
	}
	if superseded != v2.ID {
		t.Fatalf("v1.superseded_by = %q, want v2's id", superseded)
	}
}

// TestARevisionNoteJoinsThePlannerAndIsRecordedOnItsRun pins the other half:
// "Ask for a revision" took no input, so the planner regenerated from exactly
// what produced the plan being revised. The note reaches the planner's run,
// and is cleared so it cannot steer anything after this revision.
func TestARevisionNoteJoinsThePlannerAndIsRecordedOnItsRun(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Run(ctx, ini.ID, ini.Description); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Advance(ctx, ini.ID); err != nil {
		t.Fatal(err)
	}
	eng.SetNoteOverride(ini.ID, "split the import step into its own task")
	if _, err := eng.RevisePlan(ctx, ini.ID); err != nil {
		t.Fatal(err)
	}
	if got := eng.currentNoteOverride(ini.ID); got != "" {
		t.Fatalf("the revision note outlived the revision: %q", got)
	}
	var n int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM runs WHERE initiative_id = ? AND stage = ? AND note = ?`,
		ini.ID, string(StageExecutionPlan), "split the import step into its own task").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("%d execution-plan runs carry the note, want 1", n)
	}
}

// TestRequestChangesNoteSteersTheTasksNextRun pins "Request changes" taking
// feedback: the note is stored on the change set and the task's next run
// carries it, labelled with the change set it came from -- and only that next
// run, never one after it.
func TestRequestChangesNoteSteersTheTasksNextRun(t *testing.T) {
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
	if err := eng.ReviewChangeSetWithNote(ctx, iniID, cs.ID, "approved", false, "looks fine"); err == nil {
		t.Fatal("approve accepted a note it has no run to give to")
	}
	if err := eng.ReviewChangeSetWithNote(ctx, iniID, cs.ID, "changes_requested", false, "use the existing logger"); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetChangesRequested || got.ReviewNote != "use the existing logger" {
		t.Fatalf("change set = %s with note %q", got.Status, got.ReviewNote)
	}

	if _, err := eng.Execute(ctx, iniID); err != nil {
		t.Fatal(err)
	}
	rows, err := db.SQL().Query(`SELECT note FROM runs WHERE initiative_id = ? AND stage = ? ORDER BY started_at, rowid`, iniID, "agent:"+task.Code)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var notes []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		notes = append(notes, n)
	}
	if len(notes) != 2 {
		t.Fatalf("got %d agent runs, want 2", len(notes))
	}
	want := "Changes requested on " + cs.HumanID + ": use the existing logger"
	if notes[1] != want {
		t.Fatalf("second run's note = %q, want %q", notes[1], want)
	}
	if priorReviewNote([]domain.ChangeSet{*got, {HumanID: "CHG-2", Status: domain.ChangeSetReviewRequired, CreatedAt: got.CreatedAt.Add(1)}}) != "" {
		t.Fatal("a note was replayed after a later change set answered it")
	}
}
