package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// This file covers `idea disposition`, added because
// POST .../findings/{id}/disposition had no CLI caller at all (audit 6 / D1):
// the daemon's own guidance for a secret-sweep rejection even told a user to
// run `idea reviews` to do this, which only ever listed findings. See
// disposition.go's section comment.

func TestDispositionSendsTheGivenValue(t *testing.T) {
	f := newFakeDaemon(t)
	seedTaskInitiative(f)
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/findings", []map[string]any{
		{"id": "41d2b350b21baa8b875585217bf0bf94", "category": "logic", "finding": "unhandled nil", "severity": "medium"},
	})
	var body string
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/findings/{id}/disposition",
		func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			body = string(raw)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "dispositioned"})
		})

	stdout, _, err := run(t, "disposition", "41d2b350", "fixed")
	if err != nil {
		t.Fatalf("idea disposition: %v", err)
	}
	if !strings.Contains(stdout, "dispositioned") || !strings.Contains(stdout, "fixed") {
		t.Errorf("expected a confirmation naming the disposition:\n%s", stdout)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(body), &sent); err != nil {
		t.Fatalf("request body not JSON: %v", err)
	}
	if sent["disposition"] != "fixed" {
		t.Errorf("disposition not sent: %v", sent)
	}
}

// TestDispositionRejectsAnUnknownValueLocally is the main refusal: the server
// 400s an invalid disposition (store.SetFindingDisposition), and the CLI says
// so before the round trip.
func TestDispositionRejectsAnUnknownValueLocally(t *testing.T) {
	f := newFakeDaemon(t)
	seedTaskInitiative(f)
	before := len(f.requests)

	_, _, err := run(t, "disposition", "41d2b350", "wont-fix")
	if err == nil {
		t.Fatal(`disposition "wont-fix": want an error`)
	}
	if !strings.Contains(err.Error(), "wont-fix") {
		t.Errorf("error should name the rejected value, got: %v", err)
	}
	if len(f.requests) != before {
		t.Errorf("disposition reached the daemon before validating locally: %v", f.requests[before:])
	}
}

// TestDispositionNotASecretRefusedOffASecretSweepFindingLocally mirrors
// SetFindingDisposition's own category guard: not_a_secret is only valid on a
// secret_sweep finding.
func TestDispositionNotASecretRefusedOffASecretSweepFindingLocally(t *testing.T) {
	f := newFakeDaemon(t)
	seedTaskInitiative(f)
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/findings", []map[string]any{
		{"id": "f1", "category": "logic", "finding": "unhandled nil", "severity": "medium"},
	})

	_, _, err := run(t, "disposition", "f1", "not_a_secret")
	if err == nil {
		t.Fatal("not_a_secret on a logic finding: want an error")
	}
	if !strings.Contains(err.Error(), "secret_sweep") {
		t.Errorf("error should match the server's own wording, got: %v", err)
	}
	if f.asked("/disposition") {
		t.Error("the refusal reached the daemon")
	}
}

// TestDispositionNotASecretRefusedOnAConfiguredSecretLocally mirrors
// SetFindingDisposition's unconditional refusal: a match drawn from the
// project's own configured secrets (SecretOverridable == false) is never
// eligible, pattern-shaped false positive or not.
func TestDispositionNotASecretRefusedOnAConfiguredSecretLocally(t *testing.T) {
	f := newFakeDaemon(t)
	seedTaskInitiative(f)
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/findings", []map[string]any{
		{"id": "f1", "category": "secret_sweep", "finding": "looks like a configured secret",
			"severity": "critical", "secret_kind": "configured secret", "secret_overridable": false},
	})

	_, _, err := run(t, "disposition", "f1", "not_a_secret")
	if err == nil {
		t.Fatal("not_a_secret on a configured secret: want an error")
	}
	if !strings.Contains(err.Error(), "never eligible") {
		t.Errorf("error should match the server's own wording, got: %v", err)
	}
	if f.asked("/disposition") {
		t.Error("the refusal reached the daemon")
	}
}

// TestDispositionSurfacesTheRejectedWarningVerbatim: `rejected` resolves a
// reviewer's judgment call, but a finding carrying a system deny code still
// blocks afterward, and the server reports that as a `warning` field on an
// otherwise-200 response (handleFindingDisposition) -- the CLI must print it,
// not swallow it on the grounds that the call "succeeded".
func TestDispositionSurfacesTheRejectedWarningVerbatim(t *testing.T) {
	f := newFakeDaemon(t)
	seedTaskInitiative(f)
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/findings", []map[string]any{
		{"id": "f1", "category": "secret_sweep", "finding": "a secret", "severity": "critical"},
	})
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/findings/{id}/disposition",
		func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"status":  "dispositioned",
				"warning": `this finding carries system deny code "secret_sweep"; rejected does not clear it -- only fixing the underlying problem does`,
			})
		})

	stdout, _, err := run(t, "disposition", "f1", "rejected")
	if err != nil {
		t.Fatalf("idea disposition f1 rejected: %v", err)
	}
	if !strings.Contains(stdout, "does not clear it") {
		t.Errorf("expected the server's warning in output:\n%s", stdout)
	}
}

// TestDispositionWithNoteAlsoRecordsIt: --note additionally calls the "note"
// (discuss) route in the same command.
func TestDispositionWithNoteAlsoRecordsIt(t *testing.T) {
	f := newFakeDaemon(t)
	seedTaskInitiative(f)
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/findings", []map[string]any{
		{"id": "f1", "category": "logic", "finding": "unhandled nil", "severity": "medium"},
	})
	f.json("POST /api/v1/projects/{projectId}/findings/{id}/disposition", map[string]string{"status": "dispositioned"})
	var noteBody string
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/findings/{id}/note",
		func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			noteBody = string(raw)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
		})

	stdout, _, err := run(t, "disposition", "f1", "deferred", "--note", "revisit after the auth rewrite")
	if err != nil {
		t.Fatalf("idea disposition --note: %v", err)
	}
	if !strings.Contains(stdout, "note recorded") {
		t.Errorf("expected a note confirmation:\n%s", stdout)
	}
	if !strings.Contains(noteBody, "revisit after the auth rewrite") {
		t.Errorf("note not sent: %s", noteBody)
	}
}
