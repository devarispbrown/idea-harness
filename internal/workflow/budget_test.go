package workflow

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor/ohfake"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/store"
)

func capUSD(v float64) *float64 { return &v }

// The design pipeline's model calls (runModel): a cap that is already
// reached refuses the very next call, before the provider is asked.
func TestBudgetStopsTheNextModelCall(t *testing.T) {
	fp := scriptedProvider()
	fp.EnqueueCost("idea_brief", 0.30) // framing spends past the cap...
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Budget: config.BudgetConfig{DefaultInitiativeUSD: 0.25}})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "a billing tool", Status: domain.InitiativeRunning}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	_, err := eng.Run(ctx, ini.ID, ini.Description)
	var be *BudgetExceededError
	if !errors.As(err, &be) || !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("Run = %v, want a budget stop", err)
	}
	if !strings.Contains(be.Before, "research") || be.CapUSD != 0.25 || be.SpentUSD < 0.30 {
		t.Fatalf("stop = %+v, want it before the research call with $0.30 of $0.25", be)
	}
	// ...so research was never sent.
	for _, c := range fp.Calls() {
		if c.JSONSchemaName == "research_brief" {
			t.Fatal("the research call reached the provider after the cap was reached")
		}
	}
	st, err := eng.BudgetStatus(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Exceeded || st.Source != BudgetSourceProject || st.RemainingUSD == nil || *st.RemainingUSD != 0 {
		t.Fatalf("status = %+v", st)
	}
}

// An initiative's own cap wins over the project default, and raising it lets
// the call through.
func TestInitiativeCapOverridesTheProjectDefault(t *testing.T) {
	fp := scriptedProvider()
	fp.EnqueueCost("idea_brief", 1.00)
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Budget: config.BudgetConfig{DefaultInitiativeUSD: 0.50}})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "a billing tool", Status: domain.InitiativeRunning}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if err := db.SetInitiativeBudgetCap(ctx, ini.ID, capUSD(10), ""); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Run(ctx, ini.ID, ini.Description); err != nil {
		t.Fatalf("a $10 cap should not stop $1 of spend: %v", err)
	}
	st, _ := eng.BudgetStatus(ctx, ini.ID)
	if st.Source != BudgetSourceInitiative || st.Exceeded || *st.CapUSD != 10 {
		t.Fatalf("status = %+v", st)
	}
}

// No cap configured anywhere: nothing is ever refused.
func TestNoCapNeverStops(t *testing.T) {
	fp := scriptedProvider()
	fp.EnqueueCost("idea_brief", 99)
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "a billing tool", Status: domain.InitiativeRunning}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Run(ctx, ini.ID, ini.Description); err != nil {
		t.Fatal(err)
	}
	if st, _ := eng.BudgetStatus(ctx, ini.ID); st.CapUSD != nil || st.Exceeded || st.Source != BudgetSourceNone {
		t.Fatalf("status = %+v", st)
	}
}

// The coding agent, OpenHands: the planner's call reaches the cap, so the
// agent run on the remote server is never dispatched -- no conversation is
// created there.
func TestBudgetStopsTheOpenHandsAgentBeforeDispatch(t *testing.T) {
	eng, db, iniID, srv := newOpenHandsFixture(t, func(c *ohfake.Conv, dir string) { c.Say("should not run") })
	setFixtureCap(t, eng, db, iniID)
	_, err := eng.Execute(context.Background(), iniID)
	var be *BudgetExceededError
	if !errors.As(err, &be) || be.Before != "the openhands agent run" {
		t.Fatalf("Execute = %v, want a budget stop before the openhands agent run", err)
	}
	if n := len(srv.Creates()); n != 0 {
		t.Fatalf("%d conversation(s) were started on the Agent Server after the cap was reached", n)
	}
}

// The coding agent, ACP: refused before exec.Start, so no container is ever
// started (this test has no Docker and would fail at Start otherwise).
func TestBudgetStopsTheACPAgentBeforeStart(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test-0123456789abcdef")
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{
		Network: config.NetworkDisabled,
		Executor: config.ExecutorConfig{Kind: config.ExecutorACP, Model: "anthropic/claude-sonnet-5", Image: "ghcr.io/x/claude-acp:1",
			ACP: config.ACPConfig{Command: []string{"claude-code-acp"}}},
	})
	eng.SetWorkRoot(t.TempDir())
	ws := t.TempDir()
	eng.SetWorkspaceRoot(ws)
	if err := os.WriteFile(filepath.Join(ws, "app.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "Greet", Statement: "change the greeting", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	fp.EnqueueCost("implementation_brief", 0.60)
	if err := db.SetInitiativeBudgetCap(ctx, ini.ID, capUSD(0.50), ""); err != nil {
		t.Fatal(err)
	}
	_, err := eng.Execute(ctx, ini.ID)
	var be *BudgetExceededError
	if !errors.As(err, &be) || be.Before != "the acp agent run" {
		t.Fatalf("Execute = %v, want a budget stop before the acp agent run", err)
	}
}

// The image-description call (describeImage) is held to the cap too.
func TestBudgetStopsImageDescription(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled})
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	// Spend recorded by an earlier call, above a cap set afterwards.
	fp.EnqueueCost("idea_brief", 2)
	if _, _, err := eng.runModel(ctx, ini.ID, "framing", RoleOrchestrator, "idea_brief", `{"type":"object"}`, nil, "x"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetInitiativeBudgetCap(ctx, ini.ID, capUSD(1), ""); err != nil {
		t.Fatal(err)
	}
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00\x90wS\xde")
	digest, err := eng.rawBlobs.Put(png)
	if err != nil {
		t.Fatal(err)
	}
	calls := len(fp.Calls())
	err = eng.describeImage(ctx, domain.ContextDoc{ID: "doc1", InitiativeID: ini.ID, Digest: digest})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("describeImage = %v, want a budget stop", err)
	}
	if len(fp.Calls()) != calls {
		t.Fatal("the vision call reached the provider after the cap was reached")
	}
}

// setFixtureCap makes the planner's call spend past a cap set on the
// initiative, so the next call -- the agent -- is the one refused.
func setFixtureCap(t *testing.T, eng *Engine, db *store.DB, iniID string) {
	t.Helper()
	fp, ok := eng.registry.Get(eng.registry.Default())
	if !ok {
		t.Fatal("no default provider")
	}
	fp.(*model.FakeProvider).EnqueueCost("implementation_brief", 0.60)
	if err := db.SetInitiativeBudgetCap(context.Background(), iniID, capUSD(0.50), ""); err != nil {
		t.Fatal(err)
	}
}
