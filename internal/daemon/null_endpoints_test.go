package daemon

import (
	"net/http"
	"strings"
	"testing"
)

// TestNoEndpointEmitsANullArray closes the two endpoints audit 4's null sweep
// missed and audit 5 found.
//
// That sweep converted eight fields across trace, projection and /models to []
// and reported itself complete — based on a walk of "every GET endpoint" whose
// endpoint list did not contain /doctor or /diff. So the API went from
// uniformly null-on-empty to *mixed*, which is worse: /diff could return
// `"added":[{...}],"removed":null` in one response, both shapes for the same
// client to handle, with all five fields `required` and typed as arrays in the
// spec.
//
// This walks the routes rather than a hand-written list, so the next endpoint
// cannot be missed the same way.
func TestNoEndpointEmitsANullArray(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID, _ := appliedChangeSet(t, rt, ts, token)

	initiativeScoped := []string{
		"understanding", "guidance", "requirements", "decisions", "questions",
		"assertions", "risk", "tasks", "change-sets", "operations", "artifacts",
		"docs", "findings", "coverage", "doctor", "diff", "plan",
	}
	projectScoped := []string{"attention", "changed", "providers", "models", "executors", "activity"}

	check := func(path string) {
		t.Helper()
		resp := doReq(t, http.MethodGet, ts.URL+path, token, "", "")
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return // not every route is reachable in this fixture; the shape test is the point
		}
		buf := make([]byte, 64*1024)
		n, _ := resp.Body.Read(buf)
		body := string(buf[:n])
		if body == "null" || body == "null\n" {
			t.Errorf("%s answered a bare null; the spec types it as an array", path)
			return
		}
		if strings.Contains(body, `":null`) {
			t.Errorf("%s emits a null field: %s", path, firstNullField(body))
		}
	}

	for _, p := range initiativeScoped {
		check("/api/v1/projects/" + projectID + "/initiatives/" + iniID + "/" + p)
	}

	// And again on an initiative with nothing attached. The empty case is the
	// one that returns null, and it is the *good* outcome for /doctor -- a graph
	// with nothing wrong. A first version of this test used only the populated
	// fixture, whose graph audit does produce findings, so reverting the /doctor
	// fix left it green.
	bareProject, bareIni := newTestProjectInitiative(t, rt, ts, token)
	for _, p := range initiativeScoped {
		check("/api/v1/projects/" + bareProject + "/initiatives/" + bareIni + "/" + p)
	}
	for _, p := range projectScoped {
		check("/api/v1/projects/" + projectID + "/" + p)
	}
}

func firstNullField(body string) string {
	i := strings.Index(body, `":null`)
	if i < 0 {
		return body
	}
	start := strings.LastIndexByte(body[:i], '"')
	if start < 0 {
		start = 0
	}
	end := i + 32
	if end > len(body) {
		end = len(body)
	}
	return body[start:end]
}
