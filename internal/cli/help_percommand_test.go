package cli

import (
	"strings"
	"testing"
)

// TestEveryCommandAnswersHelp closes audit 5's per-command help gap.
//
// `--help` and `-h` were rejected on all 33 subcommands: `idea verify --help`
// answered `unknown flag "--help"; accepted flags: --fail, --json, --note,
// --pass, --waive`. Enumerating the accepted flags is a good error, but there
// was no way to learn what --waive does, or what `export`'s kinds are, without
// opening docs/cli.md — and --help is the first thing anyone reaches for.
func TestEveryCommandAnswersHelp(t *testing.T) {
	for _, c := range commands() {
		if c.run == nil || len(c.names) == 0 {
			continue
		}
		name := c.names[0]
		for _, flag := range []string{"--help", "-h"} {
			t.Run(name+" "+flag, func(t *testing.T) {
				stdout, _, err := run(t, name, flag)
				if err != nil {
					t.Fatalf("idea %s %s: %v", name, flag, err)
				}
				if !strings.Contains(stdout, c.usage) {
					t.Errorf("help does not show the usage line %q:\n%s", c.usage, stdout)
				}
				if c.summary != "" && !strings.Contains(stdout, c.summary) {
					t.Errorf("help does not show the summary:\n%s", stdout)
				}
			})
		}
	}
}

// TestHelpListsTheFlagsTheCommandAccepts: the point is learning what a command
// takes, so every declared flag has to appear.
func TestHelpListsTheFlagsTheCommandAccepts(t *testing.T) {
	for _, c := range commands() {
		if c.run == nil || len(c.names) == 0 || len(c.flags) == 0 {
			continue
		}
		name := c.names[0]
		stdout, _, err := run(t, name, "--help")
		if err != nil {
			t.Fatalf("idea %s --help: %v", name, err)
		}
		for flag := range c.flags {
			if !strings.Contains(stdout, flag) {
				t.Errorf("idea %s --help omits %s:\n%s", name, flag, stdout)
			}
		}
		for sub := range c.subs {
			if !strings.Contains(stdout, sub) {
				t.Errorf("idea %s --help omits subcommand %s:\n%s", name, sub, stdout)
			}
		}
	}
}

// TestHelpDoesNotStealArgumentsAfterTheTerminator: after "--" the tokens are
// the command's own data. `idea docs add -- --help` must add a file named
// --help, not print usage. The parser learned this distinction one round ago
// when the terminator walked past the subcommand check.
func TestHelpDoesNotStealArgumentsAfterTheTerminator(t *testing.T) {
	if wantsHelp([]string{"add", "--", "--help"}) {
		t.Error("--help after the terminator was treated as a request for help")
	}
	if !wantsHelp([]string{"add", "--help", "--", "x"}) {
		t.Error("--help before the terminator was not recognised")
	}
	if wantsHelp([]string{"REQ-1", "--pass"}) {
		t.Error("ordinary arguments were treated as a request for help")
	}
}
