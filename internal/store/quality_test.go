package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

func TestQualityMeasurementRoundTripAndLatest(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "q"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	req := &domain.Requirement{InitiativeID: ini.ID, Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateRequirement(ctx, req); err != nil {
		t.Fatal(err)
	}

	first := []domain.QualityResult{{
		MeasurementID: "m1", Dimension: domain.QualityPerformance, Mode: domain.QualityModeAdvisory, Status: domain.QualityPass,
		Summary: "1 pass", Metrics: map[string]float64{"bench": 100}, InputDigest: "d1",
		Checks: []domain.QualityCheck{{Name: "bench", Status: domain.QualityPass, Method: domain.QualityMethodDeterministic,
			Citations: []domain.QualityCitation{{Path: "a.go", Line: 3}}}},
		CreatedAt: time.Now().Add(-time.Minute),
	}}
	links := map[domain.QualityDimension][]QualityLink{domain.QualityPerformance: {{RequirementID: req.ID, RequirementCode: req.Code}}}
	if err := db.RecordQualityMeasurement(ctx, ini.ID, first, links); err != nil {
		t.Fatal(err)
	}
	second := []domain.QualityResult{
		{MeasurementID: "m2", Dimension: domain.QualityPerformance, Mode: domain.QualityModeAdvisory, Status: domain.QualityFail, InputDigest: "d2", Metrics: map[string]float64{"bench": 300}},
		{MeasurementID: "m2", Dimension: domain.QualitySecurity, Mode: domain.QualityModeGate, Status: domain.QualityNotMeasured, InputDigest: "d2"},
	}
	if err := db.RecordQualityMeasurement(ctx, ini.ID, second, links); err != nil {
		t.Fatal(err)
	}

	latest, err := db.LatestQualityResults(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 2 {
		t.Fatalf("want one latest per dimension, got %d", len(latest))
	}
	perf := latest[domain.QualityPerformance]
	if perf.Status != domain.QualityFail || perf.Metrics["bench"] != 300 || perf.MeasurementID != "m2" {
		t.Fatalf("latest performance result wrong: %+v", perf)
	}
	if len(perf.Requirements) != 1 || perf.Requirements[0] != req.Code {
		t.Fatalf("linked requirements: %v", perf.Requirements)
	}
	if sec := latest[domain.QualitySecurity]; len(sec.Requirements) != 0 || sec.Checks == nil {
		t.Fatalf("an unlinked result has no requirements and a non-nil checks list: %+v", sec)
	}

	got, err := db.GetQualityResult(ctx, first[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Checks[0].Citations[0].Line != 3 || got.Requirements[0] != req.Code {
		t.Fatalf("round trip lost data: %+v", got)
	}
	var nf *NotFoundError
	if _, err := db.GetQualityResult(ctx, "nope"); !errors.As(err, &nf) {
		t.Fatalf("missing result must be a NotFoundError, got %v", err)
	}

	// The verifies edge is an ordinary graph edge, so `why` and `impact`
	// traverse it.
	edges, err := db.ListGraphEdgesTo(ctx, ini.ID, "requirement", req.ID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range edges {
		if e.FromType == "quality_result" && e.EdgeType == domain.EdgeVerifies {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("want 2 verifies edges (one per performance result), got %d", n)
	}
}

func TestQualityMeasurementIsAtomic(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "q"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	bad := []domain.QualityResult{
		{Dimension: domain.QualityDocsCompleteness, Mode: domain.QualityModeGate, Status: domain.QualityPass},
		{Dimension: "bogus", Mode: domain.QualityModeGate, Status: domain.QualityPass},
	}
	if err := db.RecordQualityMeasurement(ctx, ini.ID, bad, nil); err == nil {
		t.Fatal("an invalid dimension must refuse the measurement")
	}
	latest, err := db.LatestQualityResults(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(latest) != 0 {
		t.Fatalf("a refused measurement must write nothing, got %d rows", len(latest))
	}
}
