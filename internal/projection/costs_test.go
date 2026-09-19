package projection

import (
	"context"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestBuildCostsByPhase pins C3: model-call runs are grouped by stage, summed
// for cost/duration/calls, and agent runs are excluded from by_phase.
func TestBuildCostsByPhase(t *testing.T) {
	db := openProjTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "i", Description: "i"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	mustRun := func(kind domain.RunKind, stage string, usd float64, ms int64) {
		t.Helper()
		runID, err := db.CreateRun(ctx, kind, "", "role", ini.ID, stage)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.CreateRunManifest(ctx, &domain.RunManifest{
			RunID: runID, RequestedModel: "m", ActualModel: "m",
			Usage:      domain.RunUsage{CostUSD: usd, DurationMS: ms},
			StartedAt:  time.Now(),
			FinishedAt: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
	}
	mustRun(domain.RunModel, "product_definition", 0.10, 1000)
	mustRun(domain.RunModel, "product_definition", 0.05, 500)
	mustRun(domain.RunModel, "review", 0.20, 2000)
	mustRun(domain.RunAgent, "agent:TASK-1", 0.00, 3000) // excluded from by_phase

	costs, err := BuildCosts(ctx, db, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	byPhase := map[string]domain.PhaseCost{}
	for _, p := range costs.ByPhase {
		byPhase[p.Phase] = p
	}
	pd, ok := byPhase["product_definition"]
	if !ok {
		t.Fatal("missing product_definition phase")
	}
	if pd.Calls != 2 {
		t.Errorf("product_definition calls = %d, want 2", pd.Calls)
	}
	if pd.USD < 0.1499 || pd.USD > 0.1501 {
		t.Errorf("product_definition usd = %v, want ~0.15", pd.USD)
	}
	if pd.Seconds != 1.5 {
		t.Errorf("product_definition seconds = %v, want 1.5", pd.Seconds)
	}
	if _, ok := byPhase["agent:TASK-1"]; ok {
		t.Error("agent run leaked into by_phase")
	}
	if costs.Agent.Runs != 1 {
		t.Errorf("agent runs = %d, want 1", costs.Agent.Runs)
	}
	if costs.Agent.Seconds != 3 {
		t.Errorf("agent seconds = %v, want 3", costs.Agent.Seconds)
	}
	wantTotal := 0.10 + 0.05 + 0.20
	if costs.TotalUSD < wantTotal-0.001 || costs.TotalUSD > wantTotal+0.001 {
		t.Errorf("total_usd = %v, want ~%v", costs.TotalUSD, wantTotal)
	}
}

// TestAgentCostFromRuns_NotMeasuredByDefault pins the safe default: with no
// change sets (or none whose AgentOutput parses), usd is nil with a note --
// never a guess.
func TestAgentCostFromRuns_NotMeasuredByDefault(t *testing.T) {
	db := openProjTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "i", Description: "i"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "t", Statement: "s"}
	if err := db.CreateTaskMulti(ctx, task, nil, nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d", AgentOutput: "no cost info printed here"}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	runID, err := db.CreateRun(ctx, domain.RunAgent, "", "implementer", ini.ID, "agent:TASK-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRunManifest(ctx, &domain.RunManifest{RunID: runID, Usage: domain.RunUsage{DurationMS: 500}}); err != nil {
		t.Fatal(err)
	}

	stats, err := db.ListAgentRunStats(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent := AgentCostFromRuns(ctx, db, ini.ID, stats)
	if agent.USD != nil {
		t.Fatalf("usd = %v, want nil (not measured)", *agent.USD)
	}
	if agent.Note == "" {
		t.Error("expected a note explaining usd is nil")
	}
}

// TestAgentCostFromRuns_ParsesWhenEveryRunReportsCost pins the one case usd
// is populated: every change set's AgentOutput yields a parseable cost.
func TestAgentCostFromRuns_ParsesWhenEveryRunReportsCost(t *testing.T) {
	db := openProjTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "i", Description: "i"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "t", Statement: "s"}
	if err := db.CreateTaskMulti(ctx, task, nil, nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: ini.ID, TaskID: task.ID, PatchDigest: "d", AgentOutput: "done. total cost: $0.42"}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	runID, err := db.CreateRun(ctx, domain.RunAgent, "", "implementer", ini.ID, "agent:TASK-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CreateRunManifest(ctx, &domain.RunManifest{RunID: runID, Usage: domain.RunUsage{DurationMS: 1000}}); err != nil {
		t.Fatal(err)
	}

	stats, err := db.ListAgentRunStats(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	agent := AgentCostFromRuns(ctx, db, ini.ID, stats)
	if agent.USD == nil {
		t.Fatal("usd is nil, want 0.42")
	}
	if *agent.USD < 0.419 || *agent.USD > 0.421 {
		t.Errorf("usd = %v, want ~0.42", *agent.USD)
	}
}

// TestParseAgentCostUSD is a narrow unit test of the regex itself.
func TestParseAgentCostUSD(t *testing.T) {
	cases := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"Cost: $0.1234", 0.1234, true},
		{"total cost: $1.20", 1.20, true},
		{"cost=$0.03", 0.03, true},
		{"no mention of spend here", 0, false},
	}
	for _, c := range cases {
		got, ok := ParseAgentCostUSD(c.in)
		if ok != c.ok {
			t.Errorf("ParseAgentCostUSD(%q) ok = %v, want %v", c.in, ok, c.ok)
			continue
		}
		if ok && (got < c.want-0.0001 || got > c.want+0.0001) {
			t.Errorf("ParseAgentCostUSD(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
