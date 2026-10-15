package daemon

import (
	"context"
	"net/http"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// Remote runs (GET …/initiatives/{id}/remote-runs and GET …/remote-runs): the
// openhands executor's durable dispatch records, read-only. Until these
// existed the only way to see which conversation a task went to, on which
// server, at which attempt, and why it failed, was the state database itself.

// remoteRunView is one remote run as the listing serves it: the stored row
// without its progress log -- up to 64 KiB each, and already streamed as run
// events -- plus the task's code and the log's last line, which is the
// question a listing answers ("where did it get to?").
type remoteRunView struct {
	ID             string                 `json:"id"`
	Executor       string                 `json:"executor"`
	InitiativeID   string                 `json:"initiative_id"`
	TaskID         string                 `json:"task_id"`
	TaskCode       string                 `json:"task_code,omitempty"`
	Attempt        int                    `json:"attempt"`
	ServerURL      string                 `json:"server_url"`
	ConversationID string                 `json:"conversation_id,omitempty"`
	Status         domain.RemoteRunStatus `json:"status"`
	RemoteStatus   string                 `json:"remote_status,omitempty"`
	EventCount     int                    `json:"event_count"`
	LastEventID    string                 `json:"last_event_id,omitempty"`
	LastEvent      string                 `json:"last_event,omitempty"`
	Error          string                 `json:"error,omitempty"`
	RunID          string                 `json:"run_id,omitempty"`
	CreatedAt      string                 `json:"created_at"`
	UpdatedAt      string                 `json:"updated_at"`
	FinishedAt     string                 `json:"finished_at,omitempty"`
}

// lastProgressLine is the newest non-empty line of a progress log.
func lastProgressLine(progress string) string {
	lines := strings.Split(strings.TrimRight(progress, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return l
		}
	}
	return ""
}

// remoteRunViews renders runs, resolving each task id to its code once. A
// task deleted since (plan revision) simply has no code.
func remoteRunViews(ctx context.Context, db *store.DB, runs []domain.RemoteRun) []remoteRunView {
	codes := map[string]string{}
	out := make([]remoteRunView, 0, len(runs))
	for _, r := range runs {
		code, seen := codes[r.TaskID]
		if !seen && r.TaskID != "" {
			if t, err := db.GetTask(ctx, r.TaskID); err == nil {
				code = t.Code
			}
			codes[r.TaskID] = code
		}
		v := remoteRunView{
			ID: r.ID, Executor: r.Executor, InitiativeID: r.InitiativeID, TaskID: r.TaskID, TaskCode: code,
			Attempt: r.Attempt, ServerURL: r.ServerURL, ConversationID: r.ConversationID, Status: r.Status,
			RemoteStatus: r.RemoteStatus, EventCount: r.EventCount, LastEventID: r.LastEventID,
			LastEvent: lastProgressLine(r.Progress), Error: r.Error, RunID: r.RunID,
			CreatedAt: r.CreatedAt.UTC().Format(timeLayoutRFC3339Milli), UpdatedAt: r.UpdatedAt.UTC().Format(timeLayoutRFC3339Milli),
		}
		if r.FinishedAt != nil {
			v.FinishedAt = r.FinishedAt.UTC().Format(timeLayoutRFC3339Milli)
		}
		out = append(out, v)
	}
	return out
}

const timeLayoutRFC3339Milli = "2006-01-02T15:04:05.000Z07:00"

// handleListRemoteRuns lists one initiative's remote runs, newest first.
func (s *Server) handleListRemoteRuns(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	runs, err := pr.db.ListInitiativeRemoteRuns(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, remoteRunViews(r.Context(), pr.db, runs))
}

// handleListProjectRemoteRuns lists the project's remote runs across every
// initiative, newest first; ?active=true keeps only those not yet terminal
// (what a restart re-attaches to). Capped at 200 rows.
func (s *Server) handleListProjectRemoteRuns(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	runs, err := pr.db.ListRemoteRuns(r.Context(), r.URL.Query().Get("active") == "true", 200)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, remoteRunViews(r.Context(), pr.db, runs))
}
