package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// Migration 48 numbers pre-existing assertions per initiative in creation
// order, and new ones continue from there.
func TestMigrationNumbersExistingAssertions(t *testing.T) {
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
	if err := db.migrate(migrations[:47]); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		id, ini string
		at      int64
	}{{"a2", "i1", 20}, {"a1", "i1", 10}, {"b1", "i2", 15}} {
		if _, err := db.sql.Exec(`INSERT INTO assertions (id, initiative_id, type, status, statement, evidence_digest, confidence, source, provenance_json, produced_by_run_id, superseded_by, created_at)
			VALUES (?, ?, 'assumption', 'candidate', 's', '', 'low', 'framing', '[]', '', '', ?)`, row.id, row.ini, row.at); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.migrate(migrations); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"a1": "ASM-1", "a2": "ASM-2", "b1": "ASM-1"}
	for aid, code := range want {
		var got string
		if err := db.sql.QueryRow(`SELECT code FROM assertions WHERE id = ?`, aid).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != code {
			t.Errorf("%s code = %q; want %q", aid, got, code)
		}
	}
	a := &domain.Assertion{InitiativeID: "i1", Type: domain.AssertionHypothesis, Statement: "next"}
	if err := db.CreateAssertion(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if a.Code != "ASM-3" {
		t.Errorf("next code = %q; want ASM-3", a.Code)
	}
}

// SetAssertionBasis replaces a requirement's basis but keeps its source
// assertion, adds to a decision's without duplicating, and refuses an
// assertion from another initiative.
func TestSetAssertionBasis(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	other := &domain.Initiative{Title: "y", Description: "y"}
	for _, i := range []*domain.Initiative{ini, other} {
		if err := db.CreateInitiative(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(iniID string) *domain.Assertion {
		a := &domain.Assertion{InitiativeID: iniID, Type: domain.AssertionAssumption, Statement: "s"}
		if err := db.CreateAssertion(ctx, a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	src, a1, a2, foreign := mk(ini.ID), mk(ini.ID), mk(ini.ID), mk(other.ID)
	r := &domain.Requirement{InitiativeID: ini.ID, Statement: "r", Priority: domain.PriorityMust, SourceAssertionID: src.ID}
	if err := db.CreateRequirement(ctx, r); err != nil {
		t.Fatal(err)
	}
	d := &domain.Decision{InitiativeID: ini.ID, Type: domain.DecisionProduct, Question: "q"}
	if err := db.CreateDecision(ctx, d); err != nil {
		t.Fatal(err)
	}
	basis := func(typ, nodeID string) map[string]int {
		edges, err := db.ListGraphEdgesFrom(ctx, ini.ID, typ, nodeID)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int{}
		for _, e := range edges {
			if e.EdgeType == domain.EdgeDerivedFrom && e.ToType == "assertion" {
				out[e.ToID]++
			}
		}
		return out
	}
	if err := db.SetAssertionBasis(ctx, ini.ID, "requirement", r.ID, []string{a1.ID}, true); err != nil {
		t.Fatal(err)
	}
	if err := db.SetAssertionBasis(ctx, ini.ID, "requirement", r.ID, []string{a2.ID}, true); err != nil {
		t.Fatal(err)
	}
	if got := basis("requirement", r.ID); len(got) != 2 || got[src.ID] != 1 || got[a2.ID] != 1 {
		t.Errorf("requirement basis = %v; want source + a2", got)
	}
	for i := 0; i < 2; i++ {
		if err := db.SetAssertionBasis(ctx, ini.ID, "decision", d.ID, []string{a1.ID, a1.ID}, false); err != nil {
			t.Fatal(err)
		}
	}
	if got := basis("decision", d.ID); len(got) != 1 || got[a1.ID] != 1 {
		t.Errorf("decision basis = %v; want a1 once", got)
	}
	if err := db.SetAssertionBasis(ctx, ini.ID, "decision", d.ID, []string{foreign.ID}, false); err == nil {
		t.Error("an assertion from another initiative was accepted")
	}
	if err := db.SetAssertionBasis(ctx, ini.ID, "task", "t", nil, false); err == nil {
		t.Error("a task basis was accepted")
	}
}
