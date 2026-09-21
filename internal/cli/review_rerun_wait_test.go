package cli

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// TestReviewRerunWaitsWithoutRacingTheHTTPClientTimeout is ICP re-run D2:
// `idea review <id> rerun` used to run the adversarial review synchronously
// inside one POST, which the daemon always finished but which the CLI's own
// 60s http.Client timed out waiting on -- the review took 1m12s-1m38s in
// every one of four founder-journey observations, so the command appeared to
// fail on every single use even though the work always completed.
//
// The fix makes rerun an async operation: the POST answers 202 immediately
// with an operation id, and the CLI reports success only once polling that
// operation (short, independent GETs -- never one HTTP call spanning the
// whole review) reaches a terminal state. This test proves the shape rather
// than the real timing: the fake daemon answers "running" on the first poll
// and "succeeded" on the second, so a real reviewer's multi-minute runtime
// could never trip a single request's timeout, no matter how long it took
// between polls.
func TestReviewRerunWaitsWithoutRacingTheHTTPClientTimeout(t *testing.T) {
	f := newFakeDaemon(t)

	const opID = "op-rerun-1"
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/change-sets/{id}/review", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Verdict string `json:"verdict"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Verdict != "rerun" {
			t.Fatalf("unexpected verdict in test: %q", body.Verdict)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"operation_id": opID})
	})

	var polls int32
	f.mux.HandleFunc("GET /api/v1/operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		status := "succeeded"
		if atomic.AddInt32(&polls, 1) == 1 {
			// The first poll finds it still running -- proof this is a real
			// poll loop over independent requests, not a single blocking call.
			status = "running"
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"id":     opID,
			"status": status,
			"type":   "rerun_review",
			"stage":  "Running adversarial review",
		})
	})

	stdout, _, err := run(t, "review", "CHG-1", "rerun")
	if err != nil {
		t.Fatalf("review rerun returned an error despite the operation succeeding: %v", err)
	}
	if atomic.LoadInt32(&polls) < 2 {
		t.Fatalf("expected at least 2 polls of the operation, got %d", polls)
	}
	if !strings.Contains(stdout, "review rerun started") {
		t.Errorf("stdout missing the start line: %q", stdout)
	}
	if !strings.Contains(stdout, "review rerun complete") {
		t.Errorf("stdout does not report completion once the operation succeeded: %q", stdout)
	}
}

// TestReviewRerunNoWaitReturnsImmediately pins --no-wait's escape hatch: the
// command must not block on the operation at all when asked not to.
func TestReviewRerunNoWaitReturnsImmediately(t *testing.T) {
	f := newFakeDaemon(t)

	const opID = "op-rerun-2"
	f.json("POST /api/v1/projects/{projectId}/change-sets/{id}/review", map[string]string{"operation_id": opID})

	var polled bool
	f.mux.HandleFunc("GET /api/v1/operations/{id}", func(w http.ResponseWriter, r *http.Request) {
		polled = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "succeeded"})
	})

	stdout, _, err := run(t, "review", "CHG-1", "rerun", "--no-wait")
	if err != nil {
		t.Fatalf("review rerun --no-wait returned an error: %v", err)
	}
	if polled {
		t.Error("review rerun --no-wait polled the operation; it must return immediately")
	}
	if !strings.Contains(stdout, "review rerun started") {
		t.Errorf("stdout missing the start line: %q", stdout)
	}
	if strings.Contains(stdout, "review rerun complete") {
		t.Errorf("stdout claims completion under --no-wait, which never waited: %q", stdout)
	}
}
