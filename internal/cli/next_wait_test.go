package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestWaitForOperationReportsElapsedTime covers the silence a hung provider
// produces.
//
// The provider client allows 5 minutes per attempt and the engine retries three
// times, so an endpoint that accepts and never answers yields ~15 minutes in
// which `idea new` printed `working…` and nothing else — no elapsed counter, no
// timeout, no way to tell a slow call from a dead one. `idea status` said
// "running" throughout, and `idea watch`, which five commands recommend, needs
// a TTY.
func TestWaitForOperationReportsElapsedTime(t *testing.T) {
	f := newFakeDaemon(t)

	var polls atomic.Int32
	f.mux.HandleFunc("GET /api/v1/operations/{id}", func(w http.ResponseWriter, _ *http.Request) {
		status := "running"
		if polls.Add(1) > 1 {
			status = "succeeded"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": status, "type": "run_initiative"})
	})

	// Report on the first pass rather than making the test wait 20 seconds.
	origAfter, origEvery := progressNoteAfter, progressNoteEvery
	progressNoteAfter, progressNoteEvery = 0, time.Hour
	defer func() { progressNoteAfter, progressNoteEvery = origAfter, origEvery }()

	c, projectID, err := connect()
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if werr := waitForOperation(c, projectID, "ini-1", "op-1", &out); werr != nil {
		t.Fatalf("wait: %v", werr)
	}

	got := out.String()
	if !strings.Contains(got, "still working") {
		t.Errorf("nothing was reported during the wait, so a hung call looks identical to a "+
			"slow one for as long as it takes:\n%q", got)
	}
	if !strings.Contains(got, "elapsed") {
		t.Errorf("the progress line names no elapsed time:\n%q", got)
	}
	if !strings.Contains(got, "idea cancel") {
		t.Errorf("the progress line names no way to stop it:\n%q", got)
	}
}
