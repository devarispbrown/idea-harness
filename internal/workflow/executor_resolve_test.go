package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor"
)

func TestResolveExecutor(t *testing.T) {
	keyed := config.HarnessConfig{DefaultModel: "openai/gpt-5.6-luna", Executor: config.ExecutorConfig{Kind: config.ExecutorAuto}}
	withKey := func(name string) string {
		if name == "OPENAI_API_KEY" {
			return "sk-test"
		}
		return ""
	}
	noKey := func(string) string { return "" }

	for _, tc := range []struct {
		name         string
		h            config.HarnessConfig
		lookup       func(string) string
		docker       bool
		wantResolved string
		wantWarning  string // substring; "" means no warning
		wantNote     bool
	}{
		{name: "auto with key and docker runs hermes", h: keyed, lookup: withKey, docker: true, wantResolved: "hermes"},
		{name: "auto without the key falls back loudly", h: keyed, lookup: noKey, docker: true, wantResolved: "internal", wantWarning: "OPENAI_API_KEY"},
		{name: "auto without docker falls back loudly", h: keyed, lookup: withKey, docker: false, wantResolved: "internal", wantWarning: "Docker is not running"},
		{name: "unset kind is auto", h: config.HarnessConfig{DefaultModel: "openai/gpt-5.6-luna"}, lookup: withKey, docker: true, wantResolved: "hermes"},
		{name: "demo mode falls back loudly", h: config.HarnessConfig{Executor: config.ExecutorConfig{Kind: config.ExecutorAuto}}, lookup: withKey, docker: true,
			wantResolved: "internal", wantWarning: "no coding model is configured"},
		{name: "a keyless provider is never auto-picked", h: config.HarnessConfig{DefaultModel: "ollama/qwen3.8:latest", Executor: config.ExecutorConfig{Kind: config.ExecutorAuto}},
			lookup: withKey, docker: true, wantResolved: "internal", wantWarning: "needs no key"},
		{name: "explicit internal is quiet", h: config.HarnessConfig{DefaultModel: "openai/gpt-5.6-luna", Executor: config.ExecutorConfig{Kind: config.ExecutorInternal}},
			lookup: withKey, docker: true, wantResolved: "internal", wantNote: true},
		{name: "explicit hermes is not second-guessed", h: config.HarnessConfig{Executor: config.ExecutorConfig{Kind: config.ExecutorHermes}},
			lookup: noKey, docker: false, wantResolved: "hermes"},
		{name: "explicit acp stays acp", h: config.HarnessConfig{Executor: config.ExecutorConfig{Kind: config.ExecutorACP, Image: "img"}},
			lookup: noKey, docker: false, wantResolved: "acp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restore := SetAutoDockerProbeForTest(func(context.Context) bool { return tc.docker })
			defer restore()
			res := ResolveExecutor(context.Background(), tc.h, nil, tc.lookup)
			if res.Resolved != tc.wantResolved {
				t.Fatalf("resolved = %q, want %q (%+v)", res.Resolved, tc.wantResolved, res)
			}
			if tc.wantWarning == "" && res.Warning != "" {
				t.Fatalf("unexpected warning %q", res.Warning)
			}
			if tc.wantWarning != "" {
				if !strings.Contains(res.Warning, tc.wantWarning) || !strings.HasPrefix(res.Warning, "NO REAL CODE WILL BE WRITTEN") {
					t.Fatalf("warning = %q, want it loud and naming %q", res.Warning, tc.wantWarning)
				}
				if !strings.Contains(res.Warning, "executor.kind: internal") {
					t.Fatalf("warning = %q does not say how to silence it on purpose", res.Warning)
				}
				if res.Requested != "auto" {
					t.Fatalf("requested = %q, want auto", res.Requested)
				}
			}
			if tc.wantNote != (res.Note != "") {
				t.Fatalf("note = %q, want present=%v", res.Note, tc.wantNote)
			}
			if res.Resolved == "hermes" && tc.h.Executor.Image == "" && res.Image != executor.DefaultAgentImage("hermes") {
				t.Fatalf("auto hermes image = %q, want the pinned default", res.Image)
			}
		})
	}
}

func TestSelectExecutorACP(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	base := config.ExecutorConfig{Kind: config.ExecutorACP, Model: "anthropic/claude-sonnet-5", Image: "ghcr.io/x/claude-acp:1",
		ACP: config.ACPConfig{Command: []string{"claude-code-acp"}}}

	e := &Engine{cfg: config.HarnessConfig{Executor: base}}
	ex, err := e.selectExecutor()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ex.(*executor.ACPExecutor); !ok || ex.Name() != "acp" {
		t.Fatalf("selectExecutor(acp) = %T %q", ex, ex.Name())
	}
	if tier := executor.TrustTierFor(ex.Name()); !executor.TrustAllows(tier, "write_file") {
		t.Fatalf("acp's trust tier %q may not write_file, so Execute would refuse it", tier)
	}

	noImage := base
	noImage.Image = ""
	if _, err := (&Engine{cfg: config.HarnessConfig{Executor: noImage}}).selectExecutor(); err == nil || !strings.Contains(err.Error(), "--acp-command") {
		t.Fatalf("acp without an image: err = %v", err)
	}
	noCmd := base
	noCmd.ACP.Command = nil
	if _, err := (&Engine{cfg: config.HarnessConfig{Executor: noCmd}}).selectExecutor(); err == nil || !strings.Contains(err.Error(), "executor.acp.command") {
		t.Fatalf("acp without a command: err = %v", err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := (&Engine{cfg: config.HarnessConfig{Executor: base}}).selectExecutor(); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Fatalf("acp without its key: err = %v", err)
	}
}

// TestGoTestNeverAutoResolvesToARealAgent pins the guard: without an
// injected probe, auto must not launch a real container from `go test`, even
// on a machine with Docker and a key.
func TestGoTestNeverAutoResolvesToARealAgent(t *testing.T) {
	h := config.HarnessConfig{DefaultModel: "openai/gpt-5.6-luna"}
	res := ResolveExecutor(context.Background(), h, nil, func(string) string { return "sk-test" })
	if res.Resolved != "internal" {
		t.Fatalf("resolved = %q inside a test binary with no probe injected", res.Resolved)
	}
}

// TestExecuteRecordsExecutorProvenanceOnTheManifest: "requested: auto,
// resolved: internal" and the warning are on the run manifest, and the
// warning is on the change set's agent output where a reviewer looks.
func TestExecuteRecordsExecutorProvenanceOnTheManifest(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()
	task := &domain.Task{InitiativeID: iniID, Title: "t", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(cs.AgentOutput, "NO REAL CODE WILL BE WRITTEN") {
		t.Fatalf("the change set's agent output does not carry the fallback warning: %q", cs.AgentOutput)
	}
	var runID string
	if err := db.SQL().QueryRowContext(ctx, `SELECT id FROM runs WHERE initiative_id = ? AND stage LIKE 'agent:%'`, iniID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	m, err := db.GetRunManifest(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	if m.Executor == nil || m.Executor.Requested != "auto" || m.Executor.Resolved != "internal" || !strings.Contains(m.Executor.Warning, "NO REAL CODE") {
		t.Fatalf("manifest executor = %+v, want requested auto, resolved internal, with the warning", m.Executor)
	}
}
