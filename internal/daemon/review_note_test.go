package daemon

import (
	"net/http"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestRequestChangesRecordsTheNote pins POST .../review {"note"}: "Request
// changes" used to take no input, so the task's rerun had nothing new to go
// on. The note is stored on the change set and served back as review_note; a
// note on a verdict with no next run to steer is refused, not dropped.
func TestRequestChangesRecordsTheNote(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, _, csID := reviewRequiredChangeSet(t, rt, ts, token)
	url := ts.URL + "/api/v1/projects/" + projectID + "/change-sets/" + csID + "/review"

	for _, body := range []string{
		`{"verdict":"approved","note":"ship it"}`,
		`{"verdict":"rerun","note":"look again"}`,
	} {
		resp := doReq(t, http.MethodPost, url, token, "", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, resp.StatusCode)
		}
		resp.Body.Close()
	}

	resp := doReq(t, http.MethodPost, url, token, "", `{"verdict":"changes_requested","note":"keep the public API unchanged"}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("changes_requested with a note: status %d (%s)", resp.StatusCode, errorBody(t, resp))
	}
	resp.Body.Close()

	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := pr.db.GetChangeSet(t.Context(), csID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetChangesRequested || cs.ReviewNote != "keep the public API unchanged" {
		t.Fatalf("change set is %s with review_note %q", cs.Status, cs.ReviewNote)
	}
}
