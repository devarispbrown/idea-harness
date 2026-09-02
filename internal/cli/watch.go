package cli

import (
	"fmt"
	"io"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/bubbles/spinner"
)

// cmdWatch follows an initiative to completion with a live spinner (§64).
func cmdWatch(args []string, stdout, stderr io.Writer) error {
	positional, _, err := parseArgs(flagSpec{}, args)
	if err != nil {
		return err
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	initiativeID := ""
	if len(positional) > 0 {
		initiativeID = positional[0]
	} else {
		var inis []cliInitiative
		if err := c.do("GET", "/api/v1/projects/"+projectID+"/initiatives", nil, &inis); err != nil {
			return err
		}
		if len(inis) == 0 {
			return fmt.Errorf("no initiatives yet; run `idea new` first")
		}
		initiativeID = inis[len(inis)-1].ID // most recent
	}
	return watchInitiative(c, projectID, initiativeID, stdout)
}

// watchInitiative runs the live Bubble Tea watcher for one initiative.
func watchInitiative(c *client, projectID, initiativeID string, stdout io.Writer) error {
	m := watchModel{
		c:            c,
		projectID:    projectID,
		initiativeID: initiativeID,
		spinner:      spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
	p := tea.NewProgram(m, tea.WithOutput(stdout))
	if _, err := p.Run(); err != nil {
		return err
	}
	return nil
}

type pollMsg struct{}

type watchModel struct {
	c            *client
	projectID    string
	initiativeID string
	spinner      spinner.Model
	title        string
	status       string
	counts       string
	done         bool
	lastErr      string
}

func (m watchModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, pollNow())
}

func (m watchModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case pollMsg:
		m.poll()
		if m.done {
			return m, tea.Quit
		}
		return m, pollTick()
	case tea.KeyMsg:
		if msg.Type == tea.KeyCtrlC || msg.Type == tea.KeyEsc {
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m *watchModel) poll() {
	var u cliUnderstanding
	if err := m.c.do("GET", "/api/v1/projects/"+m.projectID+"/initiatives/"+m.initiativeID+"/understanding", nil, &u); err != nil {
		m.lastErr = err.Error()
		return
	}
	m.title = u.Title
	m.status = u.Status
	m.counts = fmt.Sprintf("%d requirements · %d decisions · %d questions", len(u.Requirements), len(u.Decisions), len(u.Questions))
	switch u.Status {
	case "approved", "needs_human_judgment", "failed":
		m.done = true
	}
}

func (m watchModel) View() string {
	if m.lastErr != "" {
		return errText("error: ") + m.lastErr
	}
	title := m.title
	if title == "" {
		title = m.initiativeID
	}
	status := m.status
	if status == "" {
		status = "starting"
	}
	line := fmt.Sprintf("%s %s — %s", m.spinner.View(), bold(title), statusBadge(status))
	if m.counts != "" {
		line += "\n  " + muted(m.counts)
	}
	if m.done {
		line += "\n\n" + ok("Done.")
	}
	return line
}

func pollTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return pollMsg{} })
}

func pollNow() tea.Cmd {
	return tea.Tick(0, func(time.Time) tea.Msg { return pollMsg{} })
}
