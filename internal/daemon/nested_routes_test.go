package daemon

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// waitForOperation blocks until an asynchronous operation reaches a terminal
// state, so a test does not return while the workflow is still writing into the
// state root that t.TempDir is about to remove.
func waitForOperation(t *testing.T, ts *httptest.Server, token, opID string) {
	t.Helper()
	if opID == "" {
		return
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp := doReq(t, http.MethodGet, ts.URL+"/api/v1/operations/"+opID, token, "", "")
		var op struct {
			Status string `json:"status"`
		}
		decode(t, resp, &op)
		switch op.Status {
		case "succeeded", "failed", "cancelled":
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation %s did not finish (last status %q)", opID, op.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// newSeededProject creates a project with one initiative and waits for its
// framing workflow, returning the project base URL and the initiative id.
func newSeededProject(t *testing.T, ts *httptest.Server, token, rootPath string) (string, string) {
	t.Helper()
	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects", token, "",
		`{"name":"p","root_path":"`+rootPath+`"}`)
	var project struct {
		ProjectID string `json:"project_id"`
	}
	decode(t, resp, &project)
	if project.ProjectID == "" {
		t.Fatal("could not create a project")
	}
	base := ts.URL + "/api/v1/projects/" + project.ProjectID

	resp = doReq(t, http.MethodPost, base+"/initiatives", token, "",
		`{"title":"route test","description":"x"}`)
	var created struct {
		ID          string `json:"initiative_id"`
		OperationID string `json:"operation_id"`
	}
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatal("could not create an initiative")
	}
	waitForOperation(t, ts, token, created.OperationID)
	return base, created.ID
}

// initiativeScopedResources are the ten reads that used to answer at
// /projects/{projectId}/<resource>?initiative_id= and now answer only under the
// initiative they describe.
var initiativeScopedResources = []string{
	"requirements", "assertions", "artifacts", "decisions", "questions",
	"findings", "change-sets", "suggested-answers", "risk", "doctor",
}

// M2 step 3: the flat forms are gone. Asserted rather than assumed, because a
// removal that silently left a route behind would leave the API with the two
// shapes this migration existed to collapse.
func TestFlatInitiativeRoutesAreGone(t *testing.T) {
	_, ts, token := newTestServer(t)
	base, iniID := newSeededProject(t, ts, token, "/tmp/flat-routes-gone")

	for _, resource := range initiativeScopedResources {
		t.Run(resource, func(t *testing.T) {
			flat := doReq(t, http.MethodGet,
				base+"/"+resource+"?initiative_id="+iniID, token, "", "")
			defer flat.Body.Close()
			if flat.StatusCode != http.StatusNotFound {
				t.Errorf("flat route still answers with %d; it should be gone", flat.StatusCode)
			}

			nested := doReq(t, http.MethodGet, base+"/initiatives/"+iniID+"/"+resource, token, "", "")
			defer nested.Body.Close()
			if nested.StatusCode != http.StatusOK {
				t.Errorf("nested route answers %d, want 200", nested.StatusCode)
			}
		})
	}
}

// A client that has not caught up may still append ?initiative_id= to a nested
// URL. The path is the only thing that decides now, so a stale parameter naming
// a different initiative must be ignored rather than honored.
func TestQueryParameterNoLongerSelectsTheInitiative(t *testing.T) {
	_, ts, token := newTestServer(t)
	base, first := newSeededProject(t, ts, token, "/tmp/query-ignored")

	resp := doReq(t, http.MethodPost, base+"/initiatives", token, "",
		`{"title":"second","description":"x"}`)
	var second struct {
		ID          string `json:"initiative_id"`
		OperationID string `json:"operation_id"`
	}
	decode(t, resp, &second)
	waitForOperation(t, ts, token, second.OperationID)
	if second.ID == "" || second.ID == first {
		t.Fatalf("expected a distinct second initiative, got %q and %q", first, second.ID)
	}

	withStale := doReq(t, http.MethodGet,
		base+"/initiatives/"+first+"/doctor?initiative_id="+second.ID, token, "", "")
	pathOnly := doReq(t, http.MethodGet, base+"/initiatives/"+first+"/doctor", token, "", "")

	staleBody, pathBody := readBody(t, withStale), readBody(t, pathOnly)
	if withStale.StatusCode != http.StatusOK || pathOnly.StatusCode != http.StatusOK {
		t.Fatalf("status: withStale=%d pathOnly=%d", withStale.StatusCode, pathOnly.StatusCode)
	}
	if staleBody != pathBody {
		t.Errorf("a stale ?initiative_id= changed the answer.\n with param: %s\n path only: %s",
			staleBody, pathBody)
	}
}
