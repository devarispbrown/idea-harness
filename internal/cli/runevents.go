package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// `idea run events` and `idea steer`: the run panel's live event view and its
// "Send a note" box, from a terminal. Both name a run by id or by `current` --
// the initiative's running agent run, else its most recent one, the same run
// the Build page's run panel shows (GET …/current-run).

// cliRunEvent is the part of a run event the human output prints. --json
// passes the daemon's event through untouched instead.
type cliRunEvent struct {
	Seq      int64     `json:"seq"`
	At       time.Time `json:"at"`
	Kind     string    `json:"kind"`
	Source   string    `json:"source"`
	Title    string    `json:"title"`
	Path     string    `json:"path"`
	Status   string    `json:"status"`
	ToolKind string    `json:"tool_kind"`
}

type cliRunEventPage struct {
	RunID     string            `json:"run_id"`
	RunStatus string            `json:"run_status"`
	Events    []json.RawMessage `json:"events"`
	LastSeq   int64             `json:"last_seq"`
	More      bool              `json:"more"`
}

type cliCurrentRun struct {
	RunID        string   `json:"run_id"`
	Live         bool     `json:"live"`
	Status       string   `json:"status"`
	Executor     string   `json:"executor"`
	Steerable    bool     `json:"steerable"`
	PendingNotes []string `json:"pending_notes"`
}

func runResource(projectID, runID, action string) string {
	return "/api/v1/projects/" + url.PathEscape(projectID) + "/runs/" + url.PathEscape(runID) + "/" + action
}

// resolveRun turns `current` (with an optional initiative) into the run the
// run panel shows; any other value is taken as a run id. live reports whether
// that run is still going (always true for an explicit id: the daemon decides).
func resolveRun(c *client, projectID, ref string, iniArgs []string) (runID string, cur *cliCurrentRun, err error) {
	if !strings.EqualFold(ref, "current") {
		if len(iniArgs) > 0 {
			return "", nil, usageErrorf("an initiative goes with `current` only; a run id already names its run")
		}
		return ref, nil, nil
	}
	iniID, err := resolveInitiative(c, projectID, iniArgs)
	if err != nil {
		return "", nil, err
	}
	var cr cliCurrentRun
	if err := c.do("GET", initiativeResource(projectID, iniID, "current-run"), nil, &cr); err != nil {
		return "", nil, err
	}
	if cr.RunID == "" {
		return "", &cr, &cliError{kind: kindNotFound, err: errors.New("this initiative has no agent run yet"),
			hint: "`idea execute` runs the next task"}
	}
	return cr.RunID, &cr, nil
}

// runTerminalStatus mirrors the daemon's runTerminal: no more events come.
func runTerminalStatus(s string) bool { return s == "succeeded" || s == "failed" }

// cmdRun dispatches `idea run <sub>`; events is the only subcommand.
func cmdRun(a cmdArgs, stdout, _ io.Writer) error {
	if a.sub != "events" || len(a.positional) < 1 {
		return usageErrorf("usage: idea run events <run-id|current> [<initiative>] [--follow] [--json]")
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	runID, _, err := resolveRun(c, projectID, a.positional[0], a.positional[1:])
	if err != nil {
		return err
	}
	p := &runEventPrinter{w: stdout, json: a.has("--json")}

	// Page through what is stored first, then (with --follow) stream from the
	// last seq: the late-joiner protocol the run panel uses, so the stream
	// never replays what was already printed.
	var after int64
	status := ""
	for {
		var page cliRunEventPage
		if err := c.do("GET", runResource(projectID, runID, "events")+"?after="+strconv.FormatInt(after, 10), nil, &page); err != nil {
			return err
		}
		for _, raw := range page.Events {
			seq, err := p.print(raw)
			if err != nil {
				return err
			}
			after = max(after, seq)
		}
		status = page.RunStatus
		if !page.More {
			// last_seq is the run's newest stored event: the stream's cursor
			// once every page is read -- and only then, or a middle page
			// would skip what lies between it and the end.
			after = max(after, page.LastSeq)
			break
		}
	}
	if !a.has("--follow") || runTerminalStatus(status) {
		if !p.json && p.printed == 0 {
			fmt.Fprintln(stdout, muted("No events recorded for run "+runID+"."))
		}
		return nil
	}
	return followRunEvents(c, projectID, runID, after, p)
}

// runEventPrinter writes events one per line: NDJSON with --json (the
// daemon's event object as is), else time, kind, status and title.
type runEventPrinter struct {
	w       io.Writer
	json    bool
	printed int
}

func (p *runEventPrinter) print(raw json.RawMessage) (int64, error) {
	var ev cliRunEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return 0, fmt.Errorf("decode run event: %w", err)
	}
	p.printed++
	if p.json {
		_, err := fmt.Fprintf(p.w, "%s\n", compactJSON(raw))
		return ev.Seq, err
	}
	_, err := fmt.Fprintln(p.w, formatRunEvent(ev))
	return ev.Seq, err
}

// compactJSON puts one event on one line, whatever whitespace it came with.
func compactJSON(raw json.RawMessage) []byte {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return raw
	}
	return b.Bytes()
}

// formatRunEvent is one human line: "15:04:05  tool_call    in_progress  npm test".
func formatRunEvent(ev cliRunEvent) string {
	text := ev.Title
	if text == "" {
		text = ev.Path
	} else if ev.Path != "" && !strings.Contains(text, ev.Path) {
		text += " (" + ev.Path + ")"
	}
	if ev.Source == "user" {
		text = "[you] " + text
	}
	at := "--:--:--"
	if !ev.At.IsZero() {
		at = ev.At.Local().Format("15:04:05")
	}
	return fmt.Sprintf("%s  %-11s  %-12s  %s", muted(at), ev.Kind, ev.Status, text)
}

// Reconnect pacing for --follow; variables so a test need not wait.
var (
	runFollowBackoffMin = time.Second
	runFollowBackoffMax = 30 * time.Second
	// runFollowMaxFailures is how many connection failures in a row end the
	// follow: a daemon gone for good should not hang the terminal forever.
	runFollowMaxFailures = 8
)

// followRunEvents tails the run's SSE stream from after, reconnecting with
// ?after=<last seq> (1 s doubling to 30 s) when the connection drops, until
// the daemon sends `event: end`.
func followRunEvents(c *client, projectID, runID string, after int64, p *runEventPrinter) error {
	// No client timeout: a stream is meant to stay open; the daemon's
	// heartbeat keeps it alive through quiet stretches.
	hc := &http.Client{}
	backoff := runFollowBackoffMin
	failures := 0
	for {
		ended, progressed, err := streamRunEventsOnce(hc, c, projectID, runID, &after, p)
		if ended {
			return nil
		}
		var he *httpError
		if errors.As(err, &he) && he.Status < 500 {
			return err // a 404 or 401 will not get better by retrying
		}
		if progressed {
			failures, backoff = 0, runFollowBackoffMin
		}
		failures++
		if failures >= runFollowMaxFailures {
			if err == nil {
				err = errors.New("the stream kept closing")
			}
			return &cliError{kind: kindDaemonUnavailable, err: fmt.Errorf("lost the run's event stream after %d tries: %w", failures, err),
				hint: "`idea run events " + runID + " --follow` resumes where this stopped"}
		}
		time.Sleep(backoff)
		backoff = min(backoff*2, runFollowBackoffMax)
	}
}

// streamRunEventsOnce reads one SSE connection to its end. ended is true once
// the daemon said `event: end`; progressed is true when any event arrived.
func streamRunEventsOnce(hc *http.Client, c *client, projectID, runID string, after *int64, p *runEventPrinter) (ended, progressed bool, err error) {
	req, err := http.NewRequest("GET", c.baseURL+runResource(projectID, runID, "events/stream")+"?after="+strconv.FormatInt(*after, 10), nil)
	if err != nil {
		return false, false, err
	}
	req.Header.Set("X-Idea-Token", c.token)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := hc.Do(req)
	if err != nil {
		return false, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e map[string]string
		_ = json.NewDecoder(resp.Body).Decode(&e)
		msg := e["error"]
		if msg == "" {
			msg = resp.Status
		}
		return false, false, &httpError{Status: resp.StatusCode, Message: msg}
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4<<20)
	var event string
	var data strings.Builder
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "":
			// Dispatch. A comment-only block (": ping") has no event.
			switch event {
			case "end":
				return true, progressed, nil
			case "run_event":
				seq, perr := p.print(json.RawMessage(data.String()))
				if perr != nil {
					return false, progressed, perr
				}
				if seq > *after {
					*after = seq
				}
				progressed = true
			}
			event = ""
			data.Reset()
		case strings.HasPrefix(line, ":"):
		case strings.HasPrefix(line, "event:"):
			event = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	return false, progressed, sc.Err()
}

// cmdSteer sends a note to a running agent: POST …/runs/{runId}/steer.
func cmdSteer(a cmdArgs, stdout, _ io.Writer) error {
	if len(a.positional) < 2 || strings.TrimSpace(a.positional[1]) == "" {
		return usageErrorf(`usage: idea steer <run-id|current> "<note>" [<initiative>] [--interrupt]`)
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	runID, cur, err := resolveRun(c, projectID, a.positional[0], a.positional[2:])
	if err != nil {
		return err
	}
	noLive := &cliError{kind: kindConflict, err: fmt.Errorf("run %s is not running, so there is no agent to steer", runID),
		hint: "`idea execute --note \"<note>\"` sends the note with the task's next run"}
	if cur != nil && !cur.Live {
		return noLive
	}
	var res struct {
		RunID    string `json:"run_id"`
		Executor string `json:"executor"`
		Delivery string `json:"delivery"`
		Message  string `json:"message"`
	}
	body := map[string]any{"note": a.positional[1], "interrupt": a.has("--interrupt")}
	if err := c.do("POST", runResource(projectID, runID, "steer"), body, &res); err != nil {
		var he *httpError
		if errors.As(err, &he) && he.Status == http.StatusConflict {
			return noLive
		}
		return err
	}
	if a.has("--json") {
		return dumpJSON(stdout, res)
	}
	fmt.Fprintln(stdout, res.Message)
	fmt.Fprintln(stdout, muted("delivery: "+res.Delivery+" · run "+res.RunID))
	return nil
}
