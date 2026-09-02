package daemon

import (
	"encoding/json"
	"net/http"
	"testing"
)

// M2 step 1: the ten initiative-scoped reads are registered on both the nested
// path and the flat `?initiative_id=` form, sharing a handler. While both exist,
// they must answer identically — that equivalence is what makes step 2 (moving
// the CLI and the UI over) safe to do incrementally, and step 3 (deleting the
// flat form) a removal rather than a change.
func TestNestedAndFlatRoutesAgree(t *testing.T) {
	_, ts, token := newTestServer(t)

	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects", token, "",
		`{"name":"p","root_path":"/tmp/nested-routes-test"}`)
	var project struct {
		ProjectID string `json:"project_id"`
	}
	decode(t, resp, &project)
	if project.ProjectID == "" {
		t.Fatal("could not create a project")
	}
	base := ts.URL + "/api/v1/projects/" + project.ProjectID

	resp = doReq(t, http.MethodPost, base+"/initiatives", token, "",
		`{"title":"nested route test","description":"x"}`)
	var created struct {
		ID          string `json:"initiative_id"`
		OperationID string `json:"operation_id"`
	}
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatal("could not create an initiative")
	}
	// The framing workflow writes into the state root; let it finish so the
	// temp dir teardown does not race it.
	waitForOperation(t, ts, token, created.OperationID)

	for _, resource := range []string{
		"requirements", "assertions", "artifacts", "decisions", "questions",
		"findings", "change-sets", "suggested-answers", "risk", "doctor",
	} {
		t.Run(resource, func(t *testing.T) {
			flat := doReq(t, http.MethodGet,
				base+"/"+resource+"?initiative_id="+created.ID, token, "", "")
			flatBody := readBody(t, flat)

			nested := doReq(t, http.MethodGet,
				base+"/initiatives/"+created.ID+"/"+resource, token, "", "")
			nestedBody := readBody(t, nested)

			if nested.StatusCode != flat.StatusCode {
				t.Errorf("status: nested %d, flat %d", nested.StatusCode, flat.StatusCode)
			}
			if nested.StatusCode != http.StatusOK {
				t.Fatalf("expected 200 from both, got %d (nested body: %s)", nested.StatusCode, nestedBody)
			}

			// /risk computes a fresh profile per request, so its id and
			// created_at differ between any two calls regardless of routing.
			// Everything else — including the initiative_id the route resolved,
			// which is what this test is actually about — must match exactly.
			if resource == "risk" {
				nestedBody = stripVolatile(t, nestedBody)
				flatBody = stripVolatile(t, flatBody)
			}
			if nestedBody != flatBody {
				t.Errorf("bodies differ.\n nested: %s\n   flat: %s", nestedBody, flatBody)
			}
		})
	}
}

// stripVolatile removes the per-request fields of a risk profile, so two calls
// can be compared for everything the routing decides.
func stripVolatile(t *testing.T, body string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(body), &m); err != nil {
		t.Fatalf("not JSON: %v (%s)", err, body)
	}
	delete(m, "id")
	delete(m, "created_at")
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// The path value wins over the query, so a request carrying both is answered
// about the path's initiative. Without that precedence the migration would have
// a window where a half-migrated client could be answered about the wrong one.
func TestNestedPathBeatsTheQueryParameter(t *testing.T) {
	_, ts, token := newTestServer(t)

	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects", token, "",
		`{"name":"p","root_path":"/tmp/nested-precedence-test"}`)
	var project struct {
		ProjectID string `json:"project_id"`
	}
	decode(t, resp, &project)
	base := ts.URL + "/api/v1/projects/" + project.ProjectID

	var ids []string
	for _, title := range []string{"first", "second"} {
		resp := doReq(t, http.MethodPost, base+"/initiatives", token, "",
			`{"title":"`+title+`","description":"x"}`)
		var created struct {
			ID          string `json:"initiative_id"`
			OperationID string `json:"operation_id"`
		}
		decode(t, resp, &created)
		waitForOperation(t, ts, token, created.OperationID)
		ids = append(ids, created.ID)
	}
	if len(ids) != 2 || ids[0] == ids[1] {
		t.Fatalf("expected two distinct initiatives, got %v", ids)
	}

	// Path names the first, query names the second: the path must win.
	both := doReq(t, http.MethodGet,
		base+"/initiatives/"+ids[0]+"/doctor?initiative_id="+ids[1], token, "", "")
	pathOnly := doReq(t, http.MethodGet, base+"/initiatives/"+ids[0]+"/doctor", token, "", "")

	bothBody, pathBody := readBody(t, both), readBody(t, pathOnly)
	if both.StatusCode != http.StatusOK || pathOnly.StatusCode != http.StatusOK {
		t.Fatalf("status: both=%d pathOnly=%d", both.StatusCode, pathOnly.StatusCode)
	}
	if bothBody != pathBody {
		t.Errorf("the query overrode the path.\n with query: %s\n path only: %s", bothBody, pathBody)
	}
}
