package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/id"
)

// TestMigration35BackfillsRunsInitiativeID is review H5: migration 31 added
// runs.initiative_id/stage but never backfilled a PRE-EXISTING run row --
// every such row was left at the column's own default (”). Both cost
// queries (ListModelPhaseCosts, ListAgentRunStats) filter
// `WHERE r.initiative_id = ?`, so an initiative that existed before that
// upgrade permanently reports $0.00 / 0 calls even though the spend still
// sits in run_manifests.
//
// This applies migrations 1..31 to a fresh database (simulating a
// pre-migration-32..35 install), inserts a `runs` row the way that database
// would have one (initiative_id at the column default, exactly as a legacy
// row upgraded in place would read), wires it into the product graph with a
// produced_by edge the way CreateRequirement/CreateAssertion/etc. already
// do, then applies the rest of the migrations (including 35) and checks the
// backfill.
//
// Mutation-check: drop migration 35 (or its EXISTS guard) and this fails --
// runs.initiative_id stays ”.
func TestMigration35BackfillsRunsInitiativeID(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "state.db") + "?_pragma=busy_timeout(5000)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	db := &DB{sql: sqlDB}
	if err := db.configure(); err != nil {
		t.Fatal(err)
	}
	if err := db.migrate(migrations[:31]); err != nil {
		t.Fatalf("apply migrations 1..31: %v", err)
	}

	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	// A pre-upgrade run row: initiative_id/stage/note all at their column
	// defaults, exactly as migration 31's ADD COLUMN left every row that
	// already existed.
	const wiredRunID = "run-wired"
	const orphanRunID = "run-orphan"
	now := time.Now().UTC().UnixMilli()
	for _, runID := range []string{wiredRunID, orphanRunID} {
		if _, err := db.sql.Exec(`INSERT INTO runs (id, kind, status, started_at, finished_at) VALUES (?, 'model', 'completed', ?, ?)`,
			runID, now, now); err != nil {
			t.Fatal(err)
		}
	}

	// Wire wiredRunID into the product graph exactly the way
	// CreateRequirement's produced_by edge does (repo.go's wireEdges).
	if _, err := db.sql.Exec(`INSERT INTO graph_edges (id, initiative_id, from_type, from_id, edge_type, to_type, to_id, metadata_json, created_at)
		VALUES (?, ?, 'requirement', ?, 'produced_by', 'run', ?, '{}', ?)`,
		id.New(), ini.ID, id.New(), wiredRunID, now); err != nil {
		t.Fatal(err)
	}
	// orphanRunID has no graph edge at all (an edge that itself predates the
	// product graph, or a run nothing was ever wired from) -- nothing to
	// recover it from, and it must be left exactly as it was.

	if err := db.migrate(migrations); err != nil {
		t.Fatalf("apply the remaining migrations (including 35): %v", err)
	}

	var wiredIniID string
	if err := db.sql.QueryRow(`SELECT initiative_id FROM runs WHERE id = ?`, wiredRunID).Scan(&wiredIniID); err != nil {
		t.Fatal(err)
	}
	if wiredIniID != ini.ID {
		t.Fatalf("wired run's initiative_id = %q, want %q (migration 35 backfill)", wiredIniID, ini.ID)
	}

	var orphanIniID string
	if err := db.sql.QueryRow(`SELECT initiative_id FROM runs WHERE id = ?`, orphanRunID).Scan(&orphanIniID); err != nil {
		t.Fatal(err)
	}
	if orphanIniID != "" {
		t.Fatalf("orphan run (no graph edge) initiative_id = %q, want '' (nothing to recover it from)", orphanIniID)
	}
}
