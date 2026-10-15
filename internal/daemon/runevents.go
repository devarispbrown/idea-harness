package daemon

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
	"github.com/devarispbrown/idea-harness/internal/workflow"
)

// Run events over HTTP: a paged JSON read for late joiners, an SSE stream
// that resumes from the client's cursor, the initiative's current run,
// steering, and the run report. See docs/api.md, "Run events".

// sseHeartbeat is how often an idle stream sends a comment line: often enough
// that a proxy's idle timeout (commonly 60 s) never closes a quiet stream,
// and that a client notices a dead connection.
var sseHeartbeat = 15 * time.Second

// sseRetryMS is the reconnect delay the stream asks EventSource for.
const sseRetryMS = 3000

// runStreamEndGrace is how long a stream of a finished run waits for the
// run's last events (the recorder flushes as Execute returns, just after the
// run's status is written) before it says "end".
var runStreamEndGrace = 3 * time.Second

// startSSE writes the proxy-friendly SSE headers and the retry hint.
func startSSE(w http.ResponseWriter) (http.Flusher, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	// no-transform: a compressing proxy must not buffer the stream.
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	// nginx: do not buffer this response.
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "retry: %d\n\n", sseRetryMS)
	flusher.Flush()
	return flusher, true
}

// resumeCursor reads a stream's resume point: the Last-Event-ID header (an
// EventSource reconnecting) wins over ?after= (a client resuming on its own).
// "" when neither is present.
func resumeCursor(r *http.Request) string {
	if id := strings.TrimSpace(r.Header.Get("Last-Event-ID")); id != "" {
		return id
	}
	return strings.TrimSpace(r.URL.Query().Get("after"))
}

// runForRequest loads the run named by {runId}.
func (s *Server) runForRequest(r *http.Request) (*projectRuntime, store.RunInfo, error) {
	pr, err := s.project(r)
	if err != nil {
		return nil, store.RunInfo{}, err
	}
	info, err := pr.db.GetRunInfo(r.Context(), r.PathValue("runId"))
	return pr, info, err
}

// runTerminal reports whether a run's status means no more events will come.
func runTerminal(status string) bool {
	switch domain.RunStatus(status) {
	case domain.RunSucceeded, domain.RunFailed:
		return true
	}
	return false
}

// handleListRunEvents is the paged read: events with seq > after, oldest
// first. A late joiner pages from after=0 (or its own cursor) until more is
// false, then streams from last_seq.
func (s *Server) handleListRunEvents(w http.ResponseWriter, r *http.Request) {
	pr, info, err := s.runForRequest(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	after, err := parseNonNegative(r.URL.Query().Get("after"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "after must be a non-negative integer (a run event seq)")
		return
	}
	limit, err := parseNonNegative(r.URL.Query().Get("limit"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "limit must be a non-negative integer")
		return
	}
	page, err := pr.db.ListRunEvents(r.Context(), info.ID, after, int(limit))
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"run_id":     page.RunID,
		"run_status": info.Status,
		"events":     page.Events,
		"first_seq":  page.FirstSeq,
		"last_seq":   page.LastSeq,
		"more":       page.More,
	})
}

func parseNonNegative(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, errors.New("invalid")
	}
	return n, nil
}

// handleRunEventStream streams one run's events over SSE. The SSE id is the
// event's seq; a reconnect (Last-Event-ID) or ?after=<seq> sends only the
// events after it -- never the whole run again. Without either it starts at
// the beginning of what is stored. Once the run is over and its last events
// are sent, it sends `event: end` and closes, so EventSource clients stop
// reconnecting.
func (s *Server) handleRunEventStream(w http.ResponseWriter, r *http.Request) {
	pr, info, err := s.runForRequest(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	after, err := parseNonNegative(resumeCursor(r))
	if err != nil {
		// An unreadable cursor is not worth a failed stream: start over.
		after = 0
	}
	flusher, ok := startSSE(w)
	if !ok {
		return
	}
	ctx := r.Context()
	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()
	var quietSince time.Time
	for {
		// Taken before the read: an append between the read and the wait
		// still wakes this loop.
		changed := pr.db.RunEventsChanged()
		page, err := pr.db.ListRunEvents(ctx, info.ID, after, store.RunEventsPageMax)
		if err != nil {
			return
		}
		for _, ev := range page.Events {
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %d\nevent: run_event\ndata: %s\n\n", ev.Seq, data)
			after = ev.Seq
		}
		if len(page.Events) > 0 {
			flusher.Flush()
			quietSince = time.Time{}
		}
		if page.More {
			continue
		}
		if cur, err := pr.db.GetRunInfo(ctx, info.ID); err == nil && runTerminal(cur.Status) {
			if quietSince.IsZero() {
				quietSince = time.Now()
			} else if time.Since(quietSince) >= runStreamEndGrace {
				fmt.Fprintf(w, "event: end\ndata: {\"run_status\":%q}\n\n", cur.Status)
				flusher.Flush()
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-time.After(time.Second):
			// A backstop: the in-process signal covers every append this
			// daemon makes; this covers the terminal-status check above.
		}
	}
}

// currentRun is GET .../initiatives/{id}/current-run.
type currentRun struct {
	// RunID is the running agent run, else the initiative's most recent run
	// with events, else "".
	RunID string `json:"run_id"`
	// Live is true while the run is in progress (its events are still
	// coming).
	Live      bool   `json:"live"`
	Status    string `json:"status,omitempty"`
	Executor  string `json:"executor,omitempty"`
	Steerable bool   `json:"steerable"`
	// UIURL is "Open in OpenHands", when the run is on an OpenHands server
	// and executor.openhands.ui_url is configured.
	UIURL string `json:"ui_url,omitempty"`
	// PendingNotes are steering notes held for the task's next run.
	PendingNotes []string  `json:"pending_notes"`
	StartedAt    time.Time `json:"started_at,omitempty"`
}

// handleCurrentRun names the run the run view should show: the live one, or
// the last one, so a page loaded mid-run (or after it) can fetch its events
// and stream the rest.
func (s *Server) handleCurrentRun(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	out := currentRun{PendingNotes: pr.engine.PendingNotes(ini.ID)}
	if out.PendingNotes == nil {
		out.PendingNotes = []string{}
	}
	if live, ok := pr.engine.LiveRun(ini.ID); ok {
		out.RunID, out.Live, out.Executor, out.Steerable = live.RunID, true, live.Executor, live.Steerable
	} else {
		runID, err := pr.db.LatestEventRunForInitiative(r.Context(), ini.ID)
		if err != nil {
			s.writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out.RunID = runID
	}
	if out.RunID != "" {
		if info, err := pr.db.GetRunInfo(r.Context(), out.RunID); err == nil {
			out.Status, out.StartedAt = info.Status, info.StartedAt
		}
		if out.Executor == "" {
			if page, err := pr.db.ListRunEvents(r.Context(), out.RunID, 0, 1); err == nil && len(page.Events) > 0 {
				out.Executor = page.Events[0].Executor
			}
		}
		out.UIURL = pr.engine.RunUIURL(r.Context(), out.RunID)
	}
	writeJSON(w, http.StatusOK, out)
}

// handleSteerRun sends a note to a running agent: POST .../runs/{runId}/steer
// {"note": "...", "interrupt": false}.
func (s *Server) handleSteerRun(w http.ResponseWriter, r *http.Request) {
	pr, info, err := s.runForRequest(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		Note      string `json:"note"`
		Interrupt bool   `json:"interrupt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if strings.TrimSpace(body.Note) == "" {
		writeError(w, http.StatusBadRequest, "note is required")
		return
	}
	if len(body.Note) > 8000 {
		writeError(w, http.StatusBadRequest, "note is longer than 8000 bytes")
		return
	}
	res, err := pr.engine.SteerRun(r.Context(), info.InitiativeID, info.ID, body.Note, body.Interrupt)
	if errors.Is(err, workflow.ErrNoLiveRun) {
		writeError(w, http.StatusConflict, "run "+info.ID+" is not running; send the note with the task's next run instead (POST .../execute {\"note\": ...})")
		return
	}
	if err != nil {
		s.writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleRunReport serves the run report: ?format=json (default), md, or zip
// (report.json and report.md together).
func (s *Server) handleRunReport(w http.ResponseWriter, r *http.Request) {
	pr, info, err := s.runForRequest(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	if format != "json" && format != "md" && format != "zip" {
		writeError(w, http.StatusBadRequest, "format must be json, md or zip")
		return
	}
	rep, err := pr.engine.BuildRunReport(r.Context(), info.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	name := "run-" + info.ID
	switch format {
	case "md":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.md"`)
		_, _ = w.Write([]byte(rep.Markdown()))
	case "zip":
		js, err := json.MarshalIndent(rep, "", "  ")
		if err != nil {
			s.writeErr(w, http.StatusInternalServerError, err)
			return
		}
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, f := range []struct {
			name string
			data []byte
		}{{"report.json", js}, {"report.md", []byte(rep.Markdown())}} {
			fw, err := zw.CreateHeader(&zip.FileHeader{Name: name + "/" + f.name, Method: zip.Deflate, Modified: rep.GeneratedAt})
			if err != nil {
				s.writeErr(w, http.StatusInternalServerError, err)
				return
			}
			_, _ = fw.Write(f.data)
		}
		if err := zw.Close(); err != nil {
			s.writeErr(w, http.StatusInternalServerError, err)
			return
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.zip"`)
		_, _ = w.Write(buf.Bytes())
	default:
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.json"`)
		writeJSON(w, http.StatusOK, rep)
	}
}
