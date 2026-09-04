package workflow

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
)

// TestWhyAndImpactReturnEmptyNotNull pins the trace half of R3.
//
// A node with no provenance and no dependents is an ordinary answer — the
// first assertion of an initiative has nothing upstream of it — but both
// builders appended to a nil slice, so `/why` and `/impact` marshalled `null`.
// A client then has to distinguish null from [] for a case that means the same
// thing, and a generated client whose schema says `type: array` rejects it.
func TestWhyAndImpactReturnEmptyNotNull(t *testing.T) {
	eng, db := newTestEngine(t, model.NewFakeProvider(), config.HarnessConfig{})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "Bare", Description: "No edges anywhere"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	why, err := eng.Why(ctx, ini.ID, "requirement", "REQ-nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if why == nil {
		t.Error("Why returned nil; it marshals as null")
	}
	if b, _ := json.Marshal(why); string(b) != "[]" {
		t.Errorf("Why marshals as %s, want []", b)
	}

	impact, err := eng.Impact(ctx, ini.ID, "requirement", "REQ-nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if impact == nil {
		t.Error("Impact returned nil; it marshals as null")
	}
	if b, _ := json.Marshal(impact); string(b) != "[]" {
		t.Errorf("Impact marshals as %s, want []", b)
	}
}
