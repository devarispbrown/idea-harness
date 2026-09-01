package workflow

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/idea-harness/idea-harness/internal/blob"
	"github.com/idea-harness/idea-harness/internal/config"
	"github.com/idea-harness/idea-harness/internal/domain"
	"github.com/idea-harness/idea-harness/internal/model"
	"github.com/idea-harness/idea-harness/internal/security"
	"github.com/idea-harness/idea-harness/internal/store"
)

func newTestEngine(t *testing.T, fp *model.FakeProvider, cfg config.HarnessConfig) (*Engine, *store.DB) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	blobs, err := blob.New(filepath.Join(t.TempDir(), "blobs"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Review.MaxCycles <= 0 {
		cfg.Review.MaxCycles = 3
	}
	reg := model.NewRegistry("idea-harness-default")
	reg.Add("idea-harness-default", fp)
	eng := New(db, blobs, reg, cfg, nil)
	// Test blobs use a nil redactor, so they are already redaction-free; wire
	// them as the raw store so contentBlobs() is non-nil (fail-closed).
	eng.SetRawBlobs(blobs)
	return eng, db
}

func scriptedProvider() *model.FakeProvider {
	fp := model.NewFakeProvider()
	fp.SetResponse("idea_brief", `{"title":"Usage-based billing","summary":"Local-first tool","assumptions":["users need billing"],"open_questions":[]}`)
	fp.SetResponse("research_brief", `{"summary":"research","findings":[{"statement":"market exists","type":"assumption","confidence":"low","source":"model knowledge, unverified"}]}`)
	fp.SetResponse("prd", `{"summary":"PRD","requirements":[{"statement":"Meter usage events","priority":"must","acceptance_criteria":[{"metric":"events","target":">= 1","condition":"per request"}]}],"decisions":[],"open_questions":[],"risks":[]}`)
	fp.SetResponse("review", `{"findings":[]}`)
	return fp
}

func TestRunHappyPathApproves(t *testing.T) {
	fp := scriptedProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "Usage-based billing", Description: "build it"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	status, err := eng.Run(ctx, ini.ID, ini.Description)
	if err != nil {
		t.Fatal(err)
	}
	if status != domain.InitiativeApproved {
		t.Fatalf("expected approved, got %q", status)
	}

	reqs, err := db.ListRequirements(ctx, ini.ID)
	if err != nil || len(reqs) != 1 {
		t.Fatalf("expected 1 requirement, got %d (err=%v)", len(reqs), err)
	}
	if reqs[0].Code != "REQ-1" {
		t.Fatalf("expected REQ-1, got %q", reqs[0].Code)
	}

	// Artifacts must exist (IdeaBrief, ResearchBrief, PRD).
	versions, err := db.ListArtifactVersions(ctx, ini.ID)
	if err != nil || len(versions) != 3 {
		t.Fatalf("expected 3 artifact versions, got %d (err=%v)", len(versions), err)
	}

	got, err := db.GetInitiative(ctx, ini.ID)
	if err != nil || got.Status != domain.InitiativeApproved {
		t.Fatalf("expected persisted approved status, got %q (err=%v)", got.Status, err)
	}
}

func TestRunNonConvergencePausesForHuman(t *testing.T) {
	fp := scriptedProvider()
	// Vague criterion: triggers a deterministic high finding every cycle.
	fp.SetResponse("prd", `{"summary":"PRD","requirements":[{"statement":"Be fast","priority":"must","acceptance_criteria":[{"metric":"latency","target":"fast","condition":""}]}],"decisions":[],"open_questions":[],"risks":[]}`)
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	status, err := eng.Run(ctx, ini.ID, "x")
	if err != nil {
		t.Fatal(err)
	}
	if status != domain.InitiativeNeedsHumanJudgment {
		t.Fatalf("expected needs_human_judgment, got %q", status)
	}
}

// TestCanarySecretRedactedFromArtifacts verifies the leak-sweep invariant (§18,
// §48): a canary secret echoed by the model into an artifact body is redacted
// before it reaches the BlobStore.
func TestCanarySecretRedactedFromArtifacts(t *testing.T) {
	const canary = "sk-canary-abcdefghijklmnopqrstuvwxyz123456"

	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	redactor := security.NewRedactor(canary)
	blobs, err := blob.New(filepath.Join(t.TempDir(), "blobs"), redactor.Redact)
	if err != nil {
		t.Fatal(err)
	}

	fp := model.NewFakeProvider()
	fp.SetResponse("idea_brief", `{"title":"x","summary":"x"}`)
	fp.SetResponse("research_brief", `{"summary":"x","findings":[]}`)
	fp.SetResponse("prd", `{"summary":"leaked secret: `+canary+`","requirements":[{"statement":"Meter","priority":"must","acceptance_criteria":[{"metric":"events","target":">= 1","condition":"per request"}]}],"decisions":[],"open_questions":[],"risks":[]}`)
	fp.SetResponse("review", `{"findings":[]}`)

	reg := model.NewRegistry("idea-harness-default")
	reg.Add("idea-harness-default", fp)
	eng := New(db, blobs, reg, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}}, nil)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Run(ctx, ini.ID, "x"); err != nil {
		t.Fatal(err)
	}

	// Sweep every persisted artifact body for the canary.
	versions, err := db.ListArtifacts(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range versions {
		body, err := blobs.Get(v.BodyDigest)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(body), canary) {
			t.Fatalf("canary leaked into artifact %s", v.Kind)
		}
	}
}

func TestRunReviseConverges(t *testing.T) {
	fp := scriptedProvider()
	// First PRD is vague; the queued revised PRD is verifiable.
	fp.EnqueueResponse("prd", `{"summary":"PRD","requirements":[{"statement":"Be fast","priority":"must","acceptance_criteria":[{"metric":"latency","target":"fast","condition":""}]}],"decisions":[],"open_questions":[],"risks":[]}`)
	fp.SetResponse("prd", `{"summary":"PRD","requirements":[{"statement":"Be fast","priority":"must","acceptance_criteria":[{"metric":"p95 latency","target":"< 200ms","condition":"reference workload"}]}],"decisions":[],"open_questions":[],"risks":[]}`)

	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}

	status, err := eng.Run(ctx, ini.ID, "x")
	if err != nil {
		t.Fatal(err)
	}
	if status != domain.InitiativeApproved {
		t.Fatalf("expected approved after revise, got %q", status)
	}
}

func TestResolveModelSpecPrecedence(t *testing.T) {
	fp := model.NewFakeProvider()

	cfg := config.HarnessConfig{
		DefaultModel: "anthropic/claude-sonnet-4-5",
		Models:       map[string]string{"adversarial_reviewer": "anthropic/claude-opus-4-1"},
	}
	eng, _ := newTestEngine(t, fp, cfg)
	if got := eng.resolveModelSpec("adversarial_reviewer"); got != "anthropic/claude-opus-4-1" {
		t.Errorf("per-role model = %q, want opus", got)
	}
	if got := eng.resolveModelSpec("product_manager"); got != "anthropic/claude-sonnet-4-5" {
		t.Errorf("fallback default model = %q, want sonnet", got)
	}

	// implementerModel: role map wins over executor.model.
	cfg2 := config.HarnessConfig{
		DefaultModel: "anthropic/claude-sonnet-4-5",
		Executor:     config.ExecutorConfig{Model: "openai/gpt-4o"},
		Models:       map[string]string{"implementer": "openai/gpt-5.2"},
	}
	eng2, _ := newTestEngine(t, fp, cfg2)
	if got := eng2.implementerModel(); got != "openai/gpt-5.2" {
		t.Errorf("implementer model (role map) = %q, want gpt-5.2", got)
	}

	// Without models.implementer, executor.model wins.
	cfg3 := config.HarnessConfig{
		DefaultModel: "anthropic/claude-sonnet-4-5",
		Executor:     config.ExecutorConfig{Model: "openai/gpt-4o"},
	}
	eng3, _ := newTestEngine(t, fp, cfg3)
	if got := eng3.implementerModel(); got != "openai/gpt-4o" {
		t.Errorf("implementer model (executor.model) = %q, want gpt-4o", got)
	}
}

func TestCanarySecretRedactedFromStructuredFields(t *testing.T) {
	const canary = "AIzaSyD-canary-google-key-1234567890abcd"

	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	redactor := security.NewRedactor(canary)
	blobs, err := blob.New(filepath.Join(t.TempDir(), "blobs"), redactor.Redact)
	if err != nil {
		t.Fatal(err)
	}

	fp := model.NewFakeProvider()
	fp.SetResponse("idea_brief", `{"title":"x","summary":"x"}`)
	fp.SetResponse("research_brief", `{"summary":"x","findings":[]}`)
	fp.SetResponse("prd", `{"summary":"PRD","requirements":[{"statement":"use key `+canary+`","priority":"must","acceptance_criteria":[{"metric":"events","target":">= 1","condition":"per request"}]}],"decisions":[{"question":"which key?","options":["`+canary+`"],"recommendation":"`+canary+`"}],"open_questions":[],"risks":[]}`)
	fp.SetResponse("review", `{"findings":[]}`)

	reg := model.NewRegistry("idea-harness-default")
	reg.Add("idea-harness-default", fp)
	eng := New(db, blobs, reg, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}}, nil)
	eng.SetRedactor(redactor)
	eng.SetRawBlobs(blobs)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Run(ctx, ini.ID, "x"); err != nil {
		t.Fatal(err)
	}

	reqs, err := db.ListRequirements(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range reqs {
		if strings.Contains(r.Statement, canary) {
			t.Fatalf("canary leaked into requirement statement: %q", r.Statement)
		}
		for _, ac := range r.AcceptanceCriteria {
			if strings.Contains(ac.Metric+ac.Target+ac.Condition, canary) {
				t.Fatalf("canary leaked into acceptance criteria: %+v", ac)
			}
		}
	}
	decisions, err := db.ListDecisions(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range decisions {
		if strings.Contains(d.Question+" "+strings.Join(d.Options, " ")+" "+d.Recommendation, canary) {
			t.Fatalf("canary leaked into decision: %+v", d)
		}
	}
}
