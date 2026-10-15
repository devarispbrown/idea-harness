package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// handleEvents streams a project's events over SSE: every product-graph event
// (unnamed SSE messages, as before), and every run event of every agent run
// (`event: run_event`), so a client watching the project sees a running agent
// live without opening a stream per run.
//
// The SSE id carries both cursors, "<event seq>.<run event id>", and a
// reconnect (Last-Event-ID) or ?after= resumes BOTH from where the client
// was: only what it missed is sent, never the whole log again (the OpenHands
// GUI's reconnect replays the whole session; this one does not). A bare
// "<event seq>" -- an id from before run events existed -- resumes the
// product-graph events and starts run events at "now": run history is read
// through GET .../runs/{runId}/events, not replayed here. With no cursor at
// all the product-graph log is sent from the start, as it always was, and
// run events from now.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	ctx := r.Context()
	cursor := resumeCursor(r)
	lastSeq, runID, haveRun := parseProjectCursor(cursor)
	if cursor == "now" {
		// A client that only wants to hear about changes from here on (a
		// page load refetches what it shows anyway): skip the backlog.
		lastSeq, _ = pr.db.MaxEventSeq(ctx)
	}
	if !haveRun {
		runID, _ = pr.db.MaxRunEventID(ctx)
	}
	flusher, ok := startSSE(w)
	if !ok {
		return
	}

	heartbeat := time.NewTicker(sseHeartbeat)
	defer heartbeat.Stop()
	for {
		changed := pr.db.RunEventsChanged()
		var sent, more bool
		lastSeq, runID, sent, more = s.pushEvents(ctx, w, pr, lastSeq, runID)
		if sent {
			flusher.Flush()
		}
		if more {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-time.After(time.Second):
			// Product-graph events have no in-process signal; the store is
			// polled once a second for them, as it always was.
		}
	}
}

// parseProjectCursor reads "<seq>" or "<seq>.<run event id>". Unparseable
// parts are 0 (resume from the start), as Last-Event-ID always was.
func parseProjectCursor(s string) (seq, runID int64, haveRun bool) {
	if s == "" {
		return 0, 0, false
	}
	evPart, runPart, found := strings.Cut(s, ".")
	if n, err := strconv.ParseInt(evPart, 10, 64); err == nil && n > 0 {
		seq = n
	}
	if found {
		if n, err := strconv.ParseInt(runPart, 10, 64); err == nil && n >= 0 {
			return seq, n, true
		}
	}
	return seq, 0, false
}

// pushEvents writes the events after both cursors and returns the advanced
// cursors; more reports a full page (call again without waiting).
func (s *Server) pushEvents(ctx context.Context, w http.ResponseWriter, pr *projectRuntime, lastSeq, lastRun int64) (int64, int64, bool, bool) {
	const page = 200
	sent, more := false, false
	if events, err := pr.db.ListEvents(ctx, lastSeq, page); err == nil {
		for _, e := range events {
			data, err := json.Marshal(e)
			if err != nil {
				continue
			}
			lastSeq = e.Seq
			fmt.Fprintf(w, "id: %d.%d\ndata: %s\n\n", lastSeq, lastRun, data)
			sent = true
		}
		more = len(events) == page
	}
	if rows, err := pr.db.ListRunEventsAfterID(ctx, lastRun, page); err == nil {
		for _, row := range rows {
			data, err := json.Marshal(row.Event)
			if err != nil {
				continue
			}
			lastRun = row.ID
			fmt.Fprintf(w, "id: %d.%d\nevent: run_event\ndata: %s\n\n", lastSeq, lastRun, data)
			sent = true
		}
		more = more || len(rows) == page
	}
	return lastSeq, lastRun, sent, more
}
