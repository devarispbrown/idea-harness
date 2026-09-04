package cli

import (
	"encoding/json"
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
		{names: []string{"new"}, usage: `new "<idea>"`, summary: "create an initiative and start the workflow", flags: flagSpec{"--doc": flagRepeated}, run: cmdNew, inHelp: true},
		{names: []string{"status"}, usage: "status", summary: "show initiative + current work", flags: jsonFlag, maxPositional: 1, run: cmdStatus, inHelp: true},
		{names: []string{"changed"}, usage: "changed", summary: "show What Changed since last look", flags: jsonFlag, maxPositional: 1, run: cmdChanged, inHelp: true},
		{names: []string{"decisions"}, usage: "decisions", summary: "list open/blocked decisions", flags: jsonFlag, maxPositional: 1, run: cmdDecisions, inHelp: true},
		{names: []string{"reviews"}, usage: "reviews", summary: "list review findings", flags: jsonFlag, maxPositional: 1, run: cmdReviews, inHelp: true},
		{names: []string{"model"}, usage: "model", summary: "list/set model providers (Gemini, OpenAI, …)", flags: flagSpec{}, subs: map[string]flagSpec{
			"list": {}, "ls": {},
			"set": {"--role": flagValue},
			"add": {"--type": flagValue, "--base-url": flagValue, "--key-env": flagValue},
		}, subsAreRequired: true, run: cmdModel, inHelp: true},
		{usage: "model ls <provider>", summary: "list a provider's models live (falls back to the catalog offline)"},
		{usage: "model set <provider/model>", summary: "set the default model (--role <role> sets one role only)"},
		{usage: "model add <name>", summary: "add a provider (--type, --base-url, --key-env)"},
		{names: []string{"prd"}, usage: "prd", summary: "render the reviewed PRD in your terminal", flags: flagSpec{}, maxPositional: 2, run: cmdPRD, inHelp: true},
		{names: []string{"explain"}, usage: "explain", summary: "show the Current Understanding", flags: jsonFlag, maxPositional: 1, run: cmdExplain, inHelp: true},
		{names: []string{"export"}, usage: "export [kind]", summary: "export an artifact as markdown", flags: flagSpec{}, maxPositional: 2, run: cmdExport, inHelp: true},
		{names: []string{"advance"}, usage: "advance", summary: "run Design → Execution Plan (M2)", flags: flagSpec{}, maxPositional: 1, run: cmdAdvance, inHelp: true},
		{names: []string{"plan"}, usage: "plan", summary: "render the execution plan", flags: flagSpec{"--json": flagBool}, rejectsJSON: true, maxPositional: 1, run: cmdPlan, inHelp: true},
		{names: []string{"tasks"}, usage: "tasks", summary: "list the ordered plan tasks", flags: jsonFlag, maxPositional: 1, run: cmdTasks, inHelp: true},
		{names: []string{"why"}, usage: "why <type> <id>", summary: "show the provenance chain", flags: jsonFlag, maxPositional: 3, run: cmdWhy, inHelp: true},
		{names: []string{"impact"}, usage: "impact <type> <id>", summary: "show dependents + staleness", flags: jsonFlag, maxPositional: 3, run: cmdImpact, inHelp: true},
		{names: []string{"trace"}, usage: "trace <type> <id>", summary: "show provenance + dependents", flags: jsonFlag, maxPositional: 3, run: cmdTrace, inHelp: true},
		{names: []string{"diff"}, usage: "diff", summary: "structured requirement/decision diff", flags: jsonFlag, maxPositional: 1, run: cmdDiff, inHelp: true},
		{names: []string{"execute"}, usage: "execute", summary: "run the next ready task → change set", flags: flagSpec{}, maxPositional: 1, run: cmdExecute, inHelp: true},
		{names: []string{"retry"}, usage: "retry [<initiative>] [--model <provider/model>]", summary: "re-run a failed initiative", flags: flagSpec{"--model": flagValue}, maxPositional: 1, run: cmdRetry, inHelp: true},
		{names: []string{"changes"}, usage: "changes", summary: "list change sets", flags: jsonFlag, maxPositional: 1, run: cmdChanges, inHelp: true},
		{names: []string{"change"}, usage: "change <id>", summary: "show a change set", flags: jsonFlag, maxPositional: 1, run: cmdChange, inHelp: true},
		{names: []string{"apply"}, usage: "apply <id>", summary: "apply an approved change set", flags: flagSpec{}, maxPositional: 1, run: cmdApply, inHelp: true},
		{names: []string{"revert"}, usage: "revert <id>", summary: "roll back a change set", flags: flagSpec{}, maxPositional: 1, run: cmdRevert, inHelp: true},
		{names: []string{"review"}, usage: "review <id> <verdict>", summary: "approve|reject|changes_requested", flags: flagSpec{}, maxPositional: 2, run: cmdReview, inHelp: true},
		{names: []string{"verify"}, usage: "verify [<req>]", summary: "run the verification gate; --pass|--fail|--waive [--note <text>] record human evidence", flags: flagSpec{"--pass": flagBool, "--fail": flagBool, "--waive": flagBool, "--note": flagValue, "--json": flagBool}, maxPositional: 1, run: cmdVerify, inHelp: true},
		{names: []string{"next"}, usage: "next [--until-human]", summary: "run the next automatable step (--until-human: keep going until a human decision)", flags: nextFlags, run: cmdNext, inHelp: true},
		{names: []string{"risk"}, usage: "risk", summary: "show the deterministic risk profile", flags: jsonFlag, maxPositional: 1, run: cmdRisk, inHelp: true},
		{names: []string{"executors"}, usage: "executors", summary: "list executors + capabilities", flags: jsonFlag, subs: map[string]flagSpec{
			"setup": {"--kind": flagValue, "--image": flagValue, "--model": flagValue, "--yes": flagBool, "--smoke": flagBool},
		}, subsAreRequired: true, run: cmdExecutors, inHelp: true},
		{usage: "executors setup", summary: "wire a coding agent (hermes|omp)", inHelp: true},
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
			a, err := c.parse(cmd, rest)
			if err != nil {
				return err
			}
			return c.run(a, stdout, stderr)
		}
	}
	return fmt.Errorf("unknown command %q (try `idea help`)", cmd)
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

func sortedKeys(m map[string]flagSpec) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// cmdDev starts the daemon and serves the web UI.
func cmdDev(_ cmdArgs, _, stderr io.Writer) error {
	fmt.Fprintln(stderr, muted("starting daemon (web UI served at http://127.0.0.1:8787)"))
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
		return fmt.Errorf("unknown --format %q (use text or md)", format)
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
			fmt.Fprintln(stdout, ok("daemon: running"))
			return nil
		}
		return fmt.Errorf("daemon is not running (it auto-starts on the first command that needs it)")
	case "stop":
		return stopDaemon()
	}
	log := newDaemonLogger(stderr)
	writePID()
	defer removePID()
	return daemon.Run(daemonAddr(), daemon.DefaultStateRoot(), log)
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
	return slog.New(slog.NewTextHandler(io.MultiWriter(stderr, f), nil))
}

func writePID() {
	_ = os.MkdirAll(daemon.DefaultStateRoot(), 0o700)
	_ = os.WriteFile(pidPath(), []byte(strconv.Itoa(os.Getpid())), 0o600)
}

func removePID() { _ = os.Remove(pidPath()) }

func stopDaemon() error {
	data, err := os.ReadFile(pidPath())
	if err != nil {
		return fmt.Errorf("no daemon pidfile; is the daemon running?")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return err
	}
	return syscall.Kill(pid, syscall.SIGTERM)
}

// ---------- init / new ----------

func cmdInit(a cmdArgs, stdout io.Writer) error {
	// `idea init /some/path` used to initialise the current directory and report
	// success, so a user watched it create a project somewhere they did not ask
	// for (DX audit). The argument reads as intent, so it is honoured.
	if len(a.positional) > 1 {
		return fmt.Errorf("usage: idea init [<dir>]")
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
			return fmt.Errorf("cannot initialize %s: not a directory", dir)
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
			return fmt.Errorf("an idea is required with --doc (e.g. idea new \"<idea>\" --doc <path>)")
		}
		if !colorEnabled {
			return fmt.Errorf("usage: idea new \"<idea>\" [--doc <path> …]")
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
			return fmt.Errorf("document %q exceeds the %d MiB limit", path, maxDocUploadBytes>>20)
		}
		if len(data) == 0 {
			return fmt.Errorf("document %q is empty", path)
		}
		if docparse.Sniff(path, data) != "text" {
			return fmt.Errorf("--doc supports text files only (%s); use `idea docs add` for PDF/DOCX/images", path)
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
	fmt.Fprintln(stdout, muted("watch progress: ")+bold("idea watch"))
	return nil
}

// connect ensures the daemon is running and returns a client + the cwd project.
func connect() (*client, string, error) {
	if err := ensureDaemon(); err != nil {
		return nil, "", err
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
			return fmt.Errorf("no idea-harness project in %s (run `idea init` to create one)", cwd)
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
			return fmt.Errorf("-n needs a positive number of lines, got %q", v)
		}
		n = parsed
	}
	if arg := a.arg(0); arg != "" {
		parsed, err := strconv.Atoi(arg)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("idea logs takes a positive line count, got %q", arg)
		}
		n = parsed
	}
	if len(a.positional) > 1 {
		return fmt.Errorf("usage: idea logs [-f] [-n <lines>] [<lines>]")
	}
	if follow {
		return followLogs(stdout)
	}

	data, err := os.ReadFile(logPath())
	if err != nil {
		return fmt.Errorf("no daemon log at %s yet — run `idea daemon` or any command first", logPath())
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
		return fmt.Errorf("no daemon log at %s yet", logPath())
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
	return nil
}

func cmdDoctor(stdout io.Writer) error {
	fmt.Fprintln(stdout, muted("idea ")+buildinfo.Get().String())
	daemonUp := healthy()
	configValid := true
	if daemonUp {
		fmt.Fprintln(stdout, ok("✓ daemon")+" "+muted("running"))
	} else {
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
		return nil
	}

	// doctor is what the README names as the post-install check, so it has to
	// catch a config that parses but cannot work -- a default_model whose
	// provider does not exist used to pass here and fail at the first run.
	if cfg, err := config.Load(filepath.Join(cwd, config.Dir)); err != nil {
		configValid = false
		fmt.Fprintf(stdout, "%s %v\n", errText("✗ config:"), err)
	} else if problems := cfg.ProblemsWithEnv(config.LoadDotEnv(cwd)); config.HasErrors(problems) {
		configValid = false
		fmt.Fprintln(stdout, errText("✗ config:"))
		renderConfigProblems(problems, stdout)
	} else if len(problems) > 0 {
		renderConfigProblems(problems, stdout)
	} else {
		fmt.Fprintln(stdout, ok("✓ config")+" "+muted("valid"))
	}

	// Demo mode: no model configured means synthetic output.
	if daemonUp {
		if c, err := newClient(); err == nil {
			if projectID, perr := c.projectIDForCwd(); perr == nil {
				if isDemoMode(c, projectID) {
					fmt.Fprintln(stdout, warn("demo mode — no model configured; run `idea model set <provider>/<model>` for real output."))
				}
			}
		}
	}

	// Product-graph edge audit (§M2 tail): untraced tasks/objects + cycles.
	graphOK := true
	if daemonUp {
		if c, err := newClient(); err == nil {
			if projectID, perr := c.projectIDForCwd(); perr == nil {
				// The graph audit is one of doctor's checks, not its purpose:
				// in a project with no initiative there is no graph to audit,
				// and doctor still has to report daemon and config health. So a
				// missing initiative skips the check rather than failing the
				// command -- this is the health command someone runs when
				// things are already wrong.
				if iniID, ierr := resolveInitiative(c, projectID, nil); ierr != nil {
					fmt.Fprintln(stdout, muted("• graph audit: no initiative yet — nothing to audit"))
				} else {
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

	fmt.Fprintln(stdout)
	if daemonUp && configValid && graphOK {
		fmt.Fprintln(stdout, ok("all checks passed"))
		return nil
	}
	// A failing check has to fail the command. doctor is what README names as
	// the post-install verification step, and a script that runs it needs the
	// exit code, not the prose. A daemon that has not started yet is not a
	// failure -- it auto-starts on first use.
	if !configValid {
		return fmt.Errorf("config problems above must be fixed")
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
