// Package config loads and validates the portable project configuration
// (.idea-harness/project.yaml and harness.yaml) against embedded JSON Schemas
// (§28, §30). It also enforces that secrets never live in the portable tree.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Dir is the portable config directory name inside a project.
const Dir = ".idea-harness"

// ProjectConfig is project.yaml (portable, committable).
type ProjectConfig struct {
	Name string `json:"name" yaml:"name"`
}

// NetworkPolicy is the v0.1 network mode (§46). It governs ONLY the
// deterministic idea-brief/research/PRD pipeline (Engine.researchEvidence and
// the offline fact→assumption downgrade in Engine.runResearch) -- it does NOT
// gate the coding agent's network access (executor.kind omp/hermes always has
// egress, deliberately, to reach its model API: internal/executor/agent.go)
// or the build/test check sandbox's (internal/executor/sandbox.go always runs
// with `--network none`, unconditionally). A prior version of this comment,
// and of docs/configuration.md, did not say so, which read as though setting
// this to "disabled" also sandboxed those (review defect #3).
type NetworkPolicy string

const (
	NetworkDisabled NetworkPolicy = "disabled"
	NetworkEnabled  NetworkPolicy = "enabled"
)

func (n NetworkPolicy) Valid() bool {
	return n == NetworkDisabled || n == NetworkEnabled
}

// ReviewConfig is the review budget (§11, §16).
type ReviewConfig struct {
	MaxCycles  int     `json:"max_cycles" yaml:"max_cycles"`
	MaxTokens  int64   `json:"max_tokens" yaml:"max_tokens"`
	MaxCostUSD float64 `json:"max_cost_usd" yaml:"max_cost_usd"`
	// Rigor calibrates how much scrutiny the PRD, plan and change-set reviews
	// apply (review B1): one of "auto" (the default -- calibrated from
	// project size and change size), "quick", "standard", or "thorough".
	// Auto exists because a fixed rigor level was the ICP round's root
	// cause: a 60-line toy app got the same review depth as an enterprise
	// system, and the loop never converged.
	Rigor string `json:"rigor,omitempty" yaml:"rigor,omitempty"`
	// Convergence governs how the PRD review loop decides it has converged,
	// and how the plan review gates (see ConvergenceConfig). Zero value is
	// the guarded default.
	Convergence ConvergenceConfig `json:"convergence,omitempty" yaml:"convergence,omitempty"`
}

// ConvergenceConfig is review.convergence: the rules that let an adversarial
// review loop finish without lowering its bar for a genuine blocker.
//
// It exists because the PRD review did not converge in 2 of 2 measured
// real-model runs, and reading the loop explained why: the reviewer was
// memoryless (it never saw the previous cycle's findings or what the revision
// did about them), so every cycle was a fresh review of a larger PRD; any one
// critical/high finding anywhere -- new, rephrased, or about text that had not
// changed -- blocked; and nothing measured whether the loop was making
// progress, so it ran blind to max_cycles. See docs/configuration.md, "Review
// convergence".
type ConvergenceConfig struct {
	// Mode is "guarded" (the default) or "legacy". Legacy is the pre-0.7
	// loop exactly: no prior-cycle context, no demotions, no stall detector.
	// It stays selectable so the difference is measurable and as an escape
	// hatch.
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"`
	// GateSeverity is the lowest adversarial severity that can block:
	// "high" (the default -- critical and high block, as before) or
	// "critical". Lower severities are recorded as advisory, never dropped.
	// Deterministic checks are never affected.
	GateSeverity string `json:"gate_severity,omitempty" yaml:"gate_severity,omitempty"`
	// RequireCitation, when true (the default), makes an adversarial
	// finding block only if it cites an active requirement code -- or, for
	// a gap the PRD left out, the idea itself with evidence quoting it. An
	// uncited finding is scope expansion by construction and is recorded as
	// advisory. A pointer so an explicit false is distinguishable from unset.
	RequireCitation *bool `json:"require_citation,omitempty" yaml:"require_citation,omitempty"`
	// LaterFindings is the bar a finding that is NEW in cycle 2+ must clear
	// to block: "changed" (the default -- it must be about a requirement
	// the revision added or changed, or be critical), "critical" (it must be
	// critical), or "any" (no extra bar). A new finding about text the
	// reviewer already saw unchanged in the previous cycle is a goalpost
	// moving, not a regression the revision introduced.
	LaterFindings string `json:"later_findings,omitempty" yaml:"later_findings,omitempty"`
	// StallCycles is how many consecutive cycles the open blocking count may
	// fail to decrease before the loop stops early for a human (default 1).
	// A loop that is not making progress will not start making it by
	// spending the remaining max_cycles budget.
	StallCycles int `json:"stall_cycles,omitempty" yaml:"stall_cycles,omitempty"`
}

// Convergence modes, gate severities and later-finding bars
// (review.convergence).
var (
	ConvergenceModes     = []string{"guarded", "legacy"}
	ConvergenceGates     = []string{"high", "critical"}
	ConvergenceLaterBars = []string{"changed", "critical", "any"}
)

// Effective returns c with every unset field resolved to its default, which
// is what the engine runs under. Kept here, beside the fields, so the
// defaults are stated once (docs/configuration.md quotes them).
func (c ConvergenceConfig) Effective() ConvergenceConfig {
	if c.Mode == "" {
		c.Mode = "guarded"
	}
	if c.GateSeverity == "" {
		c.GateSeverity = "high"
	}
	if c.RequireCitation == nil {
		t := true
		c.RequireCitation = &t
	}
	if c.LaterFindings == "" {
		c.LaterFindings = "changed"
	}
	if c.StallCycles <= 0 {
		c.StallCycles = 1
	}
	return c
}

// Legacy reports whether the pre-0.7 loop was selected.
func (c ConvergenceConfig) Legacy() bool { return c.Mode == "legacy" }

// CitationRequired reports the effective require_citation.
func (c ConvergenceConfig) CitationRequired() bool {
	return c.RequireCitation == nil || *c.RequireCitation
}

func oneOf(s string, allowed []string) bool {
	if s == "" {
		return true
	}
	for _, a := range allowed {
		if s == a {
			return true
		}
	}
	return false
}

// EffectiveMaxCycles is MaxCycles as the engine actually enforces it: any
// value <= 0 (unset, or explicitly zero) coerces to 3, the same rule
// workflow.New/Engine.Reload apply to their own copy of this config before
// running a review loop. applyDefaults only defaults the raw field to 3 when
// every review.* field is unset (review MEDIUM #7) -- a harness.yaml that
// sets, say, max_tokens alongside max_cycles == 0 leaves the raw field at
// zero -- so a caller that needs to know the true effective cap without
// going through an *Engine (daemon.demoProvider's block_review_cycles clamp)
// must call this, not read MaxCycles directly.
func (r ReviewConfig) EffectiveMaxCycles() int {
	if r.MaxCycles <= 0 {
		return 3
	}
	return r.MaxCycles
}

// RigorLevels are review.rigor's allowed configured values.
var RigorLevels = []string{"auto", "quick", "standard", "thorough"}

// ValidRigor reports whether s is a recognized review.rigor value. Empty is
// valid and means "auto" (applyDefaults fills it in).
func ValidRigor(s string) bool {
	if s == "" {
		return true
	}
	for _, r := range RigorLevels {
		if s == r {
			return true
		}
	}
	return false
}

// ProviderConfig describes one model provider (§40). API keys are resolved
// from the environment variable named by APIKeyEnv — never stored inline.
// Type and BaseURL are optional: when empty they are derived from the provider
// key's preset (see providers.go).
type ProviderConfig struct {
	Type      ProviderType `json:"type" yaml:"type"`
	APIKeyEnv string       `json:"api_key_env" yaml:"api_key_env"`
	BaseURL   string       `json:"base_url" yaml:"base_url"`
	Models    []string     `json:"models" yaml:"models"`
}

// ResearchConfig controls the research web tool (§46). URLs are seed sources
// fetched (public-address-only) during the research stage when network is
// enabled.
type ResearchConfig struct {
	URLs []string `json:"urls" yaml:"urls"`
}

// ExecutorKind selects the coding executor the execute pipeline uses (§48–§52).
type ExecutorKind string

const (
	ExecutorInternal  ExecutorKind = "internal"
	ExecutorContainer ExecutorKind = "container"
	ExecutorOMP       ExecutorKind = "omp"
	ExecutorHermes    ExecutorKind = "hermes"
	// ExecutorACP, ExecutorOpenHands and ExecutorAuto are documented in
	// executor.go.
	ExecutorACP       ExecutorKind = "acp"
	ExecutorOpenHands ExecutorKind = "openhands"
	ExecutorAuto      ExecutorKind = "auto"
)

func (k ExecutorKind) Valid() bool {
	switch k {
	case ExecutorInternal, ExecutorContainer, ExecutorOMP, ExecutorHermes, ExecutorACP, ExecutorOpenHands, ExecutorAuto:
		return true
	}
	return false
}

// ExecutorConfig selects and configures the execute-pipeline executor. Image is
// the container image for the container and agent kinds; Model is the coding
// model for the agent kinds. Neither is read by the internal kind.
type ExecutorConfig struct {
	Kind  ExecutorKind `json:"kind" yaml:"kind"`
	Image string       `json:"image,omitempty" yaml:"image,omitempty"`
	Model string       `json:"model,omitempty" yaml:"model,omitempty"`
	// ACP configures kind: acp (executor.go). Ignored by every other kind.
	ACP ACPConfig `json:"acp,omitempty" yaml:"acp,omitempty"`
	// OpenHands configures kind: openhands (executor.go). Ignored by every
	// other kind.
	OpenHands OpenHandsConfig `json:"openhands,omitempty" yaml:"openhands,omitempty"`
	// MCPServers are handed to an ACP agent in session/new (executor.go).
	MCPServers []MCPServerConfig `json:"mcp_servers,omitempty" yaml:"mcp_servers,omitempty"`
}

// CheckConfig configures the deterministic build/test check sandbox (review H3).
// The image must contain the project's toolchain (e.g. golang:1.25, node:22).
type CheckConfig struct {
	Image string `json:"image,omitempty" yaml:"image,omitempty"`
}

// defaultMaxFiles is execute.max_files' built-in default when unset (<=0).
//
// Chosen BELOW the 15-file / ~26KB diff that is the known-bad case this guard
// exists to catch (the ICP re-run where an 80-line service was restructured,
// unasked, into cmd/+internal/ for a one-endpoint task, producing a diff that
// went on to hit the adversarial reviewer's own output ceiling). 10 sits under
// that observed failure while still comfortably fitting the plan's own target
// of 2-4 vertical slices per task.
const defaultMaxFiles = 10

// ExecuteConfig bounds the blast radius of a single change set (review C). A
// change set whose diff touches more files than MaxFiles allows is refused
// before the adversarial review ever runs (workflow.Engine.Execute), naming
// the count, the limit, and how to proceed: split the task, or raise the
// limit.
//
// The count is of files the change set's diff (fsdiff.Diff(before, after))
// actually touches, which already excludes fsdiff's own always-on exclusions
// (well-known toolchain/cache directories, the harness's own control-plane
// paths) and, for the after-image, the workspace's own .gitignore — so a
// lockfile regen or another generated/ignored file an agent rewrites does not
// count against a limit meant for hand-written sprawl.
type ExecuteConfig struct {
	// MaxFiles caps the file count a change set's diff may touch. <=0 (unset)
	// uses defaultMaxFiles — see EffectiveMaxFiles.
	MaxFiles int `json:"max_files,omitempty" yaml:"max_files,omitempty"`
	// MaxParallel is how many independent ready tasks `idea execute` runs at
	// once (a wave). <=0 (unset) means 1 — the serial behaviour. Independent
	// tasks whose depends_on predecessors are applied run concurrently;
	// overlapping edits are caught at apply by fsdiff.StaleBase.
	MaxParallel int `json:"max_parallel,omitempty" yaml:"max_parallel,omitempty"`
}

// EffectiveMaxFiles is execute.max_files as Execute actually enforces it —
// the same <=0-means-default convention as ReviewConfig.EffectiveMaxCycles.
func (c ExecuteConfig) EffectiveMaxFiles() int {
	if c.MaxFiles <= 0 {
		return defaultMaxFiles
	}
	return c.MaxFiles
}

// EffectiveMaxParallel is execute.max_parallel as Execute actually enforces it:
// <=0 (unset) means 1 — serial.
func (c ExecuteConfig) EffectiveMaxParallel() int {
	if c.MaxParallel <= 0 {
		return 1
	}
	return c.MaxParallel
}

// OutcomesConfig configures how outcome metrics are measured
// (`idea metric measure`). Nothing here runs on its own: a command or url
// source is measured when a human or an MCP client asks, or on its cadence
// once a person enables automations.metric_measurement (automations.go).
type OutcomesConfig struct {
	// Image is the sandbox image a command source runs in. Empty falls back
	// to check.image, then to the image derived from the project's toolchain
	// markers, then to the sandbox's minimal default -- the same no-network,
	// read-only sandbox the build/test checks use either way.
	Image string `json:"image,omitempty" yaml:"image,omitempty"`
	// MeasureTimeoutSeconds bounds one command source. Zero means 120s.
	MeasureTimeoutSeconds int `json:"measure_timeout_seconds,omitempty" yaml:"measure_timeout_seconds,omitempty"`
	// AllowURLSources opts into fetching url sources. Off by default: it is
	// the one measurement path that reaches the network, so it is never
	// implied -- not even by `network: enabled`, which governs the research
	// pipeline. Fetches go through the SSRF-guarded fetcher either way
	// (public http(s) addresses only).
	AllowURLSources bool `json:"allow_url_sources,omitempty" yaml:"allow_url_sources,omitempty"`
}

// HarnessConfig is harness.yaml (portable, committable).
type HarnessConfig struct {
	Network  NetworkPolicy  `json:"network" yaml:"network"`
	Review   ReviewConfig   `json:"review" yaml:"review"`
	Research ResearchConfig `json:"research" yaml:"research"`
	// Constitution lists the project's immutable principles. Each is injected
	// into every stage and reviewer as a hard constraint (the project_policy
	// context selector); the reviewer is instructed to flag any violation. Empty
	// means no project-specific constitution.
	Constitution []string                  `json:"constitution,omitempty" yaml:"constitution,omitempty"`
	Providers    map[string]ProviderConfig `json:"providers" yaml:"providers"`
	DefaultModel string                    `json:"default_model" yaml:"default_model"`
	Executor     ExecutorConfig            `json:"executor" yaml:"executor"`
	Check        CheckConfig               `json:"check" yaml:"check"`
	// Execute bounds a single change set's blast radius (review C). See
	// ExecuteConfig's doc comment.
	Execute ExecuteConfig `json:"execute,omitempty" yaml:"execute,omitempty"`
	// Models is the per-role model map: role name → "provider/model". Roles
	// without an entry fall back to default_model (§40). The coding "implementer"
	// role is a valid key here, letting OMP/Hermes run different models.
	Models map[string]string `json:"models" yaml:"models"`
	// BuildCmd/TestCmd are the deterministic check commands (review B5). They are
	// config-supplied, never model/agent output; when empty the corresponding
	// check is recorded not_applicable.
	BuildCmd string `json:"build_cmd,omitempty" yaml:"build_cmd,omitempty"`
	TestCmd  string `json:"test_cmd,omitempty" yaml:"test_cmd,omitempty"`
	// CheckTimeoutSeconds bounds each deterministic build/test check. Zero uses
	// the default of 600s (10m) (review SRE).
	CheckTimeoutSeconds int `json:"check_timeout_seconds,omitempty" yaml:"check_timeout_seconds,omitempty"`
	// GitCommit opts into creating a git commit (with repo hooks disabled) for
	// each applied change set (§M7). Off by default: a hook runs arbitrary code
	// under the daemon's privileged environment, so this must never be implied
	// by anything OTHER than an explicit opt-in -- which is exactly what
	// Git.BranchPerInitiative is (review defect D): it is documented as
	// committing each change set onto the initiative's branch, so setting it
	// alone, with GitCommit left false, also commits. A user who wants neither
	// leaves both false; there is no way to get branch-per-initiative's
	// bookkeeping (Initiative.GitBranch, `idea pr`) without commits landing on
	// it, since an uncommitted branch is not a reviewable one.
	GitCommit bool `json:"git_commit" yaml:"git_commit"`
	// Git configures git-native behavior beyond the commit-on-apply toggle
	// above (review #2, C4). Git.BranchPerInitiative alone is sufficient to
	// commit (see GitCommit's doc comment) -- GitCommit itself need not also
	// be set.
	Git GitConfig `json:"git,omitempty" yaml:"git,omitempty"`
	// Demo configures the built-in deterministic provider. It does nothing when
	// a real provider is configured.
	Demo DemoConfig `json:"demo,omitempty" yaml:"demo,omitempty"`
	// Integrations configures the Slack and Linear integrations
	// (integrations.go). Each is off unless its section is present; tokens are
	// never read from here, only from the environment or the project's .env,
	// named by the *_env fields.
	Integrations IntegrationsConfig `json:"integrations,omitempty" yaml:"integrations,omitempty"`
	// Verify configures the quality dimensions `idea verify` measures beyond
	// the acceptance tests (verify.go).
	Verify VerifyConfig `json:"verify,omitempty" yaml:"verify,omitempty"`
	// Outcomes configures outcome-metric measurement (OutcomesConfig).
	Outcomes OutcomesConfig `json:"outcomes,omitempty" yaml:"outcomes,omitempty"`
	// Budget is the per-initiative spending cap (automations.go).
	Budget BudgetConfig `json:"budget,omitempty" yaml:"budget,omitempty"`
	// Automations are the opt-in scheduled jobs (automations.go).
	Automations AutomationsConfig `json:"automations,omitempty" yaml:"automations,omitempty"`
}

// GitConfig configures git-native behavior beyond git_commit (review C4,
// adopted change #2). Both fields are opt-in and off by default.
type GitConfig struct {
	// AllowDefaultBranch permits the commit-on-apply path to commit while HEAD
	// is checked out on the repository's default branch. Off by default: an
	// agent commit landing directly on the default branch, with no human
	// commit of their own in between, is a stronger claim than opting into
	// git_commit alone should make. Irrelevant when BranchPerInitiative is on,
	// since apply then commits on the initiative's own branch.
	AllowDefaultBranch bool `json:"allow_default_branch,omitempty" yaml:"allow_default_branch,omitempty"`
	// BranchPerInitiative opts into creating and checking out idea/<slug> on an
	// initiative's first apply, then committing each of its change sets there
	// instead of on whatever branch the workspace was already on. This implies
	// committing (review defect D): setting this true is sufficient on its
	// own, without also setting git_commit -- see GitCommit's doc comment for
	// why that used to be a second, undocumented requirement.
	BranchPerInitiative bool `json:"branch_per_initiative,omitempty" yaml:"branch_per_initiative,omitempty"`
}

// DemoConfig steers the built-in demo provider.
//
// It exists because the demo reviewer returned zero findings unconditionally, so
// the review loop always converged and `needs_human_judgment` was structurally
// unreachable on the only path CI, scripts/smoke.sh and six DX audits ever ran.
// A P0 that fails 4/4 on the README's own quick start survived six rounds in
// that blind spot.
//
// Declared config rather than a magic string in the idea text: the marker would
// have ridden into the PRD, the task title, the placeholder file and the
// change-set patch -- and from there into the *change-set* reviewer, which
// shares the schema name `review`, where a blocking finding is unrecoverable.
type DemoConfig struct {
	// BlockReviewCycles makes the demo provider's adversarial reviewer emit a
	// blocking finding for this many review calls before reverting to its usual
	// empty list. Set it to `review.max_cycles` to make the PRD review park the
	// initiative on a human.
	//
	// A count consumed from a queue, not a flag, because all three adversarial
	// reviews share the schema name `review` and a flag would block the plan and
	// change-set reviews too. M1's review cycles are the first calls a run makes,
	// so the queue drains on exactly them.
	BlockReviewCycles int `json:"block_review_cycles,omitempty" yaml:"block_review_cycles,omitempty"`
	// BlockPlanReviewCycles does the same for the M2 execution-plan review.
	// A separate queue from the PRD review's, because they are separate reviews
	// with separate escapes -- and because one shared queue could spill a
	// leftover blocker onto the change-set review, which nothing can clear.
	//
	// Unclamped, unlike BlockReviewCycles: `idea revise-plan` clears a plan
	// review however many times it blocks, so a surplus is survivable here.
	BlockPlanReviewCycles int `json:"block_plan_review_cycles,omitempty" yaml:"block_plan_review_cycles,omitempty"`
	// BlockChangeSetReviewCycles does the same for the M3 change-set review.
	//
	// Its own queue, on its own key, for the reason the plan review needed one:
	// the change-set review shared the `review` key with the PRD review, so
	// there was no way to make it block without a leftover PRD response landing
	// here -- and no way to reach the state where a human must accept a change
	// set the reviewer will not pass. That state was a live-lock the keyed
	// magic path hit every run, and no demo configuration could produce it.
	BlockChangeSetReviewCycles int `json:"block_change_set_review_cycles,omitempty" yaml:"block_change_set_review_cycles,omitempty"`
	// DanglePlanDependency makes the first execution plan name a dependency no
	// task provides, which parks the initiative on `plan_dependencies` -- the
	// one judgment reason whose fix is regenerating the plan and never
	// approving it.
	DanglePlanDependency bool `json:"dangle_plan_dependency,omitempty" yaml:"dangle_plan_dependency,omitempty"`
	// CostPerCallUSD makes every demo model call report this cost. The demo
	// provider is otherwise free, so a spending cap (budget) could never be
	// reached on the only path CI and the browser suite run; this is what
	// lets them show a cap stopping a run without a real provider.
	CostPerCallUSD float64 `json:"cost_per_call_usd,omitempty" yaml:"cost_per_call_usd,omitempty"`
}

// Config is the fully-loaded portable configuration for one project.
type Config struct {
	Project ProjectConfig
	Harness HarnessConfig
}

// Default returns the default configuration. The network is disabled by
// default so research never silently exfiltrates local content (§15). The
// executor defaults to auto: a real coding agent when Docker and the coding
// model's key are both available, the placeholder internal executor -- with a
// loud warning -- otherwise (executor.go).
func Default() Config {
	return Config{
		Harness: HarnessConfig{
			Network:   NetworkDisabled,
			Review:    ReviewConfig{MaxCycles: 3, Rigor: "auto"},
			Providers: map[string]ProviderConfig{},
			Executor:  ExecutorConfig{Kind: ExecutorAuto},
		},
	}
}

// Load reads and validates project.yaml and harness.yaml from dir. Missing
// files fall back to Default(); present files are strictly validated against
// the raw document (so unknown fields and inline secrets are rejected, not
// silently dropped).
func Load(dir string) (Config, error) {
	cfg := Default()

	pBytes, pDoc, err := readDoc(filepath.Join(dir, "project.yaml"))
	if err != nil {
		return cfg, err
	}
	if pBytes != nil {
		if err := yaml.Unmarshal(pBytes, &cfg.Project); err != nil {
			return cfg, fmt.Errorf("config: parse project.yaml: %w", err)
		}
		if err := validateAgainst("project.yaml", projectSchema, pDoc); err != nil {
			return cfg, err
		}
		if err := rejectSecrets("", pDoc); err != nil {
			return cfg, err
		}
	}

	hBytes, hDoc, err := readDoc(filepath.Join(dir, "harness.yaml"))
	if err != nil {
		return cfg, err
	}
	if hBytes != nil {
		if err := yaml.Unmarshal(hBytes, &cfg.Harness); err != nil {
			return cfg, fmt.Errorf("config: parse harness.yaml: %w", err)
		}
		if err := validateAgainst("harness.yaml", harnessSchema, hDoc); err != nil {
			return cfg, err
		}
		if err := rejectSecrets("", hDoc); err != nil {
			return cfg, err
		}
	}

	cfg.applyDefaults()
	if err := cfg.Harness.validateStruct(); err != nil {
		return cfg, err
	}
	if cfg.Harness.Execute.MaxFiles < 0 {
		return cfg, fmt.Errorf("config: harness.execute.max_files must be >= 0")
	}
	return cfg, nil
}

// validateStruct is every load-time check the JSON Schema cannot express, on
// an already-defaulted harness config. Load and WriteHarness share it, so a
// write can never produce a file the next Load refuses.
func (h HarnessConfig) validateStruct() error {
	if !h.Network.Valid() {
		return fmt.Errorf("config: harness.network must be one of disabled|enabled, got %q", h.Network)
	}
	if !h.Executor.Kind.Valid() {
		return fmt.Errorf("config: harness.executor.kind must be one of auto|internal|container|omp|hermes|acp|openhands, got %q", h.Executor.Kind)
	}
	if err := validateExecutorExtras(h.Executor); err != nil {
		return err
	}
	if h.Review.MaxCycles < 0 {
		return fmt.Errorf("config: harness.review.max_cycles must be >= 0")
	}
	if !ValidRigor(h.Review.Rigor) {
		return fmt.Errorf("config: harness.review.rigor must be one of %s, got %q", strings.Join(RigorLevels, "|"), h.Review.Rigor)
	}
	if err := validateVerify(h.Verify); err != nil {
		return err
	}
	conv := h.Review.Convergence
	if !oneOf(conv.Mode, ConvergenceModes) {
		return fmt.Errorf("config: harness.review.convergence.mode must be one of %s, got %q", strings.Join(ConvergenceModes, "|"), conv.Mode)
	}
	if !oneOf(conv.GateSeverity, ConvergenceGates) {
		return fmt.Errorf("config: harness.review.convergence.gate_severity must be one of %s, got %q", strings.Join(ConvergenceGates, "|"), conv.GateSeverity)
	}
	if !oneOf(conv.LaterFindings, ConvergenceLaterBars) {
		return fmt.Errorf("config: harness.review.convergence.later_findings must be one of %s, got %q", strings.Join(ConvergenceLaterBars, "|"), conv.LaterFindings)
	}
	if conv.StallCycles < 0 {
		return fmt.Errorf("config: harness.review.convergence.stall_cycles must be >= 0")
	}
	if err := h.Integrations.validate(); err != nil {
		return err
	}
	if err := h.Budget.validate(); err != nil {
		return err
	}
	if err := h.Automations.validate(); err != nil {
		return err
	}
	if h.Demo.CostPerCallUSD < 0 {
		return fmt.Errorf("config: harness.demo.cost_per_call_usd must be >= 0")
	}
	return nil
}

// readDoc reads a YAML file and returns its raw bytes and its decoded document
// (as a JSON-compatible map). A missing file yields (nil, nil, nil).
func readDoc(path string) ([]byte, map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("config: read %s: %w", path, err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		// Typed, and project-relative: the caller turns this into a 4xx naming
		// the file. The absolute path is the daemon's business, and the reader
		// already knows their own project root.
		return nil, nil, &ParseError{File: filepath.Base(path), Msg: err.Error()}
	}
	return data, doc, nil
}

func (c *Config) applyDefaults() {
	if c.Harness.Network == "" {
		c.Harness.Network = NetworkDisabled
	}
	if c.Harness.Review.MaxCycles == 0 && c.Harness.Review.MaxTokens == 0 && c.Harness.Review.MaxCostUSD == 0 {
		c.Harness.Review.MaxCycles = 3
	}
	if c.Harness.Review.Rigor == "" {
		c.Harness.Review.Rigor = "auto"
	}
	if c.Harness.Executor.Kind == "" {
		c.Harness.Executor.Kind = ExecutorAuto
	}
}
