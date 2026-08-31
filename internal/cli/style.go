package cli

import (
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
)

// Charm-style styling (§63, §64). Colors are the same calm, restrained palette
// used by the web UI. Styling is disabled automatically when stdout is not a
// terminal (piped / --json / scripting).
var (
	styleHeading = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#0f766e"))
	styleMuted   = lipgloss.NewStyle().Foreground(lipgloss.Color("#71717a"))
	styleOK      = lipgloss.NewStyle().Foreground(lipgloss.Color("#15803d"))
	styleWarn    = lipgloss.NewStyle().Foreground(lipgloss.Color("#b45309"))
	styleErr     = lipgloss.NewStyle().Foreground(lipgloss.Color("#b91c1c"))
	styleAccent  = lipgloss.NewStyle().Foreground(lipgloss.Color("#0ea5e9"))
	styleBold    = lipgloss.NewStyle().Bold(true)
	styleDim     = lipgloss.NewStyle().Faint(true)
	styleCode    = lipgloss.NewStyle().Foreground(lipgloss.Color("#0891b2"))

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#e5e5e5")).
			Padding(0, 1)

	colorEnabled bool
)

// initColor detects whether stdout is a terminal and enables/disables styling.
func initColor(w io.Writer) {
	colorEnabled = false
	if f, ok := w.(*os.File); ok {
		colorEnabled = isatty.IsTerminal(f.Fd())
	}
}

// styl renders text with the given style, falling back to plain text when
// color is disabled.
func styl(s lipgloss.Style, text string) string {
	if !colorEnabled {
		return text
	}
	return s.Render(text)
}

// heading renders a section heading.
func heading(text string) string { return styl(styleHeading, text) }

// muted renders secondary text.
func muted(text string) string { return styl(styleMuted, text) }

// ok renders success text.
func ok(text string) string { return styl(styleOK, text) }

// warn renders warning text.
func warn(text string) string { return styl(styleWarn, text) }

// errText renders error text.
func errText(text string) string { return styl(styleErr, text) }

// accent renders highlighted text.
func accent(text string) string { return styl(styleAccent, text) }

// bold renders bold text.
func bold(text string) string { return styl(styleBold, text) }

// dim renders faint text.
func dim(text string) string { return styl(styleDim, text) }

// code renders a code/inline value.
func code(text string) string { return styl(styleCode, text) }

// panel renders content inside a rounded border panel.
func panel(content string) string {
	if !colorEnabled {
		return content
	}
	return panelStyle.Render(content)
}
