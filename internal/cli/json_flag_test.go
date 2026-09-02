package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// docs/cli.md states "State-returning commands support --json". Before this
// test existed, six of them treated the flag itself as a positional initiative
// reference (resolveInitiative returned args[0] unfiltered), so the daemon
// looked up an initiative named "--json", missed, and the CLI printed the
// store's raw error:
//
//	$ idea tasks --json
//	idea: sql: no rows in result set        (exit 1)
//
// Two of the affected commands (advance, execute) change state, so a mistyped
// flag aimed a workflow transition at an initiative that does not exist.
func TestStateCommandsEmitJSON(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"tasks", []string{"tasks", "--json"}},
		{"changes", []string{"changes", "--json"}},
		{"risk", []string{"risk", "--json"}},
		{"explain", []string{"explain", "--json"}},
		{"status", []string{"status", "--json"}},
		{"decisions", []string{"decisions", "--json"}},
		{"diff", []string{"diff", "--json"}},
		{"verify", []string{"verify", "--json"}},
		{"why", []string{"why", "requirement", "REQ-1", "--json"}},
		{"impact", []string{"impact", "requirement", "REQ-1", "--json"}},
		// trace runs two queries; as JSON they must be one document, not two
		// concatenated arrays.
		{"trace", []string{"trace", "requirement", "REQ-1", "--json"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDaemon(t)
			f.seedInitiative()
			f.seedTrace()
			f.json("GET /api/v1/projects/{projectId}/initiatives/{initiativeId}/decisions", []map[string]any{})
			f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/guidance", map[string]any{})

			stdout, _, err := run(t, tc.args...)
			if err != nil {
				t.Fatalf("idea %s: unexpected error: %v", strings.Join(tc.args, " "), err)
			}
			if !json.Valid([]byte(stdout)) {
				t.Fatalf("idea %s: stdout is not valid JSON:\n%s", strings.Join(tc.args, " "), stdout)
			}
		})
	}
}

// A flag must never be resolved as an initiative reference. This asserts the
// mechanism rather than the symptom: whatever the command does with --json, it
// must not ask the daemon about an initiative called "--json".
func TestFlagsAreNotTreatedAsInitiativeReferences(t *testing.T) {
	for _, tc := range []struct {
		args         []string
		wantEndpoint string
	}{
		{[]string{"tasks", "--json"}, "/tasks"},
		{[]string{"changes", "--json"}, "/change-sets"},
		{[]string{"risk", "--json"}, "/risk"},
		{[]string{"explain", "--json"}, "/understanding"},
		{[]string{"diff", "--json"}, "/diff"},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			f := newFakeDaemon(t)
			f.seedInitiative()

			// The command must succeed, not merely avoid the bad request: a
			// command that failed before reaching connect() would record no
			// requests at all and pass the negative check on its own.
			if _, _, err := run(t, tc.args...); err != nil {
				t.Fatalf("idea %s: %v", strings.Join(tc.args, " "), err)
			}
			if !f.asked(tc.wantEndpoint) {
				t.Fatalf("idea %s never reached %s: %v",
					strings.Join(tc.args, " "), tc.wantEndpoint, f.requests)
			}
			if f.asked("--json") {
				t.Fatalf("idea %s sent the flag to the daemon as an initiative reference: %v",
					strings.Join(tc.args, " "), f.requests)
			}
		})
	}
}

// why/impact/trace take two positionals of their own before the optional
// initiative reference (runTraceCmd consumes args[0:2] and passes args[2:]), so
// a fix that hands the whole positional slice to resolveInitiative would start
// resolving an initiative named "requirement".
func TestTraceCommandsKeepTheirPositionals(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{initiativeId}/requirements", []map[string]any{
		{"id": "req-1", "code": "REQ-1", "kind": "requirement", "title": "Bill by usage"},
	})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/why", []map[string]any{
		{"edge_type": "derives_from", "from_label": "EV-1", "to_label": "REQ-1"},
	})

	if _, _, err := run(t, "why", "requirement", "REQ-1", "--json"); err != nil {
		t.Fatalf("idea why requirement REQ-1 --json: %v", err)
	}
	if f.asked("/initiatives/requirement") || f.asked("/initiatives/--json") {
		t.Fatalf("why resolved a positional or flag as an initiative: %v", f.requests)
	}
}

// An unknown flag is a usage error, not a positional: it must fail before the
// command reaches the daemon.
func TestUnknownFlagFailsBeforeTheDaemon(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()

	// Control: the same command without the bad flag does reach the daemon, so
	// the negative assertion below means "rejected", not "never ran".
	if _, _, err := run(t, "tasks"); err != nil {
		t.Fatalf("idea tasks: %v", err)
	}
	if !f.asked("/tasks") {
		t.Fatalf("control run never reached the daemon: %v", f.requests)
	}

	before := len(f.requests)
	_, _, err := run(t, "tasks", "--nope")
	if err == nil {
		t.Fatal("idea tasks --nope: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "--nope") {
		t.Fatalf("error should name the rejected flag, got: %v", err)
	}
	if len(f.requests) != before {
		t.Fatalf("unknown flag still reached the daemon: %v", f.requests[before:])
	}
}
