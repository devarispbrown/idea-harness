package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestOpenAndMigrate(t *testing.T) {
	db := openTestDB(t)
	var n int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(migrations) {
		t.Fatalf("expected %d migrations, got %d", len(migrations), n)
	}
}

func TestApplyAtomicStateAndEvent(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	err := db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   "i-1",
		Actor:         "test",
		EventType:     "initiative.created",
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO initiatives (id, title, description, status, created_at, updated_at)
			VALUES ('i-1', 'Test', '', 'running', 1, 1)`)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	var title string
	if err := db.SQL().QueryRow(`SELECT title FROM initiatives WHERE id='i-1'`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title != "Test" {
		t.Fatalf("expected initiative row, got %q", title)
	}

	seq, err := db.MaxEventSeq(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if seq != 1 {
		t.Fatalf("expected one event, got seq=%d", seq)
	}
}

func TestApplyRollsBackOnError(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	err := db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   "i-2",
		Actor:         "test",
		EventType:     "initiative.created",
	}, func(tx *sql.Tx) error {
		// Insert state, then force an error: the whole transaction must roll back.
		if _, err := tx.Exec(`
			INSERT INTO initiatives (id, title, description, status, created_at, updated_at)
			VALUES ('i-2', 'Rollback', '', 'running', 1, 1)`); err != nil {
			return err
		}
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("expected error from Apply")
	}

	var n int
	if err := db.SQL().QueryRow(`SELECT COUNT(*) FROM initiatives WHERE id='i-2'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatal("state was not rolled back")
	}
	seq, _ := db.MaxEventSeq(ctx)
	if seq != 0 {
		t.Fatal("event was not rolled back")
	}
}

func TestFindingsDispositionConstraint(t *testing.T) {
	db := openTestDB(t)
	// An invalid disposition must violate the CHECK constraint.
	_, err := db.SQL().Exec(`
		INSERT INTO findings (id, review_run_id, target_type, target_id, severity, confidence,
			category, finding, evidence, impact, recommendation, disposition, disposition_actor, created_at)
		VALUES ('f-1', 'r-1', 'artifact_version', 'a-1', 'high', 'high', 'scope',
			'x', 'x', 'x', 'x', 'fix', 'human', 1)`)
	if err == nil {
		t.Fatal("expected CHECK constraint to reject non-enum disposition 'fix'")
	}
}
