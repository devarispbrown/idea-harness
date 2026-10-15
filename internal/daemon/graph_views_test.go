package daemon

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// GET …/review-telemetry exposes the reviewer runs' convergence records,
// oldest first, filterable by loop, and [] (never null) when no review ran.
func TestReviewTelemetryRoute(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	base := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID

	read := func(query string, want int) []domain.ReviewTelemetry {
		t.Helper()
		resp := doReq(t, http.MethodGet, base+"/review-telemetry"+query, token, "", "")
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("GET review-telemetry%s = %d, want %d", query, resp.StatusCode, want)
		}
		if want != http.StatusOK {
			return nil
		}
		var out []domain.ReviewTelemetry
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if out == nil {
			t.Fatal("an empty history must be [], not null")
		}
		return out
	}
	if got := read("", http.StatusOK); len(got) != 0 {
		t.Fatalf("no review ran yet: %+v", got)
	}

	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	for i, tel := range []domain.ReviewTelemetry{
		{Review: "prd", Cycle: 1, Mode: "guarded", OpenBlocking: 3, PrevOpenBlocking: -1, Outcome: "revise",
			Raised: map[domain.Severity]int{domain.SeverityHigh: 3}, New: map[domain.Severity]int{domain.SeverityHigh: 3}},
		{Review: "prd", Cycle: 2, Mode: "guarded", OpenBlocking: 0, PrevOpenBlocking: 3, Outcome: "converged",
			Resolved: map[domain.Severity]int{domain.SeverityHigh: 3}, Demoted: map[string]int{"uncited": 1}},
		{Review: "execution_plan", Cycle: 1, Mode: "guarded", PrevOpenBlocking: -1, Outcome: "converged"},
	} {
		runID, err := pr.db.CreateRun(t.Context(), domain.RunModel, "", "adversarial_reviewer", iniID, "review")
		if err != nil {
			t.Fatal(err)
		}
		if err := pr.db.CreateRunManifest(t.Context(), &domain.RunManifest{RunID: runID, RequestedModel: "demo", ActualModel: "demo"}); err != nil {
			t.Fatal(err)
		}
		if err := pr.db.SetRunManifestReview(t.Context(), runID, tel); err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
	}

	all := read("", http.StatusOK)
	if len(all) != 3 || all[0].Cycle != 1 || all[1].Outcome != "converged" || all[1].Demoted["uncited"] != 1 {
		t.Fatalf("history = %+v", all)
	}
	if prd := read("?review=prd", http.StatusOK); len(prd) != 2 {
		t.Fatalf("?review=prd = %+v", prd)
	}
	if plan := read("?review=execution_plan", http.StatusOK); len(plan) != 1 || plan[0].Review != "execution_plan" {
		t.Fatalf("?review=execution_plan = %+v", plan)
	}
	read("?review=design", http.StatusBadRequest)

	if resp := doReq(t, http.MethodGet, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/nope/review-telemetry", token, "", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown initiative = %d, want 404", resp.StatusCode)
	}
}

// GET …/quality/history lists every recorded result with its requirement
// codes; an unknown dimension is refused rather than answered with [].
func TestQualityHistoryRoute(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	pr.engine.SetQualitySandbox(noSandbox{})
	base := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID

	read := func(query string, want int) []domain.QualityResult {
		t.Helper()
		resp := doReq(t, http.MethodGet, base+"/quality/history"+query, token, "", "")
		defer resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("GET quality/history%s = %d, want %d", query, resp.StatusCode, want)
		}
		if want != http.StatusOK {
			return nil
		}
		var out []domain.QualityResult
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if out == nil {
			t.Fatal("an empty history must be [], not null")
		}
		return out
	}
	if got := read("", http.StatusOK); len(got) != 0 {
		t.Fatalf("nothing measured yet: %+v", got)
	}
	read("?dimension=vibes", http.StatusBadRequest)

	for i := 0; i < 2; i++ {
		resp := doReq(t, http.MethodPost, base+"/quality", token, "", "")
		var acc struct {
			OperationID string `json:"operation_id"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&acc)
		resp.Body.Close()
		if op := waitOperation(t, ts.URL, token, acc.OperationID); op.Status != domain.OperationSucceeded {
			t.Fatalf("measure_quality: %s", op.Status)
		}
	}
	all := read("", http.StatusOK)
	if len(all) == 0 {
		t.Skip("no dimension is enabled under the default policy; nothing to list")
	}
	for _, q := range all {
		if q.Requirements == nil || q.Checks == nil {
			t.Fatalf("history rows carry [] not null for requirements/checks: %+v", q)
		}
	}
	dim := all[0].Dimension
	one := read("?dimension="+string(dim), http.StatusOK)
	if len(one) != 2 {
		t.Fatalf("two measurements of %s, got %d", dim, len(one))
	}
	for _, q := range one {
		if q.Dimension != dim {
			t.Fatalf("?dimension=%s returned %s", dim, q.Dimension)
		}
	}
	if !one[0].CreatedAt.Before(one[1].CreatedAt) && !one[0].CreatedAt.Equal(one[1].CreatedAt) {
		t.Fatalf("history must be oldest first: %v then %v", one[0].CreatedAt, one[1].CreatedAt)
	}
}
