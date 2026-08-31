package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

// handleEvents streams project events over SSE. It supports Last-Event-ID
// resume and polls the event log (the single writer is the daemon itself).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	lastSeq := int64(0)
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		if n, err := strconv.ParseInt(id, 10, 64); err == nil {
			lastSeq = n
		}
	}

	// Initial catch-up.
	lastSeq = s.pushEvents(r.Context(), w, flusher, pr, lastSeq)

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			lastSeq = s.pushEvents(r.Context(), w, flusher, pr, lastSeq)
		}
	}
}

func (s *Server) pushEvents(ctx context.Context, w http.ResponseWriter, flusher http.Flusher, pr *projectRuntime, lastSeq int64) int64 {
	events, err := pr.db.ListEvents(ctx, lastSeq, 200)
	if err != nil {
		return lastSeq
	}
	for _, e := range events {
		data, err := json.Marshal(e)
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "id: %d\ndata: %s\n\n", e.Seq, data)
		lastSeq = e.Seq
	}
	flusher.Flush()
	return lastSeq
}
