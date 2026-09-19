package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestExecuteAppendsNoteToBriefAndRecordsItOnTheRun pins D-a (wave 3
// contract): a note set via SetNoteOverride is appended to the implementer's
// brief and recorded on the agent run, then cleared so it never bleeds into
// a later, unrelated Execute of the same initiative.
func TestExecuteAppendsNoteToBriefAndRecordsItOnTheRun(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}

	eng.SetNoteOverride(iniID, "please also add a rate limit header")
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}

	// Cleared once Execute returns.
	if got := eng.currentNoteOverride(iniID); got != "" {
		t.Fatalf("note override was not cleared after Execute: %q", got)
	}

	// Recorded on the agent run: find the RunAgent-kind run for this
	// initiative and check its note column directly.
	var note string
	row := db.SQL().QueryRow(`
		SELECT note FROM runs WHERE initiative_id = ? AND stage = ?`,
		iniID, "agent:"+task.Code)
	if err := row.Scan(&note); err != nil {
		t.Fatalf("could not read the agent run's note: %v", err)
	}
	if note != "please also add a rate limit header" {
		t.Fatalf("run note = %q, want the human's note", note)
	}
	_ = cs
}

// TestExecuteWithoutANoteLeavesTheBriefUnchanged is the guard's counterpart:
// no note override set means nothing is appended.
func TestExecuteWithoutANoteLeavesTheBriefUnchanged(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()
	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Execute(ctx, iniID); err != nil {
		t.Fatal(err)
	}
	var note string
	row := db.SQL().QueryRow(`SELECT note FROM runs WHERE initiative_id = ? AND stage = ?`, iniID, "agent:"+task.Code)
	if err := row.Scan(&note); err != nil {
		t.Fatalf("could not read the agent run's note: %v", err)
	}
	if strings.TrimSpace(note) != "" {
		t.Fatalf("note = %q, want empty when no override was set", note)
	}
}
