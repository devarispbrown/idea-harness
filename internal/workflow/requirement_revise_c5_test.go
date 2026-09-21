package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
)

// TestReviseRefusesDuplicateRequirementCode is review C5's second defect at
// the same call site: a revision response naming the same requirement code
// twice used to last-writer-win silently -- UpdateRequirement ran twice for
// the same id, the first occurrence's distinct content vanished with no
// error and no duplicate finding. The response is now refused outright.
//
// Mutation-check: remove the usedCodes check (or the delete(priorByCode, ...)
// consume) and this fails -- runProductDefinition returns nil instead of an
// error.
func TestReviseRefusesDuplicateRequirementCode(t *testing.T) {
	fp := model.NewFakeProvider()
	fp.SetResponse("idea_brief", `{"title":"x","summary":"x"}`)
	fp.SetResponse("research_brief", `{"summary":"x","findings":[]}`)
	fp.SetResponse("prd", `{"summary":"PRD","requirements":[{"statement":"Meter usage events","priority":"must","acceptance_criteria":[{"metric":"events","target":">= 1","condition":"per request"}]}],"decisions":[],"open_questions":[],"risks":[]}`)
	fp.SetResponse("review", `{"findings":[]}`)

	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	// Initial authoring: creates one requirement (code REQ-1).
	if err := eng.runProductDefinition(ctx, ini.ID, nil, ""); err != nil {
		t.Fatal(err)
	}
	reqs, err := db.ListRequirements(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 {
		t.Fatalf("expected 1 requirement after initial authoring, got %d", len(reqs))
	}
	code := reqs[0].Code

	// A revision response names the SAME code twice with two different
	// statements.
	fp.SetResponse("prd", `{"summary":"PRD","requirements":[`+
		`{"code":"`+code+`","statement":"Meter usage events precisely","priority":"must","acceptance_criteria":[{"metric":"events","target":">= 1","condition":"per request"}]},`+
		`{"code":"`+code+`","statement":"Meter usage events with retries","priority":"must","acceptance_criteria":[{"metric":"events","target":">= 1","condition":"per request"}]}`+
		`],"decisions":[],"open_questions":[],"risks":[]}`)

	err = eng.runProductDefinition(ctx, ini.ID, reqs, "human-revise")
	if err == nil {
		t.Fatal("expected an error refusing a revision response that names the same requirement code twice, got nil")
	}
	if !strings.Contains(err.Error(), code) {
		t.Errorf("error %q does not name the duplicated code %q", err.Error(), code)
	}
}
