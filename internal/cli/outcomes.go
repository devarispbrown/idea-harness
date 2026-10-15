package cli

import (
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
)

// Objectives & outcome metrics: `idea objectives`, `idea objective
// add|edit|link`, `idea metrics`, `idea metric add|edit|record|measure`.
//
// The CLI stays a thin client: these mirror the daemon's JSON with only the
// fields they print, and every rule (validation, attribution, drift) is the
// daemon's.

type cliObjective struct {
	ID             string   `json:"id"`
	Code           string   `json:"code"`
	Statement      string   `json:"statement"`
	Rationale      string   `json:"rationale,omitempty"`
	Status         string   `json:"status"`
	Owner          string   `json:"owner,omitempty"`
	RequirementIDs []string `json:"requirement_ids"`
	MetricIDs      []string `json:"metric_ids"`
}

type cliMetricStatus struct {
	Drift             string   `json:"drift"`
	Stale             bool     `json:"stale"`
	GuardrailBreached bool     `json:"guardrail_breached"`
	Achieved          bool     `json:"achieved"`
	Observations      int      `json:"observations"`
	Latest            *float64 `json:"latest"`
	EffectiveBaseline *float64 `json:"effective_baseline"`
	BaselineSource    string   `json:"baseline_source"`
	RegressedBy       []string `json:"regressed_by"`
	Reasons           []string `json:"reasons"`
}

type cliMetric struct {
	ID            string   `json:"id"`
	Code          string   `json:"code"`
	ObjectiveCode string   `json:"objective_code"`
	Name          string   `json:"name"`
	Unit          string   `json:"unit"`
	Direction     string   `json:"direction"`
	Baseline      *float64 `json:"baseline"`
	Target        float64  `json:"target"`
	Guardrail     *float64 `json:"guardrail"`
	Cadence       string   `json:"cadence"`
	Source        struct {
		Kind string `json:"kind"`
	} `json:"source"`
	Status *cliMetricStatus `json:"status"`
}

type cliOutcomes struct {
	RequirementDrift []struct {
		Detail string `json:"detail"`
	} `json:"requirement_drift"`
	Gaps struct {
		ObjectivesWithoutMetrics      []string `json:"objectives_without_metrics"`
		ObjectivesWithoutRequirements []string `json:"objectives_without_requirements"`
		RequirementsWithoutObjectives []string `json:"requirements_without_objectives"`
	} `json:"gaps"`
	Summary struct {
		Metrics   int `json:"metrics"`
		OnTrack   int `json:"on_track"`
		OffTrack  int `json:"off_track"`
		Regressed int `json:"regressed"`
		Stale     int `json:"stale"`
		NoData    int `json:"no_data"`
	} `json:"summary"`
}

// given reports whether a flag was on the command line at all -- a bool flag
// set, or a value flag passed (even as ""). cmdArgs.has answers only for bool
// flags, and an edit has to tell "--rationale ''" (clear it) from absent.
func (a cmdArgs) given(name string) bool {
	if a.flags.bools[name] {
		return true
	}
	_, ok := a.flags.values[name]
	return ok
}

func num(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func driftBadge(d string) string {
	switch d {
	case "on_track":
		return ok("on track")
	case "off_track":
		return errText("off track")
	case "regressed":
		return errText("regressed")
	case "stale":
		return warn("stale")
	default:
		return muted("no data")
	}
}

// outcomeRef is a metric/objective reference as a path segment.
func outcomeRef(projectID, iniID, collection, ref, rest string) string {
	p := initiativeResource(projectID, iniID, collection) + "/" + url.PathEscape(ref)
	if rest != "" {
		p += "/" + rest
	}
	return p
}

func cmdObjectives(a cmdArgs, stdout, _ io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional)
	if err != nil {
		return err
	}
	var objs []cliObjective
	if err := c.do("GET", initiativeResource(projectID, iniID, "objectives"), nil, &objs); err != nil {
		return err
	}
	if a.has("--json") {
		return dumpJSON(stdout, objs)
	}
	if len(objs) == 0 {
		fmt.Fprintln(stdout, muted("No objectives yet. The PRD states them; add one with ")+bold(`idea objective add "<outcome>"`))
		return nil
	}
	fmt.Fprintln(stdout, heading("Objectives"))
	for _, o := range objs {
		line := code(o.Code) + " " + o.Statement
		if o.Status != "active" {
			line += " " + muted("("+o.Status+")")
		}
		fmt.Fprintln(stdout, "  "+line)
		meta := fmt.Sprintf("%d requirement(s) · %d metric(s)", len(o.RequirementIDs), len(o.MetricIDs))
		if o.Owner != "" {
			meta += " · owner " + o.Owner
		}
		fmt.Fprintln(stdout, "      "+muted(meta))
		if o.Status == "active" && len(o.MetricIDs) == 0 {
			fmt.Fprintln(stdout, "      "+warn("no metric: `idea metric add "+o.Code+` "<name>" --target <n>`+"`"))
		}
	}
	return nil
}

// cmdObjective dispatches objective add|edit|link.
func cmdObjective(a cmdArgs, stdout, _ io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	switch a.sub {
	case "add":
		if len(a.positional) < 1 {
			return usageErrorf(`usage: idea objective add "<statement>" [--rationale <text>] [--owner <who>] [<initiative>]`)
		}
		iniID, err := resolveInitiative(c, projectID, a.positional[1:])
		if err != nil {
			return err
		}
		body := map[string]any{"statement": a.positional[0], "rationale": a.value("--rationale"), "owner": a.value("--owner")}
		var o cliObjective
		if err := c.do("POST", initiativeResource(projectID, iniID, "objectives"), body, &o); err != nil {
			return err
		}
		if a.has("--json") {
			return dumpJSON(stdout, o)
		}
		fmt.Fprintln(stdout, ok("✓ added ")+code(o.Code)+" "+o.Statement)
		fmt.Fprintln(stdout, "  "+muted("next: link requirements with `idea objective link <REQ> "+o.Code+"` and measure it with `idea metric add "+o.Code+` "<name>" --target <n>`+"`"))
		return nil
	case "edit":
		if len(a.positional) < 1 {
			return usageErrorf("usage: idea objective edit <OBJ> [--statement <text>] [--rationale <text>] [--owner <who>] [--status active|achieved|abandoned] [<initiative>]")
		}
		iniID, err := resolveInitiative(c, projectID, a.positional[1:])
		if err != nil {
			return err
		}
		body := map[string]any{}
		for flag, key := range map[string]string{"--statement": "statement", "--rationale": "rationale", "--owner": "owner", "--status": "status"} {
			if a.given(flag) {
				body[key] = a.value(flag)
			}
		}
		if len(body) == 0 {
			return usageErrorf("nothing to change: pass --statement, --rationale, --owner or --status")
		}
		var o cliObjective
		if err := c.do("PATCH", outcomeRef(projectID, iniID, "objectives", a.positional[0], ""), body, &o); err != nil {
			return err
		}
		if a.has("--json") {
			return dumpJSON(stdout, o)
		}
		fmt.Fprintln(stdout, ok("✓ updated ")+code(o.Code)+" "+o.Statement+" "+muted("("+o.Status+")"))
		return nil
	case "link":
		if len(a.positional) < 2 {
			return usageErrorf("usage: idea objective link <REQ> <OBJ> [--unlink] [<initiative>]")
		}
		iniID, err := resolveInitiative(c, projectID, a.positional[2:])
		if err != nil {
			return err
		}
		body := map[string]any{"requirement": a.positional[0], "unlink": a.has("--unlink")}
		var o cliObjective
		if err := c.do("POST", outcomeRef(projectID, iniID, "objectives", a.positional[1], "requirements"), body, &o); err != nil {
			return err
		}
		if a.has("--json") {
			return dumpJSON(stdout, o)
		}
		verb := "now serves"
		if a.has("--unlink") {
			verb = "no longer serves"
		}
		fmt.Fprintf(stdout, "%s %s %s %s %s\n", ok("✓"), code(strings.ToUpper(a.positional[0])), verb, code(o.Code), muted(fmt.Sprintf("(%d requirement(s) serve it)", len(o.RequirementIDs))))
		return nil
	}
	return usageErrorf("usage: idea objective add|edit|link")
}

func cmdMetrics(a cmdArgs, stdout, _ io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, a.positional)
	if err != nil {
		return err
	}
	var ms []cliMetric
	if err := c.do("GET", initiativeResource(projectID, iniID, "metrics"), nil, &ms); err != nil {
		return err
	}
	if a.has("--json") {
		return dumpJSON(stdout, ms)
	}
	if len(ms) == 0 {
		fmt.Fprintln(stdout, muted("No outcome metrics yet. Add one with ")+bold(`idea metric add <OBJ> "<name>" --target <n>`))
		return nil
	}
	fmt.Fprintln(stdout, heading("Outcome metrics"))
	for _, m := range ms {
		st := cliMetricStatus{Drift: "no_data"}
		if m.Status != nil {
			st = *m.Status
		}
		fmt.Fprintf(stdout, "  %s %s %s  %s\n", code(m.Code), m.Name, muted("("+m.ObjectiveCode+")"), driftBadge(st.Drift))
		fmt.Fprintln(stdout, "      "+muted(describeCLIMetric(m, st)))
		for _, r := range st.Reasons {
			if st.Drift != "on_track" {
				fmt.Fprintln(stdout, "      "+muted("· "+r))
			}
		}
	}
	return nil
}

func describeCLIMetric(m cliMetric, st cliMetricStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s", m.Direction)
	if m.Baseline != nil {
		fmt.Fprintf(&b, " from %s", num(*m.Baseline))
	}
	fmt.Fprintf(&b, " to %s%s", num(m.Target), unitSuffix(m.Unit))
	if m.Guardrail != nil {
		fmt.Fprintf(&b, " · guardrail %s", num(*m.Guardrail))
	}
	if st.Latest != nil {
		fmt.Fprintf(&b, " · latest %s%s (%d obs)", num(*st.Latest), unitSuffix(m.Unit), st.Observations)
	}
	if m.Cadence != "" {
		fmt.Fprintf(&b, " · %s", m.Cadence)
	}
	fmt.Fprintf(&b, " · %s", m.Source.Kind)
	return b.String()
}

func unitSuffix(u string) string {
	if u == "" {
		return ""
	}
	if u == "%" {
		return u
	}
	return " " + u
}

func parseNumberFlag(a cmdArgs, flag string) (*float64, error) {
	if !a.given(flag) {
		return nil, nil
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(a.value(flag)), 64)
	if err != nil {
		return nil, usageErrorf("%s must be a number, got %q", flag, a.value(flag))
	}
	return &v, nil
}

// metricBody builds the add/edit body from flags, only for flags present.
func metricBody(a cmdArgs) (map[string]any, error) {
	body := map[string]any{}
	for flag, key := range map[string]string{"--direction": "direction", "--unit": "unit", "--definition": "definition", "--cadence": "cadence", "--name": "name", "--objective": "objective"} {
		if a.given(flag) {
			body[key] = a.value(flag)
		}
	}
	for flag, key := range map[string]string{"--baseline": "baseline", "--target": "target", "--guardrail": "guardrail"} {
		v, err := parseNumberFlag(a, flag)
		if err != nil {
			return nil, err
		}
		if v != nil {
			body[key] = *v
		}
	}
	switch {
	case a.given("--command") && a.given("--url"):
		return nil, usageErrorf("a metric has one source: pass --command or --url, not both")
	case a.given("--command"):
		body["source"] = map[string]any{"kind": "command", "command": a.value("--command")}
	case a.given("--url"):
		body["source"] = map[string]any{"kind": "url", "url": a.value("--url"), "pointer": a.value("--pointer")}
	case a.has("--manual"):
		body["source"] = map[string]any{"kind": "manual"}
	}
	return body, nil
}

// cmdMetric dispatches metric add|edit|record|measure.
func cmdMetric(a cmdArgs, stdout, _ io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	switch a.sub {
	case "add":
		if len(a.positional) < 2 || !a.given("--target") {
			return usageErrorf(`usage: idea metric add <OBJ> "<name>" --target <n> [--direction increase|decrease|maintain] [--baseline <n>] [--guardrail <n>] [--unit <u>] [--cadence <c>] [--command <sh> | --url <url> [--pointer </json/pointer>]] [--hypothesis <text>] [<initiative>]`)
		}
		iniID, err := resolveInitiative(c, projectID, a.positional[2:])
		if err != nil {
			return err
		}
		body, err := metricBody(a)
		if err != nil {
			return err
		}
		body["objective"], body["name"] = a.positional[0], a.positional[1]
		if _, ok := body["direction"]; !ok {
			body["direction"] = "increase"
		}
		if a.given("--hypothesis") {
			body["hypothesis"] = a.value("--hypothesis")
		}
		var m cliMetric
		if err := c.do("POST", initiativeResource(projectID, iniID, "metrics"), body, &m); err != nil {
			return err
		}
		if a.has("--json") {
			return dumpJSON(stdout, m)
		}
		fmt.Fprintln(stdout, ok("✓ added ")+code(m.Code)+" "+m.Name+" "+muted("("+m.ObjectiveCode+": "+describeCLIMetric(m, cliMetricStatus{})+")"))
		return nil
	case "edit":
		if len(a.positional) < 1 {
			return usageErrorf("usage: idea metric edit <MET> [--name …] [--target <n>] [--baseline <n> | --clear-baseline] [--guardrail <n> | --clear-guardrail] [--direction …] [--cadence …] [--command … | --url … | --manual] [<initiative>]")
		}
		iniID, err := resolveInitiative(c, projectID, a.positional[1:])
		if err != nil {
			return err
		}
		body, err := metricBody(a)
		if err != nil {
			return err
		}
		if a.has("--clear-baseline") {
			body["clear_baseline"] = true
		}
		if a.has("--clear-guardrail") {
			body["clear_guardrail"] = true
		}
		if len(body) == 0 {
			return usageErrorf("nothing to change: pass at least one flag (see `idea metric --help`)")
		}
		var m cliMetric
		if err := c.do("PATCH", outcomeRef(projectID, iniID, "metrics", a.positional[0], ""), body, &m); err != nil {
			return err
		}
		if a.has("--json") {
			return dumpJSON(stdout, m)
		}
		drift := "no_data"
		if m.Status != nil {
			drift = m.Status.Drift
		}
		fmt.Fprintln(stdout, ok("✓ updated ")+code(m.Code)+" "+m.Name+"  "+driftBadge(drift))
		return nil
	case "record":
		if len(a.positional) < 2 {
			return usageErrorf("usage: idea metric record <MET> <value> [--note <text>] [--at <RFC3339>] [<initiative>]")
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(a.positional[1]), 64)
		if err != nil {
			return usageErrorf("value must be a number, got %q", a.positional[1])
		}
		iniID, err := resolveInitiative(c, projectID, a.positional[2:])
		if err != nil {
			return err
		}
		body := map[string]any{"value": v, "note": a.value("--note")}
		if a.given("--at") {
			body["observed_at"] = a.value("--at")
		}
		var res struct {
			Observation struct {
				ChangeSetIDs []string `json:"change_set_ids"`
			} `json:"observation"`
			Status cliMetricStatus `json:"status"`
		}
		if err := c.do("POST", outcomeRef(projectID, iniID, "metrics", a.positional[0], "observations"), body, &res); err != nil {
			return err
		}
		if a.has("--json") {
			return dumpJSON(stdout, res)
		}
		fmt.Fprintf(stdout, "%s %s = %s  %s\n", ok("✓ recorded"), code(strings.ToUpper(a.positional[0])), num(v), driftBadge(res.Status.Drift))
		if n := len(res.Observation.ChangeSetIDs); n > 0 {
			fmt.Fprintln(stdout, "  "+muted(fmt.Sprintf("%d change set(s) landed since the previous observation; `idea trace metric %s` shows which", n, strings.ToUpper(a.positional[0]))))
		}
		if res.Status.Drift != "on_track" && res.Status.Drift != "no_data" {
			for _, r := range res.Status.Reasons {
				fmt.Fprintln(stdout, "  "+muted("· "+r))
			}
		}
		return nil
	case "measure":
		iniArgs := a.positional
		ref := ""
		if len(a.positional) > 0 && strings.HasPrefix(strings.ToUpper(a.positional[0]), "MET-") {
			ref, iniArgs = a.positional[0], a.positional[1:]
		}
		iniID, err := resolveInitiative(c, projectID, iniArgs)
		if err != nil {
			return err
		}
		var body any
		if ref != "" {
			body = map[string]string{"metric": ref}
		}
		var res struct {
			Results []struct {
				MetricCode  string           `json:"metric_code"`
				Observation *struct{ Value float64 } `json:"observation"`
				Status      *cliMetricStatus `json:"status"`
				Skipped     string           `json:"skipped"`
				Error       string           `json:"error"`
			} `json:"results"`
		}
		if err := c.do("POST", initiativeResource(projectID, iniID, "metrics/measure"), body, &res); err != nil {
			return err
		}
		if a.has("--json") {
			return dumpJSON(stdout, res)
		}
		failed := 0
		for _, r := range res.Results {
			switch {
			case r.Observation != nil:
				drift := "no_data"
				if r.Status != nil {
					drift = r.Status.Drift
				}
				fmt.Fprintf(stdout, "%s %s = %s  %s\n", ok("✓"), code(r.MetricCode), num(r.Observation.Value), driftBadge(drift))
			case r.Skipped != "":
				fmt.Fprintf(stdout, "%s %s %s\n", muted("·"), code(r.MetricCode), muted("skipped: "+r.Skipped))
			default:
				failed++
				fmt.Fprintf(stdout, "%s %s %s\n", errText("✗"), code(r.MetricCode), r.Error)
			}
		}
		if len(res.Results) == 0 {
			fmt.Fprintln(stdout, muted("No metrics to measure."))
		}
		if failed > 0 {
			return errorf(kindRunFailed, "%d measurement(s) failed", failed)
		}
		return nil
	}
	return usageErrorf("usage: idea metric add|edit|record|measure")
}

// printOutcomeNote prints one line of outcome drift for an initiative, when
// there is any -- `idea status` and `idea next` both call it. Best-effort: a
// daemon without the route, or an initiative with no metrics, prints nothing.
func printOutcomeNote(c *client, projectID, iniID, indent string, stdout io.Writer) {
	var rep cliOutcomes
	if err := c.do("GET", initiativeResource(projectID, iniID, "outcomes"), nil, &rep); err != nil {
		return
	}
	s := rep.Summary
	if s.Metrics > 0 {
		parts := []string{fmt.Sprintf("%d metric(s)", s.Metrics)}
		add := func(n int, label string, style func(string) string) {
			if n > 0 {
				parts = append(parts, style(fmt.Sprintf("%d %s", n, label)))
			}
		}
		add(s.OnTrack, "on track", ok)
		add(s.OffTrack, "off track", errText)
		add(s.Regressed, "regressed", errText)
		add(s.Stale, "stale", warn)
		add(s.NoData, "no data", muted)
		fmt.Fprintln(stdout, indent+muted("outcomes: ")+strings.Join(parts, muted(" · ")))
	}
	for _, d := range rep.RequirementDrift {
		fmt.Fprintln(stdout, indent+warn("works, but fails its objective: ")+d.Detail)
	}
	if g := rep.Gaps; len(g.ObjectivesWithoutMetrics) > 0 {
		fmt.Fprintln(stdout, indent+muted("no metric yet: "+strings.Join(g.ObjectivesWithoutMetrics, ", ")))
	}
}
