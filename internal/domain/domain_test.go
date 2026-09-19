package domain

import "testing"

func TestAssertionTypeValid(t *testing.T) {
	valid := []AssertionType{AssertionFact, AssertionAssumption, AssertionHypothesis}
	for _, v := range valid {
		if !v.Valid() {
			t.Errorf("expected %q to be valid", v)
		}
	}
	if AssertionType("nonsense").Valid() {
		t.Error("expected invalid assertion type to be rejected")
	}
}

func TestAssertionStatusValid(t *testing.T) {
	for _, v := range []AssertionStatus{AssertionCandidate, AssertionAccepted, AssertionDisputed, AssertionRetracted, AssertionSuperseded} {
		if !v.Valid() {
			t.Errorf("expected %q to be valid", v)
		}
	}
	if AssertionStatus("bogus").Valid() {
		t.Error("expected invalid assertion status to be rejected")
	}
}

func TestSeverityBlocking(t *testing.T) {
	for _, s := range []Severity{SeverityCritical, SeverityHigh} {
		if !s.Blocking() {
			t.Errorf("expected %q to block approval", s)
		}
	}
	for _, s := range []Severity{SeverityMedium, SeverityLow, SeverityObservation} {
		if s.Blocking() {
			t.Errorf("expected %q NOT to block approval", s)
		}
	}
}

func TestDispositionEnum(t *testing.T) {
	// The constitution §15 seven-value vocabulary is the only valid set.
	all := []Disposition{DispositionFixed, DispositionAcceptedRisk, DispositionNotApplicable,
		DispositionDuplicate, DispositionDeferred, DispositionRejected, DispositionMitigate}
	for _, d := range all {
		if !d.Valid() {
			t.Errorf("expected %q to be valid", d)
		}
	}
	if Disposition("fix").Valid() || Disposition("na").Valid() || Disposition("discuss").Valid() {
		t.Error("non-enum dispositions must be rejected")
	}
}

// TestFindingResolvesBlocking is the ICP re-run's own defect #2: a human's
// rejected disposition on a reviewer's own judgment call (no deny code) must
// resolve a blocking finding exactly like fixed/accepted_risk; a SYSTEM-set
// rejected (a failing check, or any finding carrying a deny code) must not,
// "system deny beats human approve".
func TestFindingResolvesBlocking(t *testing.T) {
	cases := []struct {
		name string
		f    Finding
		want bool
	}{
		{"fixed", Finding{Disposition: DispositionFixed}, true},
		{"accepted_risk", Finding{Disposition: DispositionAcceptedRisk}, true},
		{
			"human_rejected_reviewer_judgment",
			Finding{Disposition: DispositionRejected, DispositionActor: "human"},
			true,
		},
		{
			"human_rejected_deny_coded_finding_still_blocks",
			Finding{Disposition: DispositionRejected, DispositionActor: "human", Code: DenyCodeSecretSweep},
			false,
		},
		{
			"system_rejected_failing_check_still_blocks",
			Finding{Disposition: DispositionRejected, DispositionActor: "orchestrator"},
			false,
		},
		{
			"unset_disposition_actor_still_blocks",
			Finding{Disposition: DispositionRejected},
			false,
		},
		{"deferred_still_blocks", Finding{Disposition: DispositionDeferred, DispositionActor: "human"}, false},
		{"duplicate_still_blocks", Finding{Disposition: DispositionDuplicate, DispositionActor: "human"}, false},
		{"mitigate_still_blocks", Finding{Disposition: DispositionMitigate, DispositionActor: "human"}, false},
		{"not_applicable_still_blocks", Finding{Disposition: DispositionNotApplicable, DispositionActor: "human"}, false},
		{"no_disposition_still_blocks", Finding{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.f.ResolvesBlocking(); got != tc.want {
				t.Errorf("ResolvesBlocking() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAcceptanceCriterionVerifiable(t *testing.T) {
	good := AcceptanceCriterion{Metric: "p95 latency", Target: "< 200ms", Condition: "on reference workload"}
	if !good.Verifiable() {
		t.Error("expected quantified criterion to be verifiable")
	}
	// "fast" with no quantified target is not verifiable.
	vague := AcceptanceCriterion{Metric: "latency", Target: "fast", Condition: ""}
	if vague.Verifiable() {
		t.Error("expected vague criterion to be non-verifiable")
	}
}

func TestTrustLevelValid(t *testing.T) {
	for _, v := range []TrustLevel{TrustSystem, TrustPolicy, TrustHumanApproved, TrustReviewedAgentOutput, TrustUnreviewedAgentOutput, TrustExternalUntrusted} {
		if !v.Valid() {
			t.Errorf("expected %q to be valid", v)
		}
	}
	if TrustLevel("model_approved").Valid() {
		t.Error("unlisted trust level must be rejected")
	}
}

func TestInitiativeStatusValid(t *testing.T) {
	for _, v := range []InitiativeStatus{InitiativeRunning, InitiativeNeedsHumanJudgment, InitiativeApproved, InitiativeFailed} {
		if !v.Valid() {
			t.Errorf("expected %q to be valid", v)
		}
	}
	if InitiativeStatus("done").Valid() {
		t.Error("unlisted initiative status must be rejected")
	}
}

func TestRunKindValid(t *testing.T) {
	for _, v := range []RunKind{RunModel, RunReview, RunCheck, RunTool, RunAgent} {
		if !v.Valid() {
			t.Errorf("expected %q to be valid", v)
		}
	}
	if RunKind("bogus").Valid() {
		t.Error("unlisted run kind must be rejected")
	}
}
