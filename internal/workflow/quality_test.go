package workflow

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/fsdiff"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// fakeSandbox scripts sandboxed quality commands: the first rule whose
// substring the command contains decides the exit code and output; anything
// unmatched exits 0.
type fakeSandbox struct {
	mu          sync.Mutex
	unavailable bool
	rules       []sandboxRule
	ran         []executor.RunOptions
}

type sandboxRule struct {
	contains string
	code     int
	out      string
}

func (f *fakeSandbox) Available(context.Context) bool { return !f.unavailable }

func (f *fakeSandbox) Run(_ context.Context, _ string, opts executor.RunOptions) (string, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ran = append(f.ran, opts)
	cmd := strings.Join(opts.Cmd, " ")
	for _, r := range f.rules {
		if strings.Contains(cmd, r.contains) {
			return r.out, r.code, nil
		}
	}
	return "ok\n", 0, nil
}

func (f *fakeSandbox) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, o := range f.ran {
		out = append(out, o.Cmd[len(o.Cmd)-1])
	}
	return out
}

type qualityFixture struct {
	eng   *Engine
	db    *store.DB
	fp    *model.FakeProvider
	sb    *fakeSandbox
	ini   string
	req   *domain.Requirement
	ws    string
	cfg   config.HarnessConfig
	ctx   context.Context
	csSeq int
}

// newQualityFixture builds a workspace holding base, then applies one change
// set that turns it into base+changes (a "" value deletes the file), with a
// task implementing one approved requirement.
func newQualityFixture(t *testing.T, cfg config.HarnessConfig, base, changes map[string]string) *qualityFixture {
	t.Helper()
	if cfg.Check.Image == "" {
		cfg.Check.Image = "golang:1.25"
	}
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, cfg)
	ws := t.TempDir()
	eng.SetWorkspaceRoot(ws)
	eng.SetWorkRoot(t.TempDir())
	sb := &fakeSandbox{}
	eng.SetQualitySandbox(sb)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "q", Description: "q"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Statement: "Serve widgets", Priority: domain.PriorityMust, Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "widgets", Target: ">= 1", Condition: "served"}}}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}
	for p, c := range base {
		writeWS(t, ws, p, c)
	}
	f := &qualityFixture{eng: eng, db: db, fp: fp, sb: sb, ini: ini.ID, req: req, ws: ws, cfg: cfg, ctx: ctx}
	if changes != nil {
		f.apply(t, base, changes)
	}
	return f
}

func writeWS(t *testing.T, ws, p, c string) {
	t.Helper()
	full := filepath.Join(ws, filepath.FromSlash(p))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
		t.Fatal(err)
	}
}

// apply records an applied change set from current (what the workspace holds)
// to changes, and writes changes into the workspace.
func (f *qualityFixture) apply(t *testing.T, current, changes map[string]string) *domain.ChangeSet {
	t.Helper()
	blobs := f.eng.contentBlobs()
	var files []domain.FileChange
	for p, after := range changes {
		fc := domain.FileChange{Path: p, Mode: "file"}
		if before, ok := current[p]; ok {
			d, err := blobs.Put([]byte(before))
			if err != nil {
				t.Fatal(err)
			}
			fc.BaseDigest = d
			fc.ChangeType = domain.FileModified
		} else {
			fc.ChangeType = domain.FileAdded
		}
		if after == "" {
			fc.ChangeType = domain.FileDeleted
			_ = os.Remove(filepath.Join(f.ws, filepath.FromSlash(p)))
		} else {
			d, err := blobs.Put([]byte(after))
			if err != nil {
				t.Fatal(err)
			}
			fc.Digest = d
			writeWS(t, f.ws, p, after)
		}
		files = append(files, fc)
	}
	raw, _ := json.Marshal(fsdiff.Patch{Files: files})
	pd, err := blobs.Put(raw)
	if err != nil {
		t.Fatal(err)
	}
	f.csSeq++
	task := &domain.Task{InitiativeID: f.ini, Title: "build", Statement: "build", Priority: domain.PriorityMust}
	if err := f.db.CreateTask(f.ctx, task, f.req.ID, nil); err != nil {
		t.Fatal(err)
	}
	cs := &domain.ChangeSet{InitiativeID: f.ini, TaskID: task.ID, PatchDigest: pd}
	if err := f.db.CreateChangeSet(f.ctx, cs); err != nil {
		t.Fatal(err)
	}
	for _, step := range [][2]domain.ChangeSetStatus{
		{domain.ChangeSetDraft, domain.ChangeSetChecksRunning},
		{domain.ChangeSetChecksRunning, domain.ChangeSetReviewRequired},
		{domain.ChangeSetReviewRequired, domain.ChangeSetApproved},
		{domain.ChangeSetApproved, domain.ChangeSetApplying},
	} {
		if err := f.db.TransitionChangeSet(f.ctx, cs.ID, step[0], step[1], ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.db.MarkChangeSetApplied(f.ctx, cs.ID, "", ""); err != nil {
		t.Fatal(err)
	}
	return cs
}

func (f *qualityFixture) measure(t *testing.T) QualityScorecard {
	t.Helper()
	sc, err := f.eng.MeasureQuality(f.ctx, f.ini)
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func dim(t *testing.T, sc QualityScorecard, d domain.QualityDimension) domain.QualityResult {
	t.Helper()
	for _, v := range sc.Dimensions {
		if v.Dimension == d {
			if v.Result == nil {
				t.Fatalf("%s was not measured (mode %s)", d, v.Mode)
			}
			return *v.Result
		}
	}
	t.Fatalf("no %s row", d)
	return domain.QualityResult{}
}

func findCheck(t *testing.T, r domain.QualityResult, name string) domain.QualityCheck {
	t.Helper()
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("%s has no check %q: %+v", r.Dimension, name, r.Checks)
	return domain.QualityCheck{}
}

func only(dims ...string) map[string]string {
	m := map[string]string{}
	for _, d := range domain.QualityDimensions {
		m[string(d)] = "off"
	}
	for _, d := range dims {
		m[d] = "advisory"
	}
	return m
}

const qReadme = "# Widgets\n\n## Installation\n\n```sh\ngo build ./...\ngo test ./...\n```\n\n## Usage\n\nRun it.\n"

func goModBase() map[string]string {
	return map[string]string{
		"go.mod":    "module example.com/widgets\n\ngo 1.25\n",
		"README.md": qReadme,
		".env":      "SECRET=1\n",
	}
}

// ---------- setup_reproducibility ----------

func TestSetupReproducibilityPassRunsDocumentedCommandsOfflineInAFreshCopy(t *testing.T) {
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: only("setup_reproducibility")}}
	f := newQualityFixture(t, cfg, goModBase(), map[string]string{"lib.go": "package widgets\n"})
	r := dim(t, f.measure(t), domain.QualitySetupReproducibility)
	if r.Status != domain.QualityPass {
		t.Fatalf("want pass, got %s: %+v", r.Status, r.Checks)
	}
	if got := strings.Join(f.sb.commands(), " | "); got != "go build ./... | go test ./..." {
		t.Fatalf("ran %q", got)
	}
	for _, o := range f.sb.ran {
		if o.Network {
			t.Error("a documented command ran with network")
		}
		if o.WorkRoot == f.ws {
			t.Error("commands must run in a fresh copy, not the live workspace")
		}
	}
	if c := findCheck(t, r, "go test ./..."); c.Citations[0].Path != "README.md" || c.Citations[0].Line != 7 {
		t.Errorf("a command result must cite its README line: %+v", c.Citations)
	}
	if len(r.Requirements) != 1 || r.Requirements[0] != f.req.Code {
		t.Errorf("result must be linked to the implemented requirement: %v", r.Requirements)
	}
}

func TestSetupReproducibilityFailsWhenADocumentedCommandFails(t *testing.T) {
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: only("setup_reproducibility")}}
	f := newQualityFixture(t, cfg, goModBase(), map[string]string{"lib.go": "package widgets\n"})
	f.sb.rules = []sandboxRule{{contains: "go build", code: 1, out: "lib.go:1: syntax error\n"}}
	r := dim(t, f.measure(t), domain.QualitySetupReproducibility)
	if r.Status != domain.QualityFail {
		t.Fatalf("want fail, got %s: %+v", r.Status, r.Checks)
	}
	if c := findCheck(t, r, "go test ./..."); c.Status != domain.QualityNotMeasured {
		t.Errorf("a command after a failure is not run, so not measured: %+v", c)
	}
	if !strings.Contains(r.Summary, "go build ./...: exit 1") {
		t.Errorf("summary should carry the failure: %s", r.Summary)
	}
}

func TestSetupReproducibilitySkippedFetchMakesAFailureNotMeasured(t *testing.T) {
	base := goModBase()
	base["README.md"] = "# W\n\n## Setup\n\n```sh\nnpm ci\nnpm test\n```\n"
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: only("setup_reproducibility")}}
	f := newQualityFixture(t, cfg, base, map[string]string{"lib.go": "package widgets\n"})
	f.sb.rules = []sandboxRule{{contains: "npm test", code: 1, out: "Cannot find module 'jest'\n"}}
	r := dim(t, f.measure(t), domain.QualitySetupReproducibility)
	if r.Status != domain.QualityNotMeasured {
		t.Fatalf("an offline failure after a skipped fetch cannot be blamed on the docs: got %s %+v", r.Status, r.Checks)
	}
	if c := findCheck(t, r, "npm ci"); c.Status != domain.QualityNotApplicable || !strings.Contains(c.Detail, "network") {
		t.Errorf("the fetch is skipped and says why: %+v", c)
	}

	// With a prepare phase configured, the same failure is a real fail, and
	// only the prepare command had the network.
	f.cfg.Verify.Setup.Prepare = []string{"npm ci"}
	f.eng.Reload(f.cfg, f.eng.registry, nil, f.eng.blobs, nil)
	f.sb.ran = nil
	r = dim(t, f.measure(t), domain.QualitySetupReproducibility)
	if r.Status != domain.QualityFail {
		t.Fatalf("with prepare, want fail, got %s", r.Status)
	}
	for _, o := range f.sb.ran {
		if o.Network != (o.Cmd[len(o.Cmd)-1] == "npm ci") {
			t.Errorf("network given to %q: %v", o.Cmd[len(o.Cmd)-1], o.Network)
		}
	}
}

func TestSetupReproducibilityNotMeasuredWithoutDockerAndGatedIsUnmet(t *testing.T) {
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: map[string]string{"setup_reproducibility": "gate", "docs_completeness": "off", "api_compatibility": "off"}}}
	f := newQualityFixture(t, cfg, goModBase(), map[string]string{"lib.go": "package widgets\n"})
	f.sb.unavailable = true
	sc := f.measure(t)
	r := dim(t, sc, domain.QualitySetupReproducibility)
	if r.Status != domain.QualityNotMeasured {
		t.Fatalf("no Docker is not measured, never pass: %s", r.Status)
	}
	if len(sc.GateUnmet) != 1 || !strings.Contains(sc.GateUnmet[0], "setup_reproducibility not measured") {
		t.Fatalf("gate unmet: %v", sc.GateUnmet)
	}
}

func TestSetupReproducibilityFallsBackToDetectedCommandsWithAWarning(t *testing.T) {
	base := goModBase()
	base["README.md"] = "# W\n\nNothing about building.\n"
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: only("setup_reproducibility")}}
	f := newQualityFixture(t, cfg, base, map[string]string{"lib.go": "package widgets\n"})
	r := dim(t, f.measure(t), domain.QualitySetupReproducibility)
	if r.Status != domain.QualityWarn {
		t.Fatalf("detected-only commands warn: %s %+v", r.Status, r.Checks)
	}
}

// ---------- docs_completeness ----------

func TestDocsCompletenessFailsOnUndocumentedRoute(t *testing.T) {
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: only("docs_completeness")}}
	f := newQualityFixture(t, cfg, goModBase(), map[string]string{
		"cmd/srv/main.go": "package main\n\nfunc main() {\n\tmux.HandleFunc(\"GET /v1/widgets\", list)\n}\n",
	})
	r := dim(t, f.measure(t), domain.QualityDocsCompleteness)
	if r.Status != domain.QualityFail {
		t.Fatalf("want fail, got %s %+v", r.Status, r.Checks)
	}
	c := findCheck(t, r, "routes_documented")
	if c.Citations[0].Path != "cmd/srv/main.go" || c.Citations[0].Line != 4 {
		t.Errorf("citation: %+v", c.Citations)
	}

	// Documenting it passes.
	f.apply(t, map[string]string{"README.md": qReadme}, map[string]string{"README.md": qReadme + "\n`GET /v1/widgets` lists widgets.\n"})
	r = dim(t, f.measure(t), domain.QualityDocsCompleteness)
	if r.Status != domain.QualityPass {
		t.Fatalf("after documenting: %s %+v", r.Status, r.Checks)
	}
}

// ---------- api_compatibility ----------

const qSpec = `openapi: 3.0.3
info: { title: w, version: "1" }
paths:
  /v1/widgets:
    get:
      responses:
        "200": { description: ok }
  /v1/gadgets:
    get:
      responses:
        "200": { description: ok }
`

func TestAPICompatibilityBreakingSpecChange(t *testing.T) {
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: only("api_compatibility")}}
	base := goModBase()
	base["api/openapi.yaml"] = qSpec
	removed := strings.Replace(qSpec, "  /v1/gadgets:\n    get:\n      responses:\n        \"200\": { description: ok }\n", "", 1)
	f := newQualityFixture(t, cfg, base, map[string]string{"api/openapi.yaml": removed})
	r := dim(t, f.measure(t), domain.QualityAPICompatibility)
	if r.Status != domain.QualityFail {
		t.Fatalf("an unacknowledged removed path fails: %s %+v", r.Status, r.Checks)
	}
	c := findCheck(t, r, "openapi: api/openapi.yaml")
	if !strings.Contains(c.Detail, "/v1/gadgets") || c.Citations[0].Line == 0 {
		t.Errorf("check: %+v", c)
	}

	// A requirement naming the path acknowledges it: warn, citing it.
	ack := &domain.Requirement{InitiativeID: f.ini, Statement: "Retire /v1/gadgets", Priority: domain.PriorityMust, Status: domain.RequirementApproved,
		AcceptanceCriteria: []domain.AcceptanceCriterion{{Metric: "gone", Target: "1", Condition: "x"}}}
	if err := f.db.CreateRequirement(f.ctx, ack); err != nil {
		t.Fatal(err)
	}
	r = dim(t, f.measure(t), domain.QualityAPICompatibility)
	if r.Status != domain.QualityWarn {
		t.Fatalf("acknowledged breaking change warns: %s %+v", r.Status, r.Checks)
	}
	c = findCheck(t, r, "openapi: api/openapi.yaml")
	if c.Citations[0].Requirement != ack.Code {
		t.Errorf("must cite the acknowledging requirement: %+v", c.Citations)
	}
	found := false
	for _, code := range r.Requirements {
		found = found || code == ack.Code
	}
	if !found {
		t.Errorf("the acknowledging requirement must be linked: %v", r.Requirements)
	}
}

func TestAPICompatibilityGoRemovedSymbolAndAdditive(t *testing.T) {
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: only("api_compatibility")}}
	base := goModBase()
	base["widgets.go"] = "package widgets\n\n// Parse parses.\nfunc Parse(s string) error { return nil }\n"
	base["other.go"] = "package widgets\n\n// Keep stays.\nfunc Keep() {}\n"
	f := newQualityFixture(t, cfg, base, map[string]string{"widgets.go": "package widgets\n\n// ParseAll parses.\nfunc ParseAll(s string) error { return nil }\n"})
	r := dim(t, f.measure(t), domain.QualityAPICompatibility)
	c := findCheck(t, r, "go_api")
	if c.Status != domain.QualityFail || !strings.Contains(c.Detail, "Parse was removed") || strings.Contains(c.Detail, "Keep") {
		t.Fatalf("go_api: %+v", c)
	}
	if findCheck(t, r, "openapi").Status != domain.QualityNotApplicable {
		t.Error("no spec: not applicable")
	}

	g := newQualityFixture(t, cfg, base, map[string]string{"widgets.go": base["widgets.go"] + "\n// More adds.\nfunc More() {}\n"})
	if c := findCheck(t, dim(t, g.measure(t), domain.QualityAPICompatibility), "go_api"); c.Status != domain.QualityPass {
		t.Fatalf("an addition is not breaking: %+v", c)
	}
}

// ---------- api_ergonomics ----------

func TestAPIErgonomicsModelReviewCitationsGovern(t *testing.T) {
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: only("api_ergonomics")}}
	base := goModBase()
	base["api/openapi.yaml"] = qSpec
	added := qSpec + "  /v1/Widget_List:\n    get:\n      responses:\n        \"200\": { description: ok }\n"
	f := newQualityFixture(t, cfg, base, map[string]string{"api/openapi.yaml": added})
	f.fp.SetResponse(QualityReviewSchemaName, `{"findings":[
		{"severity":"high","category":"naming","finding":"Widget_List breaks the lower-case plural convention","path":"api/openapi.yaml","line":12,"recommendation":"use /v1/widgets"},
		{"severity":"critical","category":"errors","finding":"no error shape anywhere","path":"api/openapi.yaml","line":2},
		{"severity":"high","category":"made-up","finding":"cites a file it was not shown","path":"main.go","line":1}
	]}`)
	r := dim(t, f.measure(t), domain.QualityAPIErgonomics)
	if r.Status != domain.QualityFail {
		t.Fatalf("a cited high finding fails: %s %+v", r.Status, r.Checks)
	}
	c := findCheck(t, r, "review: naming")
	if c.Method != domain.QualityMethodModel || c.Citations[0].Line != 12 {
		t.Errorf("cited finding: %+v", c)
	}
	sum := findCheck(t, r, "model_review")
	if !strings.Contains(sum.Detail, "2 finding(s) set aside") {
		t.Errorf("uncited findings must be recorded, not scored: %s", sum.Detail)
	}
	for _, ch := range r.Checks {
		if ch.Name == "review: errors" || ch.Name == "review: made-up" {
			t.Errorf("uncited finding reached the scorecard: %+v", ch)
		}
	}
}

func TestAPIErgonomicsDemoProviderIsNotMeasured(t *testing.T) {
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: only("api_ergonomics")}}
	base := goModBase()
	base["api/openapi.yaml"] = qSpec
	f := newQualityFixture(t, cfg, base, map[string]string{"api/openapi.yaml": qSpec + "  /v1/x:\n    get:\n      responses:\n        \"200\": { description: ok }\n"})
	// No scripted response: the fake answers with its demo output.
	r := dim(t, f.measure(t), domain.QualityAPIErgonomics)
	if r.Status != domain.QualityNotMeasured {
		t.Fatalf("a demo 'review' must not score as pass: %s %+v", r.Status, r.Checks)
	}
}

func TestAPIErgonomicsNotApplicableWithoutPublicSurface(t *testing.T) {
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: only("api_ergonomics")}}
	f := newQualityFixture(t, cfg, goModBase(), map[string]string{"internal/x/x.go": "package x\nfunc Do() {}\n"})
	if r := dim(t, f.measure(t), domain.QualityAPIErgonomics); r.Status != domain.QualityNotApplicable {
		t.Fatalf("internal-only change: %s", r.Status)
	}
}

// ---------- security ----------

func TestSecurityRescanAndUnconfiguredScannerIsNotMeasured(t *testing.T) {
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: only("security")}}
	f := newQualityFixture(t, cfg, goModBase(), map[string]string{"cfg.go": "package widgets\n\nconst key = \"AKIAIOSFODNN7EXAMPLQ\"\n"})
	r := dim(t, f.measure(t), domain.QualitySecurity)
	if c := findCheck(t, r, "secret_scan"); c.Status != domain.QualityFail || c.Citations[0].Line != 3 {
		t.Fatalf("secret in applied content: %+v", c)
	}
	if c := findCheck(t, r, "vulnerability_scan"); c.Status != domain.QualityNotMeasured {
		t.Fatalf("no scanner is not measured: %+v", c)
	}
	if r.Status != domain.QualityFail {
		t.Fatalf("rollup: %s", r.Status)
	}
}

func TestSecurityConfiguredScannerRunsWithItsNetworkSetting(t *testing.T) {
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: only("security"),
		Security: config.VerifySecurityConfig{Scanners: []config.VerifyScanner{
			{Name: "govulncheck", Command: "govulncheck ./...", Network: true},
			{Name: "gosec", Command: "gosec ./..."},
		}}}}
	f := newQualityFixture(t, cfg, goModBase(), map[string]string{"lib.go": "package widgets\n"})
	f.sb.rules = []sandboxRule{{contains: "gosec", code: 127}}
	r := dim(t, f.measure(t), domain.QualitySecurity)
	if c := findCheck(t, r, "scanner: govulncheck"); c.Status != domain.QualityPass {
		t.Fatalf("govulncheck: %+v", c)
	}
	if c := findCheck(t, r, "scanner: gosec"); c.Status != domain.QualityNotMeasured {
		t.Fatalf("a scanner missing from the image is not measured: %+v", c)
	}
	for _, o := range f.sb.ran {
		cmd := o.Cmd[len(o.Cmd)-1]
		if o.Network != strings.HasPrefix(cmd, "govulncheck") {
			t.Errorf("%s ran with network=%v", cmd, o.Network)
		}
	}
}

// ---------- performance ----------

func TestPerformanceBudgetBaselineAndRegression(t *testing.T) {
	max := 1000.0
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: only("performance"),
		Performance: config.VerifyPerformanceConfig{Budgets: []config.PerformanceBudget{
			{Name: "parse", Command: "go test -bench Parse", Metric: `BenchmarkParse\S*\s+\d+\s+([0-9.]+) ns/op`, Unit: "ns/op", Max: &max, MaxRegressionPct: 10},
		}}}}
	f := newQualityFixture(t, cfg, goModBase(), map[string]string{"lib.go": "package widgets\n"})
	f.sb.rules = []sandboxRule{{contains: "bench", out: "BenchmarkParse-8  100  500 ns/op\n"}}
	r := dim(t, f.measure(t), domain.QualityPerformance)
	if r.Status != domain.QualityPass || r.Metrics["parse"] != 500 {
		t.Fatalf("first measurement: %s %v %+v", r.Status, r.Metrics, r.Checks)
	}
	if c := findCheck(t, r, "budget: parse"); !strings.Contains(c.Detail, "no baseline yet") {
		t.Errorf("first run has no baseline: %s", c.Detail)
	}
	f.sb.rules = []sandboxRule{{contains: "bench", out: "BenchmarkParse-8  100  600 ns/op\n"}}
	r = dim(t, f.measure(t), domain.QualityPerformance)
	if r.Status != domain.QualityFail || !strings.Contains(findCheck(t, r, "budget: parse").Detail, "+20.0%") {
		t.Fatalf("20%% over the previous measurement must fail: %s %+v", r.Status, r.Checks)
	}
}

func TestPerformanceWithoutBudgetsIsNotMeasured(t *testing.T) {
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: only("performance")}}
	f := newQualityFixture(t, cfg, goModBase(), map[string]string{"lib.go": "package widgets\n"})
	if r := dim(t, f.measure(t), domain.QualityPerformance); r.Status != domain.QualityNotMeasured {
		t.Fatalf("no budgets: %s", r.Status)
	}
}

// ---------- operational_readiness ----------

func TestOperationalReadinessFixtures(t *testing.T) {
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: only("operational_readiness")}}
	ready := "package main\n\nimport (\n\t\"log/slog\"\n\t\"net/http\"\n\t\"os\"\n\t\"os/signal\"\n\t\"syscall\"\n)\n\nfunc main() {\n\tmux := http.NewServeMux()\n\tmux.HandleFunc(\"GET /healthz\", nil)\n\tsrv := &http.Server{Addr: os.Getenv(\"ADDR\"), Handler: mux}\n\tsignal.Notify(ch, syscall.SIGTERM)\n\tgo srv.ListenAndServe()\n\tslog.Info(\"up\")\n\tsrv.Shutdown(ctx)\n}\n"
	f := newQualityFixture(t, cfg, goModBase(), map[string]string{"cmd/srv/main.go": ready})
	if r := dim(t, f.measure(t), domain.QualityOperationalReadiness); r.Status != domain.QualityPass {
		t.Fatalf("ready service: %s %+v", r.Status, r.Checks)
	}
	g := newQualityFixture(t, cfg, goModBase(), map[string]string{
		"db/migrations/0002_drop.sql": "ALTER TABLE widgets DROP COLUMN legacy;\n",
	})
	r := dim(t, g.measure(t), domain.QualityOperationalReadiness)
	if c := findCheck(t, r, "migration_safety"); c.Status != domain.QualityWarn {
		t.Fatalf("destructive migration: %+v", c)
	}
}

// ---------- policy and the gate ----------

func passRequirement(t *testing.T, f *qualityFixture) {
	t.Helper()
	if err := f.eng.RecordHumanPass(f.ctx, f.ini, f.req.Code, "checked", ""); err != nil {
		t.Fatal(err)
	}
}

func TestGatedDimensionHoldsCompletionAndAdvisoryDoesNot(t *testing.T) {
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: map[string]string{
		"docs_completeness": "gate", "setup_reproducibility": "off", "api_compatibility": "off"}}}
	base := goModBase()
	delete(base, "README.md")
	f := newQualityFixture(t, cfg, base, map[string]string{"lib.go": "package widgets\n"})
	passRequirement(t, f)

	// Gated and never measured: not complete.
	if err := f.eng.VerifyInitiative(f.ctx, f.ini); err != nil {
		t.Fatal(err)
	}
	if ini, _ := f.db.GetInitiative(f.ctx, f.ini); ini.Status == domain.InitiativeComplete {
		t.Fatal("an unmeasured gated dimension must hold completion")
	}
	// Measured and failing (no README): still not complete.
	sc := f.measure(t)
	if len(sc.GateUnmet) != 1 || !strings.Contains(sc.GateUnmet[0], "docs_completeness fail") {
		t.Fatalf("gate unmet: %v", sc.GateUnmet)
	}
	if err := f.eng.VerifyInitiative(f.ctx, f.ini); err != nil {
		t.Fatal(err)
	}
	if ini, _ := f.db.GetInitiative(f.ctx, f.ini); ini.Status == domain.InitiativeComplete {
		t.Fatal("a failing gated dimension must hold completion")
	}

	// The same failure as advisory does not hold it.
	f.cfg.Verify.Dimensions["docs_completeness"] = "advisory"
	f.eng.Reload(f.cfg, f.eng.registry, nil, f.eng.blobs, nil)
	sc = f.measure(t)
	if len(sc.GateUnmet) != 0 || dim(t, sc, domain.QualityDocsCompleteness).Status != domain.QualityFail {
		t.Fatalf("advisory: unmet=%v", sc.GateUnmet)
	}
	if err := f.eng.VerifyInitiative(f.ctx, f.ini); err != nil {
		t.Fatal(err)
	}
	if ini, _ := f.db.GetInitiative(f.ctx, f.ini); ini.Status != domain.InitiativeComplete {
		t.Fatalf("advisory failure must not hold completion; status %s", ini.Status)
	}
}

func TestStaleResultIsNotMeasuredAtTheGate(t *testing.T) {
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: map[string]string{
		"docs_completeness": "gate", "setup_reproducibility": "off", "api_compatibility": "off"}}}
	f := newQualityFixture(t, cfg, goModBase(), map[string]string{"lib.go": "package widgets\n"})
	if sc := f.measure(t); len(sc.GateUnmet) != 0 || sc.NeedsMeasurement {
		t.Fatalf("fresh pass: %+v", sc)
	}
	f.apply(t, map[string]string{}, map[string]string{"more.go": "package widgets\n"})
	sc, err := f.eng.QualityScorecard(f.ctx, f.ini)
	if err != nil {
		t.Fatal(err)
	}
	if !sc.NeedsMeasurement || len(sc.GateUnmet) != 1 || !strings.Contains(sc.GateUnmet[0], "stale") {
		t.Fatalf("a new applied change set makes the result stale: %+v", sc)
	}
}

func TestQuickRigorMeasuresNothingByDefault(t *testing.T) {
	cfg := config.HarnessConfig{Review: config.ReviewConfig{Rigor: "quick"}}
	f := newQualityFixture(t, cfg, goModBase(), map[string]string{"lib.go": "package widgets\n"})
	sc := f.measure(t)
	for _, v := range sc.Dimensions {
		if v.Mode != domain.QualityModeOff || v.Result != nil {
			t.Fatalf("quick: %s mode %s result %v", v.Dimension, v.Mode, v.Result)
		}
	}
	if sc.NeedsMeasurement || len(f.sb.ran) != 0 {
		t.Fatal("nothing enabled, nothing run")
	}
}

func TestThoroughRigorGatesTheCoreDimensions(t *testing.T) {
	cfg := config.HarnessConfig{Review: config.ReviewConfig{Rigor: "thorough"}}
	f := newQualityFixture(t, cfg, goModBase(), map[string]string{"lib.go": "package widgets\n"})
	sc, err := f.eng.QualityScorecard(f.ctx, f.ini)
	if err != nil {
		t.Fatal(err)
	}
	if sc.Rigor != "thorough" || len(sc.GateUnmet) != 3 {
		t.Fatalf("thorough gates setup, docs and api_compatibility: %+v", sc.GateUnmet)
	}
}

// ---------- traceability ----------

func TestQualityResultIsTraceableFromTheRequirement(t *testing.T) {
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: only("docs_completeness")}}
	f := newQualityFixture(t, cfg, goModBase(), map[string]string{"lib.go": "package widgets\n"})
	r := dim(t, f.measure(t), domain.QualityDocsCompleteness)
	items, err := f.eng.Impact(f.ctx, f.ini, "requirement", f.req.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, it := range items {
		if it.FromType == "quality_result" && it.FromID == r.ID && it.EdgeType == string(domain.EdgeVerifies) {
			found = strings.HasPrefix(it.FromLabel, "quality docs_completeness: ")
		}
	}
	if !found {
		t.Fatalf("impact on the requirement must show the quality result: %+v", items)
	}
	why, err := f.eng.Why(f.ctx, f.ini, "quality_result", r.ID)
	if err != nil || len(why) == 0 || why[0].ToID != f.req.ID {
		t.Fatalf("why quality_result: %+v %v", why, err)
	}
}

// ---------- the real sandbox ----------

// TestSetupReproducibilityRealSandbox runs a fresh-copy `go build`/`go test`
// of a dependency-free module in the real no-network Docker sandbox. Skipped
// without Docker or the golang image.
func TestSetupReproducibilityRealSandbox(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker binary not available")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not running")
	}
	if err := exec.Command("docker", "image", "inspect", "golang:1.25").Run(); err != nil {
		t.Skip("golang:1.25 image not present locally")
	}
	cfg := config.HarnessConfig{Verify: config.VerifyConfig{Dimensions: only("setup_reproducibility")}}
	f := newQualityFixture(t, cfg, goModBase(), map[string]string{
		"lib.go":      "package widgets\n\n// Add adds.\nfunc Add(a, b int) int { return a + b }\n",
		"lib_test.go": "package widgets\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"no\")\n\t}\n}\n",
	})
	f.eng.SetQualitySandbox(nil) // the real Docker sandbox
	r := dim(t, f.measure(t), domain.QualitySetupReproducibility)
	if r.Status != domain.QualityPass {
		t.Fatalf("real sandbox: %s %+v", r.Status, r.Checks)
	}
	// And a module that does not compile fails.
	f.apply(t, map[string]string{}, map[string]string{"broken.go": "package widgets\n\nfunc Broken( {\n"})
	r = dim(t, f.measure(t), domain.QualitySetupReproducibility)
	if r.Status != domain.QualityFail {
		t.Fatalf("broken module: %s %+v", r.Status, r.Checks)
	}
}
