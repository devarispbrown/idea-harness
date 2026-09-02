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
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDaemon(t)
			f.seedInitiative()
			f.json("GET /api/v1/projects/{projectId}/decisions", []map[string]any{})
			f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/next-move", map[string]any{})

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
	for _, args := range [][]string{
		{"tasks", "--json"},
		{"changes", "--json"},
		{"risk", "--json"},
		{"explain", "--json"},
		{"diff", "--json"},
	} {
		t.Run(args[0], func(t *testing.T) {
			f := newFakeDaemon(t)
			f.seedInitiative()
			f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/diff", map[string]any{})

			_, _, _ = run(t, args...)

			if f.asked("--json") {
				t.Fatalf("idea %s sent the flag to the daemon as an initiative reference: %v",
					strings.Join(args, " "), f.requests)
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
	f.json("GET /api/v1/projects/{projectId}/requirements", []map[string]any{
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

	_, _, err := run(t, "tasks", "--nope")
	if err == nil {
		t.Fatal("idea tasks --nope: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "--nope") {
		t.Fatalf("error should name the rejected flag, got: %v", err)
	}
	if f.asked("/tasks") {
		t.Fatalf("unknown flag still reached the daemon: %v", f.requests)
	}
}
