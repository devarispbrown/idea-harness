package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/devarispbrown/idea-harness/internal/buildinfo"
	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/daemon"
	"github.com/devarispbrown/idea-harness/internal/docparse"
)

// cmdArgs is what a command receives after dispatch has parsed its flags. One
// parse, from one spec, declared in the table -- so a command cannot silently
// accept a flag the reference does not document, and cannot reject one it does.
type cmdArgs struct {
	// sub is the matched subcommand token ("add", "validate", "status"), or ""
	// when the command was invoked bare.
	sub        string
	positional []string
	flags      parsedFlags
}

func (a cmdArgs) has(name string) bool      { return a.flags.has(name) }
func (a cmdArgs) value(name string) string  { return a.flags.value(name) }
func (a cmdArgs) list(name string) []string { return a.flags.list(name) }

// arg returns the i-th positional, or "" when there are fewer.
func (a cmdArgs) arg(i int) string {
	if i < len(a.positional) {
		return a.positional[i]
	}
	return ""
}

// command is one row of the CLI's command table: the single source of truth for
// dispatch (Run), the human help screen (usage) and the generated reference in
// docs/cli.md (renderCommandTable). A command cannot be dispatched without
// appearing here, which is what keeps the docs from drifting away from the code.
type command struct {
	// names is the dispatch token plus any aliases. Empty for a documentation
	// row that describes a subcommand handled by its parent (`docs add`).
	names []string
	// usage is the display form, e.g. `new "<idea>"`.
	usage string
	// summary is the one-line description.
	summary string
	// flags is the spec for the bare command; subs holds one spec per
	// subcommand token. Declared per dispatch target rather than per command
	// because a union spec would accept `idea model ls --key-env X` -- the
	// silent-swallow defect this table exists to prevent, one level down.
	//
	// A dispatchable row must declare flags, even as the empty flagSpec{}; nil
	// is a programming error Run rejects, because the compiler cannot catch an
	// omitted field in a keyed struct literal.
	flags flagSpec
	subs  map[string]flagSpec
	// maxPositional bounds the positional arguments a command accepts.
	//
	// Zero means unbounded, deliberately: it is the Go zero value, so a row that
	// does not declare a bound keeps working. The previous round learned this the
	// hard way with flagSpec, where the zero value silently meant "reject every
	// flag". A bound is opt-in, set on the commands that take a fixed number of
	// references -- which is where the defect was: `idea apply CHG-1 CHG-2`
	// applied only the first and reported success.
	maxPositional int
	// subsAreRequired marks a command whose leading word must name a subcommand,
	// so a typo is reported rather than silently falling through to the bare
	// behaviour.
	subsAreRequired bool
	// rejectsJSON marks a command that accepts --json only to refuse it with a
	// pointer somewhere better (`plan` -> `idea tasks --json`).
	rejectsJSON bool
	// run is nil for documentation-only rows.
	run func(a cmdArgs, stdout, stderr io.Writer) error
	// inHelp marks the rows `idea help` prints. Rows added since the help
	// screen was last written are documented but not printed, so that
	// `idea help` output stays byte-for-byte what it has always been; set the
	// field to surface `verify`, `next` and the subcommand rows there too.
	inHelp bool
}

// commands is a function rather than a package-level var because cmdHelp reads
// the table, and a var initialized from a function that reads it back would be
// an initialization cycle.
func commands() []command {
	return []command{
		{names: []string{"init"}, usage: "init [<dir>]", summary: "initialize a project (defaults to the current directory)", flags: flagSpec{}, run: func(a cmdArgs, stdout, _ io.Writer) error { return cmdInit(a, stdout) }, inHelp: true},
		{names: []string{"new"}, usage: `new "<idea>" [--no-wait]`, summary: "create an initiative and start the workflow (waits by default; --no-wait returns a handle immediately)", flags: flagSpec{"--doc": flagRepeated, "--no-wait": flagBool, "--wait": flagBool}, run: cmdNew, inHelp: true},
		{names: []string{"status"}, usage: "status [<initiative>]", summary: "show initiative + current work", flags: jsonFlag, maxPositional: 1, run: cmdStatus, inHelp: true},
		{names: []string{"changed"}, usage: "changed [<initiative>]", summary: "show What Changed since last look", flags: jsonFlag, maxPositional: 1, run: cmdChanged, inHelp: true},
		{names: []string{"decisions"}, usage: "decisions", summary: "list open/blocked decisions", flags: jsonFlag, maxPositional: 1, run: cmdDecisions, inHelp: true},
		{names: []string{"questions"}, usage: "questions", summary: "list open/answered questions", flags: jsonFlag, maxPositional: 1, run: cmdQuestions, inHelp: true},
		{names: []string{"answer"}, usage: `answer <question> "<answer>" [<initiative>]`, summary: "answer an open question", flags: flagSpec{}, maxPositional: 3, run: cmdAnswer, inHelp: true},
		{names: []string{"resolve"}, usage: `resolve <decision> <option#>|"<resolution>" [<initiative>]`, summary: "resolve an open decision, by option number or free text", flags: flagSpec{}, maxPositional: 3, run: cmdResolve, inHelp: true},
		{names: []string{"reopen-decision"}, usage: "reopen-decision <decision> [<initiative>]", summary: "return a resolved decision to open", flags: flagSpec{}, maxPositional: 2, run: cmdReopenDecision, inHelp: true},
		{names: []string{"accept-recommendations"}, usage: "accept-recommendations", summary: "resolve every open decision with its own recommendation", flags: jsonFlag, maxPositional: 1, run: cmdAcceptRecommendations, inHelp: true},
		{names: []string{"triage"}, usage: "triage", summary: `show the ranked "needs your judgment" view`, flags: jsonFlag, maxPositional: 1, run: cmdTriage, inHelp: true},
		{names: []string{"reviews"}, usage: "reviews", summary: "list review findings", flags: jsonFlag, maxPositional: 1, run: cmdReviews, inHelp: true},
		{names: []string{"model"}, usage: "model [--provider <name>] [--all]", summary: "show the current model + a curated set (Gemini, OpenAI, …)", flags: flagSpec{"--provider": flagValue, "--all": flagBool}, subs: map[string]flagSpec{
			"list": {"--provider": flagValue, "--all": flagBool}, "ls": {},
			"set": {"--role": flagValue},
			"add": {"--type": flagValue, "--base-url": flagValue, "--key-env": flagValue},
		}, subsAreRequired: true, run: cmdModel, inHelp: true},
		{usage: "model list [<provider>] [--all]", summary: "list providers; a provider (by name or --provider) shows it in full, --all shows every model of every provider", inHelp: true},
		{usage: "model ls <provider>", summary: "list a provider's models live (falls back to the catalog offline)"},
		{usage: "model set <provider/model>", summary: "set the default model (--role <role> sets one role only)"},
		{usage: "model add <name>", summary: "add a provider (--type, --base-url, --key-env)"},
		{names: []string{"prd"}, usage: "prd", summary: "render the reviewed PRD in your terminal", flags: flagSpec{}, maxPositional: 2, run: cmdPRD, inHelp: true},
		{names: []string{"explain"}, usage: "explain", summary: "show the Current Understanding", flags: jsonFlag, maxPositional: 1, run: cmdExplain, inHelp: true},
		{names: []string{"export"}, usage: "export [kind]", summary: "export an artifact as markdown", flags: flagSpec{}, maxPositional: 2, run: cmdExport, inHelp: true},
		{names: []string{"advance"}, usage: "advance [--wait]", summary: "run Design → Execution Plan (M2); returns a handle immediately unless --wait", flags: flagSpec{"--wait": flagBool}, maxPositional: 1, run: cmdAdvance, inHelp: true},
		{names: []string{"approve"}, usage: "approve", summary: "accept the reviewed PRD as it stands", flags: flagSpec{}, maxPositional: 1, run: cmdApprove, inHelp: true},
		{names: []string{"revise"}, usage: "revise", summary: "regenerate the PRD from the review findings", flags: flagSpec{}, maxPositional: 1, run: cmdRevise, inHelp: true},
		{names: []string{"approve-plan"}, usage: "approve-plan", summary: "accept the execution plan as it stands", flags: flagSpec{}, maxPositional: 1, run: cmdApprovePlan, inHelp: true},
		{names: []string{"revise-plan"}, usage: "revise-plan", summary: "regenerate the execution plan", flags: flagSpec{}, maxPositional: 1, run: cmdRevisePlan, inHelp: true},
		{names: []string{"plan"}, usage: "plan", summary: "render the execution plan", flags: flagSpec{"--json": flagBool}, rejectsJSON: true, maxPositional: 1, run: cmdPlan, inHelp: true},
		{names: []string{"tasks"}, usage: "tasks", summary: "list the ordered plan tasks", flags: jsonFlag, maxPositional: 1, run: cmdTasks, inHelp: true},
		{names: []string{"why"}, usage: "why <type> <id>", summary: "show the provenance chain", flags: jsonFlag, maxPositional: 3, run: cmdWhy, inHelp: true},
		{names: []string{"impact"}, usage: "impact <type> <id>", summary: "show dependents + staleness", flags: jsonFlag, maxPositional: 3, run: cmdImpact, inHelp: true},
		{names: []string{"trace"}, usage: "trace <type> <id>", summary: "show provenance + dependents", flags: jsonFlag, maxPositional: 3, run: cmdTrace, inHelp: true},
		{names: []string{"gaps"}, usage: "gaps [<initiative>] [--kind k,...] [--fail-on <sev>] [--all]", summary: "list Product Graph gaps, each with the command that closes it (--fail-on: exit 5 at or above a severity)", flags: gapsFlags, maxPositional: 1, run: cmdGaps, inHelp: true},
		{names: []string{"assumption"}, usage: "assumption <id> accept|dispute|retract", summary: "judge an assumption or hypothesis (closes an unvalidated_assumption gap)", flags: flagSpec{}, maxPositional: 3, run: cmdAssumption, inHelp: true},
		{names: []string{"objectives"}, usage: "objectives [<initiative>]", summary: "list the initiative's objectives: what each is for, how many requirements serve it, how many metrics measure it", flags: jsonFlag, maxPositional: 1, run: cmdObjectives},
		{names: []string{"objective"}, usage: "objective add|edit|link", summary: "add, edit or link an objective (see the rows below)", flags: flagSpec{}, subs: map[string]flagSpec{
			"add":  {"--rationale": flagValue, "--owner": flagValue, "--json": flagBool},
			"edit": {"--statement": flagValue, "--rationale": flagValue, "--owner": flagValue, "--status": flagValue, "--json": flagBool},
			"link": {"--unlink": flagBool, "--json": flagBool},
		}, subsAreRequired: true, maxPositional: 3, run: cmdObjective},
		{usage: `objective add "<statement>"`, summary: "add an objective (--rationale, --owner); a human-added objective is never rewritten by a PRD revision"},
		{usage: "objective edit <OBJ>", summary: "change --statement, --rationale, --owner or --status active|achieved|abandoned"},
		{usage: "objective link <REQ> <OBJ>", summary: "record that a requirement serves an objective (--unlink removes it)"},
		{names: []string{"metrics"}, usage: "metrics [<initiative>]", summary: "list outcome metrics with their drift: on track, off track, regressed, stale or no data", flags: jsonFlag, maxPositional: 1, run: cmdMetrics},
		{names: []string{"metric"}, usage: "metric add|edit|record|measure", summary: "add, edit, record or measure an outcome metric (see the rows below)", flags: flagSpec{}, subs: map[string]flagSpec{
			"add": {"--target": flagValue, "--direction": flagValue, "--baseline": flagValue, "--guardrail": flagValue, "--unit": flagValue,
				"--definition": flagValue, "--cadence": flagValue, "--command": flagValue, "--url": flagValue, "--pointer": flagValue,
				"--manual": flagBool, "--hypothesis": flagValue, "--json": flagBool},
			"edit": {"--name": flagValue, "--objective": flagValue, "--target": flagValue, "--direction": flagValue, "--baseline": flagValue,
				"--clear-baseline": flagBool, "--guardrail": flagValue, "--clear-guardrail": flagBool, "--unit": flagValue,
				"--definition": flagValue, "--cadence": flagValue, "--command": flagValue, "--url": flagValue, "--pointer": flagValue,
				"--manual": flagBool, "--json": flagBool},
			"record":  {"--note": flagValue, "--at": flagValue, "--json": flagBool},
			"measure": {"--json": flagBool},
		}, subsAreRequired: true, maxPositional: 3, run: cmdMetric},
		{usage: `metric add <OBJ> "<name>" --target <n>`, summary: "add a metric to an objective: --direction increase|decrease|maintain (default increase), --baseline, --guardrail, --unit, --cadence, --definition, --hypothesis; source --command <sh> (no-network sandbox) or --url <url> --pointer </json/ptr>, default manual"},
		{usage: "metric edit <MET>", summary: "change any metric flag above, --clear-baseline, --clear-guardrail, or --manual to drop a command/url source"},
		{usage: "metric record <MET> <value>", summary: "record a measured value (--note, --at <RFC3339>); prints the metric's drift and how many change sets landed since the last value"},
		{usage: "metric measure [<MET>]", summary: "run command/url sources now and record what they read; a command runs in the no-network check sandbox, never on the host; url sources need outcomes.allow_url_sources"},
		{names: []string{"diff"}, usage: "diff", summary: "structured requirement/decision diff", flags: jsonFlag, maxPositional: 1, run: cmdDiff, inHelp: true},
		{names: []string{"execute"}, usage: "execute [--note <text>] [--wait]", summary: "run the next ready task → change set; --note steers this run (appended to the implementer's brief); returns a handle immediately unless --wait", flags: flagSpec{"--note": flagValue, "--wait": flagBool}, maxPositional: 1, run: cmdExecute, inHelp: true},
		{names: []string{"reopen-phase"}, usage: "reopen-phase <architecture|product_design|verification_design> [<initiative>] [--no-wait]", summary: "reopen a design phase", flags: flagSpec{"--no-wait": flagBool}, maxPositional: 2, run: cmdReopenPhase, inHelp: true},
		{names: []string{"retry"}, usage: "retry [<initiative>] [--model <provider/model>] [--note <text>] [--no-wait]", summary: "re-run a failed initiative; --note steers this retry (joins the framing prompt)", flags: flagSpec{"--model": flagValue, "--note": flagValue, "--no-wait": flagBool}, maxPositional: 1, run: cmdRetry, inHelp: true},
		{names: []string{"changes"}, usage: "changes", summary: "list change sets", flags: jsonFlag, maxPositional: 1, run: cmdChanges, inHelp: true},
		{names: []string{"change"}, usage: "change <id>", summary: "show a change set", flags: jsonFlag, maxPositional: 1, run: cmdChange, inHelp: true},
		{names: []string{"apply"}, usage: "apply <id> [--wait]", summary: "apply an approved (or reverted) change set; returns a handle immediately unless --wait", flags: flagSpec{"--wait": flagBool}, maxPositional: 1, run: cmdApply, inHelp: true},
		{names: []string{"revert"}, usage: "revert <id> [--wait]", summary: "roll back a change set; returns a handle immediately unless --wait", flags: flagSpec{"--wait": flagBool}, maxPositional: 1, run: cmdRevert, inHelp: true},
		{names: []string{"pr"}, usage: "pr [<initiative>]", summary: "open a pull request for the initiative's branch (git.branch_per_initiative)", flags: jsonFlag, subs: map[string]flagSpec{
			"checks": {"--json": flagBool},
		}, maxPositional: 1, run: cmdPR, inHelp: true},
		{usage: "pr checks <change-set-id>", summary: "show the latest CI status via `gh pr checks`", inHelp: true},
		{names: []string{"review"}, usage: "review <id> <verdict> [--no-wait]", summary: "approve|reject|changes_requested|rerun (rerun re-runs an adversarial review that never completed, e.g. cancelled mid-review or truncated at the model's output ceiling even after a retry; a real model call, waits by default -- --no-wait returns a handle immediately). approve --accept-findings needs a completed review UNLESS one was attempted and explicitly recorded as unable to complete, which it may then approve without", flags: flagSpec{"--accept-findings": flagBool, "--no-wait": flagBool}, maxPositional: 2, run: cmdReview, inHelp: true},
		{names: []string{"verify"}, usage: "verify [<req>]", summary: "run the verification gate; --pass|--fail|--waive [--note <text>] record human evidence; --confirm-passing-tests confirms every requirement whose mapped tests all passed; measures stale quality dimensions first (--no-measure skips) and shows their scorecard", flags: flagSpec{"--pass": flagBool, "--fail": flagBool, "--waive": flagBool, "--note": flagValue, "--confirm-passing-tests": flagBool, "--no-measure": flagBool, "--json": flagBool}, maxPositional: 1, run: cmdVerify, inHelp: true},
		{names: []string{"next"}, usage: "next [--until-human]", summary: "run the next automatable step (--until-human: keep going until a human decision)", flags: nextFlags, run: cmdNext, inHelp: true},
		{names: []string{"risk"}, usage: "risk", summary: "show the deterministic risk profile", flags: jsonFlag, maxPositional: 1, run: cmdRisk, inHelp: true},
		{names: []string{"executors"}, usage: "executors", summary: "list executors + capabilities", flags: jsonFlag, subs: map[string]flagSpec{
			"setup": {"--kind": flagValue, "--image": flagValue, "--model": flagValue, "--yes": flagBool, "--smoke": flagBool,
				"--acp-command": flagValue, "--acp-key-env": flagValue,
				"--url": flagValue, "--api-key-env": flagValue, "--agent": flagValue},
		}, subsAreRequired: true, maxPositional: 1, run: cmdExecutors, inHelp: true},
		{usage: "executors setup [<kind>]", summary: "wire a coding agent: hermes|omp|acp|openhands, default hermes (or --kind; acp needs --image and --acp-command; openhands needs --url, optional --api-key-env, --agent openhands|acp)", inHelp: true},
		{names: []string{"integrations"}, usage: "integrations [status]", summary: "show Slack/Linear integration health: tokens present, deliveries, socket and poll state", flags: jsonFlag, subs: map[string]flagSpec{
			"status": {"--json": flagBool},
			"test":   {"--json": flagBool},
		}, maxPositional: 1, run: cmdIntegrations, inHelp: true},
		{usage: "integrations test slack|linear", summary: "post a Slack test message (and check the Socket Mode token) / validate the Linear key, team, states and label", inHelp: true},
		{names: []string{"watch"}, usage: "watch", summary: "watch a run live", flags: flagSpec{}, maxPositional: 1, run: cmdWatch, inHelp: true},
		{names: []string{"cancel"}, usage: "cancel <op-id>", summary: "interrupt a running operation", flags: flagSpec{}, maxPositional: 1, run: cmdCancel, inHelp: true},
		{names: []string{"config"}, usage: "config", summary: "show or validate .idea-harness config", flags: flagSpec{}, subs: map[string]flagSpec{"validate": {}}, subsAreRequired: true, run: cmdConfig, inHelp: true},
		{usage: "config validate", summary: "validate .idea-harness/harness.yaml (non-zero exit when invalid)"},
		{names: []string{"doctor"}, usage: "doctor", summary: "check daemon/config/state health", flags: flagSpec{}, run: func(_ cmdArgs, stdout, _ io.Writer) error { return cmdDoctor(stdout) }, inHelp: true},
		{names: []string{"docs"}, usage: "docs", summary: "list uploaded context docs", flags: jsonFlag, subs: map[string]flagSpec{
			"add": {"--kind": flagValue, "--title": flagValue},
		}, run: cmdDocs, inHelp: true},
		{usage: "docs add <path>", summary: "upload a context doc (--kind, --title)", inHelp: true},
		{names: []string{"logs"}, usage: "logs [-f] [n]", summary: "show (or follow) the daemon log", flags: flagSpec{"-f": flagBool, "--follow": flagBool, "-n": flagValue}, run: func(a cmdArgs, stdout, _ io.Writer) error { return cmdLogs(a, stdout) }, inHelp: true},
		{names: []string{"daemon"}, usage: "daemon", summary: "run the daemon in the foreground", flags: flagSpec{}, subs: map[string]flagSpec{"status": {}, "stop": {}}, subsAreRequired: true, run: func(a cmdArgs, stdout, stderr io.Writer) error { return runDaemon(a, stdout, stderr) }, inHelp: true},
		{usage: "daemon status|stop", summary: "manage the managed daemon", inHelp: true},
		{names: []string{"dev"}, usage: "dev", summary: "start the daemon and serve the web UI", flags: flagSpec{}, run: cmdDev, inHelp: true},
		{names: []string{"mcp"}, usage: "mcp [--read-only|--allow-apply] [--dir <path>]", summary: "serve this project to MCP clients (Claude Code, Cursor, Codex) over stdio: read + judgment tools by default; --read-only exposes only reads, --allow-apply adds apply/revert (see docs/mcp.md)", flags: flagSpec{"--read-only": flagBool, "--allow-apply": flagBool, "--dir": flagValue}, run: cmdMCP},
		{names: []string{"eval"}, usage: "eval [--suite <name|file>] [--model <provider/model>] [--case <name>]", summary: "run the eval suite end to end on a private in-process daemon and report per case: how far it got, whether the PRD and plan reviews converged, human-judgment stops, cost, tokens, time. Without --model it runs the deterministic scripted provider; --policy approve|stop, --harness <file> (extra harness.yaml), --json-out/--md-out <file>, --baseline <report.json> (non-zero exit on a regression), --timeout <per-case>, --keep", flags: evalFlags, maxPositional: 0, run: cmdEval},
		{names: []string{"version", "--version", "-v"}, usage: "version", summary: "print the version, commit and build state", flags: flagSpec{}, run: cmdVersion, inHelp: true},
		{names: []string{"help", "--help", "-h"}, usage: "help [--format=md]", summary: "show this command list (--format=md emits the docs/cli.md table)", flags: flagSpec{"--format": flagValue}, run: cmdHelp, inHelp: true},
	}
}

// Run dispatches the idea CLI.
func Run(args []string, stdout, stderr io.Writer) error {
	initColor(stdout)

	if len(args) == 0 {
		usage(stderr)
		return nil
	}
	cmd, rest := args[0], args[1:]

	for _, c := range commands() {
		if c.run == nil {
			continue
		}
		for _, name := range c.names {
			if name != cmd {
				continue
			}
			// --help before parse, because parse rejects it as an unknown flag.
			// Every one of the 33 subcommands did exactly that: `idea verify
			// --help` answered `unknown flag "--help"; accepted flags: --fail,
			// --json, --note, --pass, --waive`. Enumerating the flags is a
			// genuinely good error, but there was no way to learn what --waive
			// means, or what `export`'s kinds are, without opening docs/cli.md —
			// and --help is the first thing anyone reaches for.
			if wantsHelp(rest) {
				return c.writeHelp(cmd, stdout)
			}
			a, err := c.parse(cmd, rest)
			if err != nil {
				// Every parse failure is the command line's fault, so it is
				// tagged once here rather than at each of parse's returns.
				return &cliError{kind: kindUsage, err: err,
					hint: "`idea " + cmd + " --help` shows its arguments and flags"}
			}
			return c.run(a, stdout, stderr)
		}
	}
	return usageErrorf("unknown command %q (try `idea help`)", cmd)
}

// parse turns a command's remaining argv into cmdArgs, enforcing the spec the
// table declares. Doing this here rather than inside each command is what stops
// a command from silently swallowing a flag by forgetting to call parseArgs --
// ten of them did, so `idea status --jsonn` printed human output to stdout and
// exited 0, breaking any downstream `jq`.
func (c command) parse(name string, rest []string) (cmdArgs, error) {
	if c.flags == nil {
		// Not reachable through the table, which TestEveryCommandDeclaresItsFlags
		// keeps complete. Explicit because the compiler cannot catch an omitted
		// field in a keyed struct literal, and a nil spec would otherwise reject
		// every flag the command legitimately accepts.
		return cmdArgs{}, fmt.Errorf("internal: command %q declares no flag spec", name)
	}
	spec, sub := c.flags, ""
	if len(rest) > 0 && c.subs != nil {
		if subSpec, ok := c.subs[rest[0]]; ok {
			spec, sub, rest = subSpec, rest[0], rest[1:]
		}
	}
	positional, flags, err := parseArgs(spec, rest)
	if err != nil {
		return cmdArgs{}, err
	}

	// Checked against the PARSED positionals, not the raw argv.
	//
	// Testing rest[0] let the "--" terminator walk straight past this: `idea
	// config -- validate` printed the model list and `idea daemon -- stop`
	// started a foreground daemon -- the exact two behaviours the previous round
	// fixed, still reachable one keystroke away.
	if sub == "" && c.subsAreRequired && len(positional) > 0 {
		return cmdArgs{}, fmt.Errorf("unknown %s subcommand %q (use %s)",
			name, positional[0], strings.Join(sortedKeys(c.subs), ", "))
	}

	// Extra positionals are an error, not something to drop silently. `idea apply
	// CHG-1 CHG-2` applied only the first and reported success, so the user
	// believed both had landed.
	if c.maxPositional > 0 && len(positional) > c.maxPositional {
		return cmdArgs{}, fmt.Errorf("%s takes at most %d argument(s), got %d: %s",
			name, c.maxPositional, len(positional), strings.Join(positional, " "))
	}
	return cmdArgs{sub: sub, positional: positional, flags: flags}, nil
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// cmdDev starts the daemon and serves the web UI.
//
// It used to call runDaemon unconditionally, which binds daemonAddr()
// (hard-coded to 8787 unless IDEA_HARNESS_ADDR overrides it) in the
// foreground. `idea init` already auto-starts a background daemon on that
// same address (ensureDaemon), so a plain `idea dev` run afterwards failed
// with a raw `bind: address already in use` -- true, but useless: the goal of
// `idea dev` (a daemon serving the UI) was already met (H2).
func cmdDev(_ cmdArgs, stdout, stderr io.Writer) error {
	if healthy() {
		fmt.Fprintln(stdout, ok("✓ daemon already running")+" "+muted("— web UI at "+uiURL()))
		return nil
	}
	fmt.Fprintln(stderr, muted("starting daemon (web UI served at "+uiURL()+")"))
	return runDaemon(cmdArgs{}, io.Discard, stderr)
}

// cmdHelp prints the command list. The human form goes to stderr exactly as it
// always has; `--format=md` writes the generated docs/cli.md table to the
// caller's stdout, so a drift check can capture it.
func cmdHelp(a cmdArgs, stdout, stderr io.Writer) error {
	flags := a.flags
	switch format := flags.value("--format"); format {
	case "", "text":
		usage(stderr)
		return nil
	case "md", "markdown":
		return renderCommandTable(stdout)
	default:
		return usageErrorf("unknown --format %q (use text or md)", format)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, heading("idea-harness")+" — a local-first AI product-development control plane\n\n")
	fmt.Fprintln(w, bold("Usage:"), "idea <command>")
	fmt.Fprintln(w, bold("Commands:"))
	for _, c := range commands() {
		if !c.inHelp {
			continue
		}
		fmt.Fprintf(w, "  %-20s %s\n", accent(c.usage), c.summary)
	}
	fmt.Fprintf(w, "\n%s\n", muted("State commands support --json."))
}

// renderCommandTable writes the markdown command table embedded in docs/cli.md
// between the generated-block markers. Pipes are escaped because a literal one
// ends a markdown table cell, code span or not.
func renderCommandTable(w io.Writer) error {
	var b strings.Builder
	b.WriteString("| Command | What it does | Flags | `--json` |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, c := range commands() {
		fmt.Fprintf(&b, "| `idea %s` | %s | %s | %s |\n",
			escapeTableCell(c.usage), escapeTableCell(c.summary), c.flagDoc(), c.jsonDoc())
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func escapeTableCell(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

// ---------- daemon ----------

func runDaemon(a cmdArgs, stdout, stderr io.Writer) error {
	// The subcommand token is matched by dispatch, so `idea daemon stauts` is
	// rejected there rather than falling through to run a second daemon in the
	// foreground, which is what it used to do (DX audit).
	switch a.sub {
	case "status":
		// Exits non-zero when the daemon is down, so `idea daemon status && …`
		// means what it reads as, and writes to the injected writer so the
		// output is testable through Run.
		if healthy() {
			line := ok("daemon: running")
			// Best-effort: naming the build is useful, and a missing or rejected
			// token must not turn "is it running" into a failure. Exit stays 0
			// on a mismatch too -- scripts/smoke.sh pipes this under `set -o
			// pipefail`, and `daemon status` answers whether it is running, not
			// whether it is the right one. `idea doctor` answers that.
			if info, aok := daemonBuild(); aok {
				line += " " + muted(info.String())
				if !buildinfo.SameBuild(buildinfo.Get(), info) {
					line += " " + warn("— this CLI is "+buildinfo.Get().String()+"; `idea daemon stop` restarts it")
				}
			}
			fmt.Fprintln(stdout, line)
			// H2: "is it running" and "where do I use it" are the same question
			// from the UI side, and the address was otherwise something you had
			// to already know (or infer from IDEA_HARNESS_ADDR) to answer it.
			fmt.Fprintln(stdout, muted("web UI: ")+bold(uiURL()))
			return nil
		}
		return errorf(kindDaemonUnavailable, "daemon is not running (it auto-starts on the first command that needs it)")
	case "stop":
		pid, err := stopDaemon()
		if err == nil {
			fmt.Fprintf(stdout, "daemon stopped (pid %d)\n", pid)
		}
		return err
	}
	log := newDaemonLogger(stderr)
	defer removePID()
	// Written only once this process owns the address and the state dir. A
	// second `idea daemon` wrote it first and then failed on the port, leaving
	// the running daemon with a pidfile naming a dead process.
	return daemon.Run(daemonAddr(), daemon.DefaultStateRoot(), log, writePID)
}

func pidPath() string {
	return filepath.Join(daemon.DefaultStateRoot(), "daemon.pid")
}

func logPath() string {
	return filepath.Join(daemon.DefaultStateRoot(), "daemon.log")
}

// newDaemonLogger writes structured logs to both stderr and the daemon log file
// (~/.local/share/idea-harness/daemon.log), so auto-started daemons leave a
// trail the user can inspect with `idea logs`.
func newDaemonLogger(stderr io.Writer) *slog.Logger {
	if err := os.MkdirAll(daemon.DefaultStateRoot(), 0o700); err != nil {
		return slog.New(slog.NewTextHandler(stderr, nil))
	}
	f, err := os.OpenFile(logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return slog.New(slog.NewTextHandler(stderr, nil))
	}
	// An auto-started daemon's stderr IS daemon.log (ensureDaemon redirects
	// it there), so writing to both put every line in the file twice.
	if sf, ok := stderr.(*os.File); ok {
		a, aerr := sf.Stat()
		b, berr := f.Stat()
		if aerr == nil && berr == nil && os.SameFile(a, b) {
			f.Close()
			return slog.New(slog.NewTextHandler(stderr, nil))
		}
	}
	return slog.New(slog.NewTextHandler(io.MultiWriter(stderr, f), nil))
}

func writePID() {
	_ = os.MkdirAll(daemon.DefaultStateRoot(), 0o700)
	_ = os.WriteFile(pidPath(), []byte(strconv.Itoa(os.Getpid())), 0o600)
}

// removePID removes the pidfile only if it is still ours. A stop now takes up
// to 30 s, and a command in that window starts a new daemon that writes its own
// pid; removing the file unconditionally on the way out left that daemon with
// no pidfile, so it could not be stopped.
func removePID() {
	data, err := os.ReadFile(pidPath())
	if err == nil && strings.TrimSpace(string(data)) == strconv.Itoa(os.Getpid()) {
		_ = os.Remove(pidPath())
	}
}

func stopDaemon() (int, error) {
	data, err := os.ReadFile(pidPath())
	if err != nil {
		// The pidfile lives in the state directory, so removing that directory
		// takes the only handle on the daemon with it. The old message --
		// "is the daemon running?" -- was then actively wrong: it usually was,
		// and it was the reason every other command was failing.
		//
		// There is no shutdown endpoint, and adding an unauthenticated one to
		// solve a token problem would be a worse trade than this message. So
		// say precisely what is true and what will work.
		if healthy() {
			return 0, fmt.Errorf("a daemon is answering on %s but its pidfile is gone, "+
				"so it cannot be stopped from here -- this is what happens when the state "+
				"directory is removed while it runs. Stop it with: %s",
				daemonAddr(), stopHint())
		}
		return 0, errorf(kindDaemonUnavailable, "no daemon pidfile, and nothing answering on %s; it is already stopped",
			daemonAddr())
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}
	if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
		return 0, err
	}
	// Wait for it to go. The daemon now cancels running operations on SIGTERM
	// and removes their agent containers before exiting, which takes a moment;
	// returning at once let `idea daemon stop && idea ...` race a daemon that
	// was still shutting down. Bounded by the daemon's own 30 s shutdown limit.
	deadline := time.Now().Add(35 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return pid, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return pid, fmt.Errorf("the daemon (pid %d) is still stopping after 35 s; check `idea logs`", pid)
}

// ---------- init / new ----------

func cmdInit(a cmdArgs, stdout io.Writer) error {
	// `idea init /some/path` used to initialise the current directory and report
	// success, so a user watched it create a project somewhere they did not ask
	// for (DX audit). The argument reads as intent, so it is honoured.
	if len(a.positional) > 1 {
		return usageErrorf("usage: idea init [<dir>]")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if dir := a.arg(0); dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return fmt.Errorf("cannot initialize %s: %w", dir, err)
		}
		if !info.IsDir() {
			return usageErrorf("cannot initialize %s: not a directory", dir)
		}
		cwd = abs
	}
	if err := ensureDaemon(); err != nil {
		return err
	}
	c, err := newClient()
	if err != nil {
		return err
	}
	var out map[string]string
	if err := c.do("POST", "/api/v1/projects", map[string]string{"root_path": cwd}, &out); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ initialized project")+" "+code(out["project_id"])+" in "+cwd)
	// H2: this is the first moment the daemon (auto-started above by
	// ensureDaemon) is actually running, and the web UI is one of the two ways
	// to use it. Without this line, finding the UI meant already knowing the
	// port -- or hitting `idea dev`'s bind error moments later trying to guess.
	fmt.Fprintln(stdout, muted("web UI: ")+bold(uiURL()))
	fmt.Fprintln(stdout, muted("next: ")+bold("idea new")+muted(" \"<describe your idea>\""))
	return nil
}

// newDocInput is one context doc read from a --doc flag.
type newDocInput struct {
	Content string `json:"content"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
}

func cmdNew(a cmdArgs, stdout, stderr io.Writer) error {
	// --doc is declared in the table's spec for `new`; this used to be a
	// hand-rolled loop with its own "unknown flag" arm.
	docPaths := a.list("--doc")
	idea := strings.Join(a.positional, " ")
	if strings.TrimSpace(idea) == "" {
		if len(docPaths) > 0 {
			return usageErrorf("an idea is required with --doc (e.g. idea new \"<idea>\" --doc <path>)")
		}
		if !colorEnabled {
			return usageErrorf("usage: idea new \"<idea>\" [--doc <path> …]")
		}
		return interactiveNew(stdout, stderr)
	}

	// Read each --doc file as text, bound and swept atomically BEFORE the M1 run.
	docs := make([]newDocInput, 0, len(docPaths))
	for _, path := range docPaths {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(f, maxDocUploadBytes+1))
		f.Close()
		if err != nil {
			return err
		}
		if len(data) > maxDocUploadBytes {
			return usageErrorf("document %q exceeds the %d MiB limit", path, maxDocUploadBytes>>20)
		}
		if len(data) == 0 {
			return usageErrorf("document %q is empty", path)
		}
		if docparse.Sniff(path, data) != "text" {
			return usageErrorf("--doc supports text files only (%s); use `idea docs add` for PDF/DOCX/images", path)
		}
		kind := docparse.InferKind(path)
		title := filepath.Base(path)
		docs = append(docs, newDocInput{Content: string(data), Kind: kind, Title: title})
	}

	c, projectID, err := connect()
	if err != nil {
		return err
	}
	body := struct {
		Title       string        `json:"title"`
		Description string        `json:"description"`
		Docs        []newDocInput `json:"docs,omitempty"`
	}{Title: idea, Description: idea, Docs: docs}
	var out map[string]string
	if err := c.do("POST", "/api/v1/projects/"+projectID+"/initiatives", body, &out); err != nil {
		return err
	}
	fmt.Fprintln(stdout, ok("✓ initiative started")+" "+code(out["initiative_id"]))
	if isDemoMode(c, projectID) {
		fmt.Fprintln(stdout, warn("demo mode — synthetic output. Configure a model with:")+" "+bold("idea model set <provider/model>"))
	}

	// Wait for the run this command just started, unless asked not to.
	//
	// It used to return here, discarding the operation_id it already had. That
	// made the README's own eight-command quick start a race: the very next
	// command saw the initiative still `running`, `idea next --until-human`
	// fell through to "workflow running — try `idea watch`" without doing
	// anything, and the block went on to report "✓ initiative complete" over an
	// initiative with zero change sets. Measured before this fix: 5/5 runs
	// passed with provider keys exported, 0/5 without, because ambient keys
	// shifted the timing enough to win the race.
	//
	// Fixing the producer rather than teaching every consumer to poll: `idea
	// new` is the only command that starts work and does not wait for it, and
	// this also settles `idea new` followed by status, prd, advance or docs add.
	opID := out["operation_id"]
	if a.has("--no-wait") || opID == "" {
		fmt.Fprintln(stdout, muted("watch progress: ")+bold("idea watch"))
		return nil
	}
	fmt.Fprintln(stdout, muted("working… (")+bold("--no-wait")+muted(" returns immediately; ")+bold("idea watch")+muted(" follows it)"))
	if err := waitForOperation(c, projectID, out["initiative_id"], opID, stdout); err != nil {
		return err
	}
	// The line has to reflect what actually happened. It used to be printed
	// unconditionally on operation success, so a PRD parked for human judgment
	// -- `demo.block_review_cycles: 3`, or any real model whose review does not
	// converge -- was reported as ready while `idea status` said "needs your
	// judgment" one command later. That is the defect audit 6 fixed one command
	// over, on the path README tells you to expect with a real model.
	//
	// Asked rather than derived: the property that was violated is agreement
	// between this line and what `idea next` will say, so the answer comes from
	// the same endpoint `idea next` reads. Only two states are reachable here --
	// engine.Run ends in runReviewLoop, which returns approved or
	// needs_human_judgment/prd_review -- but the default arm renders whatever a
	// third would be rather than guessing.
	//
	// renderNextAction, NOT reportNextAction: the latter RUNS `advance`, and
	// `idea new` driving into M2 on its own would spend model budget through
	// design before anyone has read the PRD.
	step, nerr := nextAction(c, projectID, out["initiative_id"])
	switch {
	case nerr != nil:
		// The initiative exists and its run succeeded; a failure to ask what is
		// next is not a reason to report failure. Fall back to the line that was
		// unconditional before, which is right for the converging case.
		fmt.Fprintln(stdout, ok("✓ reviewed PRD ready")+" "+muted("— next: ")+bold("idea next --until-human"))
	case step.Action == "advance":
		fmt.Fprintln(stdout, ok("✓ reviewed PRD ready")+" "+muted("— next: ")+bold("idea next --until-human"))
	default:
		// Errors are swallowed on purpose. `idea new` succeeded: it created the
		// initiative and its run finished. A parked review is the workflow
		// working, and `renderNextAction` returns an error only for `failed`,
		// which `idea new` reports through its own error path already.
		_ = renderNextAction(step, stdout)
	}
	return nil
}

// connect ensures the daemon is running and returns a client + the cwd project.
func connect() (*client, string, error) {
	if err := ensureDaemon(); err != nil {
		// Whatever stopped it starting -- including a failure to find our own
		// executable -- the outcome for the caller is the same: no daemon.
		return nil, "", withKind(kindDaemonUnavailable, err)
	}
	c, err := newClient()
	if err != nil {
		return nil, "", err
	}
	projectID, err := c.projectIDForCwd()
	if err != nil {
		return nil, "", err
	}
	return c, projectID, nil
}

// ---------- config / doctor ----------

func cmdConfig(a cmdArgs, stdout, stderr io.Writer) error {
	if a.sub == "validate" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		// The same false green doctor was fixed for twice, in its sibling.
		// config.Load returns usable defaults for a directory with no
		// .idea-harness, so this printed "config: valid" and exited 0 anywhere on
		// the filesystem -- while docs/cli.md advertises "non-zero exit when
		// invalid". Unlike doctor, this command is asked to validate a specific
		// thing, so the absence of that thing is an error rather than a note.
		if _, statErr := os.Stat(filepath.Join(cwd, config.Dir)); statErr != nil {
			return errorf(kindNotFound, "no idea-harness project in %s (run `idea init` to create one)", cwd)
		}
		// Schema validity is not the same as a usable config: a default_model
		// naming a provider that does not exist parses fine and fails at the
		// first run. validateConfig reports both, and exits non-zero only for
		// error-severity problems (a missing API key is demo mode, not a fault).
		return validateConfig(cwd, stdout)
	}
	return showConfig(stdout, stderr)
}

// cmdLogs prints the tail of the daemon log (§57, §61).
func cmdLogs(a cmdArgs, stdout io.Writer) error {
	// `idea logs -n 200` was silently ignored and `idea logs 200x` silently
	// printed the default 50 lines: the old loop dropped anything Atoi refused
	// (DX audit).
	n := 50
	follow := a.has("-f") || a.has("--follow")
	if v := a.value("-n"); v != "" {
		parsed, err := strconv.Atoi(v)
		if err != nil || parsed <= 0 {
			return usageErrorf("-n needs a positive number of lines, got %q", v)
		}
		n = parsed
	}
	if arg := a.arg(0); arg != "" {
		parsed, err := strconv.Atoi(arg)
		if err != nil || parsed <= 0 {
			return usageErrorf("idea logs takes a positive line count, got %q", arg)
		}
		n = parsed
	}
	if len(a.positional) > 1 {
		return usageErrorf("usage: idea logs [-f] [-n <lines>] [<lines>]")
	}
	if follow {
		return followLogs(stdout)
	}

	data, err := os.ReadFile(logPath())
	if err != nil {
		return errorf(kindNotFound, "no daemon log at %s yet — run `idea daemon` or any command first", logPath())
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	fmt.Fprintln(stdout, muted("daemon log: ")+code(logPath()))
	for _, l := range lines {
		fmt.Fprintln(stdout, l)
	}
	return nil
}

// followLogs tails the daemon log like `tail -f`, streaming new lines to stdout.
func followLogs(stdout io.Writer) error {
	f, err := os.Open(logPath())
	if err != nil {
		return errorf(kindNotFound, "no daemon log at %s yet", logPath())
	}
	defer f.Close()
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	fmt.Fprintln(stdout, muted("following daemon log: ")+code(logPath())+muted("  (Ctrl-C to stop)"))
	buf := make([]byte, 32*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if _, werr := stdout.Write(buf[:n]); werr != nil {
				return werr
			}
		}
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// cmdVersion prints what build this is. A bug report that cannot name a build
// is most of a bug report missing, and every install used to answer "0.1.0".
func cmdVersion(a cmdArgs, stdout, _ io.Writer) error {
	fmt.Fprintln(stdout, buildinfo.Get())
	// Only on a mismatch, and only if a daemon is already up -- this must not
	// start one, which it never has, and the one-line output a script may parse
	// stays one line in the ordinary case.
	if info, ok := daemonBuild(); ok && !buildinfo.SameBuild(buildinfo.Get(), info) {
		fmt.Fprintln(stdout, muted("daemon: ")+info.String()+
			muted("  — it outlives the binary that started it; `idea daemon stop` restarts it on this build"))
	}
	return nil
}

func cmdDoctor(stdout io.Writer) error {
	fmt.Fprintln(stdout, muted("idea ")+buildinfo.Get().String())
	daemonUp := healthy()
	configValid := true
	// Reachable is not the same as usable. /api/v1/health is unauthenticated, so
	// a daemon holding a token this CLI no longer has answers it happily while
	// refusing everything else -- and doctor reported "all checks passed" while
	// every other command returned a bare `idea: unauthorized`. A diagnostic
	// that says healthy during a hard failure is worse than no diagnostic.
	// One fetch, two questions: whether the token is accepted at all, and what
	// build answered.
	var daemonInfo buildinfo.Info
	authOK := false
	if daemonUp {
		daemonInfo, authOK = daemonBuild()
	}
	tokenRejected := daemonUp && !authOK
	// The daemon is detached and outlives the binary that started it, so
	// replacing that binary leaves the previous version serving every command
	// while `idea version` reports the new one. doctor said "✓ daemon running"
	// and "all checks passed" through it, in both directions.
	staleDaemon := daemonUp && authOK && !buildinfo.SameBuild(buildinfo.Get(), daemonInfo)
	var daemonProblem error
	switch {
	case tokenRejected:
		fmt.Fprintln(stdout, warn("✗ daemon")+" "+muted("running, but it is rejecting this CLI's token"))
		fmt.Fprintln(stdout, muted("  It is serving with a token from before the state directory changed."))
		fmt.Fprintln(stdout, muted("  Fix: ")+bold("idea daemon stop")+
			muted(" — or, if its pidfile went with the state directory, ")+bold(stopHint())+muted("."))
		fmt.Fprintln(stdout, muted("  The next command starts a fresh one."))
		daemonProblem = errorf(kindUnauthorized, "the daemon is rejecting this CLI's token; stop it and let the next command restart it")
	case staleDaemon:
		fmt.Fprintln(stdout, errText("✗ daemon")+" "+muted("running ")+code(daemonInfo.String())+
			muted(", but this CLI is ")+code(buildinfo.Get().String()))
		fmt.Fprintln(stdout, muted("  The daemon is detached and survives replacing the binary, so an upgrade"))
		fmt.Fprintln(stdout, muted("  leaves the previous version serving every command."))
		fmt.Fprintln(stdout, muted("  Fix: ")+bold("idea daemon stop")+muted(" — the next command starts one from this binary."))
		daemonProblem = fmt.Errorf("the daemon is running %s but this CLI is %s; run `idea daemon stop` and the next command starts a fresh one",
			daemonInfo, buildinfo.Get())
	case daemonUp:
		fmt.Fprintln(stdout, ok("✓ daemon")+" "+muted("running"))
	case daemonAnswersHealth():
		// healthy() returned false because newClient() could not read the token,
		// not because nothing answered. Reporting "not running" here sent people
		// to restart a daemon that was serving fine, while `idea daemon status`
		// said running and every other command said `unauthorized`.
		fmt.Fprintln(stdout, warn("✗ daemon")+" "+muted("running, but this CLI has no token for it"))
		fmt.Fprintln(stdout, muted("  The state directory is gone or unreadable; the daemon still holds the token it read at start."))
		fmt.Fprintln(stdout, muted("  Fix: ")+bold("idea daemon stop")+
			muted(" — or, if its pidfile went with the state directory, ")+bold(stopHint())+muted("."))
		daemonProblem = errorf(kindUnauthorized, "a daemon is running but this CLI has no token for it; stop it and let the next command restart it")
	default:
		fmt.Fprintln(stdout, muted("• daemon: not running (auto-starts on first command)"))
	}
	cwd, _ := os.Getwd()

	// Outside a project, report the install and stop -- without claiming
	// anything about a config that is not there.
	//
	// config.Load returns a usable default for a directory with no
	// .idea-harness, so doctor used to print "✓ config valid" and "all checks
	// passed" anywhere on the filesystem: a false green, flagged in the first DX
	// audit and still true in the second.
	//
	// The first fix for that exited non-zero here, which broke the other thing
	// doctor is for. README puts `idea doctor` under "Verify:" straight after
	// the install block and before the quick start, so the documented
	// post-install check runs in exactly this situation -- and a developer who
	// has just installed correctly would have been told their install failed.
	// The third DX audit caught it as an install-stage failure.
	//
	// So: no claim about config, an explicit note that there is no project, and
	// exit 0, because nothing is actually wrong. The bug was the false claim,
	// not the exit code.
	if _, err := os.Stat(filepath.Join(cwd, config.Dir)); err != nil {
		fmt.Fprintln(stdout)
		fmt.Fprintln(stdout, ok("install looks good")+" "+muted("— no idea-harness project in this directory"))
		fmt.Fprintln(stdout, muted("  ")+bold("idea init")+muted(" creates one, then `idea doctor` checks it"))
		// Not `nil`. A daemon problem is an install problem, and this is exactly
		// where the README puts `idea doctor` -- straight after the install
		// block, which is where someone verifies an upgrade. Returning nil here
		// meant a rejected token or a stale daemon exited 0 outside a project,
		// which is the same false green this branch's own comment is about, one
		// directory over.
		return daemonProblem
	}

	// doctor is what the README names as the post-install check, so it has to
	// catch a config that parses but cannot work -- a default_model whose
	// provider does not exist used to pass here and fail at the first run.
	dotEnv := config.LoadDotEnv(cwd)
	cfg, cfgErr := config.Load(filepath.Join(cwd, config.Dir))
	if err := cfgErr; err != nil {
		configValid = false
		fmt.Fprintf(stdout, "%s %v\n", errText("✗ config:"), err)
	} else if problems := doctorConfigProblems(cfg, dotEnv, daemonUp); config.HasErrors(problems) {
		configValid = false
		fmt.Fprintln(stdout, errText("✗ config:"))
		renderConfigProblems(problems, stdout)
	} else if len(problems) > 0 {
		renderConfigProblems(problems, stdout)
	} else {
		fmt.Fprintln(stdout, ok("✓ config")+" "+muted("valid"))
	}

	// One client and one project id for everything below. doctor built two of
	// each and fetched /providers twice.
	var (
		doctorClient *client
		doctorProj   string
	)
	if daemonUp {
		if c, err := newClient(); err == nil {
			if projectID, perr := c.projectIDForCwd(); perr == nil {
				doctorClient, doctorProj = c, projectID
			}
		}
	}

	// The API keys the configured models need, answered by the daemon.
	//
	// This is the finding: keyProblems resolves the key with os.Getenv, so
	// asking it from the CLI reports the CLI's environment. The daemon reads
	// its own once, at start, so a key exported afterwards is invisible to it
	// -- and doctor said "all checks passed" in exactly the state four
	// documents credit it with catching.
	// One source of truth for the key verdict: keyErr is both what fails the
	// command and what excludes "all checks passed".
	var keyErr error
	switch {
	case !daemonUp:
		// Nothing to ask. Said out loud rather than skipped in silence, which
		// is how every false green in this command shipped.
		// The CLI-side check above already answered, and correctly: with no
		// daemon running, this shell is the environment the next command will
		// start one from.
		fmt.Fprintln(stdout, muted("• keys: checked against this shell — no daemon is running, so it is the one the next command starts"))
	case doctorClient == nil:
		fmt.Fprintln(stdout, muted("• keys: not checked — no project registered for this directory"))
	default:
		var view cliProvidersView
		switch err := doctorClient.do("GET", "/api/v1/projects/"+doctorProj+"/providers", nil, &view); {
		case err != nil:
			fmt.Fprintln(stdout, muted("• keys: could not ask the daemon ")+muted("("+err.Error()+")"))
		case cfgErr != nil:
			// Said, not skipped: without the config there is nothing to say
			// which providers this project routes to.
			fmt.Fprintln(stdout, muted("• keys: not checked — the config above could not be read"))
		default:
			stale, missing := keyFindings(view, referencedProviders(cfg.Harness), dotEnv)
			keyErr = renderKeyFindings(stale, missing, stdout)
		}
		// Outside the config branch: an unreadable config does not stop the
		// daemon reporting demo mode, and hoisting the fetch had silently made
		// this banner conditional on the config loading.
		if view.Demo {
			fmt.Fprintln(stdout, warn("demo mode — no model configured; run `idea model set <provider>/<model>` for real output."))
		}
	}

	// Which executor the next run uses -- loud when auto fell back to the
	// placeholder (doctor_executor.go).
	doctorExecutor(cfg, cfgErr, dotEnv, doctorClient, doctorProj, stdout)
	if cfgErr == nil {
		doctorOpenHands(cfg, dotEnv, stdout)
	}

	// Product-graph edge audit (§M2 tail): untraced tasks/objects + cycles.
	graphOK := true
	{
		{
			c, projectID := doctorClient, doctorProj
			if c != nil {
				// The graph audit is one of doctor's checks, not its purpose:
				// in a project with no initiative there is no graph to audit,
				// and doctor still has to report daemon and config health. So a
				// missing initiative skips the check rather than failing the
				// command -- this is the health command someone runs when
				// things are already wrong.
				iniID, ierr := resolveInitiative(c, projectID, nil)
				switch {
				case errors.Is(ierr, errNoInitiatives):
					fmt.Fprintln(stdout, muted("• graph audit: no initiative yet — nothing to audit"))
				case ierr != nil:
					// Not an absence. Listing initiatives failed, which means the
					// store or the daemon is unhealthy -- the condition doctor
					// exists to report. Rendering it as "no initiative yet" is how
					// an unreadable store produced "all checks passed".
					graphOK = false
					fmt.Fprintf(stdout, "%s %v\n", errText("✗ graph audit:"), ierr)
				default:
					var findings []cliFinding
					if derr := c.do("GET", initiativeResource(projectID, iniID, "doctor"), nil, &findings); derr != nil {
						graphOK = false
						fmt.Fprintf(stdout, "%s %v\n", errText("✗ graph audit:"), derr)
					} else if len(findings) > 0 {
						graphOK = false
						printDoctorFindings(stdout, findings)
					}
				}
			}
		}
	}

	// Slack/Linear health. Reported, deliberately not failing the command:
	// a revoked Slack token or a Linear outage is worth seeing here, but it
	// does not make the harness itself unhealthy, and a script gating on
	// `idea doctor` should not start failing because a chat service did.
	if doctorClient != nil {
		var iv cliIntegrationsView
		if err := doctorClient.do("GET", "/api/v1/projects/"+doctorProj+"/integrations", nil, &iv); err != nil {
			fmt.Fprintln(stdout, muted("• integrations: could not ask the daemon ("+err.Error()+")"))
		} else {
			renderIntegrations(stdout, iv, true)
		}
	}

	fmt.Fprintln(stdout)
	if daemonUp && !tokenRejected && !staleDaemon && configValid && keyErr == nil && graphOK {
		fmt.Fprintln(stdout, ok("all checks passed"))
		return nil
	}
	// A failing check has to fail the command. doctor is what README names as
	// the post-install verification step, and a script that runs it needs the
	// exit code, not the prose. A daemon that has not started yet is not a
	// failure -- it auto-starts on first use.
	// A rejected token is checked first: it makes every other command fail, so
	// reporting a config problem underneath it would name the wrong cause.
	// Both daemon problems come first, and for the same reason: a stale daemon
	// is running another commit's code for every command, so a config finding
	// underneath it names the wrong cause too.
	if daemonProblem != nil {
		return daemonProblem
	}
	if !configValid {
		return errorf(kindConfigInvalid, "config problems above must be fixed")
	}
	if keyErr != nil {
		return keyErr
	}
	if !graphOK {
		return fmt.Errorf("graph audit findings above must be resolved")
	}
	return nil
}

// printDoctorFindings renders the product-graph edge audit findings.
func printDoctorFindings(stdout io.Writer, findings []cliFinding) {
	fmt.Fprintln(stdout, heading("Graph audit")+muted(fmt.Sprintf("  (%d finding(s))", len(findings))))
	for _, f := range findings {
		sev := f.Severity
		switch sev {
		case "critical", "high":
			sev = errText(sev)
		case "medium":
			sev = warn(sev)
		default:
			sev = muted(sev)
		}
		fmt.Fprintln(stdout, "  "+sev+" "+f.Finding)
		if f.Recommendation != "" {
			fmt.Fprintln(stdout, "      "+muted("→ ")+f.Recommendation)
		}
	}
}

var _ = json.RawMessage{}

// flagDoc renders a command's accepted flags for the generated reference.
//
// This column is the point of the exercise. Deriving only the --json column
// from the spec would centralise enforcement while leaving the flag *names*
// keyed to the hand-written summary string -- so `verify --waive` could be
// parsed, enforced, and still absent from docs/cli.md with `make docs-check`
// green, which is exactly the drift the audit found. With the names rendered
// from the spec, a flag that exists in code cannot be missing from the docs.
func (c command) flagDoc() string {
	if c.run == nil {
		return "—"
	}
	var parts []string
	for _, name := range c.flags.names() {
		parts = append(parts, "`"+name+"`")
	}
	// Subcommand names, not only their flags. Rendering flags alone left the
	// names hand-maintained: a subcommand with no flags contributed nothing, so
	// adding `daemon restart` would dispatch, document nothing, and keep
	// docs-check green -- the same defect one level down.
	for _, sub := range sortedKeys(c.subs) {
		flags := c.subs[sub].names()
		if len(flags) == 0 {
			parts = append(parts, "`"+sub+"`")
			continue
		}
		for _, name := range flags {
			parts = append(parts, "`"+sub+" "+name+"`")
		}
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

// jsonDoc records how a command answers --json, derived from its spec rather
// than kept beside it as a hand-maintained field. The old field rendered the
// same "—" for six commands that reject every flag and for ten that silently
// swallowed them, which is two opposite behaviours under one symbol.
func (c command) jsonDoc() string {
	switch {
	case c.run == nil:
		return "—"
	// Checked before the empty-spec case: `plan` declares --json precisely so it
	// can refuse it with a pointer somewhere better, and an ordering that tested
	// the spec first would silently relabel it "takes no flags".
	case c.rejectsJSON:
		return "rejected — use `idea tasks --json`"
	case len(c.flags) == 0:
		// Subcommands may still take flags; the Flags column shows them. What
		// this column reports is the command's own flag surface, and it has none.
		return "takes no flags"
	// accepts() rather than a direct comparison: a map miss returns flagUnset,
	// and comparing against flagBool would be true for every command that does
	// not declare --json.
	case c.flags.accepts("--json", flagBool):
		return "yes"
	default:
		return "no"
	}
}

// wantsHelp reports whether the argv asks for this command's help.
//
// Only before "--": after the terminator the tokens are the command's own data,
// and `idea docs add -- --help` must add a file called --help rather than print
// usage. The parser learned that distinction the hard way one round ago.
func wantsHelp(rest []string) bool {
	for _, a := range rest {
		if a == "--" {
			return false
		}
		if a == "--help" || a == "-h" {
			return true
		}
	}
	return false
}

// writeHelp renders one command's usage, summary, subcommands and flags from
// the same table that drives `idea help` and docs/cli.md, so the three cannot
// disagree.
func (c command) writeHelp(name string, stdout io.Writer) error {
	fmt.Fprintln(stdout, bold("idea "+c.usage))
	if c.summary != "" {
		fmt.Fprintln(stdout, "\n"+c.summary)
	}

	if len(c.subs) > 0 {
		fmt.Fprintln(stdout, "\n"+muted("Subcommands:"))
		for _, sub := range sortedKeys(c.subs) {
			line := "  " + code(sub)
			if flags := renderFlagList(c.subs[sub]); flags != "" {
				line += "  " + muted(flags)
			}
			fmt.Fprintln(stdout, line)
		}
	}

	if flags := renderFlagList(c.flags); flags != "" {
		fmt.Fprintln(stdout, "\n"+muted("Flags:")+" "+flags)
	} else if len(c.subs) == 0 {
		fmt.Fprintln(stdout, "\n"+muted("No flags."))
	}
	fmt.Fprintln(stdout, "\n"+muted("Full reference: ")+code("docs/cli.md"))
	return nil
}

// renderFlagList renders a spec as the flags a user types, with a placeholder
// for the ones that take a value.
func renderFlagList(spec flagSpec) string {
	if len(spec) == 0 {
		return ""
	}
	var out []string
	for _, f := range sortedKeys(spec) {
		switch spec[f] {
		case flagValue:
			out = append(out, f+" <value>")
		case flagRepeated:
			out = append(out, f+" <value> (repeatable)")
		default:
			out = append(out, f)
		}
	}
	return strings.Join(out, ", ")
}
