package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
)

// cmdWatch follows an initiative to completion with a live spinner (§64).
func cmdWatch(a cmdArgs, stdout, stderr io.Writer) error {
	positional := a.positional
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
	return watchInitiativeWith(c, projectID, initiativeID, stdout)
}

// watchInitiativeWith runs the watcher with extra program options, so a test can
// supply a headless input and renderer. Without that, tea.NewProgram fails to
// open /dev/tty under `go test` and returns before any polling -- which is why
// the previous round's test could not reach this function's exit path at all,
// and passed whether or not the fix worked.
func watchInitiativeWith(c *client, projectID, initiativeID string, stdout io.Writer, opts ...tea.ProgramOption) error {
	m := watchModel{
		c:            c,
		projectID:    projectID,
		initiativeID: initiativeID,
		spinner:      spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
	p := tea.NewProgram(&m, append([]tea.ProgramOption{tea.WithOutput(stdout)}, opts...)...)
	final, err := p.Run()
	if err != nil {
		return err
	}
	// Read fatal off the model the program returns, not off the local one.
	//
	// Update has a value receiver, so Bubble Tea copies the model on the first
	// update and thereafter carries the returned value. The outer m is never
	// written again, so `return m.fatal` was always nil -- watch quit correctly
	// on a bad reference and still exited 0, which is the bug the previous round
	// thought it had fixed. The test written for it drove poll() on a pointer,
	// which is not the shape the program uses, so it passed either way.
	if fm, ok := final.(watchModel); ok {
		return fm.fatal
	}
	return m.fatal
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
	// failures counts consecutive poll errors. watch used to store the error and
	// keep polling at 1 Hz forever, exiting 0 on Ctrl-C -- so `idea watch CHG-1`
	// (a change-set id, not an initiative) spun until interrupted and a CI job
	// invoking it never terminated (DX audit).
	failures int
	// fatal is set when polling has failed in a way that will not recover, so
	// watchInitiative can return it and main.go can exit non-zero.
	fatal error
}

// maxWatchFailures is how many consecutive poll errors watch tolerates before
// giving up. A transient blip during a restart should not end the watch; a wrong
// id or a dead daemon should not spin forever.
const maxWatchFailures = 5

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
		if m.done || m.fatal != nil {
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
	if err := m.c.do("GET", initiativeResource(m.projectID, m.initiativeID, "understanding"), nil, &u); err != nil {
		m.lastErr = err.Error()
		m.failures++
		// A not-found will never become found: the id is wrong, so stop now
		// rather than after the retry budget.
		if isNotFound(err) {
			m.fatal = err
		} else if m.failures >= maxWatchFailures {
			m.fatal = fmt.Errorf("gave up after %d consecutive poll failures: %w", m.failures, err)
		}
		return
	}
	m.failures = 0
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
