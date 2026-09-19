package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestMigration32SetsSecretOverridableFalseForConfiguredSecrets is review
// low-priority item: migration 30 left every pre-existing configured-secret
// finding's secret_overridable at its column default (1/true), which
// mis-describes it -- a configured-secret match is never eligible for the
// not_a_secret override. Migration 32 corrects that.
//
// This is the "migration-30 upgrade path" the review found untested: it
// applies migrations 1..31 to a fresh database (simulating a pre-migration-32
// install), inserts a configured-secret finding the way that database would
// have had one, then applies the rest (migration 32) and checks the column
// flips.
func TestMigration32SetsSecretOverridableFalseForConfiguredSecrets(t *testing.T) {
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
	f := &domain.Finding{
		InitiativeID: ini.ID, TargetType: "change_set", TargetID: "cs-1",
		Severity: domain.SeverityCritical, Confidence: domain.ConfidenceHigh,
		Category: "secret_sweep", Finding: "a configured secret was found", Recommendation: "remove it",
		SecretKind: "configured secret value", SecretOverridable: true, // pre-migration-32 default
	}
	if err := db.CreateFinding(ctx, f); err != nil {
		t.Fatal(err)
	}

	var before int
	if err := db.sql.QueryRow(`SELECT secret_overridable FROM findings WHERE id = ?`, f.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if before != 1 {
		t.Fatalf("setup: expected secret_overridable=1 before migration 32, got %d", before)
	}

	if err := db.migrate(migrations); err != nil {
		t.Fatalf("apply the remaining migrations (including 32): %v", err)
	}

	var after int
	if err := db.sql.QueryRow(`SELECT secret_overridable FROM findings WHERE id = ?`, f.ID).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != 0 {
		t.Fatalf("expected migration 32 to set secret_overridable=0 for a configured-secret finding, got %d", after)
	}

	// A pattern-shaped (overridable) finding is untouched.
	other := &domain.Finding{
		InitiativeID: ini.ID, TargetType: "change_set", TargetID: "cs-1",
		Severity: domain.SeverityCritical, Confidence: domain.ConfidenceHigh,
		Category: "secret_sweep", Finding: "a pattern match was found", Recommendation: "remove it",
		SecretKind: "AWS access key ID", SecretOverridable: true,
	}
	if err := db.CreateFinding(ctx, other); err != nil {
		t.Fatal(err)
	}
	var otherOverridable int
	if err := db.sql.QueryRow(`SELECT secret_overridable FROM findings WHERE id = ?`, other.ID).Scan(&otherOverridable); err != nil {
		t.Fatal(err)
	}
	if otherOverridable != 1 {
		t.Fatalf("expected a pattern-match finding to stay overridable, got %d", otherOverridable)
	}
}
