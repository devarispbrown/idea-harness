package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// cliRemoteRun is one row of GET …/remote-runs: an openhands dispatch record.
type cliRemoteRun struct {
	ID             string `json:"id"`
	InitiativeID   string `json:"initiative_id"`
	TaskCode       string `json:"task_code"`
	Attempt        int    `json:"attempt"`
	ServerURL      string `json:"server_url"`
	ConversationID string `json:"conversation_id"`
	Status         string `json:"status"`
	RemoteStatus   string `json:"remote_status"`
	EventCount     int    `json:"event_count"`
	LastEvent      string `json:"last_event"`
	Error          string `json:"error"`
	UpdatedAt      string `json:"updated_at"`
}

// cmdRuns is `idea runs remote [<initiative>] [--all] [--json]`: the openhands
// executor's remote runs, newest first. Until it existed, which conversation
// a task went to, on which server, at which attempt and why it failed was
// readable only from state.db.
func cmdRuns(a cmdArgs, stdout, _ io.Writer) error {
	if a.sub != "remote" {
		return usageErrorf("usage: idea runs remote [<initiative>] [--all] [--json]")
	}
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	path := "/api/v1/projects/" + url.PathEscape(projectID) + "/remote-runs"
	if !a.has("--all") {
		iniID, err := resolveInitiative(c, projectID, a.positional)
		if err != nil {
			return err
		}
		path = initiativeResource(projectID, iniID, "remote-runs")
	}
	var raw json.RawMessage
	if err := c.do("GET", path, nil, &raw); err != nil {
		return err
	}
	if a.has("--json") {
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		return dumpJSON(stdout, v)
	}
	var runs []cliRemoteRun
	if err := json.Unmarshal(raw, &runs); err != nil {
		return err
	}
	renderRemoteRuns(runs, stdout)
	return nil
}

func renderRemoteRuns(runs []cliRemoteRun, w io.Writer) {
	if len(runs) == 0 {
		fmt.Fprintln(w, muted("No remote runs. They are recorded when a task runs on the openhands executor."))
		return
	}
	for _, r := range runs {
		status := r.Status
		switch status {
		case "finished", "collected":
			status = ok(status)
		case "failed":
			status = errText(status)
		case "cancelled":
			status = warn(status)
		}
		task := r.TaskCode
		if task == "" {
			task = "(task removed)"
		}
		fmt.Fprintf(w, "%s  %s  attempt %d  %s\n", code(task), status, r.Attempt, muted(r.UpdatedAt))
		conv := r.ConversationID
		if conv == "" {
			conv = "(not created yet)"
		}
		fmt.Fprintf(w, "  server        %s\n", r.ServerURL)
		fmt.Fprintf(w, "  conversation  %s\n", conv)
		if r.RemoteStatus != "" {
			fmt.Fprintf(w, "  remote status %s\n", r.RemoteStatus)
		}
		if r.LastEvent != "" {
			fmt.Fprintf(w, "  last event    %s %s\n", strings.TrimSpace(r.LastEvent), muted(fmt.Sprintf("(%d events)", r.EventCount)))
		}
		if r.Error != "" {
			fmt.Fprintf(w, "  error         %s\n", errText(r.Error))
		}
	}
}
