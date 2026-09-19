package store

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestCreateInitiativeGeneratesShortTitle pins C1: short_title is generated
// deterministically on create from the description, when the caller does not
// supply one.
func TestCreateInitiativeGeneratesShortTitle(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "idea", Description: "I want to add per-user API keys with rate limiting."}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	want := domain.GenerateShortTitle(ini.Description)
	if want == "" {
		t.Fatal("test setup: GenerateShortTitle produced empty for a non-empty description")
	}
	if ini.ShortTitle != want {
		t.Fatalf("CreateInitiative did not set ShortTitle on the struct: got %q, want %q", ini.ShortTitle, want)
	}
	got, err := db.GetInitiative(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ShortTitle != want {
		t.Fatalf("GetInitiative returned ShortTitle %q, want %q", got.ShortTitle, want)
	}
}

// TestCreateInitiativeKeepsExplicitShortTitle: a caller-supplied ShortTitle
// (a fixture, say) is left alone rather than overwritten.
func TestCreateInitiativeKeepsExplicitShortTitle(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "idea", Description: "long idea text", ShortTitle: "custom"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if ini.ShortTitle != "custom" {
		t.Fatalf("ShortTitle was overwritten: %q", ini.ShortTitle)
	}
}

// TestGetInitiativeBackfillsShortTitle pins the lazy-backfill path (C1):
// a row written before short_title existed (empty in the column) gets one
// generated and persisted the first time it is read.
func TestGetInitiativeBackfillsShortTitle(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "idea", Description: "Build a rate limiter for the API."}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-migration row: blank out short_title directly.
	if _, err := db.SQL().ExecContext(ctx, `UPDATE initiatives SET short_title = '' WHERE id = ?`, ini.ID); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetInitiative(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := domain.GenerateShortTitle(ini.Description)
	if got.ShortTitle != want {
		t.Fatalf("GetInitiative did not backfill: got %q, want %q", got.ShortTitle, want)
	}
	// And it was actually written back, not just computed for this read.
	var stored string
	if err := db.SQL().QueryRowContext(ctx, `SELECT short_title FROM initiatives WHERE id = ?`, ini.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != want {
		t.Fatalf("backfill was not persisted: column has %q, want %q", stored, want)
	}
}

// TestListInitiativesBackfillsShortTitle is TestGetInitiativeBackfillsShortTitle
// for the list path, which scans and closes rows before backfilling -- a
// regression here (backfilling from an open *sql.Rows) would deadlock on the
// single-connection pool rather than merely fail an assertion, so this test
// existing and passing is itself the guard.
func TestListInitiativesBackfillsShortTitle(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "idea", Description: "Build a rate limiter for the API."}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().ExecContext(ctx, `UPDATE initiatives SET short_title = '' WHERE id = ?`, ini.ID); err != nil {
		t.Fatal(err)
	}
	list, err := db.ListInitiatives(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ShortTitle == "" {
		t.Fatalf("ListInitiatives did not backfill short_title: %+v", list)
	}
}

// TestTaskEdges pins the requirement_ids/depends_on projection (extra scope:
// web-A's contract check) -- always non-nil, and split by edge type.
func TestTaskEdges(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "t", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	dep := &domain.Task{InitiativeID: ini.ID, Title: "dep task", Statement: "s"}
	if err := db.CreateTaskMulti(ctx, dep, []string{req.ID}, nil); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "main task", Statement: "s"}
	if err := db.CreateTaskMulti(ctx, task, []string{req.ID}, []string{dep.ID}); err != nil {
		t.Fatal(err)
	}

	reqIDs, deps, err := db.TaskEdges(ctx, ini.ID, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqIDs) != 1 || reqIDs[0] != req.ID {
		t.Fatalf("requirement_ids = %v, want [%s]", reqIDs, req.ID)
	}
	if len(deps) != 1 || deps[0] != dep.ID {
		t.Fatalf("depends_on = %v, want [%s]", deps, dep.ID)
	}

	// A task with no edges gets empty (non-nil) slices, never nil -- the
	// contract is `string[]`, not `string[] | null`.
	bare := &domain.Task{InitiativeID: ini.ID, Title: "bare task", Statement: "s"}
	if err := db.CreateTaskMulti(ctx, bare, nil, nil); err != nil {
		t.Fatal(err)
	}
	reqIDs, deps, err = db.TaskEdges(ctx, ini.ID, bare.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reqIDs == nil || deps == nil {
		t.Fatalf("TaskEdges returned nil slices for an edge-less task: reqIDs=%v deps=%v", reqIDs, deps)
	}
	if len(reqIDs) != 0 || len(deps) != 0 {
		t.Fatalf("expected empty slices, got reqIDs=%v deps=%v", reqIDs, deps)
	}
}

// TestListEventsForInitiativeScoping is the JTBD 6 regression test: an event
// belonging to one initiative's change set must never appear in another
// initiative's scoped event list, even though both share one events table
// with no initiative_id column of its own.
func TestListEventsForInitiativeScoping(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	iniA := &domain.Initiative{Title: "a", Description: "a"}
	iniB := &domain.Initiative{Title: "b", Description: "b"}
	if err := db.CreateInitiative(ctx, iniA); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateInitiative(ctx, iniB); err != nil {
		t.Fatal(err)
	}
	taskA := &domain.Task{InitiativeID: iniA.ID, Title: "ta", Statement: "s"}
	if err := db.CreateTaskMulti(ctx, taskA, nil, nil); err != nil {
		t.Fatal(err)
	}
	csA := &domain.ChangeSet{InitiativeID: iniA.ID, TaskID: taskA.ID, PatchDigest: "d"}
	if err := db.CreateChangeSet(ctx, csA); err != nil {
		t.Fatal(err)
	}

	eventsA, err := db.ListEventsForInitiative(ctx, iniA.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	eventsB, err := db.ListEventsForInitiative(ctx, iniB.ID, 0, 500)
	if err != nil {
		t.Fatal(err)
	}

	foundInA := false
	for _, e := range eventsA {
		if e.AggregateType == "change_set" && e.AggregateID == csA.ID {
			foundInA = true
		}
	}
	if !foundInA {
		t.Fatal("iniA's own change_set.created event is missing from its own scoped event list")
	}
	for _, e := range eventsB {
		if e.AggregateType == "change_set" && e.AggregateID == csA.ID {
			t.Fatal("iniB's scoped event list leaked iniA's change set event -- the JTBD 6 bug")
		}
	}
	// iniB's own creation event must still be visible to itself.
	foundBOwn := false
	for _, e := range eventsB {
		if e.AggregateType == "initiative" && e.AggregateID == iniB.ID {
			foundBOwn = true
		}
	}
	if !foundBOwn {
		t.Fatal("iniB's own initiative.created event is missing from its own scoped event list")
	}
}

// TestSetOperationStage pins C3's stage callback: it updates the RUNNING
// operation for an initiative, and is a harmless no-op when there is none.
func TestSetOperationStage(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "t", Description: "d"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	// No running operation yet: must not error.
	if err := db.SetOperationStage(ctx, ini.ID, "Writing the PRD"); err != nil {
		t.Fatalf("SetOperationStage with no running operation returned an error: %v", err)
	}

	opID, err := db.CreateOperation(ctx, "proj", ini.ID, "advance_initiative")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.SetOperationStage(ctx, ini.ID, "Writing the PRD"); err != nil {
		t.Fatal(err)
	}
	op, err := db.GetOperation(ctx, opID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Stage != "Writing the PRD" {
		t.Fatalf("Stage = %q, want %q", op.Stage, "Writing the PRD")
	}

	// A later stage call overwrites, tracking the engine's progress.
	if err := db.SetOperationStage(ctx, ini.ID, "Reviewing the PRD (cycle 1)"); err != nil {
		t.Fatal(err)
	}
	op, err = db.GetOperation(ctx, opID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Stage != "Reviewing the PRD (cycle 1)" {
		t.Fatalf("Stage did not update: got %q", op.Stage)
	}

	// Once finished, stage is frozen -- SetOperationStage only ever targets a
	// RUNNING operation, so it must not resurrect a terminal one.
	if err := db.SetOperationStatus(ctx, opID, domain.OperationSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetOperationStage(ctx, ini.ID, "should not apply"); err != nil {
		t.Fatal(err)
	}
	op, err = db.GetOperation(ctx, opID)
	if err != nil {
		t.Fatal(err)
	}
	if op.Stage != "Reviewing the PRD (cycle 1)" {
		t.Fatalf("stage changed on a finished operation: got %q", op.Stage)
	}
}
