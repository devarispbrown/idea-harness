package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// TestSuggestAnswersRefusesAnInitiativeThatDoesNotExist closes the C4 item,
// recorded in deferred.md since audit 3 and found again by audit 5.
//
// It was the one 202 endpoint that lied about accepting work: POST against a
// nonexistent initiative returned a real operation id, giving the client a
// pollable handle for work that could never succeed and leaving an operations
// row with a dangling initiative_id. Every sibling answered 404.
func TestSuggestAnswersRefusesAnInitiativeThatDoesNotExist(t *testing.T) {
	_, ts, token := newTestServer(t)
	projectID := newTestProject(t, ts.URL, token)

	for _, path := range []string{"suggest-answers", "advance", "execute"} {
		t.Run(path, func(t *testing.T) {
			resp := doReq(t, http.MethodPost,
				ts.URL+"/api/v1/projects/"+projectID+"/initiatives/NOPE/"+path, token, "", "")
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusAccepted {
				t.Fatalf("%s accepted work for an initiative that does not exist (202)", path)
			}
			if resp.StatusCode != http.StatusNotFound {
				t.Errorf("%s = %d, want 404 to match its siblings", path, resp.StatusCode)
			}
		})
	}
}

// TestUnknownNodeTypeIsRejected: a bad id was caught and a bad type was not, so
// `idea why requirements REQ-1` — one character wrong — answered "No provenance
// edges found." and exited 0. On a tool whose headline claim is provenance, a
// confident false negative is the worst possible answer.
func TestUnknownNodeTypeIsRejected(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID, _ := appliedChangeSet(t, rt, ts, token)

	for _, c := range []struct {
		nodeType   string
		wantStatus int
	}{
		{"requirement", http.StatusOK},
		{"task", http.StatusOK},
		{"requirements", http.StatusBadRequest}, // the plural typo
		{"Requirement", http.StatusBadRequest},  // wrong case
		{"req", http.StatusBadRequest},          // an abbreviation
	} {
		t.Run(c.nodeType, func(t *testing.T) {
			resp := doReq(t, http.MethodGet,
				ts.URL+"/api/v1/projects/"+projectID+"/initiatives/"+iniID+
					"/why?type="+c.nodeType+"&id=REQ-1", token, "", "")
			defer resp.Body.Close()
			if resp.StatusCode != c.wantStatus {
				t.Fatalf("type=%q = %d, want %d", c.nodeType, resp.StatusCode, c.wantStatus)
			}
			if c.wantStatus != http.StatusBadRequest {
				return
			}
			var body struct {
				Error string `json:"error"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&body)
			if !strings.Contains(body.Error, "requirement") {
				t.Errorf("the error should name the valid types, got: %s", body.Error)
			}
		})
	}
}
