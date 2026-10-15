package store

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// Held notes survive a reopen of the database (a daemon restart), come back
// oldest first, and are taken once: a second take, or a concurrent one, gets
// nothing.
func TestHeldSteeringNotesSurviveReopenAndAreTakenOnce(t *testing.T) {
	ctx := context.Background()
	db, path := openRemoteRunDB(t)
	for i := 1; i <= 3; i++ {
		if err := db.HoldSteeringNote(ctx, HeldSteeringNote{InitiativeID: "ini", TaskID: "task-1", RunID: "run-1", Note: fmt.Sprintf("note %d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.HoldSteeringNote(ctx, HeldSteeringNote{InitiativeID: "other", Note: "not mine"}); err != nil {
		t.Fatal(err)
	}
	if err := db.HoldSteeringNote(ctx, HeldSteeringNote{InitiativeID: "ini", Note: "   "}); err == nil {
		t.Fatal("an empty note was held")
	}
	db.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db2.Close() })
	pending, err := db2.PendingSteeringNotes(ctx, "ini")
	if err != nil || len(pending) != 3 || pending[0].Note != "note 1" || pending[2].Note != "note 3" || pending[0].TaskID != "task-1" {
		t.Fatalf("pending after reopen = %+v, %v", pending, err)
	}

	var wg sync.WaitGroup
	got := make([][]HeldSteeringNote, 8)
	for i := range got {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			n, err := db2.TakeSteeringNotes(ctx, "ini", "task-2")
			if err != nil {
				t.Error(err)
			}
			got[i] = n
		}(i)
	}
	wg.Wait()
	total := 0
	for _, g := range got {
		if len(g) > 0 {
			if len(g) != 3 || g[0].Note != "note 1" || g[1].Note != "note 2" || g[2].Note != "note 3" {
				t.Fatalf("a take returned %+v; want all three, oldest first", g)
			}
		}
		total += len(g)
	}
	if total != 3 {
		t.Fatalf("notes taken across concurrent takers = %d, want exactly 3", total)
	}
	if left, _ := db2.PendingSteeringNotes(ctx, "ini"); len(left) != 0 {
		t.Fatalf("still pending after take: %+v", left)
	}
	if other, _ := db2.PendingSteeringNotes(ctx, "other"); len(other) != 1 {
		t.Fatalf("another initiative's note was taken: %+v", other)
	}
}
