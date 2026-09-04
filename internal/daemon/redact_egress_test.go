package daemon

import (
	"net/http"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/model"
)

// TestTheIdeaIsRedactedBeforeItReachesTheModel pins a credential-egress bug
// found while reviewing the audit-4 fix plan.
//
// StartInitiativeWithDocs redacted the description for the initiative row and
// then passed the *raw* parameter to engine.Run, which sends it to the
// configured model provider. So a credential pasted into an idea was scrubbed
// from local storage and egressed to a third party anyway -- strictly worse than
// keeping it local, since the leak is then irreversible.
//
// It stayed invisible because every retry took the redacted path
// (workflow/retry.go passes ini.Description), so it only ever happened on a
// first run.
//
// The assertion is on what the provider actually received. A first version of
// this test checked the stored description instead and was vacuous: that value
// was already redacted before the fix, so it passed with the bug restored.
func TestTheIdeaIsRedactedBeforeItReachesTheModel(t *testing.T) {
	rt, ts, token := newTestServer(t)

	const secret = "sk-ant-api03-AbCdEfGhIjKlMnOpQrStUvWxYz0123456789ABCDEF"
	resp := doReq(t, http.MethodPost, ts.URL+"/api/v1/projects", token, "",
		`{"name":"p","root_path":"`+t.TempDir()+`"}`)
	var project struct {
		ProjectID string `json:"project_id"`
	}
	decode(t, resp, &project)

	resp = doReq(t, http.MethodPost, ts.URL+"/api/v1/projects/"+project.ProjectID+"/initiatives",
		token, "", `{"title":"billing","description":"use the key `+secret+` for the API"}`)
	var created struct {
		ID          string `json:"initiative_id"`
		OperationID string `json:"operation_id"`
	}
	decode(t, resp, &created)
	waitForOperation(t, ts, token, created.OperationID)

	pr, err := rt.EnsureProject(t.Context(), project.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	prov, ok := pr.registry.Get("idea-harness-default")
	if !ok {
		t.Fatal("the demo provider is not registered; this test cannot see the egress path")
	}
	fake, ok := prov.(*model.FakeProvider)
	if !ok {
		t.Fatalf("the demo provider is %T, not the fake; cannot inspect its calls", prov)
	}
	calls := fake.Calls()
	if len(calls) == 0 {
		t.Fatal("the provider was never called; this test cannot see the egress path")
	}
	for i, c := range calls {
		body := c.System
		for _, m := range c.Messages {
			body += "\n" + m.Content
		}
		if strings.Contains(body, secret) {
			t.Fatalf("model call %d carried the credential to the provider:\n%s",
				i, firstLines(body, secret))
		}
	}
}

func firstLines(s, needle string) string {
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, needle) {
			return line
		}
	}
	return s
}
