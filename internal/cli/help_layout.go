package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The help screen is grouped by workflow phase. A group has a short id (used to
// look commands up) and a human title. Order here is the display order.

type helpGroup struct {
	id    string
	title string
}

var helpGroups = []helpGroup{
	{id: "start", title: "Start"},
	{id: "define", title: "Explore & define"},
	{id: "plan", title: "Design & plan"},
	{id: "build", title: "Build"},
	{id: "verify", title: "Verify & review"},
	{id: "understand", title: "Understand the graph"},
	{id: "configure", title: "Configure & connect"},
	{id: "admin", title: "Run & administer"},
}

// commandGroup maps a command's dispatch token to its help group. Subcommand
// documentation rows (which have no dispatch token) resolve through the first
// word of their usage, which is the parent's token. Commands that are not in
// `idea help` (mcp, eval, objectives, metric) still need an entry so the
// generated docs/cli.md table can group them too.
var commandGroup = map[string]string{
	"init": "start", "status": "start", "changed": "start", "doctor": "start",

	"new": "define", "prd": "define", "explain": "define", "export": "define",
	"approve": "define", "revise": "define",
	"decisions": "define", "questions": "define", "answer": "define",
	"resolve": "define", "accept-recommendations": "define",
	"reopen-decision": "define", "triage": "define",

	"advance": "plan", "approve-plan": "plan", "revise-plan": "plan", "plan": "plan",
	"tasks": "plan", "add-task": "plan", "merge-tasks": "plan", "split-task": "plan",
	"depends-on": "plan", "reorder": "plan", "reopen-phase": "plan",

	"execute": "build", "changes": "build", "change": "build", "apply": "build",
	"revert": "build", "review": "build", "retry": "build", "executors": "build",
	"watch": "build", "run": "build", "runs": "build", "steer": "build", "cancel": "build",

	"verify": "verify", "next": "verify", "risk": "verify",
	"reviews": "verify", "disposition": "verify", "pr": "verify",

	"why": "understand", "impact": "understand", "trace": "understand",
	"gaps": "understand", "assumption": "understand",
	"objectives": "understand", "objective": "understand",
	"budget": "understand", "metrics": "understand", "metric": "understand",
	"diff": "understand",

	"model": "configure", "key": "configure", "config": "configure",
	"integrations": "configure", "channel": "configure", "capture": "configure",
	"remote": "configure", "ui": "configure", "docs": "configure", "mcp": "configure",

	"daemon": "admin", "logs": "admin", "dev": "admin",
	"eval": "admin",
	"version": "admin", "help": "admin",
}

// groupOf returns the help-group id for a command row. Unknown rows fall into
// the last group rather than being dropped.
func groupOf(c command) string {
	if len(c.names) > 0 {
		if g, ok := commandGroup[c.names[0]]; ok {
			return g
		}
	}
	if f := strings.Fields(c.usage); len(f) > 0 {
		if g, ok := commandGroup[f[0]]; ok {
			return g
		}
	}
	return "admin"
}

// groupRows returns the in-help rows for a group, in registration order.
func groupRows(id string) []command {
	var rows []command
	for _, c := range commands() {
		if c.inHelp && groupOf(c) == id {
			rows = append(rows, c)
		}
	}
	return rows
}

// quickstartStep is one line of the happy path shown at the top of `idea help`.
type quickstartStep struct {
	cmd  string
	note string
}

var quickstart = []quickstartStep{
	{cmd: "idea init", note: "create a project + start the daemon"},
	{cmd: `idea new "<your idea>"`, note: "idea → reviewed PRD"},
	{cmd: "idea next --until-human", note: "design → plan → change set, stops for you"},
	{cmd: "idea review CHG-1 approve", note: "approve the change set"},
	{cmd: "idea apply CHG-1", note: "write it to your workspace"},
	{cmd: "idea verify REQ-1 --pass", note: "record evidence"},
	{cmd: "idea next", note: "→ initiative complete"},
}

// padRight pads s with spaces to the given display width. Padding is applied
// before color so ANSI codes never skew the column alignment.
func padRight(s string, w int) string {
	if n := lipgloss.Width(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

// maxUsageWidth is the cap on the command column: usages longer than this
// overflow into the summary column rather than forcing every row in the group
// absurdly wide.
const maxUsageWidth = 42

// groupColumnWidth returns the command-column width for a group's rows, capped
// at maxUsageWidth.
func groupColumnWidth(rows []command) int {
	width := 0
	for _, r := range rows {
		if n := lipgloss.Width(r.usage); n > width {
			width = n
		}
	}
	if width > maxUsageWidth {
		width = maxUsageWidth
	}
	return width
}

// quickstartWidth returns the width of the quickstart's command column.
func quickstartWidth() int {
	w := 0
	for _, s := range quickstart {
		if n := lipgloss.Width(s.cmd); n > w {
			w = n
		}
	}
	return w
}

// helpRow renders one command row. The usage is coloured and padded to the
// column width; when a usage is longer than the column, the summary drops to
// the next line, indented to the summary column so every summary still aligns.
func helpRow(r command, width int) string {
	if lipgloss.Width(r.usage) > width {
		return fmt.Sprintf("  %s\n%s%s\n", accent(r.usage), strings.Repeat(" ", width+3), r.summary)
	}
	return fmt.Sprintf("  %s %s\n", accent(padRight(r.usage, width)), r.summary)
}
