package domain

import (
	"strings"
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func obsAt(v float64, day int, changeSets ...string) MetricObservation {
	return MetricObservation{Value: v, ObservedAt: t0.Add(time.Duration(day) * 24 * time.Hour), ChangeSetIDs: changeSets}
}

func TestEvaluateMetricDriftStatuses(t *testing.T) {
	now := t0.Add(10 * 24 * time.Hour)
	inc := OutcomeMetric{Name: "activation", Direction: DirectionIncrease, Baseline: f64(10), Target: 20, CreatedAt: t0}
	dec := OutcomeMetric{Name: "p95 ms", Direction: DirectionDecrease, Baseline: f64(500), Target: 300, CreatedAt: t0}

	cases := []struct {
		name  string
		m     OutcomeMetric
		obs   []MetricObservation
		want  MetricDrift
		check func(t *testing.T, st MetricStatus)
	}{
		{name: "no observations is no_data", m: inc, want: DriftNoData},
		{name: "increase, above baseline, short of target is on_track", m: inc, obs: []MetricObservation{obsAt(12, 9)}, want: DriftOnTrack},
		{name: "increase, exactly at baseline is on_track (not the wrong side)", m: inc, obs: []MetricObservation{obsAt(10, 9)}, want: DriftOnTrack},
		{name: "increase, below baseline is off_track", m: inc, obs: []MetricObservation{obsAt(9, 9)}, want: DriftOffTrack},
		{name: "decrease, above baseline is off_track", m: dec, obs: []MetricObservation{obsAt(510, 9)}, want: DriftOffTrack},
		{name: "decrease, below baseline is on_track and achieved at target", m: dec, obs: []MetricObservation{obsAt(300, 9)}, want: DriftOnTrack,
			check: func(t *testing.T, st MetricStatus) {
				if !st.Achieved {
					t.Error("300ms against a 300ms decrease target should be achieved")
				}
			}},
		{name: "a drop with a change set between observations is regressed", m: inc,
			obs: []MetricObservation{obsAt(15, 8), obsAt(13, 9, "cs-1")}, want: DriftRegressed,
			check: func(t *testing.T, st MetricStatus) {
				if len(st.RegressedBy) != 1 || st.RegressedBy[0] != "cs-1" {
					t.Errorf("RegressedBy = %v, want [cs-1]", st.RegressedBy)
				}
				if st.OffTrack {
					t.Error("13 is still above the baseline 10: regressed, but not off track")
				}
			}},
		{name: "a drop with no change set in between is not regressed", m: inc,
			obs: []MetricObservation{obsAt(15, 8), obsAt(13, 9)}, want: DriftOnTrack},
		{name: "a rise after a change set is not regressed", m: inc,
			obs: []MetricObservation{obsAt(13, 8), obsAt(15, 9, "cs-1")}, want: DriftOnTrack},
		{name: "regressed outranks off_track", m: inc,
			obs: []MetricObservation{obsAt(11, 8), obsAt(8, 9, "cs-2")}, want: DriftRegressed,
			check: func(t *testing.T, st MetricStatus) {
				if !st.OffTrack || !st.Regressed {
					t.Errorf("both signals should be reported: off=%v regressed=%v", st.OffTrack, st.Regressed)
				}
			}},
		{name: "observations out of order are sorted by observed_at", m: inc,
			obs: []MetricObservation{obsAt(8, 9, "cs-2"), obsAt(11, 8)}, want: DriftRegressed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := EvaluateMetricDrift(c.m, c.obs, now)
			if st.Drift != c.want {
				t.Fatalf("drift = %s, want %s (reasons %v)", st.Drift, c.want, st.Reasons)
			}
			if !st.Drift.Valid() {
				t.Fatalf("drift %q is not a declared MetricDrift", st.Drift)
			}
			if c.check != nil {
				c.check(t, st)
			}
		})
	}
}

func TestEvaluateMetricDriftNoBaselineUsesFirstObservation(t *testing.T) {
	m := OutcomeMetric{Name: "signups", Direction: DirectionIncrease, Target: 100, CreatedAt: t0}
	now := t0.Add(5 * 24 * time.Hour)

	st := EvaluateMetricDrift(m, []MetricObservation{obsAt(40, 1)}, now)
	if st.Drift != DriftOnTrack || st.BaselineSource != "first_observation" || *st.EffectiveBaseline != 40 {
		t.Fatalf("one observation, no baseline: got %s, source %q, baseline %v", st.Drift, st.BaselineSource, st.EffectiveBaseline)
	}
	st = EvaluateMetricDrift(m, []MetricObservation{obsAt(40, 1), obsAt(30, 2)}, now)
	if st.Drift != DriftOffTrack {
		t.Fatalf("below the first observation with no declared baseline should be off_track, got %s", st.Drift)
	}
}

func TestEvaluateMetricDriftMaintain(t *testing.T) {
	// Hold error rate at 1%: the baseline was 1.5, so anything farther from
	// 1 than 1.5 is worse than where it started.
	m := OutcomeMetric{Name: "error rate", Direction: DirectionMaintain, Baseline: f64(1.5), Target: 1, CreatedAt: t0}
	now := t0.Add(3 * 24 * time.Hour)
	for _, c := range []struct {
		v    float64
		want MetricDrift
	}{
		{1.0, DriftOnTrack}, {1.4, DriftOnTrack}, {0.6, DriftOnTrack}, {1.6, DriftOffTrack}, {0.4, DriftOffTrack},
	} {
		if st := EvaluateMetricDrift(m, []MetricObservation{obsAt(c.v, 1)}, now); st.Drift != c.want {
			t.Errorf("maintain at 1 from 1.5: %v -> %s, want %s", c.v, st.Drift, c.want)
		}
	}
	// With the baseline AT the target, any deviation is worse than the start.
	m.Baseline = f64(1)
	if st := EvaluateMetricDrift(m, []MetricObservation{obsAt(1.01, 1)}, now); st.Drift != DriftOffTrack {
		t.Errorf("baseline == target: a deviation should be off_track, got %s", st.Drift)
	}
	// Guardrail on maintain is the allowed distance from the target, and
	// also defines "achieved".
	m.Baseline, m.Guardrail = nil, f64(0.5)
	st := EvaluateMetricDrift(m, []MetricObservation{obsAt(1.2, 1)}, now)
	if st.Drift != DriftOnTrack || !st.Achieved {
		t.Errorf("1.2 within a 0.5 guardrail of 1: got %s achieved=%v", st.Drift, st.Achieved)
	}
	st = EvaluateMetricDrift(m, []MetricObservation{obsAt(1.2, 1), obsAt(1.6, 2)}, now)
	if !st.GuardrailBreached || st.Drift != DriftOffTrack {
		t.Errorf("1.6 is 0.6 from 1, past a 0.5 guardrail: got %s breached=%v", st.Drift, st.GuardrailBreached)
	}
}

func TestEvaluateMetricDriftGuardrail(t *testing.T) {
	now := t0.Add(3 * 24 * time.Hour)
	// Increase metric that started low: above baseline, but under the floor.
	inc := OutcomeMetric{Name: "retention", Direction: DirectionIncrease, Baseline: f64(20), Target: 40, Guardrail: f64(25), CreatedAt: t0}
	st := EvaluateMetricDrift(inc, []MetricObservation{obsAt(22, 1)}, now)
	if !st.GuardrailBreached || st.Drift != DriftOffTrack {
		t.Fatalf("22 is above the baseline but below the 25 floor: got %s breached=%v", st.Drift, st.GuardrailBreached)
	}
	dec := OutcomeMetric{Name: "cost", Direction: DirectionDecrease, Baseline: f64(100), Target: 50, Guardrail: f64(90), CreatedAt: t0}
	if st := EvaluateMetricDrift(dec, []MetricObservation{obsAt(95, 1)}, now); !st.GuardrailBreached {
		t.Fatal("95 is over a 90 ceiling")
	}
	if st := EvaluateMetricDrift(dec, []MetricObservation{obsAt(85, 1)}, now); st.GuardrailBreached || st.Drift != DriftOnTrack {
		t.Fatalf("85 is under a 90 ceiling and below the baseline: got %s", st.Drift)
	}
}

func TestEvaluateMetricDriftStale(t *testing.T) {
	m := OutcomeMetric{Name: "nps", Direction: DirectionIncrease, Baseline: f64(10), Target: 30, Cadence: "weekly", CreatedAt: t0}
	o := obsAt(12, 0)
	exactly := t0.Add(7 * 24 * time.Hour)
	if st := EvaluateMetricDrift(m, []MetricObservation{o}, exactly); st.Stale {
		t.Error("exactly one cadence after the observation is due, not stale")
	}
	late := exactly.Add(time.Minute)
	st := EvaluateMetricDrift(m, []MetricObservation{o}, late)
	if !st.Stale || st.Drift != DriftStale {
		t.Fatalf("a minute past the cadence should be stale, got %s", st.Drift)
	}
	if st.NextDueAt == nil || !st.NextDueAt.Equal(exactly) {
		t.Errorf("NextDueAt = %v, want %v", st.NextDueAt, exactly)
	}
	// off_track outranks stale, but both are reported.
	st = EvaluateMetricDrift(m, []MetricObservation{obsAt(5, 0)}, late)
	if st.Drift != DriftOffTrack || !st.Stale {
		t.Errorf("off track and stale: drift %s stale %v", st.Drift, st.Stale)
	}
	// Never observed and past one cadence since it was defined.
	st = EvaluateMetricDrift(m, nil, late)
	if st.Drift != DriftStale || !st.Stale {
		t.Errorf("never observed past its cadence: got %s", st.Drift)
	}
	if st := EvaluateMetricDrift(m, nil, t0.Add(time.Hour)); st.Drift != DriftNoData || st.Stale {
		t.Errorf("never observed, within its cadence: got %s stale=%v", st.Drift, st.Stale)
	}
	// No cadence: never stale.
	m.Cadence = ""
	if st := EvaluateMetricDrift(m, []MetricObservation{o}, t0.Add(10000*time.Hour)); st.Stale {
		t.Error("no cadence means no staleness")
	}
	// A bad cadence is reported, not silently treated as none.
	m.Cadence = "fortnightly-ish"
	st = EvaluateMetricDrift(m, []MetricObservation{o}, late)
	if st.Stale || !strings.Contains(strings.Join(st.Reasons, " "), "staleness is not checked") {
		t.Errorf("bad cadence: stale=%v reasons=%v", st.Stale, st.Reasons)
	}
}

func TestParseCadence(t *testing.T) {
	for in, want := range map[string]time.Duration{
		"": 0, "daily": 24 * time.Hour, "Weekly": 168 * time.Hour, "3d": 72 * time.Hour,
		"2w": 336 * time.Hour, "36h": 36 * time.Hour, "monthly": 720 * time.Hour,
	} {
		got, err := ParseCadence(in)
		if err != nil || got != want {
			t.Errorf("ParseCadence(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"often", "-1d", "0d", "-5h"} {
		if _, err := ParseCadence(bad); err == nil {
			t.Errorf("ParseCadence(%q) should fail", bad)
		}
	}
}

func TestMetricSourceValidate(t *testing.T) {
	ok := []MetricSource{
		{Kind: MetricSourceManual},
		{Kind: MetricSourceCommand, Command: "wc -l < /repo/USERS"},
		{Kind: MetricSourceURL, URL: "https://example.com/stats.json", Pointer: "/daily/active"},
		{Kind: MetricSourceURL, URL: "https://example.com/count"},
	}
	for _, s := range ok {
		if err := s.Validate(); err != nil {
			t.Errorf("%+v: %v", s, err)
		}
	}
	bad := []MetricSource{
		{Kind: "cron"},
		{Kind: MetricSourceCommand},
		{Kind: MetricSourceURL, URL: "file:///etc/passwd"},
		{Kind: MetricSourceURL, URL: "https://x", Pointer: "daily"},
	}
	for _, s := range bad {
		if err := s.Validate(); err == nil {
			t.Errorf("%+v should be invalid", s)
		}
	}
}

func TestOutcomeVocabulariesValid(t *testing.T) {
	for _, v := range []ObjectiveStatus{ObjectiveActive, ObjectiveAchieved, ObjectiveAbandoned} {
		if !v.Valid() {
			t.Errorf("%q", v)
		}
	}
	for _, v := range []MetricDirection{DirectionIncrease, DirectionDecrease, DirectionMaintain} {
		if !v.Valid() {
			t.Errorf("%q", v)
		}
	}
	for _, v := range []EdgeType{EdgeServes, EdgeMeasures, EdgePredicts, EdgeAttributedTo} {
		if !v.Valid() {
			t.Errorf("edge %q", v)
		}
	}
	if !DriftOffTrack.Drifting() || !DriftRegressed.Drifting() || !DriftStale.Drifting() || DriftOnTrack.Drifting() || DriftNoData.Drifting() {
		t.Error("Drifting() is off_track, regressed and stale only")
	}
	if ObjectiveStatus("done").Valid() || MetricDirection("up").Valid() || MetricDrift("bad").Valid() {
		t.Error("unknown values must be rejected")
	}
}
