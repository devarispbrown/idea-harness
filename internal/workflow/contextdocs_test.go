package workflow

import (
	"context"
	"testing"

	"github.com/idea-harness/idea-harness/internal/config"
	"github.com/idea-harness/idea-harness/internal/domain"
)

// TestContextDocsWireConsumesEdges verifies deterministic provenance: when an
// initiative has ingested context docs and a run injects them, the engine wires
// run --consumes--> context_doc edges so `impact <doc>`/`why <run>` resolve.
func TestContextDocsWireConsumesEdges(t *testing.T) {
	fp := scriptedProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	digest, err := eng.blobs.Put([]byte("# PRD\n\nGrounding content"))
	if err != nil {
		t.Fatal(err)
	}
	doc := &domain.ContextDoc{InitiativeID: ini.ID, Kind: "prd", Title: "spec", Digest: digest}
	if err := db.CreateContextDoc(ctx, doc); err != nil {
		t.Fatal(err)
	}

	if _, err := eng.Run(ctx, ini.ID, ini.Description); err != nil {
		t.Fatal(err)
	}

	edges, err := db.ListGraphEdgesTo(ctx, ini.ID, "context_doc", doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	hasConsumes := false
	var runID string
	for _, e := range edges {
		if e.EdgeType == domain.EdgeConsumes && e.FromType == "run" {
			hasConsumes = true
			runID = e.FromID
			break
		}
	}
	if !hasConsumes {
		t.Fatal("expected a run --consumes--> context_doc edge after M1 with docs")
	}

	// Why(run) must resolve the consumed doc via the outgoing edge.
	why, err := eng.Why(ctx, ini.ID, "run", runID)
	if err != nil {
		t.Fatal(err)
	}
	whySeesDoc := false
	for _, item := range why {
		if item.ToType == "context_doc" && item.ToID == doc.ID {
			whySeesDoc = true
			if item.ToLabel != doc.Title {
				t.Fatalf("Why(run) ToLabel = %q, want %q", item.ToLabel, doc.Title)
			}
		}
	}
	if !whySeesDoc {
		t.Fatal("Why(run) did not resolve the consumed context_doc")
	}

	// Impact(doc) must resolve the consuming run via the incoming edge.
	impact, err := eng.Impact(ctx, ini.ID, "context_doc", doc.ID)
	if err != nil {
		t.Fatal(err)
	}
	impactSeesRun := false
	for _, item := range impact {
		if item.FromType == "run" && item.FromID == runID {
			impactSeesRun = true
		}
	}
	if !impactSeesRun {
		t.Fatal("Impact(doc) did not resolve the consuming run")
	}
}
