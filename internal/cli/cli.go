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

// Run dispatches the idea CLI.
func Run(args []string, stdout, stderr io.Writer) error {
	initColor(stdout)

	if len(args) == 0 {
		usage(stderr)
		return nil
	}
	cmd, rest := args[0], args[1:]

	switch cmd {
	case "daemon":
		return runDaemon(rest, stderr)
	case "dev":
		fmt.Fprintln(stderr, muted("starting daemon (web UI served at http://127.0.0.1:8787)"))
		return runDaemon(nil, stderr)
	case "init":
		return cmdInit(stdout)
	case "new":
		return cmdNew(rest, stdout, stderr)
	case "status":
		return cmdStatus(rest, stdout, stderr)
	case "changed":
		return cmdChanged(rest, stdout, stderr)
	case "decisions":
		return cmdDecisions(rest, stdout, stderr)
	case "reviews":
		return cmdReviews(rest, stdout, stderr)
	case "model":
		return cmdModel(rest, stdout, stderr)
	case "config":
		return cmdConfig(rest, stdout, stderr)
	case "prd":
		return cmdPRD(rest, stdout, stderr)
	case "explain":
		return cmdExplain(rest, stdout, stderr)
	case "export":
		return cmdExport(rest, stdout, stderr)
	case "advance":
		return cmdAdvance(rest, stdout, stderr)
	case "plan":
		return cmdPlan(rest, stdout, stderr)
	case "tasks":
		return cmdTasks(rest, stdout, stderr)
	case "why":
		return cmdWhy(rest, stdout, stderr)
	case "impact":
		return cmdImpact(rest, stdout, stderr)
	case "trace":
		return cmdTrace(rest, stdout, stderr)
	case "diff":
		return cmdDiff(rest, stdout, stderr)
	case "execute":
		return cmdExecute(rest, stdout, stderr)
	case "changes":
		return cmdChanges(rest, stdout, stderr)
	case "change":
		return cmdChange(rest, stdout, stderr)
	case "apply":
		return cmdApply(rest, stdout, stderr)
	case "revert":
		return cmdRevert(rest, stdout, stderr)
	case "review":
		return cmdReview(rest, stdout, stderr)
	case "risk":
		return cmdRisk(rest, stdout, stderr)
	case "executors":
		return cmdExecutors(rest, stdout, stderr)
	case "verify":
		return cmdVerify(rest, stdout, stderr)
	case "next":
		return cmdNext(rest, stdout, stderr)
	case "watch":
		return cmdWatch(rest, stdout, stderr)
	case "cancel":
		return cmdCancel(rest, stdout, stderr)
	case "doctor":
		return cmdDoctor(stdout)
	case "docs":
		return cmdDocs(rest, stdout, stderr)
	case "logs":
		return cmdLogs(rest, stdout)
	case "help", "--help", "-h":
		usage(stderr)
		return nil
	default:
		return fmt.Errorf("unknown command %q (try `idea help`)", cmd)
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, heading("idea-harness")+" — a local-first AI product-development control plane\n\n")
	fmt.Fprintln(w, bold("Usage:"), "idea <command>")
	fmt.Fprintln(w, bold("Commands:"))
	fmt.Fprintf(w, "  %-20s %s\n", accent("init"), "initialize a project in the current directory")
	fmt.Fprintf(w, "  %-20s %s\n", accent("new \"<idea>\""), "create an initiative and start the workflow")
	fmt.Fprintf(w, "  %-20s %s\n", accent("status"), "show initiative + current work")
	fmt.Fprintf(w, "  %-20s %s\n", accent("changed"), "show What Changed since last look")
	fmt.Fprintf(w, "  %-20s %s\n", accent("decisions"), "list open/blocked decisions")
	fmt.Fprintf(w, "  %-20s %s\n", accent("reviews"), "list review findings")
	fmt.Fprintf(w, "  %-20s %s\n", accent("model"), "list/set model providers (Gemini, OpenAI, …)")
	fmt.Fprintf(w, "  %-20s %s\n", accent("prd"), "render the reviewed PRD in your terminal")
	fmt.Fprintf(w, "  %-20s %s\n", accent("explain"), "show the Current Understanding")
	fmt.Fprintf(w, "  %-20s %s\n", accent("export [kind]"), "export an artifact as markdown")
	fmt.Fprintf(w, "  %-20s %s\n", accent("advance"), "run Design → Execution Plan (M2)")
	fmt.Fprintf(w, "  %-20s %s\n", accent("plan"), "render the execution plan")
	fmt.Fprintf(w, "  %-20s %s\n", accent("tasks"), "list the ordered plan tasks")
	fmt.Fprintf(w, "  %-20s %s\n", accent("why <type> <id>"), "show the provenance chain")
	fmt.Fprintf(w, "  %-20s %s\n", accent("impact <type> <id>"), "show dependents + staleness")
	fmt.Fprintf(w, "  %-20s %s\n", accent("trace <type> <id>"), "show provenance + dependents")
	fmt.Fprintf(w, "  %-20s %s\n", accent("diff"), "structured requirement/decision diff")
	fmt.Fprintf(w, "  %-20s %s\n", accent("execute"), "run the next ready task → change set")
	fmt.Fprintf(w, "  %-20s %s\n", accent("changes"), "list change sets")
	fmt.Fprintf(w, "  %-20s %s\n", accent("change <id>"), "show a change set")
	fmt.Fprintf(w, "  %-20s %s\n", accent("apply <id>"), "apply an approved change set")
	fmt.Fprintf(w, "  %-20s %s\n", accent("revert <id>"), "roll back a change set")
	fmt.Fprintf(w, "  %-20s %s\n", accent("review <id> <verdict>"), "approve|reject|changes_requested")
	fmt.Fprintf(w, "  %-20s %s\n", accent("risk"), "show the deterministic risk profile")
	fmt.Fprintf(w, "  %-20s %s\n", accent("executors"), "list executors + capabilities")
	fmt.Fprintf(w, "  %-20s %s\n", accent("executors setup"), "wire a coding agent (hermes|omp)")
	fmt.Fprintf(w, "  %-20s %s\n", accent("watch"), "watch a run live")
	fmt.Fprintf(w, "  %-20s %s\n", accent("cancel <op-id>"), "interrupt a running operation")
	fmt.Fprintf(w, "  %-20s %s\n", accent("config"), "show or validate .idea-harness config")
	fmt.Fprintf(w, "  %-20s %s\n", accent("doctor"), "check daemon/config/state health")
	fmt.Fprintf(w, "  %-20s %s\n", accent("docs"), "list uploaded context docs")
	fmt.Fprintf(w, "  %-20s %s\n", accent("docs add <path>"), "upload a context doc (--kind, --title)")
	fmt.Fprintf(w, "  %-20s %s\n", accent("logs [-f] [n]"), "show (or follow) the daemon log")
	fmt.Fprintf(w, "  %-20s %s\n", accent("daemon"), "run the daemon in the foreground")
	fmt.Fprintf(w, "  %-20s %s\n", accent("daemon status|stop"), "manage the managed daemon")
	fmt.Fprintf(w, "  %-20s %s\n", accent("dev"), "start the daemon and serve the web UI")
	fmt.Fprintf(w, "\n%s\n", muted("State commands support --json."))
}

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
		if _, err := config.Load(filepath.Join(cwd, config.Dir)); err != nil {
			return err
		}
		fmt.Fprintln(stdout, ok("config: valid"))
		return nil
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
	if _, err := config.Load(filepath.Join(cwd, config.Dir)); err != nil {
		configValid = false
		fmt.Fprintf(stdout, "%s %v\n", errText("✗ config:"), err)
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
