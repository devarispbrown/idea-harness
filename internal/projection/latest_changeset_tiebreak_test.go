package projection

import (
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestLatestChangeSetReducersAgreeOnASameMillisecondTie is the MEDIUM
// "single-source it" fix: eligibleChangeSets (evidence eligibility) and
// LatestChangeSetsPerActiveTask (/guidance, and by the same logic /queue)
// used two different tie-break rules for two change sets created in the same
// millisecond -- store.ListChangeSets orders by created_at ASC with no
// secondary key, so this is a real, reachable tie, not a hypothetical one.
// Before the fix, evidence eligibility (and /queue) picked the LATER one
// scanned while /guidance's reducer picked the FIRST one scanned -- the same
// task's "latest" change set could differ depending which surface asked.
func TestLatestChangeSetReducersAgreeOnASameMillisecondTie(t *testing.T) {
	tie := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	older := domain.ChangeSet{ID: "cs-1", HumanID: "CHG-1", TaskID: "task-1", Status: domain.ChangeSetApplied, CreatedAt: tie}
	// The actual later attempt: same millisecond, later in scan order --
	// exactly what a fast test (or two rapid real requests) produces.
	newer := domain.ChangeSet{ID: "cs-2", HumanID: "CHG-2", TaskID: "task-1", Status: domain.ChangeSetApplied, CreatedAt: tie}

	changeSets := []domain.ChangeSet{older, newer}
	tasks := []domain.Task{{ID: "task-1", Status: domain.TaskDone}}

	// Evidence eligibility's reducer.
	eligible := eligibleChangeSets(changeSets)
	if !eligible[newer.ID] {
		t.Errorf("eligibleChangeSets did not pick the later-scanned change set %s as eligible", newer.HumanID)
	}
	if eligible[older.ID] {
		t.Errorf("eligibleChangeSets picked the earlier-scanned change set %s as eligible", older.HumanID)
	}

	// /guidance's reducer.
	perTask := LatestChangeSetsPerActiveTask(changeSets, tasks)
	if len(perTask) != 1 {
		t.Fatalf("LatestChangeSetsPerActiveTask returned %d entries, want 1: %+v", len(perTask), perTask)
	}
	if perTask[0].ID != newer.ID {
		t.Errorf("MEDIUM: LatestChangeSetsPerActiveTask named %s as the latest change set for task-1, "+
			"but eligibleChangeSets named %s -- /queue and /guidance disagree on a same-millisecond tie",
			perTask[0].HumanID, newer.HumanID)
	}

	// latestChangeSetWithStatus (guidance's other reducer, e.g. for
	// rejected/changes_requested outcomes) must agree too.
	rejectedOlder := domain.ChangeSet{ID: "cs-3", HumanID: "CHG-3", TaskID: "task-2", Status: domain.ChangeSetRejected, CreatedAt: tie}
	rejectedNewer := domain.ChangeSet{ID: "cs-4", HumanID: "CHG-4", TaskID: "task-2", Status: domain.ChangeSetRejected, CreatedAt: tie}
	got, found := latestChangeSetWithStatus([]domain.ChangeSet{rejectedOlder, rejectedNewer}, domain.ChangeSetRejected)
	if !found {
		t.Fatal("latestChangeSetWithStatus found nothing")
	}
	if got.ID != rejectedNewer.ID {
		t.Errorf("latestChangeSetWithStatus picked %s, want the later-scanned %s", got.HumanID, rejectedNewer.HumanID)
	}
}
