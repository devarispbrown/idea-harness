package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// ---------- shared API response types ----------

type cliInitiative struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	// The only thing that matters about a failed initiative, and the machine
	// path did not carry it: `idea status` printed the provider's 401 in full
	// while `idea status --json` returned {id,title,status} and nothing else,
	// so a script could tell that something failed but never why. The daemon
	// has always sent it (domain.Initiative.FailureReason, and the spec's
	// Initiative schema declares it); the CLI struct dropped it on the floor.
	FailureReason string `json:"failure_reason,omitempty"`
}

type cliUnderstanding struct {
	Title        string           `json:"title"`
	Status       string           `json:"status"`
	Assertions   []cliAssertion   `json:"assertions"`
	Requirements []cliRequirement `json:"requirements"`
	Decisions    []cliDecision    `json:"decisions"`
	Questions    []cliQuestion    `json:"questions"`
}

type cliAssertion struct {
	Type       string `json:"type"`
	Statement  string `json:"statement"`
	Confidence string `json:"confidence"`
	Source     string `json:"source"`
}

type cliRequirement struct {
	Code      string `json:"code"`
	Statement string `json:"statement"`
	Priority  string `json:"priority"`
}

type cliDecision struct {
	ID             string   `json:"id"`
	Code           string   `json:"code"`
	Question       string   `json:"question"`
	Recommendation string   `json:"recommendation"`
	Options        []string `json:"options"`
	Status         string   `json:"status"`
}

type cliQuestion struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	Status   string `json:"status"`
}

type cliGuidance struct {
	Action   string   `json:"action"`
	Why      string   `json:"why"`
	Affected []string `json:"affected"`
}

// cliTimelineEntry mirrors one entry of GET .../timeline (C1, wave 3
// contract).
type cliTimelineEntry struct {
	At     string `json:"at"`
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

type cliFinding struct {
	ID             string `json:"id"`
	Severity       string `json:"severity"`
	Confidence     string `json:"confidence"`
	Category       string `json:"category"`
	Finding        string `json:"finding"`
	Recommendation string `json:"recommendation"`
	Disposition    string `json:"disposition"`
}

func statusBadge(s string) string {
	switch s {
	case "approved":
		return ok("● approved")
	case "needs_human_judgment":
		return warn("● needs your judgment")
	case "running":
		return accent("● running")
	case "failed":
		return errText("● failed")
	default:
		return muted("● " + s)
	}
}

// cmdStatus shows initiative + current work.
func cmdStatus(a cmdArgs, stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var inis []cliInitiative
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/initiatives", nil, &inis); err != nil {
		return err
	}
	// docs/cli.md promises every initiative-scoped command takes an optional
	// reference, and uses THIS command as its worked example -- while cmdStatus
	// read no positional at all, so `idea status <id>` and `idea status ZZZZ`
	// both printed every initiative and exited 0, the second also contradicting
	// the promise that an unknown argument is an error.
	//
	// Unscoped it stays the overview, which is what its help line says and what
	// an overview command should do.
	if len(a.positional) > 0 {
		iniID, err := resolveInitiative(c, projectID, a.positional)
		if err != nil {
			return err
		}
		kept := inis[:0:0]
		for _, ini := range inis {
			if ini.ID == iniID {
				kept = append(kept, ini)
			}
		}
		inis = kept
	}
	if a.has("--json") {
		// Still an array, filtered. docs/cli.md documents the shape and ledger
		// row G4 pins that sentence, so returning an object here would be an
		// undeclared response change and would break `jq '.[0]'`. Adding a field
		// is not that: `failure_reason` is omitempty, so every existing shape is
		// byte-identical and only a failed initiative gains a key.
		if err := dumpJSON(stdout, inis); err != nil {
			return err
		}
		return failedInitiativeExit(inis)
	}
	if isDemoMode(c, projectID) {
		fmt.Fprintln(stdout, warn("demo mode — synthetic output. Configure a model with:")+" "+bold("idea model set <provider/model>"))
		fmt.Fprintln(stdout)
	}
	if len(inis) == 0 {
		fmt.Fprintln(stdout, muted("No initiatives yet. Run ")+bold("idea new")+muted(" to start."))
		return nil
	}
	for _, ini := range inis {
		fmt.Fprintln(stdout, bold(ini.Title)+"  "+statusBadge(ini.Status))
		// Understanding + next move for context.
		var u cliUnderstanding
		if err := c.do("GET", initiativeResource(projectID, ini.ID, "understanding"), nil, &u); err == nil {
			fmt.Fprintf(stdout, "  %s %d · %s %d · %s %d\n",
				muted("requirements:"), len(u.Requirements),
				muted("decisions:"), len(u.Decisions),
				muted("questions:"), len(u.Questions))
		}
		// Total spend and time (C3, wave 3 contract). Best-effort: a costs read
		// failure (e.g. an older daemon with no such route) never blocks status.
		var costs cliCosts
		if err := c.do("GET", initiativeResource(projectID, ini.ID, "costs"), nil, &costs); err == nil {
			fmt.Fprintln(stdout, "  "+muted(costs.summary()))
		}
		var nm cliGuidance
		if err := c.do("GET", initiativeResource(projectID, ini.ID, "guidance"), nil, &nm); err == nil && nm.Action != "" {
			fmt.Fprintln(stdout, "  "+accent("→ ")+nm.Action)
			if nm.Why != "" {
				fmt.Fprintln(stdout, "    "+muted(nm.Why))
			}
		}
		fmt.Fprintln(stdout)
	}
	return failedInitiativeExit(inis)
}

// cliCosts mirrors GET .../costs (C3, wave 3 contract).
type cliCosts struct {
	TotalUSD float64 `json:"total_usd"`
	Agent    struct {
		Runs    int      `json:"runs"`
		Seconds float64  `json:"seconds"`
		USD     *float64 `json:"usd"`
		Note    string   `json:"note"`
	} `json:"agent"`
	ByPhase []struct {
		Seconds float64 `json:"seconds"`
	} `json:"by_phase"`
}

// summary renders "spend: $0.1234 · time: 2m" for `idea status`. Time sums
// every phase's seconds plus the agent's; TotalUSD is null-safe by
// construction (InitiativeCosts.TotalUSD only ever adds a measured agent USD,
// never a null one) -- but that made TotalUSD read as a genuine grand total
// even when the coding agent actually ran and its own spend just could not be
// parsed from its output (review defect #5: /costs correctly reported
// agent.usd null with agent.note "agent spend not measured" for six real
// Sonnet runs, and this command dropped both and printed a bare "spend:
// $0.0032" -- the model-phase cost alone, with nothing to say the run's real
// (larger) spend was not in it). When there were real agent runs and their
// cost is not measured, the qualifier is now printed alongside the number,
// every time -- not just when Note happens to be set, since a daemon budget
// tighter than this one's is exactly when a silent undercount matters most.
func (c cliCosts) summary() string {
	seconds := c.Agent.Seconds
	for _, p := range c.ByPhase {
		seconds += p.Seconds
	}
	d := time.Duration(seconds * float64(time.Second)).Round(time.Second)
	spend := fmt.Sprintf("spend: $%.4f", c.TotalUSD)
	if c.Agent.Runs > 0 && c.Agent.USD == nil {
		note := c.Agent.Note
		if note == "" {
			note = "agent spend not measured"
		}
		spend = fmt.Sprintf("%s (model phases only; %s)", spend, note)
	}
	return fmt.Sprintf("%s · time: %s", spend, d)
}

// failedInitiativeExit makes `idea status` exit non-zero when it has just
// printed a failed initiative.
//
// `idea status` rendered `● failed` together with the provider's full error and
// returned 0, so a script that ran it as a health check could not tell. Audit 8
// counted 18 commands exiting 0 over a reported or concealed failure; this is
// the one that reports the failure in full and is most often scripted.
//
// Deliberately only `failed`. `needs_human_judgment` is the loop working as
// designed -- it is waiting on a person, not broken -- and treating it as an
// error would make `idea next`, `idea reviews` and the whole judgment path exit
// non-zero on their happy path. The full exit-code taxonomy is a separate piece
// of work (docs/plans/deferred.md, audit 8 / E2).
func failedInitiativeExit(inis []cliInitiative) error {
	// The newest only, which is the one every other command means by "the
	// initiative" -- ListInitiatives is ORDER BY created_at ASC and
	// resolveInitiative takes the last row. When scoped, the list is already
	// filtered to one, so this is that one.
	//
	// Scanning them all instead made the exit code STICKY: a single abandoned
	// experiment from weeks ago made the project's overview command exit 1
	// forever, with nothing a user could do to clear it. An overview of ten
	// initiatives has no single status, and the exit code has to mean the
	// thing the next command will act on.
	if len(inis) == 0 {
		return nil
	}
	ini := inis[len(inis)-1]
	if ini.Status != "failed" {
		return nil
	}
	if ini.FailureReason != "" {
		return fmt.Errorf("%s failed: %s", ini.Title, ini.FailureReason)
	}
	return fmt.Errorf("%s failed; `idea logs` has the full text", ini.Title)
}

// cmdChanged shows What Changed since last look, per initiative, in human
// language (C1, wave 3 contract). Rewritten onto GET .../timeline: the old
// GET .../changed it used read the whole project's events with no initiative
// filter, so one initiative's "changed" could report another's counts, and
// narrated only raw event type names for anything it did not special-case
// (JTBD 6).
func cmdChanged(a cmdArgs, stdout, stderr io.Writer) error {
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	var inis []cliInitiative
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/initiatives", nil, &inis); err != nil {
		return err
	}
	if len(inis) == 0 {
		fmt.Fprintln(stdout, muted("No changes yet."))
		return nil
	}
	// inis[0] was the OLDEST -- ListInitiatives is ORDER BY created_at ASC --
	// which is the direct opposite of the "most recently created" default
	// docs/cli.md promises. And like cmdStatus it is declared maxPositional: 1
	// and read no positional, so its documented reference was ignored too.
	iniID, err := resolveInitiative(c, projectID, a.positional)
	if err != nil {
		return err
	}
	var entries []cliTimelineEntry
	if err := c.do("GET", initiativeResource(projectID, iniID, "timeline"), nil, &entries); err != nil {
		return err
	}
	if a.has("--json") {
		return dumpJSON(stdout, entries)
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, muted("No changes yet."))
		return nil
	}
	fmt.Fprintln(stdout, heading("Since you last looked"))
	for _, e := range entries {
		line := "  · " + e.Title
		if e.Detail != "" {
			line += " — " + e.Detail
		}
		fmt.Fprintln(stdout, line)
	}
	return nil
}

// cmdDecisions lists decisions.
func cmdDecisions(a cmdArgs, stdout, stderr io.Writer) error {
	positional := a.positional
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	// Resolved here rather than left to the daemon: the flat route's
	// server-side fallback is going away, and "the initiative you are working
	// on" is a CLI notion, not something the API should guess at.
	iniID, err := resolveInitiative(c, projectID, positional)
	if err != nil {
		return err
	}
	var decisions []cliDecision
	if err := c.do("GET", initiativeResource(projectID, iniID, "decisions"), nil, &decisions); err != nil {
		return err
	}
	if a.has("--json") {
		return dumpJSON(stdout, decisions)
	}
	open := 0
	for _, d := range decisions {
		if d.Status == "open" {
			open++
		}
	}
	if len(decisions) == 0 {
		fmt.Fprintln(stdout, muted("No decisions yet."))
		return nil
	}
	fmt.Fprintln(stdout, heading("Decisions")+muted(fmt.Sprintf("  (%d open)", open)))
	for _, d := range decisions {
		marker := "  "
		if d.Status == "open" {
			marker = warn("● ")
		} else {
			marker = ok("✓ ")
		}
		fmt.Fprintln(stdout, marker+code(d.Code)+" "+d.Question)
		if d.Recommendation != "" {
			fmt.Fprintln(stdout, "      "+muted("recommendation: ")+d.Recommendation)
		}
	}
	return nil
}

// cmdReviews lists review findings.
func cmdReviews(a cmdArgs, stdout, stderr io.Writer) error {
	positional := a.positional
	c, projectID, err := connect()
	if err != nil {
		return err
	}
	iniID, err := resolveInitiative(c, projectID, positional)
	if err != nil {
		return err
	}
	var findings []cliFinding
	if err := c.do("GET", initiativeResource(projectID, iniID, "findings"), nil, &findings); err != nil {
		return err
	}
	if a.has("--json") {
		return dumpJSON(stdout, findings)
	}
	if len(findings) == 0 {
		fmt.Fprintln(stdout, ok("No open findings."))
		return nil
	}
	fmt.Fprintln(stdout, heading("Review findings"))
	for _, f := range findings {
		sev := f.Severity
		switch sev {
		case "critical", "high":
			sev = errText(sev)
		case "medium":
			sev = warn(sev)
		default:
			sev = muted(sev)
		}
		fmt.Fprintln(stdout, "  "+sev+" "+f.Finding)
		if f.Recommendation != "" {
			fmt.Fprintln(stdout, "      "+muted("→ ")+f.Recommendation)
		}
	}
	return nil
}

func dumpJSON(w io.Writer, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(w, string(data))
	return nil
}

var _ = strings.TrimSpace
