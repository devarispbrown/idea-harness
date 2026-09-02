package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/idea-harness/idea-harness/internal/config"
	"github.com/idea-harness/idea-harness/internal/daemon"
	"github.com/idea-harness/idea-harness/internal/docparse"
)

// jsonSupport records how a command answers `--json`. It is documentation, not
// enforcement — the enforcement lives in each command's flag spec (flags.go) —
// but it is kept next to the dispatch entry so the generated reference in
// docs/cli.md and the code move together.
type jsonSupport int

const (
	jsonNone     jsonSupport = iota // not a state-returning command
	jsonYes                         // --json prints the response as JSON
	jsonNoFlags                     // takes no flags at all; any flag is an error
	jsonRejected                    // --json is rejected with a pointer elsewhere
)

func (j jsonSupport) doc() string {
	switch j {
	case jsonYes:
		return "yes"
	case jsonNoFlags:
		return "takes no flags"
	case jsonRejected:
		return "rejected — use `idea tasks --json`"
	default:
		return "—"
	}
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
	json    jsonSupport
	// run is nil for documentation-only rows.
	run func(args []string, stdout, stderr io.Writer) error
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
		{names: []string{"init"}, usage: "init", summary: "initialize a project in the current directory", run: func(_ []string, stdout, _ io.Writer) error { return cmdInit(stdout) }, inHelp: true},
		{names: []string{"new"}, usage: `new "<idea>"`, summary: "create an initiative and start the workflow", run: cmdNew, inHelp: true},
		{names: []string{"status"}, usage: "status", summary: "show initiative + current work", json: jsonYes, run: cmdStatus, inHelp: true},
		{names: []string{"changed"}, usage: "changed", summary: "show What Changed since last look", json: jsonYes, run: cmdChanged, inHelp: true},
		{names: []string{"decisions"}, usage: "decisions", summary: "list open/blocked decisions", json: jsonYes, run: cmdDecisions, inHelp: true},
		{names: []string{"reviews"}, usage: "reviews", summary: "list review findings", json: jsonYes, run: cmdReviews, inHelp: true},
		{names: []string{"model"}, usage: "model", summary: "list/set model providers (Gemini, OpenAI, …)", run: cmdModel, inHelp: true},
		{usage: "model ls <provider>", summary: "list a provider's models live (falls back to the catalog offline)"},
		{usage: "model set <provider/model>", summary: "set the default model (--role <role> sets one role only)"},
		{usage: "model add <name>", summary: "add a provider (--type, --base-url, --key-env)"},
		{names: []string{"prd"}, usage: "prd", summary: "render the reviewed PRD in your terminal", run: cmdPRD, inHelp: true},
		{names: []string{"explain"}, usage: "explain", summary: "show the Current Understanding", json: jsonYes, run: cmdExplain, inHelp: true},
		{names: []string{"export"}, usage: "export [kind]", summary: "export an artifact as markdown", run: cmdExport, inHelp: true},
		{names: []string{"advance"}, usage: "advance", summary: "run Design → Execution Plan (M2)", json: jsonNoFlags, run: cmdAdvance, inHelp: true},
		{names: []string{"plan"}, usage: "plan", summary: "render the execution plan", json: jsonRejected, run: cmdPlan, inHelp: true},
		{names: []string{"tasks"}, usage: "tasks", summary: "list the ordered plan tasks", json: jsonYes, run: cmdTasks, inHelp: true},
		{names: []string{"why"}, usage: "why <type> <id>", summary: "show the provenance chain", json: jsonYes, run: cmdWhy, inHelp: true},
		{names: []string{"impact"}, usage: "impact <type> <id>", summary: "show dependents + staleness", json: jsonYes, run: cmdImpact, inHelp: true},
		{names: []string{"trace"}, usage: "trace <type> <id>", summary: "show provenance + dependents", json: jsonYes, run: cmdTrace, inHelp: true},
		{names: []string{"diff"}, usage: "diff", summary: "structured requirement/decision diff", json: jsonYes, run: cmdDiff, inHelp: true},
		{names: []string{"execute"}, usage: "execute", summary: "run the next ready task → change set", json: jsonNoFlags, run: cmdExecute, inHelp: true},
		{names: []string{"changes"}, usage: "changes", summary: "list change sets", json: jsonYes, run: cmdChanges, inHelp: true},
		{names: []string{"change"}, usage: "change <id>", summary: "show a change set", json: jsonYes, run: cmdChange, inHelp: true},
		{names: []string{"apply"}, usage: "apply <id>", summary: "apply an approved change set", run: cmdApply, inHelp: true},
		{names: []string{"revert"}, usage: "revert <id>", summary: "roll back a change set", run: cmdRevert, inHelp: true},
		{names: []string{"review"}, usage: "review <id> <verdict>", summary: "approve|reject|changes_requested", run: cmdReview, inHelp: true},
		{names: []string{"verify"}, usage: "verify [<req>]", summary: "run the verification gate; --pass --note <text> record human evidence", json: jsonYes, run: cmdVerify, inHelp: true},
		{names: []string{"next"}, usage: "next [--until-human]", summary: "run the next automatable step (--until-human: keep going until a human decision)", json: jsonNoFlags, run: cmdNext, inHelp: true},
		{names: []string{"risk"}, usage: "risk", summary: "show the deterministic risk profile", json: jsonYes, run: cmdRisk, inHelp: true},
		{names: []string{"executors"}, usage: "executors", summary: "list executors + capabilities", json: jsonYes, run: cmdExecutors, inHelp: true},
		{usage: "executors setup", summary: "wire a coding agent (hermes|omp)", inHelp: true},
		{names: []string{"watch"}, usage: "watch", summary: "watch a run live", json: jsonNoFlags, run: cmdWatch, inHelp: true},
		{names: []string{"cancel"}, usage: "cancel <op-id>", summary: "interrupt a running operation", run: cmdCancel, inHelp: true},
		{names: []string{"config"}, usage: "config", summary: "show or validate .idea-harness config", run: cmdConfig, inHelp: true},
		{usage: "config validate", summary: "validate .idea-harness/harness.yaml (non-zero exit when invalid)"},
		{names: []string{"doctor"}, usage: "doctor", summary: "check daemon/config/state health", run: func(_ []string, stdout, _ io.Writer) error { return cmdDoctor(stdout) }, inHelp: true},
		{names: []string{"docs"}, usage: "docs", summary: "list uploaded context docs", json: jsonYes, run: cmdDocs, inHelp: true},
		{usage: "docs add <path>", summary: "upload a context doc (--kind, --title)", inHelp: true},
		{names: []string{"logs"}, usage: "logs [-f] [n]", summary: "show (or follow) the daemon log", run: func(args []string, stdout, _ io.Writer) error { return cmdLogs(args, stdout) }, inHelp: true},
		{names: []string{"daemon"}, usage: "daemon", summary: "run the daemon in the foreground", run: func(args []string, _, stderr io.Writer) error { return runDaemon(args, stderr) }, inHelp: true},
		{usage: "daemon status|stop", summary: "manage the managed daemon", inHelp: true},
		{names: []string{"dev"}, usage: "dev", summary: "start the daemon and serve the web UI", run: cmdDev, inHelp: true},
		{names: []string{"help", "--help", "-h"}, usage: "help [--format=md]", summary: "show this command list (--format=md emits the docs/cli.md table)", run: cmdHelp, inHelp: true},
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
			if name == cmd {
				return c.run(rest, stdout, stderr)
			}
		}
	}
	return fmt.Errorf("unknown command %q (try `idea help`)", cmd)
}

// cmdDev starts the daemon and serves the web UI.
func cmdDev(_ []string, _, stderr io.Writer) error {
	fmt.Fprintln(stderr, muted("starting daemon (web UI served at http://127.0.0.1:8787)"))
	return runDaemon(nil, stderr)
}

// cmdHelp prints the command list. The human form goes to stderr exactly as it
// always has; `--format=md` writes the generated docs/cli.md table to the
// caller's stdout, so a drift check can capture it.
func cmdHelp(args []string, stdout, stderr io.Writer) error {
	_, flags, err := parseArgs(flagSpec{"--format": flagValue}, args)
	if err != nil {
		return err
	}
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
	b.WriteString("| Command | What it does | `--json` |\n")
	b.WriteString("| --- | --- | --- |\n")
	for _, c := range commands() {
		fmt.Fprintf(&b, "| `idea %s` | %s | %s |\n",
			escapeTableCell(c.usage), escapeTableCell(c.summary), c.json.doc())
	}
	_, err := io.WriteString(w, b.String())
	return err
}

func escapeTableCell(s string) string { return strings.ReplaceAll(s, "|", `\|`) }

// ---------- daemon ----------

func runDaemon(rest []string, stderr io.Writer) error {
	if len(rest) > 0 {
		switch rest[0] {
		case "status":
			if healthy() {
				fmt.Println(ok("daemon: running"))
				return nil
			}
			fmt.Println(muted("daemon: not running"))
			return nil
		case "stop":
			return stopDaemon()
		}
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

func cmdInit(stdout io.Writer) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
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
	fmt.Fprintln(stdout, muted("next: ") + bold("idea new") + muted(" \"<describe your idea>\""))
	return nil
}

// newDocInput is one context doc read from a --doc flag.
type newDocInput struct {
	Content string `json:"content"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
}

func cmdNew(args []string, stdout, stderr io.Writer) error {
	// Parse --doc flags (before or after the idea); remaining args are the idea.
	var ideaParts []string
	var docPaths []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--doc":
			if i+1 >= len(args) {
				return fmt.Errorf("--doc requires a path")
			}
			i++
			docPaths = append(docPaths, args[i])
		case strings.HasPrefix(a, "--doc="):
			docPaths = append(docPaths, strings.TrimPrefix(a, "--doc="))
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q", a)
		default:
			ideaParts = append(ideaParts, a)
		}
	}
	idea := strings.Join(ideaParts, " ")
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
	fmt.Fprintln(stdout, muted("watch progress: ") + bold("idea watch"))
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

func cmdConfig(args []string, stdout, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "validate" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
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
func cmdLogs(args []string, stdout io.Writer) error {
	n := 50
	follow := false
	for _, a := range args {
		switch a {
		case "-f", "--follow":
			follow = true
		default:
			if v, err := strconv.Atoi(a); err == nil && v > 0 {
				n = v
			}
		}
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

func cmdDoctor(stdout io.Writer) error {
	daemonUp := healthy()
	configValid := true
	if daemonUp {
		fmt.Fprintln(stdout, ok("✓ daemon")+" "+muted("running"))
	} else {
		fmt.Fprintln(stdout, muted("• daemon: not running (auto-starts on first command)"))
	}
	cwd, _ := os.Getwd()
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
		fmt.Fprintln(stdout, ok("✓ config")+" "+muted("valid (with warnings)"))
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
				var findings []cliFinding
				if derr := c.do("GET", "/api/v1/projects/"+projectID+"/doctor", nil, &findings); derr != nil {
					graphOK = false
					fmt.Fprintf(stdout, "%s %v\n", errText("✗ graph audit:"), derr)
				} else if len(findings) > 0 {
					graphOK = false
					printDoctorFindings(stdout, findings)
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
