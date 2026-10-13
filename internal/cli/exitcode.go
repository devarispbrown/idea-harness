package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// The CLI's exit-code taxonomy (deferred.md, audit 8 / E2).
//
// Every failure used to share os.Exit(1), so a script could tell "it worked"
// from "it did not" and nothing more: a mistyped change-set id, a daemon that
// would not start, a rejected token and an unmet verification gate all read the
// same. And `--json` emitted no error object at all, so the one output form
// meant for machines carried the least information about a failure.
//
// The design keeps the whole classification in this file:
//
//   - Commands return ordinary errors. Most need nothing more, because the
//     daemon's HTTP status already says what kind of failure it was and
//     classify reads it off the *httpError the client returns.
//   - The handful of failures the CLI decides on its own (an argument it
//     rejects, an unmet gate, a daemon it cannot reach) are tagged where they
//     are raised with withKind / errorf.
//   - Main is the single exit point: it classifies, renders the error once
//     (human or JSON) and returns the code. cmd/idea/main.go only calls
//     os.Exit on what Main returns.
//
// So a new command gets a sensible code for free, and the table below is the
// only place a code is ever spelled.

// errKind is the stable, machine-readable name of a failure class. It is the
// `kind` field of the --json error object, so values are a public contract:
// add new ones, never rename or renumber.
type errKind string

const (
	kindError             errKind = "error"
	kindUsage             errKind = "usage"
	kindNotFound          errKind = "not_found"
	kindConflict          errKind = "conflict"
	kindGateUnmet         errKind = "gate_unmet"
	kindDaemonUnavailable errKind = "daemon_unavailable"
	kindUnauthorized      errKind = "unauthorized"
	kindConfigInvalid     errKind = "config_invalid"
	kindRunFailed         errKind = "run_failed"
)

// exitClass is one row of the taxonomy.
type exitClass struct {
	code int
	kind errKind
	// meaning is the docs/cli.md description, rendered from here so the
	// documented table cannot drift from the code.
	meaning string
	// hint is the default next step for this class, used when the error does
	// not carry a more specific one. Empty where no single move is right.
	hint string
}

// exitClasses is the taxonomy, in code order. 0 is listed for the docs table;
// classify never produces it.
var exitClasses = []exitClass{
	{0, "", "success", ""},
	{1, kindError, "any failure not classified below, including a daemon-side 500 (which carries a correlation id) and a client-side timeout", ""},
	{2, kindUsage, "the command line was wrong: unknown command, subcommand or flag, a missing or extra argument, or a request the daemon rejected as malformed (HTTP 400, 405, 413)", "`idea help` lists the commands; `idea <command> --help` shows one command's arguments and flags"},
	{3, kindNotFound, "a reference named nothing: an initiative, change set, decision, question, artifact or project that does not exist (HTTP 404)", "`idea status` lists initiatives; `idea changes`, `idea decisions` and `idea questions` list the other references"},
	{4, kindConflict, "the request is valid but the current state does not allow it — approving what is already approved, revising under an approved plan (HTTP 409)", "`idea status` shows where the initiative is; `idea next` names the move it is waiting for"},
	{5, kindGateUnmet, "a gate a script asked about is not met: `idea verify` with requirements still unverified", "`idea verify` lists what is unmet; record evidence with `idea verify <req> --pass|--fail|--waive`"},
	{6, kindDaemonUnavailable, "the daemon could not be reached, started, or is not initialized", "`idea daemon status` checks it, `idea logs` shows why it stopped, `idea doctor` diagnoses"},
	{7, kindUnauthorized, "the daemon rejected this CLI's token or origin (HTTP 401, 403)", "`idea daemon stop`; the next command starts a daemon that reads this CLI's token"},
	{8, kindConfigInvalid, "the project's configuration is invalid or incomplete: harness.yaml does not parse (HTTP 422), `idea config validate` found errors, or no model or API key is configured", "`idea config validate` lists the problems in .idea-harness/harness.yaml"},
	{9, kindRunFailed, "work the daemon ran failed or was cancelled: an operation, an initiative, or an executor smoke run. Model and provider failures surface here, with the provider's reason", "`idea status` shows where it is, `idea logs` has the full text, `idea retry` re-runs a failed initiative"},
}

func classOf(k errKind) exitClass {
	for _, c := range exitClasses {
		if c.kind == k {
			return c
		}
	}
	return exitClasses[1]
}

// cliError tags an error with its class. The message is the wrapped error's,
// unchanged, so tagging a failure never alters what a human reads.
type cliError struct {
	kind errKind
	err  error
	// hint overrides the class default with a move specific to this failure.
	hint string
}

func (e *cliError) Error() string { return e.err.Error() }
func (e *cliError) Unwrap() error { return e.err }

// withKind tags err with a class. nil stays nil, so it can wrap a call's result
// directly. An error that already carries a class keeps it: the innermost tag
// is the one placed nearest the cause.
func withKind(k errKind, err error) error {
	if err == nil {
		return nil
	}
	var ce *cliError
	if errors.As(err, &ce) {
		return err
	}
	return &cliError{kind: k, err: err}
}

// errorf is fmt.Errorf tagged with a class.
func errorf(k errKind, format string, args ...any) error {
	return &cliError{kind: k, err: fmt.Errorf(format, args...)}
}

// usageErrorf is the common case of errorf, spelled out because it is used at
// every "usage: idea …" site.
func usageErrorf(format string, args ...any) error {
	return errorf(kindUsage, format, args...)
}

// errorReport is the classified form of a failure: what Main prints and the
// code it exits with.
type errorReport struct {
	Kind       errKind `json:"kind"`
	Message    string  `json:"message"`
	Hint       string  `json:"hint,omitempty"`
	ExitCode   int     `json:"exit_code"`
	HTTPStatus int     `json:"http_status,omitempty"`
	// explicitHint records whether Hint came from the failure rather than the
	// class default. The human form prints only an explicit hint: most
	// messages already end in their own next step, and a generic one under it
	// would say the same thing twice.
	explicitHint bool
}

// classify maps any error to its report. Order matters: an explicit tag wins,
// then the daemon's HTTP status, then what the Go error itself says.
func classify(err error) errorReport {
	r := errorReport{Message: err.Error()}

	var he *httpError
	if errors.As(err, &he) {
		r.HTTPStatus = he.Status
	}

	var ce *cliError
	switch {
	case errors.As(err, &ce):
		r.Kind, r.Hint = ce.kind, ce.hint
	case he != nil:
		r.Kind = kindForStatus(he.Status)
	case isConfigParseError(err):
		r.Kind = kindConfigInvalid
	case isUnreachable(err):
		r.Kind = kindDaemonUnavailable
	default:
		r.Kind = kindError
	}

	class := classOf(r.Kind)
	r.ExitCode = class.code
	r.explicitHint = r.Hint != ""
	if r.Hint == "" {
		r.Hint = class.hint
	}
	return r
}

// kindForStatus maps the daemon's status codes, which internal/daemon/errors.go
// already assigns by cause: a store miss is 404, a *PreconditionError 409, a
// config parse failure 422, a storage fault 500.
func kindForStatus(status int) errKind {
	switch status {
	case http.StatusBadRequest, http.StatusMethodNotAllowed, http.StatusRequestEntityTooLarge:
		return kindUsage
	case http.StatusUnauthorized, http.StatusForbidden:
		return kindUnauthorized
	case http.StatusNotFound:
		return kindNotFound
	case http.StatusConflict:
		return kindConflict
	case http.StatusUnprocessableEntity:
		return kindConfigInvalid
	}
	return kindError
}

func isConfigParseError(err error) bool {
	var pe *config.ParseError
	return errors.As(err, &pe)
}

// isUnreachable reports a connection that never reached a daemon. A timeout is
// deliberately excluded: the request did reach one, which is still working
// (timeoutHint says so), and "unavailable" would send the user to restart it.
func isUnreachable(err error) bool {
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return false
	}
	var oe *net.OpError
	return errors.As(err, &oe) && oe.Op == "dial"
}

// wantsJSON reports whether the invocation asked for JSON output. Read from
// argv rather than the parsed flags because the error being reported may be
// the parse failure itself. Arguments after "--" are positionals, not flags.
func wantsJSON(args []string) bool {
	for _, a := range args {
		if a == "--" {
			return false
		}
		if a == "--json" {
			return true
		}
	}
	return false
}

// writeCounter records whether anything reached stdout, so Main can keep
// stdout to exactly one JSON document under --json.
type writeCounter struct {
	w io.Writer
	n int
}

func (c *writeCounter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += n
	return n, err
}

// Main runs the CLI and returns the process exit code. It is the single exit
// point: every failure any command returns is classified and rendered here,
// once.
//
// Where the error goes:
//
//   - Without --json: `idea: <message>` on stderr, as it always was, plus a
//     `hint:` line when the failure carries a specific next step.
//   - With --json: `{"error":{…}}` on stdout, so a script reads one stream and
//     parses one document whether the command succeeded or not. The exception
//     is a command that already printed its payload before failing — `idea
//     verify --json` prints the coverage and then exits 5 over an unmet gate.
//     A second document there would make stdout unparseable as JSON, so the
//     error object goes to stderr instead. The rule a script can rely on:
//     under --json, stdout holds exactly one JSON document.
func Main(args []string, stdout, stderr io.Writer) int {
	jsonMode := wantsJSON(args)
	out := stdout
	var counted *writeCounter
	if jsonMode {
		// Wrapping hides the *os.File from initColor, which turns styling off.
		// Correct here: JSON output must not carry ANSI escapes either.
		counted = &writeCounter{w: stdout}
		out = counted
	}

	err := Run(args, out, stderr)
	if err == nil {
		return 0
	}
	r := classify(err)

	if jsonMode {
		dst := stdout
		if counted.n > 0 {
			dst = stderr
		}
		enc := json.NewEncoder(dst)
		enc.SetIndent("", "  ")
		_ = enc.Encode(map[string]errorReport{"error": r})
		return r.ExitCode
	}

	fmt.Fprintln(stderr, "idea:", r.Message)
	if r.explicitHint && !strings.Contains(r.Message, r.Hint) {
		fmt.Fprintln(stderr, "hint:", r.Hint)
	}
	return r.ExitCode
}

// ExitCodeTable renders the taxonomy as the markdown table embedded in
// docs/cli.md. internal/docsgen splices it between its markers, and
// `make docs-check` fails when the two differ.
func ExitCodeTable() string {
	var b strings.Builder
	b.WriteString("| Code | `kind` | Meaning |\n")
	b.WriteString("| --- | --- | --- |\n")
	for _, c := range exitClasses {
		kind := "—"
		if c.kind != "" {
			kind = "`" + string(c.kind) + "`"
		}
		fmt.Fprintf(&b, "| %d | %s | %s |\n", c.code, kind, escapeTableCell(c.meaning))
	}
	return b.String()
}
