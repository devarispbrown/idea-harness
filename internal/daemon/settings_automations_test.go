package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/integrations"
	"github.com/devarispbrown/idea-harness/internal/integrations/linear"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// setHarness rewrites a test project's harness.yaml through the real write
// path and reloads it.
func setHarness(t *testing.T, rt *Runtime, projectID string, mutate func(h *config.HarnessConfig)) *projectRuntime {
	t.Helper()
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(pr.rootPath, config.Dir)
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&cfg.Harness)
	if err := config.WriteHarness(dir, cfg.Harness); err != nil {
		t.Fatal(err)
	}
	pr, err = rt.ReloadProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	return pr
}

func eventsOfType(t *testing.T, db *store.DB, eventType string) []store.EventRow {
	t.Helper()
	rows, err := db.ListEvents(context.Background(), 0, 5000)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.EventRow
	for _, r := range rows {
		if r.EventType == eventType {
			out = append(out, r)
		}
	}
	return out
}

// A design-phase run that reaches its cap is parked, not failed; the stop is
// announced on the integration bus as initiative.budget_exceeded (and not a
// second time as needs_judgment); raising the cap only part of the way leaves
// it parked, and raising it enough returns it -- here to `failed`, because a
// brand-new initiative has nowhere earlier to go, with `idea retry` named.
func TestBudgetCapParksADesignRunAndRaisingItResumes(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)
	pr := setHarness(t, rt, projectID, func(h *config.HarnessConfig) { h.Demo.CostPerCallUSD = 0.10 })
	base := ts.URL + "/api/v1/projects/" + projectID

	var set struct {
		DefaultInitiativeUSD float64 `json:"default_initiative_usd"`
	}
	decodeInto(t, doReq(t, http.MethodPut, base+"/budget", token, "", `{"default_initiative_usd":0.15}`), http.StatusOK, &set)
	if set.DefaultInitiativeUSD != 0.15 {
		t.Fatalf("PUT /budget = %+v", set)
	}
	decodeInto(t, doReq(t, http.MethodPut, base+"/budget", token, "", `{"default_initiative_usd":-1}`), http.StatusBadRequest, nil)

	var created struct {
		InitiativeID string `json:"initiative_id"`
		OperationID  string `json:"operation_id"`
	}
	decodeInto(t, doReq(t, http.MethodPost, base+"/initiatives", token, "", `{"title":"todo","description":"a todo app"}`), http.StatusAccepted, &created)
	op := waitOperation(t, ts.URL, token, created.OperationID)
	if op.Status != domain.OperationFailed || !strings.Contains(op.Error, "budget exceeded") {
		t.Fatalf("operation = %s %q, want failed with the budget stop", op.Status, op.Error)
	}
	pr, _ = rt.EnsureProject(t.Context(), projectID)
	ini, err := pr.db.GetInitiative(t.Context(), created.InitiativeID)
	if err != nil {
		t.Fatal(err)
	}
	if ini.Status != domain.InitiativeNeedsHumanJudgment || ini.JudgmentReason != domain.JudgmentBudget {
		t.Fatalf("initiative = %s/%s, want needs_human_judgment/budget_exceeded", ini.Status, ini.JudgmentReason)
	}

	// The bus publishes the stop once, as its own kind.
	stops := eventsOfType(t, pr.db, store.BudgetExceededEvent)
	if len(stops) != 1 {
		t.Fatalf("%d budget_exceeded events, want 1", len(stops))
	}
	evs, err := integrations.Translate(t.Context(), pr.db, projectID, stops[0])
	if err != nil || len(evs) != 1 || evs[0].Kind != integrations.KindBudgetExceeded || !strings.Contains(evs[0].Reason, "of $0.15") {
		t.Fatalf("translated = %+v (%v)", evs, err)
	}
	for _, row := range eventsOfType(t, pr.db, "initiative.status_changed") {
		if row.Operation == "needs_human_judgment:budget_exceeded" {
			if evs, _ := integrations.Translate(t.Context(), pr.db, projectID, row); len(evs) != 0 {
				t.Fatalf("the park also published %v; the budget event already says it", evs[0].Kind)
			}
		}
	}

	var next struct {
		Action string `json:"action"`
	}
	decodeInto(t, doReq(t, http.MethodGet, base+"/initiatives/"+ini.ID+"/next", token, "", ""), http.StatusOK, &next)
	if next.Action != "resolve budget" {
		t.Fatalf("next = %q, want resolve budget", next.Action)
	}

	type setResp struct {
		Budget struct {
			SpentUSD float64 `json:"spent_usd"`
			Parked   bool    `json:"parked"`
			Exceeded bool    `json:"exceeded"`
			Source   string  `json:"source"`
		} `json:"budget"`
		Resumed bool `json:"resumed"`
	}
	var raised setResp
	decodeInto(t, doReq(t, http.MethodPut, base+"/initiatives/"+ini.ID+"/budget", token, "", `{"cap_usd":0.18}`), http.StatusOK, &raised)
	if raised.Resumed || !raised.Budget.Parked || !raised.Budget.Exceeded {
		t.Fatalf("a cap still under the spend must leave it parked: %+v", raised)
	}
	decodeInto(t, doReq(t, http.MethodPut, base+"/initiatives/"+ini.ID+"/budget", token, "", `{"cap_usd":0}`), http.StatusBadRequest, nil)
	decodeInto(t, doReq(t, http.MethodPut, base+"/initiatives/"+ini.ID+"/budget", token, "", `{}`), http.StatusBadRequest, nil)
	decodeInto(t, doReq(t, http.MethodPut, base+"/initiatives/"+ini.ID+"/budget", token, "", `{"cap_usd":10}`), http.StatusOK, &raised)
	if !raised.Resumed || raised.Budget.Source != "initiative" {
		t.Fatalf("raising the cap past the spend should resume: %+v", raised)
	}
	ini, _ = pr.db.GetInitiative(t.Context(), ini.ID)
	if ini.Status != domain.InitiativeFailed || !strings.Contains(ini.FailureReason, "idea retry") {
		t.Fatalf("resumed to %s (%q), want failed naming idea retry", ini.Status, ini.FailureReason)
	}
	// And retry now runs to the end under the raised cap.
	var retried struct {
		OperationID string `json:"operation_id"`
	}
	decodeInto(t, doReq(t, http.MethodPost, base+"/initiatives/"+ini.ID+"/retry", token, "", `{}`), http.StatusAccepted, &retried)
	if op := waitOperation(t, ts.URL, token, retried.OperationID); op.Status != domain.OperationSucceeded {
		t.Fatalf("retry after raising the cap: %s %s", op.Status, op.Error)
	}
}

// An execute that reaches its cap -- the planner's call spends past it, so
// the agent is never started -- is parked FROM plan_approved and goes back
// there when the cap is raised.
func TestBudgetCapParksAnExecuteAndReturnsItToPlanApproved(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)
	pr := setHarness(t, rt, projectID, func(h *config.HarnessConfig) {
		h.Demo.CostPerCallUSD = 0.10
		h.Executor.Kind = config.ExecutorInternal
	})
	ctx := t.Context()
	ini := &domain.Initiative{Title: "x", Description: "x", Status: domain.InitiativePlanApproved}
	if err := pr.db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if err := pr.db.CreateTask(ctx, &domain.Task{InitiativeID: ini.ID, Title: "Greet", Statement: "greet", Priority: domain.PriorityMust}, "", nil); err != nil {
		t.Fatal(err)
	}
	base := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + ini.ID
	decodeInto(t, doReq(t, http.MethodPut, base+"/budget", token, "", `{"cap_usd":0.05}`), http.StatusOK, nil)

	var started struct {
		OperationID string `json:"operation_id"`
	}
	decodeInto(t, doReq(t, http.MethodPost, base+"/execute", token, "", `{}`), http.StatusAccepted, &started)
	op := waitOperation(t, ts.URL, token, started.OperationID)
	if op.Status != domain.OperationFailed || !strings.Contains(op.Error, "internal agent run") {
		t.Fatalf("execute = %s %q, want stopped before the agent run", op.Status, op.Error)
	}
	got, _ := pr.db.GetInitiative(ctx, ini.ID)
	if got.Status != domain.InitiativeNeedsHumanJudgment || got.JudgmentReason != domain.JudgmentBudget {
		t.Fatalf("initiative = %s/%s", got.Status, got.JudgmentReason)
	}
	// Execute is refused while parked: the precondition names the cap.
	resp := doReq(t, http.MethodPost, base+"/execute", token, "", `{}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("execute while parked = %d, want 409", resp.StatusCode)
	}
	var raised struct {
		Resumed bool `json:"resumed"`
	}
	decodeInto(t, doReq(t, http.MethodPut, base+"/budget", token, "", `{"clear":true}`), http.StatusOK, &raised)
	if !raised.Resumed {
		t.Fatal("clearing the cap (no project default) should resume")
	}
	got, _ = pr.db.GetInitiative(ctx, ini.ID)
	if got.Status != domain.InitiativePlanApproved {
		t.Fatalf("resumed to %s, want plan_approved", got.Status)
	}
}

// Integrations are edited through the API: the section is validated like a
// hand edit, written with the file's comments kept, applied without a
// restart, and a token pasted into any field is refused.
func TestIntegrationConfigWriteRoundTripAndRefusals(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)
	pr, _ := rt.EnsureProject(t.Context(), projectID)
	path := filepath.Join(pr.rootPath, config.Dir, "harness.yaml")
	orig, _ := os.ReadFile(path)
	if err := os.WriteFile(path, append([]byte("# owned by the platform team\n"), orig...), 0o644); err != nil {
		t.Fatal(err)
	}
	url := ts.URL + "/api/v1/projects/" + projectID + "/integrations/"

	decodeInto(t, doReq(t, http.MethodPut, url+"slack", token, "",
		`{"config":{"channel":"C0123ABC","events":["initiative.needs_judgment","initiative.budget_exceeded"],"mention_on_judgment":["here"],"allowed_users":["U012ABC"]}}`),
		http.StatusOK, nil)
	decodeInto(t, doReq(t, http.MethodPut, url+"linear", token, "",
		`{"config":{"team_key":"ENG","inbound_label":"idea","poll_interval_seconds":120,"state_mapping":{"done":"Shipped"}}}`),
		http.StatusOK, nil)
	written, _ := os.ReadFile(path)
	for _, want := range []string{"# owned by the platform team", "channel: C0123ABC", "poll_interval_seconds: 120", "done: Shipped"} {
		if !strings.Contains(string(written), want) {
			t.Errorf("harness.yaml lacks %q:\n%s", want, written)
		}
	}

	var view struct {
		Integrations []struct {
			Name      string          `json:"name"`
			Config    json.RawMessage `json:"config"`
			Tokens    map[string]bool `json:"tokens"`
			TokenEnvs []string        `json:"token_envs"`
		} `json:"integrations"`
		EventKinds []string `json:"event_kinds"`
	}
	decodeInto(t, doReq(t, http.MethodGet, ts.URL+"/api/v1/projects/"+projectID+"/integrations", token, "", ""), http.StatusOK, &view)
	for _, in := range view.Integrations {
		switch in.Name {
		case "slack":
			if !strings.Contains(string(in.Config), `"channel":"C0123ABC"`) {
				t.Errorf("slack config = %s", in.Config)
			}
			if set, ok := in.Tokens["SLACK_BOT_TOKEN"]; !ok || set {
				t.Errorf("tokens = %v, want SLACK_BOT_TOKEN reported unset", in.Tokens)
			}
		case "linear":
			if !strings.Contains(string(in.Config), `"team_key":"ENG"`) {
				t.Errorf("linear config = %s", in.Config)
			}
		}
	}
	if len(view.EventKinds) != len(config.IntegrationEventKinds) {
		t.Errorf("event_kinds = %v", view.EventKinds)
	}

	for name, body := range map[string]string{
		"slack: a user that is not a Slack id": `{"config":{"channel":"C1","allowed_users":["bob"]}}`,
		"slack: a token as the env name":       `{"config":{"channel":"C1","bot_token_env":"xoxb-1234567890-abcdefghij"}}`,
		"slack: a token pasted as the channel": `{"config":{"channel":"xoxb-123456789012-123456789012-abcdefghijklmnopqrstuvwx"}}`,
		"slack: an unknown field":              `{"config":{"channel":"C1","bot_token":"x"}}`,
		"slack: no channel":                    `{"config":{"events":["verify.passed"]}}`,
		"slack: an unknown event":              `{"config":{"channel":"C1","events":["change_set.exploded"]}}`,
		"linear: polling too often":            `{"config":{"team_key":"ENG","poll_interval_seconds":5}}`,
		"linear: no team":                      `{"config":{"project":"Roadmap"}}`,
		"no config at all":                     `{}`,
	} {
		target := "slack"
		if strings.HasPrefix(name, "linear") {
			target = "linear"
		}
		resp := doReq(t, http.MethodPut, url+target, token, "", body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, resp.StatusCode)
		}
	}
	// Nothing refused above reached the file.
	if after, _ := os.ReadFile(path); string(after) != string(written) {
		t.Errorf("a refused write changed harness.yaml:\n%s", after)
	}

	decodeInto(t, doReq(t, http.MethodPut, url+"slack", token, "", `{"config":null}`), http.StatusOK, nil)
	pr, _ = rt.EnsureProject(t.Context(), projectID)
	if pr.cfg.Harness.Integrations.Slack != nil || pr.cfg.Harness.Integrations.Linear == nil {
		t.Fatalf("after removing slack: %+v", pr.cfg.Harness.Integrations)
	}
	resp := doReq(t, http.MethodPut, url+"jira", token, "", `{"config":null}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown integration = %d", resp.StatusCode)
	}
}

// The executor profile editor's writes: switch to acp with an MCP server and
// back to internal, with a secret-looking literal refused on the way.
func TestExecutorProfileSwitchAndMCPServers(t *testing.T) {
	_, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)
	url := ts.URL + "/api/v1/projects/" + projectID + "/executors"

	decodeInto(t, doReq(t, http.MethodPut, url, token, "", `{"kind":"internal"}`), http.StatusOK, nil)
	decodeInto(t, doReq(t, http.MethodPut, url, token, "",
		`{"kind":"acp","image":"ghcr.io/x/claude-acp:1","acp_command":["claude-code-acp"],"acp_timeout_seconds":600,
		  "mcp_servers":[{"name":"gh","command":"gh-mcp","env":[{"name":"GITHUB_TOKEN","value_env":"GITHUB_TOKEN"},{"name":"LOG_LEVEL","value":"debug"}]},
		                 {"name":"docs","type":"http","url":"https://mcp.example.com","headers":[{"name":"Authorization","value_env":"DOCS_MCP_TOKEN"}]}]}`),
		http.StatusOK, nil)

	var view ExecutorConfigView
	decodeInto(t, doReq(t, http.MethodGet, url+"/config", token, "", ""), http.StatusOK, &view)
	if view.Config.Kind != config.ExecutorACP || view.Config.ACP.TimeoutSeconds != 600 || len(view.Config.MCPServers) != 2 {
		t.Fatalf("config = %+v", view.Config)
	}
	if set, ok := view.Env["GITHUB_TOKEN"]; !ok || set {
		t.Errorf("env = %v, want GITHUB_TOKEN reported unset", view.Env)
	}

	for name, body := range map[string]string{
		"a token literal":          `{"mcp_servers":[{"name":"gh","command":"gh-mcp","env":[{"name":"GITHUB_TOKEN","value":"ghp_0123456789abcdefghijABCDEFGHIJ012345"}]}]}`,
		"an Authorization literal": `{"mcp_servers":[{"name":"d","type":"http","url":"https://x.example.com","headers":[{"name":"Authorization","value":"Bearer abc"}]}]}`,
		"value and value_env":      `{"mcp_servers":[{"name":"gh","command":"gh-mcp","env":[{"name":"A","value":"1","value_env":"A"}]}]}`,
		"stdio with a url":         `{"mcp_servers":[{"name":"gh","command":"gh-mcp","url":"https://x"}]}`,
		"an unsafe openhands url":  `{"kind":"openhands","openhands_url":"http://agents.example.com"}`,
	} {
		resp := doReq(t, http.MethodPut, url, token, "", body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, resp.StatusCode)
		}
	}

	decodeInto(t, doReq(t, http.MethodPut, url, token, "", `{"kind":"internal","mcp_servers":[]}`), http.StatusOK, nil)
	view = ExecutorConfigView{}
	decodeInto(t, doReq(t, http.MethodGet, url+"/config", token, "", ""), http.StatusOK, &view)
	if view.Config.Kind != config.ExecutorInternal || len(view.Config.MCPServers) != 0 {
		t.Fatalf("back to internal: %+v", view.Config)
	}
}

// The metric-measurement schedule, on a fake clock: nothing runs until it is
// enabled; once enabled, a metric with no observation is measured on the
// next tick; it is not measured again inside its cadence; it is once the
// cadence has passed; and turning the schedule off stops it.
func TestScheduledMetricMeasurementFollowsTheCadence(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	base := ts.URL + "/api/v1/projects/" + projectID
	decodeInto(t, doReq(t, http.MethodPost, base+"/initiatives/"+iniID+"/objectives", token, "", `{"statement":"Fast pages"}`), http.StatusCreated, nil)
	decodeInto(t, doReq(t, http.MethodPost, base+"/initiatives/"+iniID+"/metrics", token, "",
		`{"objective":"OBJ-1","name":"p95 ms","direction":"decrease","target":200,"cadence":"daily","source":{"kind":"command","command":"./measure.sh"}}`),
		http.StatusCreated, nil)
	decodeInto(t, doReq(t, http.MethodPost, base+"/initiatives/"+iniID+"/metrics", token, "",
		`{"objective":"OBJ-1","name":"nps","direction":"increase","target":40,"cadence":"weekly"}`), http.StatusCreated, nil)

	pr, _ := rt.EnsureProject(t.Context(), projectID)
	measured := 0
	pr.engine.SetMetricSandbox(func(ctx context.Context, opts executor.RunOptions) (string, string, int, error) {
		measured++
		return "180\n", "", 0, nil
	})
	sched := rt.automations()
	now := time.Now()
	sched.now = func() time.Time { return now }
	tick := func(at time.Time) {
		now = at
		sched.tick(t.Context())
		sched.wait()
	}

	tick(now)
	if measured != 0 {
		t.Fatal("measured with the schedule off")
	}
	decodeInto(t, doReq(t, http.MethodPut, base+"/automations/metric_measurement", token, "", `{"enabled":true}`), http.StatusOK, nil)
	t0 := now
	tick(t0)
	if measured != 1 {
		t.Fatalf("measured %d time(s) on the first enabled tick, want 1 (the manual metric is never measured)", measured)
	}
	tick(t0.Add(2 * time.Hour))
	if measured != 1 {
		t.Fatalf("measured again inside its daily cadence (%d)", measured)
	}
	tick(t0.Add(25 * time.Hour))
	if measured != 2 {
		t.Fatalf("not measured once its cadence passed (%d)", measured)
	}

	var list struct {
		Automations []AutomationView `json:"automations"`
	}
	decodeInto(t, doReq(t, http.MethodGet, base+"/automations", token, "", ""), http.StatusOK, &list)
	var mm AutomationView
	for _, a := range list.Automations {
		if a.Name == AutomationMetricMeasurement {
			mm = a
		}
	}
	if !mm.Enabled || len(mm.History) != 2 || mm.History[0].Status != store.AutomationSucceeded || !strings.Contains(mm.History[0].Summary, "MET-1 = 180") {
		t.Fatalf("view = %+v", mm)
	}
	if mm.NextRunAt == nil || len(mm.Targets) != 1 || mm.Targets[0].Code != "MET-1" {
		t.Fatalf("next/targets = %v %+v", mm.NextRunAt, mm.Targets)
	}

	decodeInto(t, doReq(t, http.MethodPut, base+"/automations/metric_measurement", token, "", `{"enabled":false}`), http.StatusOK, nil)
	tick(t0.Add(100 * time.Hour))
	if measured != 2 {
		t.Fatal("measured after the schedule was turned off")
	}
	// "Run now" still runs one measurement on request, recorded as manual.
	decodeInto(t, doReq(t, http.MethodPost, base+"/automations/metric_measurement/run", token, "", `{"target":"MET-1"}`), http.StatusAccepted, nil)
	sched.wait()
	runs, _ := pr.db.ListAutomationRuns(t.Context(), AutomationMetricMeasurement, "", 10)
	if measured != 3 || len(runs) != 3 || runs[0].Trigger != triggerManual {
		t.Fatalf("run now: measured=%d runs=%+v", measured, runs)
	}
	decodeInto(t, doReq(t, http.MethodPut, base+"/automations/metric_measurement", token, "", `{"enabled":true,"model":"x"}`), http.StatusBadRequest, nil)
}

// The eval schedule: its first run is one interval after it is enabled
// (never the moment the switch flips), the run is capped and costed in the
// history, and a regression against the baseline is an eval.regressed event.
func TestScheduledEvalRunsOnItsIntervalAndFlagsRegressions(t *testing.T) {
	var got []EvalRequest
	SetEvalRunner(func(ctx context.Context, req EvalRequest) (EvalResult, error) {
		got = append(got, req)
		res := EvalResult{CostUSD: 0.42, Summary: "7 case(s): 7 complete", ReportPath: filepath.Join(req.ReportDir, req.Stamp+".json")}
		if len(got) > 1 {
			res.Regressions = []string{"todo-cli reached: complete → plan_approved"}
		}
		return res, os.WriteFile(res.ReportPath, []byte(`{}`), 0o600)
	})
	t.Cleanup(func() { SetEvalRunner(nil) })

	rt, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)
	base := ts.URL + "/api/v1/projects/" + projectID
	sched := rt.automations()
	now := time.Now()
	sched.now = func() time.Time { return now }
	tick := func(at time.Time) {
		now = at
		sched.tick(t.Context())
		sched.wait()
	}

	// Enabling without a model or a cap is refused by the config itself.
	decodeInto(t, doReq(t, http.MethodPut, base+"/automations/eval", token, "", `{"enabled":true}`), http.StatusBadRequest, nil)
	decodeInto(t, doReq(t, http.MethodPut, base+"/automations/eval", token, "", `{"enabled":true,"model":"openai/gpt-4o-mini","max_cost_usd":1.5,"interval_hours":1}`), http.StatusOK, nil)
	t0 := now
	tick(t0)
	if len(got) != 0 {
		t.Fatal("the eval ran the moment it was enabled")
	}
	tick(t0.Add(61 * time.Minute))
	if len(got) != 1 || got[0].MaxCostUSD != 1.5 || got[0].Model != "openai/gpt-4o-mini" || got[0].Baseline != "" {
		t.Fatalf("first run = %+v", got)
	}
	tick(t0.Add(90 * time.Minute))
	if len(got) != 1 {
		t.Fatal("ran again inside its interval")
	}
	tick(t0.Add(125 * time.Minute))
	if len(got) != 2 || got[1].Baseline == "" {
		t.Fatalf("second run should compare with the first's report: %+v", got)
	}
	pr, _ := rt.EnsureProject(t.Context(), projectID)
	runs, _ := pr.db.ListAutomationRuns(t.Context(), AutomationEval, "", 10)
	if len(runs) != 2 || runs[0].CostUSD == nil || *runs[0].CostUSD != 0.42 || runs[0].Status != store.AutomationSucceeded {
		t.Fatalf("history = %+v", runs)
	}
	regressed := eventsOfType(t, pr.db, store.EvalRegressedEvent)
	if len(regressed) != 1 {
		t.Fatalf("%d eval.regressed events, want 1", len(regressed))
	}
	evs, err := integrations.Translate(t.Context(), pr.db, projectID, regressed[0])
	if err != nil || len(evs) != 1 || evs[0].Kind != integrations.KindEvalRegressed || !strings.Contains(evs[0].Reason, "todo-cli") {
		t.Fatalf("translated = %+v (%v)", evs, err)
	}
}

// A runner error is recorded as a failed run, never retried on the next tick.
func TestScheduledEvalFailureIsRecorded(t *testing.T) {
	SetEvalRunner(func(ctx context.Context, req EvalRequest) (EvalResult, error) {
		return EvalResult{CostUSD: 0.1}, errors.New("eval: --model openai/gpt-4o-mini needs OPENAI_API_KEY in the environment")
	})
	t.Cleanup(func() { SetEvalRunner(nil) })
	_, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)
	base := ts.URL + "/api/v1/projects/" + projectID
	decodeInto(t, doReq(t, http.MethodPut, base+"/automations/eval", token, "", `{"model":"openai/gpt-4o-mini","max_cost_usd":1}`), http.StatusOK, nil)
	decodeInto(t, doReq(t, http.MethodPost, base+"/automations/eval/run", token, "", ""), http.StatusAccepted, nil)
	var list struct {
		Automations []AutomationView `json:"automations"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		decodeInto(t, doReq(t, http.MethodGet, base+"/automations", token, "", ""), http.StatusOK, &list)
		if ev := list.Automations[2]; ev.LastRun != nil && ev.LastRun.Status != store.AutomationRunning {
			if ev.LastRun.Status != store.AutomationFailed || !strings.Contains(ev.LastRun.Error, "OPENAI_API_KEY") || ev.Enabled {
				t.Fatalf("last run = %+v enabled=%v", ev.LastRun, ev.Enabled)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the run never finished")
}

// Linear polls are kept only when they mattered.
func TestLinearPollHistoryKeepsOnlyPollsThatMattered(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)
	pr, _ := rt.EnsureProject(t.Context(), projectID)
	now := time.Now()
	rt.recordLinearPoll(pr.db, linear.PollRecord{Started: now, Finished: now})
	rt.recordLinearPoll(pr.db, linear.PollRecord{Started: now, Finished: now, Imported: 2})
	rt.recordLinearPoll(pr.db, linear.PollRecord{Started: now, Finished: now, Err: errors.New("401")})
	rt.recordLinearPoll(pr.db, linear.PollRecord{Started: now, Finished: now, Manual: true})
	runs, _ := pr.db.ListAutomationRuns(t.Context(), AutomationLinearInbound, "", 10)
	if len(runs) != 3 {
		t.Fatalf("kept %d polls, want 3 (imported, failed, manual)", len(runs))
	}
}
