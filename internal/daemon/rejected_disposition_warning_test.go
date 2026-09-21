package daemon

import (
	"encoding/json"
	"net/http"
	"testing"
)

// TestRejectedDispositionOnDenyCodedFindingWarns is the ICP re-run's own
// reported "the API returns 200 for rejected with no warning" (defect #2): a
// human dispositioning a system-deny-coded finding (secret_sweep) "rejected"
// still gets a 200 -- rejected IS a valid disposition to record -- but now
// the response carries a "warning" explaining it did not clear the deny,
// since only a human's rejected on a reviewer's OWN judgment call (no deny
// code) does that (domain.Finding.ResolvesBlocking).
func TestRejectedDispositionOnDenyCodedFindingWarns(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, _, f := executeToSecretSweepDenial(t, rt, ts, token)

	resp := doReq(t, http.MethodPost,
		ts.URL+"/api/v1/projects/"+projectID+"/findings/"+f.ID+"/disposition", token, "",
		`{"disposition":"rejected"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("disposition rejected got %d: %s", resp.StatusCode, errorBody(t, resp))
	}
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if body["status"] != "dispositioned" {
		t.Errorf(`status = %q, want "dispositioned"`, body["status"])
	}
	if body["warning"] == "" {
		t.Error("expected a warning explaining that rejected does not clear a system-deny-coded finding")
	}
}
