package daemon

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestReviewVerdictVocabulary pins the wire vocabulary against the spec.
//
// docs/openapi.yaml declared `enum: [approve, reject, changes_requested]`.
// The engine's switch takes `approved` and `rejected`, so two of the three
// documented values were rejected by the endpoint: a client generated from the
// spec could not approve or reject a change set at all. The CLI accepts the
// short forms and maps them before sending, which is how the spec came to
// document the CLI's vocabulary instead of the API's.
func TestReviewVerdictVocabulary(t *testing.T) {
	for _, c := range []struct {
		verdict string
		// A verdict the engine knows reaches the state machine and is refused on
		// the change set's status; one it does not know is refused as an unknown
		// word. Both are 400, so the word is what distinguishes them.
		wantUnknown bool
	}{
		{"approved", false},
		{"rejected", false},
		{"changes_requested", false},
		{"approve", true},
		{"reject", true},
	} {
		t.Run(c.verdict, func(t *testing.T) {
			rt, ts, token := newTestServer(t)
			projectID, iniID, csID := appliedChangeSet(t, rt, ts, token)
			_ = iniID

			resp := doReq(t, http.MethodPost,
				ts.URL+"/api/v1/projects/"+projectID+"/change-sets/"+csID+"/review",
				token, "", `{"verdict":"`+c.verdict+`"}`)
			defer resp.Body.Close()

			// The precise later status does not matter; what matters is whether
			// the verdict word itself was understood.
			b, _ := io.ReadAll(resp.Body)
			gotUnknown := strings.Contains(string(b), "unknown review verdict")
			if gotUnknown != c.wantUnknown {
				t.Errorf("verdict %q: %d %s\n  wantUnknown=%v — the spec's enum and the engine's "+
					"switch disagree, so a spec-generated client cannot use this verdict",
					c.verdict, resp.StatusCode, string(b), c.wantUnknown)
			}
		})
	}
}
