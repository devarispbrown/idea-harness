package daemon

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// sseFrame is one parsed SSE message (comments are kept as event ":").
type sseFrame struct {
	id, event, data string
}

// sseReader parses frames off a stream in the background.
type sseReader struct {
	frames chan sseFrame
	body   io.ReadCloser
}

func openSSE(t *testing.T, url, token string, header map[string]string) (*http.Response, *sseReader) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("X-Idea-Token", token)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	r := &sseReader{frames: make(chan sseFrame, 1024), body: resp.Body}
	go func() {
		defer close(r.frames)
		br := bufio.NewReader(resp.Body)
		var f sseFrame
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				return
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case line == "":
				if f != (sseFrame{}) {
					r.frames <- f
				}
				f = sseFrame{}
			case strings.HasPrefix(line, ":"):
				r.frames <- sseFrame{event: ":"}
			case strings.HasPrefix(line, "id: "):
				f.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				f.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				f.data = strings.TrimPrefix(line, "data: ")
			}
		}
	}()
	return resp, r
}

// next returns the next frame that is not a comment or the retry hint.
func (r *sseReader) next(t *testing.T, within time.Duration) (sseFrame, bool) {
	t.Helper()
	deadline := time.After(within)
	for {
		select {
		case f, ok := <-r.frames:
			if !ok {
				return sseFrame{}, false
			}
			if f.event == ":" || (f.id == "" && f.event == "" && f.data == "") {
				continue
			}
			return f, true
		case <-deadline:
			return sseFrame{}, false
		}
	}
}

// runFixture executes a task on the internal executor and returns the agent
// run that produced the change set (it has a full event stream).
func runFixture(t *testing.T) (rt *Runtime, base, token, projectID, iniID, runID string) {
	t.Helper()
	rt, ts, token := newTestServer(t)
	projectID, iniID, _ = reviewRequiredChangeSet(t, rt, ts, token)
	pr, err := rt.EnsureProject(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	runID, err = pr.db.LatestEventRunForInitiative(context.Background(), iniID)
	if err != nil || runID == "" {
		t.Fatalf("no run with events: %v", err)
	}
	return rt, ts.URL + "/api/v1/projects/" + projectID, token, projectID, iniID, runID
}

func TestRunEventsPageResumesAfterASeq(t *testing.T) {
	_, base, token, _, _, runID := runFixture(t)
	var page struct {
		RunID     string            `json:"run_id"`
		RunStatus string            `json:"run_status"`
		Events    []domain.RunEvent `json:"events"`
		FirstSeq  int64             `json:"first_seq"`
		LastSeq   int64             `json:"last_seq"`
		More      bool              `json:"more"`
	}
	resp := httpDo(t, http.MethodGet, base+"/runs/"+runID+"/events?after=3&limit=2", token, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(page.Events) != 2 || page.Events[0].Seq != 4 || !page.More || page.FirstSeq != 1 || page.RunStatus != "succeeded" {
		t.Fatalf("page = %+v", page)
	}
	if r := httpDo(t, http.MethodGet, base+"/runs/"+runID+"/events?after=-1", token, ""); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("after=-1: %d, want 400", r.StatusCode)
	}
	// The JSON page is not a stream: a token in the URL is refused.
	if r := httpDo(t, http.MethodGet, base+"/runs/"+runID+"/events?token="+token, "", ""); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("query token on the JSON page: %d, want 401", r.StatusCode)
	}
	if r := httpDo(t, http.MethodGet, base+"/runs/no-such-run/events", token, ""); r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown run: %d", r.StatusCode)
	}
}

// A reconnect with Last-Event-ID (or ?after=) gets only the events after it
// -- never the run again from the start -- and a finished run's stream ends
// with `event: end` so EventSource stops reconnecting.
func TestRunEventStreamResumesFromLastEventID(t *testing.T) {
	old := runStreamEndGrace
	runStreamEndGrace = 50 * time.Millisecond
	t.Cleanup(func() { runStreamEndGrace = old })
	_, base, token, _, _, runID := runFixture(t)

	for _, tc := range []struct {
		name   string
		url    string
		header map[string]string
		token  string
		first  int64
	}{
		{"Last-Event-ID", base + "/runs/" + runID + "/events/stream", map[string]string{"Last-Event-ID": "3"}, token, 4},
		{"after", base + "/runs/" + runID + "/events/stream?after=5", nil, token, 6},
		{"header wins over after", base + "/runs/" + runID + "/events/stream?after=1", map[string]string{"Last-Event-ID": "6"}, token, 7},
		{"query token (EventSource)", base + "/runs/" + runID + "/events/stream?token=" + token, nil, "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, r := openSSE(t, tc.url, tc.token, tc.header)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode)
			}
			if ct, cc, xab := resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control"), resp.Header.Get("X-Accel-Buffering"); ct != "text/event-stream" ||
				!strings.Contains(cc, "no-transform") || xab != "no" {
				t.Fatalf("headers: %q %q %q", ct, cc, xab)
			}
			want := tc.first
			for {
				f, ok := r.next(t, 5*time.Second)
				if !ok {
					t.Fatal("the stream of a finished run never ended")
				}
				if f.event == "end" {
					if !strings.Contains(f.data, "succeeded") {
						t.Fatalf("end = %q", f.data)
					}
					break
				}
				if f.event != "run_event" {
					t.Fatalf("frame %+v", f)
				}
				id, _ := strconv.ParseInt(f.id, 10, 64)
				var ev domain.RunEvent
				_ = json.Unmarshal([]byte(f.data), &ev)
				if id != want || ev.Seq != want {
					t.Fatalf("frame id %d (seq %d), want %d: a resume must send exactly what follows the cursor", id, ev.Seq, want)
				}
				want++
			}
			if want <= tc.first {
				t.Fatal("no events after the cursor")
			}
		})
	}
}

// Live: events appended while a client is connected arrive as they are
// written; a reconnect from the last id seen gets the rest and nothing twice.
func TestRunEventStreamLiveAndReconnect(t *testing.T) {
	rt, base, token, projectID, iniID, _ := runFixture(t)
	pr, err := rt.EnsureProject(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	runID, err := pr.db.CreateRun(context.Background(), domain.RunAgent, "", "implementer", iniID, "agent:TASK-9")
	if err != nil {
		t.Fatal(err)
	}
	if err := pr.db.SetRunStatus(context.Background(), runID, domain.RunRunning, "implementer"); err != nil {
		t.Fatal(err)
	}
	appendN := func(titles ...string) {
		var evs []domain.RunEvent
		for _, ti := range titles {
			evs = append(evs, domain.RunEvent{Kind: domain.RunEventAction, Title: ti, InitiativeID: iniID})
		}
		if _, err := pr.db.AppendRunEvents(context.Background(), runID, evs); err != nil {
			t.Fatal(err)
		}
	}
	appendN("one")
	ctx, cancel := context.WithCancel(context.Background())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/runs/"+runID+"/events/stream", nil)
	req.Header.Set("X-Idea-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r := &sseReader{frames: make(chan sseFrame, 64)}
	go readFrames(resp.Body, r.frames)
	got := func() sseFrame {
		f, ok := r.next(t, 3*time.Second)
		if !ok {
			t.Fatal("no frame")
		}
		return f
	}
	if f := got(); f.id != "1" {
		t.Fatalf("first frame %+v", f)
	}
	start := time.Now()
	appendN("two")
	if f := got(); f.id != "2" || time.Since(start) > 900*time.Millisecond {
		t.Fatalf("live frame %+v after %s; an append must wake the stream, not wait for a poll", f, time.Since(start))
	}
	cancel()
	resp.Body.Close()

	appendN("three", "four") // missed while disconnected
	resp2, r2 := openSSE(t, base+"/runs/"+runID+"/events/stream", token, map[string]string{"Last-Event-ID": "2"})
	if resp2.StatusCode != http.StatusOK {
		t.Fatal(resp2.StatusCode)
	}
	for _, want := range []string{"3", "4"} {
		f, ok := r2.next(t, 3*time.Second)
		if !ok || f.id != want {
			t.Fatalf("after reconnect got %+v, want id %s", f, want)
		}
	}
	if f, ok := r2.next(t, 300*time.Millisecond); ok {
		t.Fatalf("unexpected frame after the missed events: %+v", f)
	}
}

func readFrames(body io.Reader, out chan<- sseFrame) {
	defer close(out)
	br := bufio.NewReader(body)
	var f sseFrame
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			if f != (sseFrame{}) {
				out <- f
			}
			f = sseFrame{}
		case strings.HasPrefix(line, "id: "):
			f.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "event: "):
			f.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			f.data = strings.TrimPrefix(line, "data: ")
		}
	}
}

// An idle stream sends heartbeat comments, so a proxy never times it out.
func TestRunEventStreamHeartbeats(t *testing.T) {
	old := sseHeartbeat
	sseHeartbeat = 30 * time.Millisecond
	t.Cleanup(func() { sseHeartbeat = old })
	rt, base, token, projectID, iniID, _ := runFixture(t)
	pr, _ := rt.EnsureProject(context.Background(), projectID)
	runID, _ := pr.db.CreateRun(context.Background(), domain.RunAgent, "", "implementer", iniID, "agent:TASK-9")
	_, r := openSSE(t, base+"/runs/"+runID+"/events/stream", token, nil)
	deadline := time.After(3 * time.Second)
	for {
		select {
		case f, ok := <-r.frames:
			if !ok {
				t.Fatal("stream closed")
			}
			if f.event == ":" {
				return
			}
		case <-deadline:
			t.Fatal("no heartbeat")
		}
	}
}

// The project stream carries run events as `event: run_event` with a
// composite "<event seq>.<run event id>" id, and resumes both cursors.
func TestProjectStreamCarriesRunEventsAndResumesBothCursors(t *testing.T) {
	rt, base, token, projectID, iniID, runID := runFixture(t)
	pr, _ := rt.EnsureProject(context.Background(), projectID)
	maxSeq, _ := pr.db.MaxEventSeq(context.Background())
	maxRun, _ := pr.db.MaxRunEventID(context.Background())

	cursor := strconv.FormatInt(maxSeq, 10) + "." + strconv.FormatInt(maxRun, 10)
	_, r := openSSE(t, base+"/events", token, map[string]string{"Last-Event-ID": cursor})
	if _, err := pr.db.AppendRunEvents(context.Background(), runID, []domain.RunEvent{{Kind: domain.RunEventMessage, Title: "late", InitiativeID: iniID}}); err != nil {
		t.Fatal(err)
	}
	f, ok := r.next(t, 3*time.Second)
	if !ok || f.event != "run_event" || !strings.Contains(f.data, `"late"`) {
		t.Fatalf("frame %+v, want the new run event and nothing older", f)
	}
	if f.id != strconv.FormatInt(maxSeq, 10)+"."+strconv.FormatInt(maxRun+1, 10) {
		t.Fatalf("id = %q", f.id)
	}

	// "now" skips the product-graph backlog; a bare legacy seq resumes the
	// graph events and starts run events at now.
	_, rNow := openSSE(t, base+"/events?after=now", token, nil)
	if f, ok := rNow.next(t, 300*time.Millisecond); ok {
		t.Fatalf("after=now sent backlog: %+v", f)
	}
	_, rLegacy := openSSE(t, base+"/events", token, map[string]string{"Last-Event-ID": strconv.FormatInt(maxSeq-1, 10)})
	f, ok = rLegacy.next(t, 3*time.Second)
	if !ok || f.event != "" || !strings.HasPrefix(f.id, strconv.FormatInt(maxSeq, 10)+".") {
		t.Fatalf("legacy resume frame %+v, want product-graph event %d", f, maxSeq)
	}
	if f, ok := rLegacy.next(t, 300*time.Millisecond); ok {
		t.Fatalf("legacy cursor replayed run events: %+v", f)
	}
}

func TestParseProjectCursor(t *testing.T) {
	for in, want := range map[string][3]int64{
		"":      {0, 0, 0},
		"12":    {12, 0, 0},
		"12.7":  {12, 7, 1},
		"x.7":   {0, 7, 1},
		"12.x":  {12, 0, 0},
		"-3.-1": {0, 0, 0},
	} {
		seq, run, have := parseProjectCursor(in)
		h := int64(0)
		if have {
			h = 1
		}
		if seq != want[0] || run != want[1] || h != want[2] {
			t.Errorf("parseProjectCursor(%q) = %d %d %v, want %v", in, seq, run, have, want)
		}
	}
}

func TestCurrentRunSteerAndReport(t *testing.T) {
	_, base, token, _, iniID, runID := runFixture(t)
	var cur currentRun
	resp := httpDo(t, http.MethodGet, base+"/initiatives/"+iniID+"/current-run", token, "")
	if err := json.NewDecoder(resp.Body).Decode(&cur); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if cur.RunID != runID || cur.Live || cur.Executor != "internal" || cur.Status != "succeeded" || cur.PendingNotes == nil {
		t.Fatalf("current run = %+v", cur)
	}

	// Steering a finished run is a conflict, with the way forward.
	r := httpDo(t, http.MethodPost, base+"/runs/"+runID+"/steer", token, `{"note":"hi"}`)
	if r.StatusCode != http.StatusConflict {
		t.Fatalf("steer a finished run: %d, want 409", r.StatusCode)
	}
	if r := httpDo(t, http.MethodPost, base+"/runs/"+runID+"/steer", token, `{"note":"  "}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty note: %d, want 400", r.StatusCode)
	}

	md := httpDo(t, http.MethodGet, base+"/runs/"+runID+"/report?format=md", token, "")
	body, _ := io.ReadAll(md.Body)
	md.Body.Close()
	if md.StatusCode != 200 || !strings.HasPrefix(md.Header.Get("Content-Type"), "text/markdown") ||
		!strings.Contains(md.Header.Get("Content-Disposition"), "attachment") || !strings.Contains(string(body), "# Run report") {
		t.Fatalf("md: %d %q %q\n%s", md.StatusCode, md.Header.Get("Content-Type"), md.Header.Get("Content-Disposition"), body)
	}
	js := httpDo(t, http.MethodGet, base+"/runs/"+runID+"/report", token, "")
	var rep struct {
		Run       struct{ ID string } `json:"run"`
		ChangeSet *struct {
			HumanID string `json:"human_id"`
		} `json:"change_set"`
		Events []domain.RunEvent       `json:"events"`
		Files  []struct{ Path string } `json:"files"`
	}
	if err := json.NewDecoder(js.Body).Decode(&rep); err != nil {
		t.Fatal(err)
	}
	js.Body.Close()
	if rep.Run.ID != runID || rep.ChangeSet == nil || rep.ChangeSet.HumanID != "CHG-1" || len(rep.Events) == 0 || len(rep.Files) != 1 {
		t.Fatalf("json report = %+v", rep)
	}
	zr := httpDo(t, http.MethodGet, base+"/runs/"+runID+"/report?format=zip", token, "")
	zb, _ := io.ReadAll(zr.Body)
	zr.Body.Close()
	z, err := zip.NewReader(bytes.NewReader(zb), int64(len(zb)))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	var names []string
	for _, f := range z.File {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "run-"+runID+"/report.json,run-"+runID+"/report.md" {
		t.Fatalf("zip entries = %v", names)
	}
	if r := httpDo(t, http.MethodGet, base+"/runs/"+runID+"/report?format=pdf", token, ""); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("format=pdf: %d", r.StatusCode)
	}
}
