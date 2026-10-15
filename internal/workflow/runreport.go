package workflow

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor"
)

// RunReport is one agent run, written down: what ran, what it did (its
// events), what it changed (the file list with line counts), what review
// found, and what it cost. GET .../runs/{runId}/report serves it as JSON,
// Markdown, or both in a zip. Everything in it is already redacted: events
// on the way in, findings at creation.
type RunReport struct {
	GeneratedAt time.Time             `json:"generated_at"`
	Run         RunReportRun          `json:"run"`
	Manifest    *domain.RunManifest   `json:"manifest,omitempty"`
	ChangeSet   *RunReportChangeSet   `json:"change_set,omitempty"`
	Files       []RunReportFile       `json:"files"`
	Findings    []RunReportFinding    `json:"findings"`
	Cost        domain.RunUsage       `json:"cost"`
	Steering    []domain.SteeringNote `json:"steering"`
	Events      []domain.RunEvent     `json:"events"`
	// EventsDropped is how many of the run's earliest events the per-run cap
	// removed (store.RunEventsPerRunMax).
	EventsDropped int64 `json:"events_dropped,omitempty"`
	// OpenHandsURL links the run's conversation in an OpenHands UI, when one
	// is configured (executor.openhands.ui_url).
	OpenHandsURL string `json:"openhands_url,omitempty"`
}

// RunReportRun is the run itself.
type RunReportRun struct {
	ID           string     `json:"id"`
	Status       string     `json:"status"`
	InitiativeID string     `json:"initiative_id,omitempty"`
	TaskID       string     `json:"task_id,omitempty"`
	TaskCode     string     `json:"task_code,omitempty"`
	TaskTitle    string     `json:"task_title,omitempty"`
	Executor     string     `json:"executor,omitempty"`
	Note         string     `json:"note,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
}

// RunReportChangeSet is the change set the run produced.
type RunReportChangeSet struct {
	ID      string `json:"id"`
	HumanID string `json:"human_id"`
	Status  string `json:"status"`
	NoOp    bool   `json:"no_op,omitempty"`
}

// RunReportFile is one file the run changed, with line counts when known.
type RunReportFile struct {
	Path    string `json:"path"`
	Change  string `json:"change,omitempty"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// RunReportFinding is one finding on the run's change set.
type RunReportFinding struct {
	Severity    string `json:"severity"`
	Category    string `json:"category"`
	Finding     string `json:"finding"`
	Disposition string `json:"disposition,omitempty"`
}

// RunUIURL is the run's "Open in OpenHands" link: its conversation in the
// configured OpenHands UI, or "" (not an openhands run, or no ui_url).
func (e *Engine) RunUIURL(ctx context.Context, runID string) string {
	ui := e.snapshotConfig().cfg.Executor.OpenHands.UIURL
	if ui == "" {
		return ""
	}
	rr, err := e.db.GetRemoteRunByAgentRun(ctx, runID)
	if err != nil || rr == nil {
		return ""
	}
	return executor.ConversationURL(ui, rr.ConversationID)
}

// BuildRunReport assembles the report for an agent run.
func (e *Engine) BuildRunReport(ctx context.Context, runID string) (RunReport, error) {
	info, err := e.db.GetRunInfo(ctx, runID)
	if err != nil {
		return RunReport{}, err
	}
	rep := RunReport{GeneratedAt: time.Now().UTC(), Files: []RunReportFile{}, Findings: []RunReportFinding{},
		Steering: []domain.SteeringNote{}, Events: []domain.RunEvent{}}
	// The run's note is stored as the human typed it (it went into the brief);
	// the report is a file that leaves the daemon, so it is redacted here.
	rep.Run = RunReportRun{ID: info.ID, Status: info.Status, InitiativeID: info.InitiativeID, Note: e.redactRemote(info.Note),
		StartedAt: info.StartedAt, FinishedAt: info.FinishedAt}
	if m, err := e.db.GetRunManifest(ctx, runID); err == nil {
		rep.Manifest = &m
		rep.Cost = m.Usage
		if m.Executor != nil {
			rep.Run.Executor = m.Executor.Resolved
			rep.Steering = append(rep.Steering, m.Executor.Steering...)
		}
	}
	events, err := e.db.ListAllRunEvents(ctx, runID)
	if err != nil {
		return RunReport{}, err
	}
	rep.Events = append(rep.Events, events...)
	if len(events) > 0 && events[0].Seq > 1 {
		rep.EventsDropped = events[0].Seq - 1
	}

	files := map[string]*RunReportFile{}
	var order []string
	var changeSetID string
	var evIn, evOut int64
	var evUSD float64
	fromEvents := len(rep.Steering) == 0
	for _, ev := range events {
		if rep.Run.TaskID == "" {
			rep.Run.TaskID = ev.TaskID
		}
		if rep.Run.Executor == "" {
			rep.Run.Executor = ev.Executor
		}
		switch ev.Kind {
		case domain.RunEventFileChange:
			if ev.Path == "" {
				continue
			}
			f := files[ev.Path]
			if f == nil {
				f = &RunReportFile{Path: ev.Path}
				files[ev.Path] = f
				order = append(order, ev.Path)
			}
			// The orchestrator's own diff (source harness, with a change
			// type) is authoritative; a live event only fills a gap.
			if ev.Source == domain.RunEventSourceHarness && ev.Status != "" {
				f.Change, f.Added, f.Removed = ev.Status, ev.Added, ev.Removed
			} else if f.Change == "" && (ev.Added > 0 || ev.Removed > 0) {
				f.Added, f.Removed = ev.Added, ev.Removed
			}
		case domain.RunEventCost:
			evIn += ev.InputTokens
			evOut += ev.OutputTokens
			evUSD += ev.CostUSD
		case domain.RunEventMessage:
			// A failed run has no manifest: its notes come from the stream.
			if fromEvents && ev.Source == domain.RunEventSourceUser && ev.Title == "Note from you" {
				rep.Steering = append(rep.Steering, domain.SteeringNote{At: ev.At, Note: ev.Detail, Delivery: ev.Status})
			}
		case domain.RunEventStatus:
			if ev.ChangeSetID != "" {
				changeSetID = ev.ChangeSetID
			}
		}
	}
	for _, p := range order {
		rep.Files = append(rep.Files, *files[p])
	}
	// The manifest's usage is authoritative where it has a figure; an agent
	// run's manifest records only its duration today, so tokens and cost
	// come from the cost events (deltas, summed).
	if rep.Cost.InputTokens == 0 && rep.Cost.OutputTokens == 0 {
		rep.Cost.InputTokens, rep.Cost.OutputTokens = evIn, evOut
	}
	if rep.Cost.CostUSD == 0 {
		rep.Cost.CostUSD = evUSD
	}
	if rep.Run.TaskID != "" {
		if t, err := e.db.GetTask(ctx, rep.Run.TaskID); err == nil && t != nil {
			rep.Run.TaskCode, rep.Run.TaskTitle = t.Code, t.Title
		}
	}
	if changeSetID != "" {
		if cs, err := e.db.GetChangeSet(ctx, changeSetID); err == nil && cs != nil {
			rep.ChangeSet = &RunReportChangeSet{ID: cs.ID, HumanID: cs.HumanID, Status: string(cs.Status), NoOp: cs.NoOp}
			if fs, err := e.db.ListFindingsByTarget(ctx, "change_set", cs.ID); err == nil {
				for _, f := range fs {
					rep.Findings = append(rep.Findings, RunReportFinding{Severity: string(f.Severity), Category: f.Category,
						Finding: f.Finding, Disposition: string(f.Disposition)})
				}
			}
		}
	}
	sort.SliceStable(rep.Findings, func(i, j int) bool {
		return severityRank[domain.Severity(rep.Findings[i].Severity)] > severityRank[domain.Severity(rep.Findings[j].Severity)]
	})
	rep.OpenHandsURL = e.RunUIURL(ctx, runID)
	return rep, nil
}

// Markdown renders the report for a person: summary, files, findings,
// steering, then the event log. Event text is already redacted; it is
// rendered inside code spans/blocks so an agent's message cannot inject
// Markdown structure (links, headings) into the report.
func (r RunReport) Markdown() string {
	var b strings.Builder
	title := r.Run.ID
	if r.Run.TaskCode != "" {
		title = r.Run.TaskCode + ": " + r.Run.TaskTitle
	}
	fmt.Fprintf(&b, "# Run report — %s\n\n", mdText(title))
	fmt.Fprintf(&b, "| | |\n|---|---|\n")
	fmt.Fprintf(&b, "| Run | `%s` |\n", r.Run.ID)
	fmt.Fprintf(&b, "| Status | %s |\n", mdText(r.Run.Status))
	if r.Run.Executor != "" {
		fmt.Fprintf(&b, "| Executor | %s |\n", mdText(r.Run.Executor))
	}
	fmt.Fprintf(&b, "| Started | %s |\n", r.Run.StartedAt.UTC().Format(time.RFC3339))
	if r.Run.FinishedAt != nil {
		fmt.Fprintf(&b, "| Finished | %s (%s) |\n", r.Run.FinishedAt.UTC().Format(time.RFC3339), r.Run.FinishedAt.Sub(r.Run.StartedAt).Round(time.Second))
	}
	if r.ChangeSet != nil {
		fmt.Fprintf(&b, "| Change set | %s (%s) |\n", mdText(r.ChangeSet.HumanID), mdText(strings.ReplaceAll(r.ChangeSet.Status, "_", " ")))
	}
	if r.Cost.InputTokens > 0 || r.Cost.OutputTokens > 0 {
		fmt.Fprintf(&b, "| Tokens | %d in / %d out |\n", r.Cost.InputTokens, r.Cost.OutputTokens)
	}
	if r.Cost.CostUSD > 0 {
		fmt.Fprintf(&b, "| Cost | $%.4f |\n", r.Cost.CostUSD)
	}
	if r.Cost.DurationMS > 0 {
		fmt.Fprintf(&b, "| Agent time | %s |\n", (time.Duration(r.Cost.DurationMS) * time.Millisecond).Round(time.Second))
	}
	if r.OpenHandsURL != "" {
		fmt.Fprintf(&b, "| OpenHands | <%s> |\n", r.OpenHandsURL)
	}
	if r.Run.Note != "" {
		fmt.Fprintf(&b, "\n**Note for this run:**\n\n```text\n%s\n```\n", mdFence(r.Run.Note))
	}

	fmt.Fprintf(&b, "\n## Files changed (%d)\n\n", len(r.Files))
	if len(r.Files) == 0 {
		b.WriteString("None.\n")
	}
	for _, f := range r.Files {
		fmt.Fprintf(&b, "- `%s` %s +%d −%d\n", mdCode(f.Path), mdText(f.Change), f.Added, f.Removed)
	}

	fmt.Fprintf(&b, "\n## Findings (%d)\n\n", len(r.Findings))
	if len(r.Findings) == 0 {
		b.WriteString("None.\n")
	}
	for _, f := range r.Findings {
		disp := ""
		if f.Disposition != "" {
			disp = " — " + mdText(f.Disposition)
		}
		fmt.Fprintf(&b, "- **%s** %s: %s%s\n", mdText(f.Severity), mdText(f.Category), mdText(f.Finding), disp)
	}

	if len(r.Steering) > 0 {
		fmt.Fprintf(&b, "\n## Steering notes (%d)\n\n", len(r.Steering))
		for _, s := range r.Steering {
			fmt.Fprintf(&b, "- %s (%s): %s\n", s.At.UTC().Format(time.RFC3339), mdText(strings.ReplaceAll(s.Delivery, "_", " ")), mdText(s.Note))
		}
	}

	fmt.Fprintf(&b, "\n## Events (%d)\n\n", len(r.Events))
	if r.EventsDropped > 0 {
		fmt.Fprintf(&b, "The first %d events were dropped by the per-run cap.\n\n", r.EventsDropped)
	}
	b.WriteString("```text\n")
	for _, ev := range r.Events {
		line := fmt.Sprintf("%5d %s %-11s %s", ev.Seq, ev.At.UTC().Format("15:04:05"), ev.Kind, ev.Title)
		if ev.Status != "" && ev.Kind != domain.RunEventStatus {
			line += " [" + ev.Status + "]"
		}
		if ev.Added > 0 || ev.Removed > 0 {
			line += fmt.Sprintf(" +%d -%d", ev.Added, ev.Removed)
		}
		b.WriteString(mdFence(line) + "\n")
	}
	b.WriteString("```\n")
	return b.String()
}

// mdText makes free text safe as inline Markdown inside a table cell or a
// list item: one line, and every character that could open emphasis, code,
// a link or image (`[`), raw HTML (`<`), a heading or a table cell escaped.
// Characters that only mean something at the start of a line (`-`, `+`,
// `1.`) cannot: the text never starts one.
func mdText(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune("\\`*_[]<>#|~", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// mdCode makes s safe inside a single-backtick code span.
func mdCode(s string) string { return strings.ReplaceAll(s, "`", "'") }

// mdFence keeps s from closing a ``` fence it is inside.
func mdFence(s string) string { return strings.ReplaceAll(s, "```", "'''") }
