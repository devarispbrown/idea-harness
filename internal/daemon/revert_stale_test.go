package daemon

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRevertOfAStaleWorkspaceIs409 is the API side of review A3: an edit made
// in the workspace after apply must not be silently erased by revert, and the
// refusal must be a synchronous 409 -- not a "202 Accepted" whose failure only
// ever shows up in an operation's background log (the same class of defect
// TestRevertRejectsANonAppliedChangeSet pins for the status guard).
func TestRevertOfAStaleWorkspaceIs409(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID, csID := appliedChangeSet(t, rt, ts, token)

	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	// The internal executor's placeholder file: edit it the way a human would
	// after apply, before ever calling revert.
	hello := filepath.Join(pr.rootPath, "hello.txt")
	edited := "a human's note, added after apply\n"
	if err := os.WriteFile(hello, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}

	base := ts.URL + "/api/v1/projects/" + projectID
	resp := doReq(t, http.MethodPost, base+"/change-sets/"+csID+"/revert", token, "", "")
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("revert of a stale workspace = %d, want 409 (got %q)",
			resp.StatusCode, readError(t, resp))
	}
	msg := readError(t, resp)
	if !strings.Contains(msg, "hello.txt") {
		t.Errorf("409 message does not name the changed file: %q", msg)
	}
	// review M4: committing does not change the file's bytes, so the advice
	// must say discard (or stash), never "commit".
	if !strings.Contains(msg, "discard") {
		t.Errorf("409 message does not say what to do next: %q", msg)
	}
	if strings.Contains(msg, "commit") {
		t.Errorf("409 message suggests committing, which cannot help: %q", msg)
	}

	// Nothing was written: no operation was even started, and the file the
	// human just edited is untouched.
	got, err := os.ReadFile(hello)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != edited {
		t.Fatalf("revert wrote the workspace despite the 409:\ngot:  %q\nwant: %q", got, edited)
	}
	ini := statusOf(t, rt, projectID, iniID)
	if ini.Status == "running" {
		t.Error("a refused revert left the initiative claimed as running")
	}
}
