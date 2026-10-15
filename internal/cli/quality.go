package cli

import (
	"fmt"
	"io"
	"strings"
)

// cliQualityCitation, cliQualityCheck, cliQualityResult and cliScorecard
// mirror the daemon's quality scorecard (GET/POST .../quality, and the
// `quality` field of POST .../verify).
type cliQualityCitation struct {
	Path        string `json:"path,omitempty"`
	Line        int    `json:"line,omitempty"`
	Requirement string `json:"requirement,omitempty"`
}

type cliQualityCheck struct {
	Name      string               `json:"name"`
	Status    string               `json:"status"`
	Method    string               `json:"method"`
	Detail    string               `json:"detail,omitempty"`
	Citations []cliQualityCitation `json:"citations,omitempty"`
}

type cliQualityResult struct {
	ID           string             `json:"id"`
	Status       string             `json:"status"`
	Summary      string             `json:"summary"`
	Checks       []cliQualityCheck  `json:"checks"`
	Metrics      map[string]float64 `json:"metrics,omitempty"`
	Requirements []string           `json:"requirements"`
}

type cliQualityDimension struct {
	Dimension string            `json:"dimension"`
	Mode      string            `json:"mode"`
	Result    *cliQualityResult `json:"result"`
	Stale     bool              `json:"stale"`
}

type cliScorecard struct {
	Rigor            string                `json:"rigor"`
	Overridden       []string              `json:"overridden"`
	Dimensions       []cliQualityDimension `json:"dimensions"`
	InputDigest      string                `json:"input_digest"`
	NeedsMeasurement bool                  `json:"needs_measurement"`
	GateUnmet        []string              `json:"gate_unmet"`
}

// measureQualityIfStale measures the quality dimensions when the scorecard
// says an enabled dimension has no result for the current applied work, and
// waits for it. It is best-effort about the READ: a daemon that predates the
// quality endpoint answers 404, and verify then runs exactly as before. A
// measurement that starts and fails is an error -- the gate cannot be told
// about a dimension that was never measured.
func measureQualityIfStale(c *client, projectID, iniID string, progress io.Writer) error {
	var sc cliScorecard
	if err := c.do("GET", initiativeResource(projectID, iniID, "quality"), nil, &sc); err != nil {
		return nil
	}
	if !sc.NeedsMeasurement {
		return nil
	}
	var acc struct {
		OperationID string `json:"operation_id"`
	}
	if err := c.do("POST", initiativeResource(projectID, iniID, "quality"), nil, &acc); err != nil {
		return err
	}
	fmt.Fprintln(progress, muted("measuring quality dimensions… (")+bold("--no-measure")+muted(" uses the last recorded scorecard)"))
	return waitForOperation(c, projectID, iniID, acc.OperationID, progress)
}

// renderScorecard prints the quality scorecard: one line per enabled
// dimension (mode, status, summary), the first citation of each check that
// is not passing, and the off dimensions on one line.
func renderScorecard(w io.Writer, sc *cliScorecard) {
	if sc == nil || len(sc.Dimensions) == 0 {
		return
	}
	var off []string
	shown := false
	for _, d := range sc.Dimensions {
		if d.Mode == "off" {
			off = append(off, d.Dimension)
			continue
		}
		if !shown {
			fmt.Fprintln(w, heading("Quality")+" "+muted("(rigor "+sc.Rigor+")"))
			shown = true
		}
		status := "not measured"
		summary := "never measured — `idea verify` measures it"
		if d.Result != nil {
			status = strings.ReplaceAll(d.Result.Status, "_", " ")
			summary = d.Result.Summary
		}
		if d.Stale {
			status += " (stale)"
		}
		fmt.Fprintf(w, "  %-22s %-8s %s  %s\n", d.Dimension, d.Mode, styleStatus(status), muted(summary))
		if d.Result == nil {
			continue
		}
		for _, c := range d.Result.Checks {
			if c.Status == "pass" || c.Status == "not_applicable" {
				continue
			}
			where := ""
			if len(c.Citations) > 0 {
				ct := c.Citations[0]
				switch {
				case ct.Path != "" && ct.Line > 0:
					where = fmt.Sprintf(" %s:%d", ct.Path, ct.Line)
				case ct.Path != "":
					where = " " + ct.Path
				}
				if ct.Requirement != "" {
					where += " (" + ct.Requirement + ")"
				}
			}
			first, _, _ := strings.Cut(c.Detail, "\n")
			fmt.Fprintf(w, "      %s %s%s %s\n", muted(strings.ReplaceAll(c.Status, "_", " ")+":"), c.Name, code(where), muted("["+c.Method+"] "+first))
		}
	}
	if len(off) > 0 && shown {
		fmt.Fprintln(w, "  "+muted("off: "+strings.Join(off, ", ")))
	}
}

func styleStatus(s string) string {
	switch {
	case strings.HasPrefix(s, "pass"), strings.HasPrefix(s, "not applicable"):
		return ok(s)
	case strings.HasPrefix(s, "fail"):
		return errText(s)
	default:
		return warn(s)
	}
}
