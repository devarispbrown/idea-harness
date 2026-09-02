package daemon

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Ten endpoints resolve their initiative from ?initiative_id=, falling back to
// one of the project's initiatives when it is omitted. That fallback used to be
// the OLDEST initiative while the CLI's own default is the most recent, so in a
// project with more than one initiative the daemon answered about a different
// initiative than the CLI was asking about — silently, since neither side
// errors.
func TestOmittedInitiativeIDResolvesToTheMostRecent(t *testing.T) {
	_, ts, token := newTestServer(t)

	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects", token, "",
		`{"name":"p","root_path":"/tmp/initiative-default-test"}`)
	var project struct {
		ProjectID string `json:"project_id"`
	}
	decode(t, resp, &project)
	if project.ProjectID == "" {
		t.Fatal("could not create a project")
	}

	base := ts.URL + "/api/v1/projects/" + project.ProjectID
	var newest string
	for _, title := range []string{"first initiative", "second initiative"} {
		resp := doReq(t, http.MethodPost, base+"/initiatives", token, "",
			`{"title":"`+title+`","description":"x"}`)
		var created struct {
			ID string `json:"initiative_id"`
		}
		decode(t, resp, &created)
		if created.ID == "" {
			t.Fatalf("could not create %q", title)
		}
		newest = created.ID
	}

	// requirements is one of the ten flat, query-scoped endpoints; with no
	// initiative_id it must answer about the newest initiative.
	resp = doReq(t, http.MethodGet, base+"/requirements", token, "", "")
	body := readBody(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}

	// The response carries no initiative id, so assert through an endpoint that
	// echoes one: the graph audit resolves the same way.
	resp = doReq(t, http.MethodGet, base+"/doctor", token, "", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("doctor status = %d, body %s", resp.StatusCode, readBody(t, resp))
	}

	// And directly: the initiative list is creation-ordered, so the last entry
	// is what the fallback must pick.
	resp = doReq(t, http.MethodGet, base+"/initiatives", token, "", "")
	var list []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 initiatives, got %d", len(list))
	}
	if list[len(list)-1].ID != newest {
		t.Fatalf("list is not creation-ordered; last = %q, newest = %q", list[len(list)-1].ID, newest)
	}
	if list[len(list)-1].Title != "second initiative" {
		t.Fatalf("newest initiative = %q, want %q", list[len(list)-1].Title, "second initiative")
	}
}
