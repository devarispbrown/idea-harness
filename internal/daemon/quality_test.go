package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/workflow"
)

// noSandbox is a quality sandbox that is never available, so the test does
// not depend on (or start) Docker.
type noSandbox struct{}

func (noSandbox) Available(context.Context) bool { return false }
func (noSandbox) Run(context.Context, string, executor.RunOptions) (string, int, error) {
	return "", -1, nil
}

func TestQualityScorecardReadMeasureAndVerifyResponse(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	pr.engine.SetQualitySandbox(noSandbox{})
	base := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID

	read := func() workflow.QualityScorecard {
		t.Helper()
		resp := doReq(t, http.MethodGet, base+"/quality", token, "", "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET quality = %d", resp.StatusCode)
		}
		var sc workflow.QualityScorecard
		if err := json.NewDecoder(resp.Body).Decode(&sc); err != nil {
			t.Fatal(err)
		}
		return sc
	}
	sc := read()
	if len(sc.Dimensions) != len(domain.QualityDimensions) || sc.GateUnmet == nil {
		t.Fatalf("scorecard shape: %+v", sc)
	}
	enabled := false
	for _, d := range sc.Dimensions {
		if d.Result != nil {
			t.Fatalf("nothing measured yet, but %s has a result", d.Dimension)
		}
		enabled = enabled || d.Mode != domain.QualityModeOff
	}
	if enabled != sc.NeedsMeasurement {
		t.Fatalf("needs_measurement must follow whether any dimension is enabled: %+v", sc)
	}

	resp := doReq(t, http.MethodPost, base+"/quality", token, "", "")
	var acc struct {
		OperationID string `json:"operation_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&acc)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted || acc.OperationID == "" {
		t.Fatalf("POST quality = %d %q", resp.StatusCode, acc.OperationID)
	}
	if op := waitOperation(t, ts.URL, token, acc.OperationID); op.Status != domain.OperationSucceeded {
		t.Fatalf("measure_quality: %s", op.Status)
	}
	sc = read()
	if sc.NeedsMeasurement {
		t.Fatalf("after measuring, nothing is stale: %+v", sc)
	}

	resp = doReq(t, http.MethodPost, base+"/verify", token, "", `{}`)
	defer resp.Body.Close()
	var v struct {
		Quality *workflow.QualityScorecard `json:"quality"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if v.Quality == nil || len(v.Quality.Dimensions) != len(domain.QualityDimensions) {
		t.Fatalf("verify must carry the scorecard: %+v", v.Quality)
	}

	if resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+projectID+"/initiatives/nope/quality", token, "", ""); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown initiative = %d, want 404", resp.StatusCode)
	}
}
