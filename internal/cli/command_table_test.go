package cli

import (
	"strings"
	"testing"
)

// blockingCommands are the rows this test must not invoke. Each either blocks
// forever or takes over the terminal, so a regression would hang the suite
// instead of failing it:
//
//	daemon, dev  run daemon.Run in-process
//	new          opens an interactive form when given no idea
//	watch        runs a Bubble Tea event loop
//
// They are still covered: dispatch-level parsing is asserted directly below,
// which is where the rejection has to happen anyway.
var blockingCommands = map[string]string{
	"daemon": "runs the daemon in-process",
	"dev":    "runs the daemon in-process",
	"new":    "opens an interactive form",
	"watch":  "runs a terminal event loop",
}

// TestEveryCommandDeclaresItsFlags is the only thing standing between the table
// and a nil spec. Go permits omitting a field in a keyed struct literal, so a
// new row without `flags:` compiles clean -- and a nil spec would reject every
// flag the command legitimately accepts.
func TestEveryCommandDeclaresItsFlags(t *testing.T) {
	for _, c := range commands() {
		if c.run == nil {
			continue // documentation-only row
		}
		if c.flags == nil {
			t.Errorf("command %q declares no flag spec; use flagSpec{} if it takes none",
				c.names[0])
		}
	}
}

// TestEveryCommandRejectsAnUnknownFlag iterates the table itself, so a new
// command joins this test the moment it joins the table.
//
// The audit found ten commands silently swallowing unknown flags -- `idea status
// --jsonn` printed human output to stdout and exited 0, breaking a downstream
// jq. They were exactly the commands missing from the previous hand-written
// test table, which is why this one is generated.
func TestEveryCommandRejectsAnUnknownFlag(t *testing.T) {
	for _, c := range commands() {
		if c.run == nil {
			continue
		}
		name := c.names[0]
		t.Run(name, func(t *testing.T) {
			// Parse-level assertion: true for every command, including the ones
			// too dangerous to invoke.
			if _, err := c.parse(name, []string{"--definitely-not-a-flag"}); err == nil {
				t.Fatalf("%s --definitely-not-a-flag: parse accepted it", name)
			}
			if reason, blocked := blockingCommands[name]; blocked {
				t.Logf("not invoked end to end: %s", reason)
				return
			}
			// End-to-end: the rejection must happen before any daemon call.
			f := newFakeDaemon(t)
			f.seedInitiative()
			before := len(f.requests)
			_, _, err := run(t, name, "--definitely-not-a-flag")
			if err == nil {
				t.Fatalf("idea %s --definitely-not-a-flag: want an error, got nil", name)
			}
			if !strings.Contains(err.Error(), "--definitely-not-a-flag") {
				t.Errorf("error should name the rejected flag, got: %v", err)
			}
			if len(f.requests) != before {
				t.Errorf("unknown flag still reached the daemon: %v", f.requests[before:])
			}
		})
	}
}

// TestSubcommandFlagsAreNotShared pins the reason specs are per dispatch target
// rather than per command. A union spec would accept a flag belonging to a
// sibling subcommand -- the silent-swallow defect one level down.
func TestSubcommandFlagsAreNotShared(t *testing.T) {
	cases := []struct{ name, args, reject string }{
		{"model", "ls --key-env FOO", "--key-env"},
		{"model", "set x --type openai", "--type"},
		{"docs", "--kind prd", "--kind"},
		{"executors", "--smoke", "--smoke"},
	}
	for _, c := range cases {
		got, err := parseFor(t, c.name, strings.Fields(c.args))
		if err == nil {
			t.Errorf("idea %s %s: accepted %s (sub=%q); a sibling's flag must not be shared",
				c.name, c.args, c.reject, got.sub)
		}
	}
}

// TestUnknownSubcommandIsRejected covers two audit findings at once:
// `idea daemon stauts` used to start a second daemon in the foreground, and
// `idea config valdiate` printed the model list and exited 0.
func TestUnknownSubcommandIsRejected(t *testing.T) {
	for _, c := range []struct{ name, sub string }{
		{"daemon", "stauts"},
		{"config", "valdiate"},
	} {
		_, err := parseFor(t, c.name, []string{c.sub})
		if err == nil {
			t.Errorf("idea %s %s: accepted an unknown subcommand", c.name, c.sub)
			continue
		}
		if !strings.Contains(err.Error(), c.sub) {
			t.Errorf("idea %s %s: error should name the typo, got: %v", c.name, c.sub, err)
		}
	}
}

// TestPlanKeepsItsJSONPointer locks the message the audit's plan row exists for.
// `plan` declares --json precisely so it can refuse it with somewhere better to
// go; nothing tested that string, so deriving the docs column from the spec
// could have silently replaced it with the parser's generic "unknown flag".
func TestPlanKeepsItsJSONPointer(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	_, _, err := run(t, "plan", "--json")
	if err == nil {
		t.Fatal("idea plan --json: want an error, got nil")
	}
	if !strings.Contains(err.Error(), "idea tasks --json") {
		t.Errorf("plan --json must point somewhere better, got: %v", err)
	}
}
