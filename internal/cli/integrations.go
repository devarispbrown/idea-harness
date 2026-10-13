package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"time"
)

// cliIntegrationStatus mirrors daemon.IntegrationStatus.
type cliIntegrationStatus struct {
	Name       string          `json:"name"`
	Configured bool            `json:"configured"`
	Active     bool            `json:"active"`
	Tokens     map[string]bool `json:"tokens,omitempty"`
	Problems   []string        `json:"problems"`
	Outbox     *struct {
		Pending         int        `json:"pending"`
		Delivered       int        `json:"delivered"`
		Dead            int        `json:"dead"`
		LastError       string     `json:"last_error,omitempty"`
		LastDeliveredAt *time.Time `json:"last_delivered_at,omitempty"`
	} `json:"outbox,omitempty"`
	IngestError string `json:"ingest_error,omitempty"`
	Socket      *struct {
		Connected  bool   `json:"connected"`
		LastError  string `json:"last_error,omitempty"`
		Reconnects int    `json:"reconnects"`
	} `json:"socket,omitempty"`
	Poll *struct {
		LastPollAt *time.Time `json:"last_poll_at,omitempty"`
		LastError  string     `json:"last_error,omitempty"`
	} `json:"poll,omitempty"`
	LinkedIssues int `json:"linked_issues,omitempty"`
}

type cliIntegrationsView struct {
	Integrations []cliIntegrationStatus `json:"integrations"`
}

// cmdIntegrations is `idea integrations [status]` and
// `idea integrations test slack|linear`.
func cmdIntegrations(a cmdArgs, stdout, _ io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	if a.sub == "test" {
		name := a.arg(0)
		if name != "slack" && name != "linear" {
			return fmt.Errorf("usage: idea integrations test slack|linear")
		}
		var out struct {
			Name   string          `json:"name"`
			OK     bool            `json:"ok"`
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		if err := c.do("POST", "/api/v1/projects/"+projectID+"/integrations/"+url.PathEscape(name)+"/test", nil, &out); err != nil {
			return err
		}
		if a.flags.has("--json") {
			if err := dumpJSON(stdout, out); err != nil {
				return err
			}
		} else {
			renderIntegrationTest(stdout, name, out.OK, out.Result, out.Error)
		}
		if !out.OK {
			return fmt.Errorf("%s test failed: %s", name, out.Error)
		}
		return nil
	}
	if arg := a.arg(0); arg != "" && arg != "status" {
		return fmt.Errorf("unknown integrations subcommand %q (use status or test)", arg)
	}
	var view cliIntegrationsView
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/integrations", nil, &view); err != nil {
		return err
	}
	if a.flags.has("--json") {
		return dumpJSON(stdout, view)
	}
	renderIntegrations(stdout, view, false)
	return nil
}

func renderIntegrationTest(w io.Writer, name string, okRes bool, result json.RawMessage, errMsg string) {
	if !okRes {
		fmt.Fprintln(w, errText("✗ "+name)+" "+errMsg)
		return
	}
	fmt.Fprintln(w, ok("✓ "+name)+" "+muted("test passed"))
	var fields map[string]any
	if json.Unmarshal(result, &fields) == nil {
		keys := make([]string, 0, len(fields))
		for k := range fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if v := fmt.Sprint(fields[k]); v != "" {
				fmt.Fprintf(w, "  %s %s\n", muted(k+":"), v)
			}
		}
	}
}

// renderIntegrations prints the status view. compact (doctor) prints one line
// per configured integration and skips the unconfigured ones.
func renderIntegrations(w io.Writer, view cliIntegrationsView, compact bool) {
	shown := false
	for _, in := range view.Integrations {
		if !in.Configured {
			if !compact {
				fmt.Fprintln(w, muted("• "+in.Name+": not configured (integrations."+in.Name+" in harness.yaml)"))
			}
			continue
		}
		shown = true
		state := ok("✓ " + in.Name)
		detail := "active"
		switch {
		case !in.Active:
			state, detail = warn("✗ "+in.Name), "configured but not running"
		case len(in.Problems) > 0:
			state = warn("! " + in.Name)
		}
		var bits []string
		if in.Outbox != nil {
			bits = append(bits, fmt.Sprintf("%d delivered, %d pending, %d dropped", in.Outbox.Delivered, in.Outbox.Pending, in.Outbox.Dead))
		}
		if in.Socket != nil {
			if in.Socket.Connected {
				bits = append(bits, "socket connected")
			} else {
				bits = append(bits, "socket disconnected")
			}
		}
		if in.Name == "linear" && in.Active {
			bits = append(bits, fmt.Sprintf("%d linked issue(s)", in.LinkedIssues))
		}
		line := state + " " + muted(detail)
		if len(bits) > 0 {
			line += muted(" · " + strings.Join(bits, " · "))
		}
		fmt.Fprintln(w, line)
		for _, p := range in.Problems {
			fmt.Fprintln(w, "  "+warn("•")+" "+p)
		}
		if in.IngestError != "" {
			fmt.Fprintln(w, "  "+warn("•")+" reading the event log: "+in.IngestError)
		}
		if !compact {
			names := make([]string, 0, len(in.Tokens))
			for k := range in.Tokens {
				names = append(names, k)
			}
			sort.Strings(names)
			for _, k := range names {
				mark := ok("set")
				if !in.Tokens[k] {
					mark = warn("missing")
				}
				fmt.Fprintf(w, "  %s %s\n", muted(k+":"), mark)
			}
		}
	}
	if compact && !shown {
		fmt.Fprintln(w, muted("• integrations: none configured"))
	}
}
