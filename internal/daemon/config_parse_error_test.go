package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// A malformed harness.yaml is the user's file, not the daemon's fault, and the
// response has to say which.
//
// config.Load runs while resolving the project, so its failure reached every
// project-scoped handler as a bare error and was mapped to 404 on all of them —
// neither a store miss nor "a message naming the kind and the reference that
// was asked for", which is what the spec's NotFound promises. It also carried
// the absolute path of the file, which InternalError explicitly promises never
// reaches a client.
//
// 500 would be the wrong repair: errors.go reserves it for the daemon's own
// faults, and telling a client to retry a syntax error in a file only the user
// can fix is worse than the 404 was.
//
// This is reachable only on a cold daemon: EnsureProject caches the loaded
// config for the process's lifetime, which is the same reason `idea daemon
// stop` is in every "export your key" instruction.
func TestConfigParseErrorIsTypedAndProjectRelative(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "harness.yaml"),
		[]byte("review:\n  max_cycles: [broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := config.Load(dir)
	if err == nil {
		t.Fatal("a malformed harness.yaml loaded without error")
	}
	var pe *config.ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("error is %T, not *config.ParseError — writeErr cannot classify it, so every\n"+
			"project-scoped route reports it as a 404: %v", err, err)
	}
	if strings.Contains(err.Error(), dir) {
		t.Errorf("the error carries an absolute path, which the InternalError contract promises\n"+
			"never reaches a client: %v", err)
	}
	if !strings.Contains(err.Error(), "harness.yaml") {
		t.Errorf("the error does not name the file the user has to fix: %v", err)
	}
}

// The schema half, which is the likelier malformation: a file that is valid
// YAML and that the schema rejects. `tests_cmd` is not a hypothetical — it is
// the key this project's own `idea reviews` told users to write.
//
// Untyped, this reached every project-scoped route as a 404 carrying raw
// jsonschema text, and the first version of this fix covered only syntax
// errors while the CHANGELOG claimed "a malformed harness.yaml".
func TestSchemaRejectionIsAlsoATypedParseError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "harness.yaml"),
		[]byte("tests_cmd: go test ./...\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := config.Load(dir)
	if err == nil {
		t.Fatal("the schema accepted a key it does not declare")
	}
	var pe *config.ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("error is %T, not *config.ParseError — so a schema rejection still reports as a\n"+
			"404 store miss on every project-scoped route: %v", err, err)
	}
	if strings.Contains(err.Error(), dir) {
		t.Errorf("the error carries an absolute path: %v", err)
	}
}

// The mapping half: writeErr has to turn that type into a 4xx that names the
// file, rather than letting it fall through to the 404 or the masked 500.
func TestWriteErrMapsAConfigParseErrorTo422(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.writeErr(rec, http.StatusInternalServerError,
		&config.ParseError{File: "harness.yaml", Msg: "yaml: line 2: did not find expected ',' or ']'"})

	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status %d, want 422 — a malformed user file is neither a store miss (404) nor\n"+
			"the daemon's fault (500), and the handler's guessed status must not win", rec.Code)
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("response is not the JSON error envelope: %v", err)
	}
	if !strings.Contains(body["error"], "harness.yaml") {
		t.Errorf("the response does not name the file: %q", body["error"])
	}
	if strings.Contains(body["error"], "correlation id") {
		t.Errorf("the response was masked as an internal error: %q", body["error"])
	}
}
