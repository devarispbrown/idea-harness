package workflow

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/store"
)

type fakeMetricSandbox struct {
	image  string
	opts   executor.RunOptions
	stdout string
	code   int
	err    error
}

func (f *fakeMetricSandbox) Run(_ context.Context, opts executor.RunOptions) (string, string, int, error) {
	f.opts = opts
	return f.stdout, "", f.code, f.err
}

type fakeFetcher struct {
	url  string
	body string
	err  error
}

func (f *fakeFetcher) FetchJSON(_ context.Context, url string) ([]byte, error) {
	f.url = url
	return []byte(f.body), f.err
}

// measureFixture is an initiative with one objective and three metrics: a
// manual one, a command one and a url one.
func measureFixture(t *testing.T, cfg config.HarnessConfig) (*Engine, *store.DB, string) {
	t.Helper()
	eng, db := newTestEngine(t, scriptedProvider(), cfg)
	eng.workspaceRoot = t.TempDir()
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	obj := &domain.Objective{InitiativeID: ini.ID, Statement: "More users"}
	if err := db.CreateObjective(ctx, obj, store.HumanActor); err != nil {
		t.Fatal(err)
	}
	base := 10.0
	for _, m := range []*domain.OutcomeMetric{
		{Name: "survey", Direction: domain.DirectionIncrease, Target: 50},
		{Name: "users", Direction: domain.DirectionIncrease, Baseline: &base, Target: 50,
			Source: domain.MetricSource{Kind: domain.MetricSourceCommand, Command: "wc -l < USERS"}},
		{Name: "dau", Direction: domain.DirectionIncrease, Target: 1000,
			Source: domain.MetricSource{Kind: domain.MetricSourceURL, URL: "https://stats.example.com/v1", Pointer: "/data/dau"}},
	} {
		m.InitiativeID, m.ObjectiveID = ini.ID, obj.ID
		if err := db.CreateMetric(ctx, m, store.HumanActor); err != nil {
			t.Fatal(err)
		}
	}
	return eng, db, ini.ID
}

func TestMeasureCommandSourceInSandbox(t *testing.T) {
	eng, db, ini := measureFixture(t, config.HarnessConfig{Check: config.CheckConfig{Image: "busybox:1"}})
	sb := &fakeMetricSandbox{stdout: "counting users...\n  42\n"}
	eng.metricSandbox = func(image string) sandboxRunner { sb.image = image; return sb }

	res, err := eng.MeasureMetrics(context.Background(), ini, "MET-2", store.HumanActor)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].Observation == nil || res[0].Observation.Value != 42 {
		t.Fatalf("result = %+v", res)
	}
	if res[0].Status.Drift != domain.DriftOnTrack {
		t.Errorf("42 from a baseline of 10, target 50: got %s", res[0].Status.Drift)
	}
	// The sandbox got the check image, the workspace read-only at /repo, and
	// the command run from /repo -- never the host.
	if sb.image != "busybox:1" || sb.opts.RepoRoot != eng.workspaceRoot || !strings.Contains(strings.Join(sb.opts.Cmd, " "), "cd /repo && wc -l < USERS") {
		t.Fatalf("sandbox call: image %q opts %+v", sb.image, sb.opts)
	}
	prov := res[0].Observation.Provenance
	if len(prov) != 1 || prov[0].Kind != "metric_command" || prov[0].Digest == "" || prov[0].Trust != domain.TrustExternalUntrusted {
		t.Fatalf("provenance = %+v", prov)
	}
	obs, _ := db.ListObservations(context.Background(), res[0].MetricID)
	if len(obs) != 1 || obs[0].Source != domain.MetricSourceCommand {
		t.Fatalf("stored observations = %+v", obs)
	}

	// The scratch work root sits under the engine's work root (the project's
	// state directory, where startup recovery can find a leftover), and is
	// gone once the command is done.
	state := t.TempDir()
	eng.SetWorkRoot(state)
	if _, err := eng.MeasureMetrics(context.Background(), ini, "MET-2", store.HumanActor); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(sb.opts.WorkRoot) != state || !strings.HasPrefix(filepath.Base(sb.opts.WorkRoot), "metric-") {
		t.Fatalf("metric work root %q, want %s/metric-*", sb.opts.WorkRoot, state)
	}
	if _, err := os.Stat(sb.opts.WorkRoot); !os.IsNotExist(err) {
		t.Fatalf("the metric work root was left behind: %v", err)
	}

	// outcomes.image wins over check.image.
	eng.cfg.Outcomes.Image = "alpine:3.20"
	if _, err := eng.MeasureMetrics(context.Background(), ini, "MET-2", store.HumanActor); err != nil {
		t.Fatal(err)
	}
	if sb.image != "alpine:3.20" {
		t.Errorf("outcomes.image should be used, got %q", sb.image)
	}
}

func TestMeasureCommandFailuresAreReportedNotRecorded(t *testing.T) {
	eng, db, ini := measureFixture(t, config.HarnessConfig{})
	cases := []struct {
		sb   *fakeMetricSandbox
		want string
	}{
		{&fakeMetricSandbox{err: errors.New("docker: not found")}, "sandbox unavailable"},
		{&fakeMetricSandbox{stdout: "boom", code: 2}, "exited 2"},
		{&fakeMetricSandbox{stdout: "no digits here"}, "printed no number"},
	}
	for _, c := range cases {
		eng.metricSandbox = func(string) sandboxRunner { return c.sb }
		res, err := eng.MeasureMetrics(context.Background(), ini, "MET-2", store.HumanActor)
		if err != nil {
			t.Fatal(err)
		}
		if res[0].Observation != nil || !strings.Contains(res[0].Error, c.want) {
			t.Errorf("want error containing %q, got %+v", c.want, res[0])
		}
	}
	ms, _ := db.ListMetrics(context.Background(), ini, time.Now())
	for _, m := range ms {
		if m.Status.Observations != 0 {
			t.Fatalf("a failed measurement must record nothing: %s has %d", m.Code, m.Status.Observations)
		}
	}
}

func TestMeasureURLSourceIsOptIn(t *testing.T) {
	eng, _, ini := measureFixture(t, config.HarnessConfig{})
	f := &fakeFetcher{body: `{"data":{"dau":"1250"}}`}
	eng.metricFetcher = f
	res, err := eng.MeasureMetrics(context.Background(), ini, "MET-3", store.HumanActor)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Observation != nil || !strings.Contains(res[0].Error, "allow_url_sources") || f.url != "" {
		t.Fatalf("url sources must be refused until opted in, without fetching: %+v (fetched %q)", res[0], f.url)
	}

	eng.cfg.Outcomes.AllowURLSources = true
	res, err = eng.MeasureMetrics(context.Background(), ini, "MET-3", store.HumanActor)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Observation == nil || res[0].Observation.Value != 1250 || f.url != "https://stats.example.com/v1" {
		t.Fatalf("url measurement = %+v (fetched %q)", res[0], f.url)
	}
}

func TestMeasureAllSkipsManualAndRefusesNamedManual(t *testing.T) {
	eng, _, ini := measureFixture(t, config.HarnessConfig{Outcomes: config.OutcomesConfig{AllowURLSources: true}})
	eng.metricSandbox = func(string) sandboxRunner { return &fakeMetricSandbox{stdout: "7"} }
	eng.metricFetcher = &fakeFetcher{body: `{"data":{"dau":3}}`}
	res, err := eng.MeasureMetrics(context.Background(), ini, "", store.HumanActor)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]MeasureResult{}
	for _, r := range res {
		got[r.MetricCode] = r
	}
	if got["MET-1"].Skipped == "" || got["MET-2"].Observation == nil || got["MET-3"].Observation == nil {
		t.Fatalf("bulk measure = %+v", res)
	}
	if _, err := eng.MeasureMetrics(context.Background(), ini, "MET-1", store.HumanActor); err == nil || !strings.Contains(err.Error(), "idea metric record") {
		t.Fatalf("measuring a manual metric by name should point at `idea metric record`, got %v", err)
	}
}

func TestJSONPointerNumber(t *testing.T) {
	doc := []byte(`{"a":{"b/c":[1,{"d":2.5}],"m~n":"3","s":"x"},"n":7}`)
	for ptr, want := range map[string]float64{"/a/b~1c/1/d": 2.5, "/a/m~0n": 3, "/n": 7, "/a/b~1c/0": 1} {
		got, err := jsonPointerNumber(doc, ptr)
		if err != nil || got != want {
			t.Errorf("%s = %v, %v; want %v", ptr, got, err, want)
		}
	}
	for _, bad := range []string{"/a/s", "/a/missing", "/a/b~1c/9", "a", "/n/x"} {
		if _, err := jsonPointerNumber(doc, bad); err == nil {
			t.Errorf("%s should fail", bad)
		}
	}
	if v, err := jsonPointerNumber([]byte(`12`), ""); err != nil || v != 12 {
		t.Errorf("empty pointer on a bare number: %v %v", v, err)
	}
	if _, err := jsonPointerNumber([]byte(`<html>`), ""); err == nil {
		t.Error("non-JSON must fail")
	}
}

func TestRecordMetricIsManualWithActorProvenance(t *testing.T) {
	eng, _, ini := measureFixture(t, config.HarnessConfig{})
	obs, st, err := eng.RecordMetric(context.Background(), ini, "met-2", 5, "  survey week 2  ", store.HumanActor, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if obs.Source != domain.MetricSourceManual || obs.Note != "survey week 2" || obs.Provenance[0].ID != store.HumanActor {
		t.Fatalf("observation = %+v", obs)
	}
	if st.Drift != domain.DriftOffTrack {
		t.Errorf("5 against a baseline of 10 (increase) is off track, got %s", st.Drift)
	}
	if _, _, err := eng.RecordMetric(context.Background(), ini, "MET-99", 1, "", store.HumanActor, time.Time{}); err == nil {
		t.Error("an unknown metric should be refused")
	}
}

// TestMeasureCommandInRealSandbox runs a command source in the real
// no-network container: it can read the workspace at /repo, and it cannot
// reach the network. Skipped when Docker, its daemon, or the image is
// unavailable.
func TestMeasureCommandInRealSandbox(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker binary not available")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not running")
	}
	const image = "alpine:3.20"
	if err := exec.Command("docker", "image", "inspect", image).Run(); err != nil {
		if err := exec.Command("docker", "pull", image).Run(); err != nil {
			t.Skipf("%s not available and could not be pulled: %v", image, err)
		}
	}
	eng, _, ini := measureFixture(t, config.HarnessConfig{Outcomes: config.OutcomesConfig{Image: image, MeasureTimeoutSeconds: 60}})
	if err := os.WriteFile(filepath.Join(eng.workspaceRoot, "USERS"), []byte("ada\ngrace\nlinus\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := eng.MeasureMetrics(context.Background(), ini, "MET-2", store.HumanActor)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Observation == nil || res[0].Observation.Value != 3 {
		t.Fatalf("wc -l of a 3-line /repo/USERS should observe 3: %+v", res[0])
	}

	// No network: the same sandbox cannot resolve or reach anything.
	ctx := context.Background()
	obj, _ := eng.db.GetObjectiveByRef(ctx, ini, "OBJ-1")
	net := &domain.OutcomeMetric{InitiativeID: ini, ObjectiveID: obj.ID, Name: "net", Direction: domain.DirectionIncrease, Target: 1,
		Source: domain.MetricSource{Kind: domain.MetricSourceCommand, Command: "wget -q -T 5 -O - http://example.com >/dev/null && echo 1"}}
	if err := eng.db.CreateMetric(ctx, net, store.HumanActor); err != nil {
		t.Fatal(err)
	}
	res, err = eng.MeasureMetrics(ctx, ini, net.Code, store.HumanActor)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Observation != nil {
		t.Fatalf("a command source must have no network, but it observed %v", res[0].Observation.Value)
	}
}
