package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestExecuteSendsNote pins D-a: `idea execute --note` puts the note in the
// POST body; without the flag, no body is sent at all (not `{"note":""}`),
// matching the endpoint's lenient decode.
func TestExecuteSendsNote(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	var bodies []string
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/execute",
		func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(raw))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"operation_id": "", "executor_kind": "internal"})
		})

	if _, _, err := run(t, "execute", "--note", "add a rate limit header"); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 {
		t.Fatalf("got %d execute requests, want 1", len(bodies))
	}
	if !strings.Contains(bodies[0], `"note":"add a rate limit header"`) {
		t.Fatalf("execute body = %q, want it to carry the note", bodies[0])
	}

	if _, _, err := run(t, "execute"); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("got %d execute requests, want 2", len(bodies))
	}
	if bodies[1] != "" {
		t.Fatalf("execute with no --note sent a body: %q, want none", bodies[1])
	}
}

// TestRetrySendsNote pins D-a for `idea retry --note`.
func TestRetrySendsNote(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.providers = cliProvidersView{DefaultModel: "openai/gpt-5"}
	var bodies []string
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/initiatives/{id}/retry",
		func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			bodies = append(bodies, string(raw))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"operation_id": ""})
		})

	if _, _, err := run(t, "retry", "--note", "try a smaller diff this time"); err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 1 {
		t.Fatalf("got %d retry requests, want 1", len(bodies))
	}
	if !strings.Contains(bodies[0], `"note":"try a smaller diff this time"`) {
		t.Fatalf("retry body = %q, want it to carry the note", bodies[0])
	}
	if !strings.Contains(bodies[0], `"model":"openai/gpt-5"`) {
		t.Fatalf("retry body = %q, want the default model", bodies[0])
	}
}

// TestVerifyConfirmPassingTestsFlag pins the CLI's --confirm-passing-tests.
func TestVerifyConfirmPassingTestsFlag(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()

	if _, _, err := run(t, "verify", "--confirm-passing-tests"); err != nil {
		t.Fatal(err)
	}
	if len(f.verifyBodies) != 1 {
		t.Fatalf("got %d verify requests, want 1", len(f.verifyBodies))
	}
	if !strings.Contains(f.verifyBodies[0], `"confirm_passing_tests":true`) {
		t.Fatalf("verify body = %q, want confirm_passing_tests", f.verifyBodies[0])
	}
}
