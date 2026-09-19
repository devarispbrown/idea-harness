package workflow

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
)

func TestCalibrateRigor(t *testing.T) {
	cases := []struct {
		name       string
		configured RigorLevel
		scale      projectScale
		chg        changeSize
		want       RigorLevel
	}{
		{"configured wins outright", RigorThorough, projectScale{Files: 2, LOC: 10}, changeSize{Requirements: 1}, RigorThorough},
		{"small project, small change -> quick", RigorAuto, projectScale{Files: 10, LOC: 400}, changeSize{Requirements: 2, Files: 2}, RigorQuick},
		{"unmeasured workspace is NOT small", RigorAuto, projectScale{}, changeSize{Requirements: 1}, RigorStandard},
		{"empty configured behaves like auto", "", projectScale{Files: 10, LOC: 400}, changeSize{Requirements: 1}, RigorQuick},
		{"large project -> thorough even with a small change", RigorAuto, projectScale{Files: 500, LOC: 50000}, changeSize{Requirements: 1}, RigorThorough},
		{"large change -> thorough even on a small project", RigorAuto, projectScale{Files: 5, LOC: 100}, changeSize{Requirements: 20}, RigorThorough},
		{"mid-size -> standard", RigorAuto, projectScale{Files: 80, LOC: 8000}, changeSize{Requirements: 5}, RigorStandard},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := calibrateRigor(c.configured, c.scale, c.chg); got != c.want {
				t.Errorf("calibrateRigor(%q, %+v, %+v) = %q, want %q", c.configured, c.scale, c.chg, got, c.want)
			}
		})
	}
}

func TestFindingCap(t *testing.T) {
	cases := []struct {
		rigor RigorLevel
		want  int
	}{
		{RigorQuick, 5},
		{RigorStandard, 10},
		{RigorThorough, 20},
		{"", 10},
		{"garbage", 10},
	}
	for _, c := range cases {
		if got := findingCap(c.rigor); got != c.want {
			t.Errorf("findingCap(%q) = %d, want %d", c.rigor, got, c.want)
		}
	}
}

// TestCapFindingsKeepsHighestSeverityAndNotesTheDrop is review B1: "Keep the
// highest severity. Record the dropped count in a note."
func TestCapFindingsKeepsHighestSeverityAndNotesTheDrop(t *testing.T) {
	findings := []domain.Finding{
		{Severity: domain.SeverityLow, Finding: "low1"},
		{Severity: domain.SeverityCritical, Finding: "crit1"},
		{Severity: domain.SeverityMedium, Finding: "med1"},
		{Severity: domain.SeverityHigh, Finding: "high1"},
		{Severity: domain.SeverityObservation, Finding: "obs1"},
	}
	kept := capFindings(findings, 2, RigorQuick)
	if len(kept) != 3 { // 2 kept + 1 synthetic drop-note
		t.Fatalf("expected 2 kept + 1 note, got %d: %+v", len(kept), kept)
	}
	if kept[0].Severity != domain.SeverityCritical || kept[1].Severity != domain.SeverityHigh {
		t.Fatalf("expected critical then high kept, got %+v", kept[:2])
	}
	note := kept[len(kept)-1]
	if note.Category != RigorCapCategory || !note.Advisory {
		t.Fatalf("expected an advisory rigor-cap note, got %+v", note)
	}
	if !strings.Contains(note.Finding, "3") {
		t.Fatalf("expected the drop-note to say 3 dropped, got %q", note.Finding)
	}

	// Under the cap: nothing changes, no note.
	small := []domain.Finding{{Severity: domain.SeverityHigh}}
	if got := capFindings(small, 5, RigorStandard); len(got) != 1 {
		t.Fatalf("under the cap should be a no-op, got %d", len(got))
	}
}

func TestDetectStack(t *testing.T) {
	dir := t.TempDir()
	if got := detectStack(dir); got != nil {
		t.Fatalf("empty dir should detect no stack, got %v", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := detectStack(dir)
	if len(got) != 1 || got[0] != "Go" {
		t.Fatalf("expected [Go], got %v", got)
	}
}

// TestPRDPromptIncludesScaleAndStackSection is review B5, made testable: "a
// test that the prompt includes the scale/stack section when a workspace
// exists."
func TestPRDPromptIncludesScaleAndStackSection(t *testing.T) {
	fp := scriptedProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	workspace := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "go.mod"), []byte("module x\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "main.go"), []byte("package main\nfunc main(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	eng.SetWorkspaceRoot(workspace)

	ini := &domain.Initiative{Title: "x", Description: "add a widget"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if err := eng.runFraming(ctx, ini.ID, "add a widget"); err != nil {
		t.Fatal(err)
	}
	if err := eng.runProductDefinition(ctx, ini.ID, nil); err != nil {
		t.Fatal(err)
	}

	for _, schemaName := range []string{"idea_brief", "prd"} {
		found := false
		for _, call := range fp.Calls() {
			if call.JSONSchemaName != schemaName {
				continue
			}
			found = true
			if !strings.Contains(call.System, "Existing project scale") {
				t.Errorf("%s prompt missing the scale section:\n%s", schemaName, call.System)
			}
			if !strings.Contains(call.System, "Stack: Go") {
				t.Errorf("%s prompt missing the detected stack:\n%s", schemaName, call.System)
			}
			if !strings.Contains(call.System, "Do not add infrastructure") {
				t.Errorf("%s prompt missing the proportionality instruction:\n%s", schemaName, call.System)
			}
		}
		if !found {
			t.Errorf("no %s call recorded", schemaName)
		}
	}
}

// TestQuickRigorConvergesInOnePassWithAdvisories is review B1: "Quick
// converges in one pass: one review cycle, then accept with advisories."
// Configured explicitly here (calibration is exercised by TestCalibrateRigor)
// so the test is not coupled to the auto thresholds.
func TestQuickRigorConvergesInOnePassWithAdvisories(t *testing.T) {
	fp := scriptedProvider()
	// Vague criterion: triggers a deterministic HIGH (blocking) finding every
	// cycle -- the same fixture TestRunNonConvergencePausesForHuman uses to
	// prove the OPPOSITE (standard rigor) behavior.
	fp.SetResponse("prd", `{"summary":"PRD","requirements":[{"statement":"Be fast","priority":"must","acceptance_criteria":[{"metric":"latency","target":"fast","condition":""}]}],"decisions":[],"open_questions":[],"risks":[]}`)
	eng, db := newTestEngine(t, fp, config.HarnessConfig{
		Network: config.NetworkDisabled,
		Review:  config.ReviewConfig{MaxCycles: 3, Rigor: "quick"},
	})
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
		t.Fatalf("quick rigor should converge and approve in one pass, got %q", status)
	}

	// Exactly one PRD-authoring call: no revise cycle happened.
	prdCalls := 0
	for _, call := range fp.Calls() {
		if call.JSONSchemaName == "prd" {
			prdCalls++
		}
	}
	if prdCalls != 1 {
		t.Fatalf("expected exactly 1 PRD authoring call (one pass), got %d", prdCalls)
	}

	// The blocking finding is on record, but NOT falsely dispositioned
	// "fixed" -- nothing was fixed, it was accepted as an advisory by rigor
	// policy.
	findings, err := db.ListFindingsByInitiative(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	sawHigh := false
	for _, f := range findings {
		if f.Severity == domain.SeverityHigh {
			sawHigh = true
			if f.Disposition == domain.DispositionFixed {
				t.Errorf("a quick-rigor advisory finding must not be dispositioned 'fixed': %+v", f)
			}
		}
	}
	if !sawHigh {
		t.Fatal("expected the vague-criterion HIGH finding to still be recorded")
	}
}

// TestQuickRigorPlanReviewConvergesWithAdvisories is review B1 applied to the
// M2 plan review: quick accepts a blocking plan-review finding as advisory
// rather than parking the initiative on needs_human_judgment.
func TestQuickRigorPlanReviewConvergesWithAdvisories(t *testing.T) {
	fp := scriptedProvider()
	fp.SetResponse("plan_review", `{"findings":[{`+
		`"severity":"high","confidence":"high","category":"x",`+
		`"finding":"a plan problem","recommendation":"fix it"`+
		`}]}`)
	eng, db := newTestEngine(t, fp, config.HarnessConfig{
		Network: config.NetworkDisabled,
		Review:  config.ReviewConfig{MaxCycles: 3, Rigor: "quick"},
	})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Run(ctx, ini.ID, ini.Description); err != nil {
		t.Fatal(err)
	}
	status, err := eng.Advance(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if status != domain.InitiativePlanApproved {
		t.Fatalf("quick rigor should accept the plan despite the blocking finding, got %q", status)
	}
}

// TestPRDPromptSaysNewProjectWhenNoWorkspace proves the "brand new idea, no
// code yet" branch does not claim a scale it never measured.
func TestPRDPromptSaysNewProjectWhenNoWorkspace(t *testing.T) {
	fp := scriptedProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "a brand new idea"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if err := eng.runFraming(ctx, ini.ID, "a brand new idea"); err != nil {
		t.Fatal(err)
	}
	for _, call := range fp.Calls() {
		if call.JSONSchemaName != "idea_brief" {
			continue
		}
		if !strings.Contains(call.System, "new project with no existing code yet") {
			t.Errorf("expected the new-project sentence with no workspace set:\n%s", call.System)
		}
	}
}
