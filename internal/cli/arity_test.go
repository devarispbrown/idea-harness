package cli

import (
	"strings"
	"testing"
)

// TestTerminatorDoesNotBypassSubcommandChecks pins two behaviours the previous
// round fixed and left reachable one keystroke away.
//
// The subcommand check tested the raw argv's first element, so "--" walked
// straight past it: `idea config -- validate` printed the model list and exited
// 0, and `idea daemon -- stop` started a foreground daemon. It now runs against
// the parsed positionals.
func TestTerminatorDoesNotBypassSubcommandChecks(t *testing.T) {
	for _, c := range []struct{ name, arg string }{
		{"config", "validate"},
		{"daemon", "stop"},
	} {
		if _, err := parseFor(t, c.name, []string{"--", c.arg}); err == nil {
			t.Errorf("idea %s -- %s: accepted; the terminator bypassed the subcommand check",
				c.name, c.arg)
		}
	}
}

// TestExtraPositionalsAreRejected pins the arity finding: `idea apply CHG-1
// CHG-2` applied only the first and reported success, so the user believed both
// had landed.
func TestExtraPositionalsAreRejected(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"apply", []string{"CHG-1", "CHG-2"}},
		{"revert", []string{"CHG-1", "CHG-2"}},
		{"cancel", []string{"op-1", "op-2"}},
		{"change", []string{"CHG-1", "CHG-2"}},
	} {
		_, err := parseFor(t, c.name, c.args)
		if err == nil {
			t.Errorf("idea %s %s: extra argument accepted and silently dropped",
				c.name, strings.Join(c.args, " "))
			continue
		}
		if !strings.Contains(err.Error(), "at most") {
			t.Errorf("idea %s: error should name the limit, got: %v", c.name, err)
		}
	}
}

// TestBoundedCommandsStillAcceptTheirArguments keeps the bound from being a
// blanket rejection -- prd takes [kind] [initiative], which a first attempt at
// this got wrong and an existing test caught.
func TestBoundedCommandsStillAcceptTheirArguments(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"apply", []string{"CHG-1"}},
		{"review", []string{"CHG-1", "approve"}},
		{"prd", []string{"prd", "ini-1"}},
		{"why", []string{"requirement", "REQ-1"}},
		{"verify", []string{"REQ-1"}},
	} {
		if _, err := parseFor(t, c.name, c.args); err != nil {
			t.Errorf("idea %s %s: rejected a legitimate invocation: %v",
				c.name, strings.Join(c.args, " "), err)
		}
	}
}
