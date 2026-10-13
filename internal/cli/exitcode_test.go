package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// runMain is run() for the process-level contract: the exit code and what
// reached each stream, which only Main produces.
func runMain(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	colorEnabled = false
	var out, errBuf bytes.Buffer
	code = Main(args, &out, &errBuf)
	return code, out.String(), errBuf.String()
}

// decodeErrorObject parses the --json error envelope, failing on anything
// else -- including trailing data, which is what a second document would be.
func decodeErrorObject(t *testing.T, s string) errorReport {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(s))
	var env struct {
		Error *struct {
			Kind       errKind `json:"kind"`
			Message    string  `json:"message"`
			Hint       string  `json:"hint"`
			ExitCode   int     `json:"exit_code"`
			HTTPStatus int     `json:"http_status"`
		} `json:"error"`
	}
	if err := dec.Decode(&env); err != nil {
		t.Fatalf("not a JSON error object: %v\n%s", err, s)
	}
	if dec.More() {
		t.Fatalf("more than one JSON document:\n%s", s)
	}
	if env.Error == nil {
		t.Fatalf("JSON has no \"error\" object:\n%s", s)
	}
	e := env.Error
	return errorReport{Kind: e.Kind, Message: e.Message, Hint: e.Hint, ExitCode: e.ExitCode, HTTPStatus: e.HTTPStatus}
}

// The numbers are the public contract: a script written against v0.7 must
// keep working. This pins every one, so renumbering a class fails here rather
// than in someone's CI.
func TestExitCodesArePinned(t *testing.T) {
	want := map[errKind]int{
		kindError:             1,
		kindUsage:             2,
		kindNotFound:          3,
		kindConflict:          4,
		kindGateUnmet:         5,
		kindDaemonUnavailable: 6,
		kindUnauthorized:      7,
		kindConfigInvalid:     8,
		kindRunFailed:         9,
	}
	for k, code := range want {
		if got := classOf(k).code; got != code {
			t.Errorf("kind %q exits %d, want %d -- exit codes are a contract and are never renumbered", k, got, code)
		}
	}
	// And the converse: a class added to the table without a pin here has
	// not been thought about as a contract yet.
	for _, c := range exitClasses {
		if c.kind == "" {
			continue
		}
		if _, ok := want[c.kind]; !ok {
			t.Errorf("exit class %d (%q) is not pinned in this test", c.code, c.kind)
		}
	}
}

func TestExitClassesAreWellFormed(t *testing.T) {
	codes, kinds := map[int]bool{}, map[errKind]bool{}
	for i, c := range exitClasses {
		if c.code != i {
			t.Errorf("exitClasses[%d] has code %d; keep the table in code order with no gaps", i, c.code)
		}
		if codes[c.code] || (c.kind != "" && kinds[c.kind]) {
			t.Errorf("duplicate exit class %d / %q", c.code, c.kind)
		}
		codes[c.code], kinds[c.kind] = true, true
		if c.meaning == "" {
			t.Errorf("exit class %d has no meaning; docs/cli.md renders it", c.code)
		}
	}
	if exitClasses[0].code != 0 || exitClasses[0].kind != "" {
		t.Error("exitClasses[0] must be success, with no kind")
	}
}

func TestClassify(t *testing.T) {
	refused := dialRefused(t)
	for _, tc := range []struct {
		name       string
		err        error
		wantKind   errKind
		wantStatus int
	}{
		{"plain error", errors.New("boom"), kindError, 0},
		{"400", &httpError{Status: 400, Message: "answer is required"}, kindUsage, 400},
		{"401", &httpError{Status: 401, Message: "unauthorized"}, kindUnauthorized, 401},
		{"403", &httpError{Status: 403, Message: "invalid origin"}, kindUnauthorized, 403},
		{"404", &httpError{Status: 404, Message: `no change set matching "CHG-9"`}, kindNotFound, 404},
		{"405", &httpError{Status: 405, Message: "method not allowed"}, kindUsage, 405},
		{"409", &httpError{Status: 409, Message: "already approved"}, kindConflict, 409},
		{"413", &httpError{Status: 413, Message: "too large"}, kindUsage, 413},
		{"422", &httpError{Status: 422, Message: "harness.yaml: bad"}, kindConfigInvalid, 422},
		{"500", &httpError{Status: 500, Message: "internal error (correlation id: x)"}, kindError, 500},
		{"wrapped 404", fmt.Errorf("pull failed: %w", &httpError{Status: 404, Message: "x"}), kindNotFound, 404},
		{"explicit tag", usageErrorf("usage: idea x"), kindUsage, 0},
		{"tag over status", withKind(kindRunFailed, &httpError{Status: 404, Message: "x"}), kindRunFailed, 404},
		{"config parse error", &config.ParseError{File: "harness.yaml", Msg: "bad"}, kindConfigInvalid, 0},
		{"connection refused", refused, kindDaemonUnavailable, 0},
		{"timeout", timeoutErr{}, kindError, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := classify(tc.err)
			if r.Kind != tc.wantKind {
				t.Errorf("kind = %q, want %q", r.Kind, tc.wantKind)
			}
			if r.ExitCode != classOf(tc.wantKind).code {
				t.Errorf("exit code = %d, want %d", r.ExitCode, classOf(tc.wantKind).code)
			}
			if r.HTTPStatus != tc.wantStatus {
				t.Errorf("http_status = %d, want %d", r.HTTPStatus, tc.wantStatus)
			}
			if r.Message != tc.err.Error() {
				t.Errorf("message = %q; classification must not alter the message (%q)", r.Message, tc.err.Error())
			}
		})
	}
}

// The innermost tag wins: it was placed nearest the cause, and a caller
// wrapping it generically (connect's daemon_unavailable) must not override a
// more specific one.
func TestWithKindKeepsTheInnerTag(t *testing.T) {
	inner := errorf(kindUnauthorized, "token rejected")
	if got := classify(withKind(kindDaemonUnavailable, inner)).Kind; got != kindUnauthorized {
		t.Fatalf("kind = %q, want the inner %q", got, kindUnauthorized)
	}
	if withKind(kindUsage, nil) != nil {
		t.Fatal("withKind(nil) must stay nil so it can wrap a call's result")
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// dialRefused produces a real connection-refused error from the HTTP client,
// rather than a hand-built one that encodes a belief about its shape.
func dialRefused(t *testing.T) error {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	_, err = (&http.Client{Timeout: 2 * time.Second}).Get("http://" + addr + "/")
	if err == nil {
		t.Fatal("want a dial error from a closed port")
	}
	return err
}

// ---------- end to end, through Main ----------

func TestMainExitCodeFollowsTheDaemonStatus(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   int
	}{
		{400, 2}, {401, 7}, {403, 7}, {404, 3}, {409, 4}, {422, 8}, {500, 1},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			f := newFakeDaemon(t)
			f.seedInitiative()
			f.fail("GET /api/v1/projects/{projectId}/change-sets/{id}", tc.status, "daemon says no")

			code, stdout, stderr := runMain(t, "change", "CHG-1")
			if code != tc.want {
				t.Fatalf("HTTP %d exited %d, want %d (stderr %q)", tc.status, code, tc.want, stderr)
			}
			if stdout != "" {
				t.Errorf("a human-mode failure wrote to stdout: %q", stdout)
			}
			if !strings.HasPrefix(stderr, "idea: daemon says no\n") {
				t.Errorf("stderr = %q; the message must reach the user unchanged", stderr)
			}
		})
	}
}

func TestMainJSONErrorObjectGoesToStdout(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.fail("GET /api/v1/projects/{projectId}/change-sets/{id}", 404, `no change set matching "CHG-99"`)

	code, stdout, stderr := runMain(t, "change", "CHG-99", "--json")
	if code != 3 {
		t.Fatalf("exit %d, want 3", code)
	}
	if stderr != "" {
		t.Errorf("--json failure also wrote to stderr: %q", stderr)
	}
	r := decodeErrorObject(t, stdout)
	if r.Kind != kindNotFound || r.ExitCode != 3 || r.HTTPStatus != 404 {
		t.Errorf("error object = %+v", r)
	}
	if !strings.Contains(r.Message, "CHG-99") {
		t.Errorf("message lost the reference: %q", r.Message)
	}
	if r.Hint == "" {
		t.Error("not_found carries a default hint; it is missing")
	}
}

// A usage failure is reported before any command runs, so the JSON object has
// to come from argv, not from a parsed --json flag.
func TestMainUsageErrors(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()

	code, _, stderr := runMain(t, "tasks", "--nope")
	if code != 2 {
		t.Fatalf("unknown flag exited %d, want 2", code)
	}
	if !strings.Contains(stderr, "hint: `idea tasks --help`") {
		t.Errorf("a usage error should point at the command's own help, got %q", stderr)
	}

	code, stdout, _ := runMain(t, "tasks", "--nope", "--json")
	if code != 2 {
		t.Fatalf("unknown flag under --json exited %d, want 2", code)
	}
	if r := decodeErrorObject(t, stdout); r.Kind != kindUsage || r.HTTPStatus != 0 {
		t.Errorf("error object = %+v; want kind usage and no http_status", r)
	}

	if code, _, _ := runMain(t, "frobnicate"); code != 2 {
		t.Errorf("unknown command exited %d, want 2", code)
	}
	// After "--", --json is a positional, not a request for JSON.
	if code, stdout, _ := runMain(t, "frobnicate", "--", "--json"); code != 2 || stdout != "" {
		t.Errorf("`--json` after `--` was read as a flag: exit %d, stdout %q", code, stdout)
	}
}

// verify --json prints its payload and then fails over an unmet gate (H10).
// The error object must not land on stdout as a second document: stdout under
// --json is always exactly one JSON value.
func TestMainVerifyJSONKeepsStdoutOneDocument(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.verifyCoverage = map[string]any{
		"status":   "in_progress",
		"coverage": []map[string]any{{"code": "REQ-1", "pass": 0, "total": 1, "needs_manual": 0}},
	}

	code, stdout, stderr := runMain(t, "verify", "--json")
	if code != 5 {
		t.Fatalf("unmet gate exited %d, want 5 (gate_unmet)", code)
	}
	dec := json.NewDecoder(strings.NewReader(stdout))
	var payload map[string]any
	if err := dec.Decode(&payload); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if dec.More() {
		t.Fatalf("stdout holds more than one JSON document:\n%s", stdout)
	}
	if _, ok := payload["coverage"]; !ok {
		t.Fatalf("stdout should be the coverage payload, got %v", payload)
	}
	if r := decodeErrorObject(t, stderr); r.Kind != kindGateUnmet || !strings.Contains(r.Message, "REQ-1") {
		t.Errorf("stderr error object = %+v", r)
	}

	// The human form exits with the same code.
	if code, _, _ := runMain(t, "verify"); code != 5 {
		t.Errorf("human verify on an unmet gate exited %d, want 5", code)
	}
}

func TestMainSuccessExitsZeroAndAddsNothing(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	code, stdout, stderr := runMain(t, "tasks", "--json")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout, `"error"`) || !json.Valid([]byte(stdout)) {
		t.Fatalf("stdout = %q", stdout)
	}
}

func TestMainDaemonUnavailable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	t.Setenv("IDEA_HARNESS_ADDR", addr)
	t.Setenv("IDEA_HARNESS_STATE", t.TempDir())
	orig := daemonExecutable
	daemonExecutable = func() (string, error) { return "", errors.New("no executable in this test") }
	t.Cleanup(func() { daemonExecutable = orig })

	code, stdout, _ := runMain(t, "tasks", "--json")
	if code != 6 {
		t.Fatalf("exit %d, want 6 (daemon_unavailable)", code)
	}
	if r := decodeErrorObject(t, stdout); r.Kind != kindDaemonUnavailable {
		t.Errorf("kind = %q", r.Kind)
	}
}

func TestMainFailedInitiativeIsRunFailed(t *testing.T) {
	f := newFakeDaemon(t)
	f.initiatives = []map[string]any{
		{"id": "ini-1", "title": "Billing", "status": "failed", "failure_reason": "provider returned 401"},
	}
	f.seedInitiative()
	code, _, stderr := runMain(t, "status")
	if code != 9 {
		t.Fatalf("status over a failed initiative exited %d, want 9 (run_failed); stderr %q", code, stderr)
	}
}

// docs/cli.md carries the table make docs generates from exitClasses. This is
// the same comparison `make docs-check` makes, here so `go test` catches it
// too.
func TestExitCodeTableIsDocumented(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok || !filepath.IsAbs(file) {
		t.Skip("cannot locate the repository from this test binary")
	}
	doc, err := os.ReadFile(filepath.Join(filepath.Dir(file), "..", "..", "docs", "cli.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(doc), ExitCodeTable()) {
		t.Fatal("docs/cli.md's exit-code table differs from internal/cli/exitcode.go; run `make docs`")
	}
}
