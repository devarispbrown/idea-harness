package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// seedRun serves a run with three stored events, paged two then one, and
// current-run naming it.
func (f *fakeDaemon) seedRun(live bool, status string) {
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/current-run", map[string]any{
		"run_id": "run-1", "live": live, "status": status, "executor": "acp", "steerable": true, "pending_notes": []string{},
	})
	at := "2026-10-14T10:00:00Z"
	f.mux.HandleFunc("GET /api/v1/projects/{projectId}/runs/{runId}/events", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("runId") != "run-1" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "run not found"})
			return
		}
		page := map[string]any{"run_id": "run-1", "run_status": status, "first_seq": 1, "last_seq": 3}
		switch r.URL.Query().Get("after") {
		case "0", "":
			page["events"] = []map[string]any{
				{"seq": 1, "at": at, "kind": "status", "title": "Agent started", "status": "running"},
				{"seq": 2, "at": at, "kind": "tool_call", "title": "npm test", "status": "in_progress", "tool_call_id": "c1"},
			}
			page["more"] = true
		default:
			page["events"] = []map[string]any{
				{"seq": 3, "at": at, "kind": "message", "source": "user", "title": "Note from you", "status": "queued"},
			}
			page["more"] = false
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	})
}

func TestRunEventsPagesTheStoredEventsOfTheCurrentRun(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedRun(false, "succeeded")
	stdout, _, err := run(t, "run", "events", "current")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "Agent started") || !strings.Contains(lines[1], "tool_call") ||
		!strings.Contains(lines[1], "in_progress") || !strings.Contains(lines[2], "[you] Note from you") {
		t.Fatalf("output:\n%s", stdout)
	}
	if !f.asked("/runs/run-1/events?after=2") {
		t.Fatalf("second page not asked from the first page's last seq: %v", f.requests)
	}
	if f.asked("/events/stream") {
		t.Fatal("streamed without --follow")
	}
}

func TestRunEventsJSONIsOneObjectPerLine(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedRun(false, "succeeded")
	stdout, _, err := run(t, "run", "events", "run-1", "--json")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 NDJSON lines:\n%s", stdout)
	}
	for i, l := range lines {
		var ev map[string]any
		if err := json.Unmarshal([]byte(l), &ev); err != nil || ev["seq"].(float64) != float64(i+1) {
			t.Fatalf("line %d %q: %v", i, l, err)
		}
	}
	if f.asked("/current-run") {
		t.Fatal("a run id was resolved through current-run")
	}
}

func TestRunEventsUnknownRunIsNotFound(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedRun(false, "succeeded")
	_, _, err := run(t, "run", "events", "nope")
	if r := classify(err); r.Kind != kindNotFound {
		t.Fatalf("kind = %s (%v)", r.Kind, err)
	}
	_, _, err = run(t, "run", "events", "run-1", "ini-1")
	if r := classify(err); r.Kind != kindUsage {
		t.Fatalf("an initiative with a run id: kind = %s (%v)", r.Kind, err)
	}
}

// --follow streams after the paged events, resumes from the last seq it
// printed when the connection drops, and stops on `event: end`.
func TestRunEventsFollowResumesAfterADropAndStopsAtEnd(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedRun(true, "running")
	oldMin, oldMax := runFollowBackoffMin, runFollowBackoffMax
	runFollowBackoffMin, runFollowBackoffMax = time.Millisecond, 2*time.Millisecond
	t.Cleanup(func() { runFollowBackoffMin, runFollowBackoffMax = oldMin, oldMax })

	var conns atomic.Int32
	var afters []string
	f.mux.HandleFunc("GET /api/v1/projects/{projectId}/runs/{runId}/events/stream", func(w http.ResponseWriter, r *http.Request) {
		afters = append(afters, r.URL.Query().Get("after"))
		w.Header().Set("Content-Type", "text/event-stream")
		fl := w.(http.Flusher)
		fmt.Fprint(w, "retry: 3000\n\n: ping\n\n")
		if conns.Add(1) == 1 {
			fmt.Fprint(w, "id: 4\nevent: run_event\ndata: {\"seq\":4,\"kind\":\"tool_result\",\"status\":\"completed\",\"title\":\"npm test passed\"}\n\n")
			fl.Flush()
			return // a dropped connection, no end
		}
		fmt.Fprint(w, "id: 5\nevent: run_event\ndata: {\"seq\":5,\"kind\":\"status\",\"status\":\"finished\",\"title\":\"Run finished\"}\n\n")
		fmt.Fprint(w, "event: end\ndata: {\"run_status\":\"succeeded\"}\n\n")
		fl.Flush()
	})
	stdout, _, err := run(t, "run", "events", "current", "--follow")
	if err != nil {
		t.Fatal(err)
	}
	if len(afters) != 2 || afters[0] != "3" || afters[1] != "4" {
		t.Fatalf("stream cursors = %v, want 3 then 4", afters)
	}
	for _, want := range []string{"Agent started", "npm test passed", "Run finished"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("output lacks %q:\n%s", want, stdout)
		}
	}
	if strings.Count(stdout, "npm test passed") != 1 {
		t.Fatalf("an event was printed twice:\n%s", stdout)
	}
}

func TestRunEventsFollowOfAFinishedRunDoesNotStream(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedRun(false, "failed")
	if _, _, err := run(t, "run", "events", "current", "--follow"); err != nil {
		t.Fatal(err)
	}
	if f.asked("/events/stream") {
		t.Fatal("followed a run that is over")
	}
}

func TestSteerSendsTheNoteAndPrintsWhatBecameOfIt(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedRun(true, "running")
	var body map[string]any
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/runs/{runId}/steer", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"run_id": r.PathValue("runId"), "executor": "acp", "delivery": "queued",
			"message": "The agent will get your note as its next instruction, once its current step ends."})
	})
	stdout, _, err := run(t, "steer", "current", "also update the README", "--interrupt")
	if err != nil {
		t.Fatal(err)
	}
	if body["note"] != "also update the README" || body["interrupt"] != true {
		t.Fatalf("sent %v", body)
	}
	if !strings.Contains(stdout, "next instruction") || !strings.Contains(stdout, "delivery: queued · run run-1") {
		t.Fatalf("output:\n%s", stdout)
	}
}

func TestSteerARunThatIsNotRunningIsAConflictPointingAtExecuteNote(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedRun(false, "succeeded")
	f.fail("POST /api/v1/projects/{projectId}/runs/{runId}/steer", http.StatusConflict, "run run-9 is not running")
	_, _, err := run(t, "steer", "current", "x")
	r := classify(err)
	if r.Kind != kindConflict || !strings.Contains(r.Hint, "idea execute --note") {
		t.Fatalf("current, not live: %+v", r)
	}
	if f.asked("/steer") {
		t.Fatal("posted a note to a run current-run says is over")
	}
	_, _, err = run(t, "steer", "run-9", "x")
	var ce *cliError
	if r := classify(err); r.Kind != kindConflict || !errors.As(err, &ce) || !strings.Contains(r.Hint, "idea execute --note") {
		t.Fatalf("explicit id, daemon 409: %+v", r)
	}
	if _, _, err := run(t, "steer", "run-9"); classify(err).Kind != kindUsage {
		t.Fatalf("no note: %v", err)
	}
}
