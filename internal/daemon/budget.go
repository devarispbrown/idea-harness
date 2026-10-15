package daemon

import (
	"context"
	"errors"
	"math"
	"net/http"
	"path/filepath"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/workflow"
)

// Spending caps: the daemon half (workflow/budget.go is the engine half).
//
// The engine refuses a model or agent call once an initiative has reached
// its cap and returns a *workflow.BudgetExceededError. Every operation's
// failure path comes through failOperation, which turns that one error into
// a park instead of a failure: needs_human_judgment with reason
// budget_exceeded, remembering where the initiative was, plus an
// initiative.budget_exceeded event the integration bus publishes. Raising
// the cap (PUT .../budget) returns it there.

// parkIfBudget parks initiativeID when runErr is a budget stop, and reports
// whether it did.
func (r *Runtime) parkIfBudget(pr *projectRuntime, initiativeID string, runErr error) bool {
	var be *workflow.BudgetExceededError
	if initiativeID == "" || !errors.As(runErr, &be) {
		return false
	}
	ctx := context.Background()
	// The status before this operation claimed the initiative: execute has
	// already released its claim by now, every other operation still holds
	// it, and skipping `running` gives the right answer for both.
	prev, err := pr.db.PreviousInitiativeStatus(ctx, initiativeID, math.MaxInt64, true)
	if err != nil {
		r.log.Error("budget stop: could not read the initiative's prior status", "initiative", initiativeID, "err", err)
	}
	if prev == string(domain.InitiativeNeedsHumanJudgment)+":"+string(domain.JudgmentBudget) {
		prev = ""
	}
	if err := pr.db.ParkForBudget(ctx, initiativeID, prev, be.Detail()); err != nil {
		r.log.Error("budget stop: could not park the initiative", "initiative", initiativeID, "err", err)
		return false
	}
	r.log.Warn("initiative stopped at its spending cap", "initiative", initiativeID, "spent_usd", be.SpentUSD, "cap_usd", be.CapUSD, "before", be.Before)
	return true
}

func (s *Server) handleGetBudget(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	st, err := pr.engine.BudgetStatus(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// handleSetBudget sets (cap_usd: number) or clears (cap_usd: null, inherit
// the project default) one initiative's cap. When the initiative is parked
// at its cap and the new cap leaves room, it is returned to where it
// stopped, and the response says so (resumed: true).
func (s *Server) handleSetBudget(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		// A pointer to a pointer would be needed to tell "absent" from
		// "null"; the body has one field, so absent is refused instead.
		CapUSD *float64 `json:"cap_usd"`
		Clear  bool     `json:"clear"`
	}
	if err := decodeStrict(r.Body, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	switch {
	case body.Clear && body.CapUSD != nil:
		writeError(w, http.StatusBadRequest, "give cap_usd or clear, not both")
		return
	case !body.Clear && body.CapUSD == nil:
		writeError(w, http.StatusBadRequest, "cap_usd is required (a dollar amount > 0), or clear: true to inherit the project default")
		return
	case body.CapUSD != nil && (*body.CapUSD <= 0 || math.IsNaN(*body.CapUSD) || math.IsInf(*body.CapUSD, 0)):
		writeError(w, http.StatusBadRequest, "cap_usd must be a dollar amount > 0; use clear: true to inherit the project default")
		return
	}
	if err := pr.db.SetInitiativeBudgetCap(r.Context(), ini.ID, body.CapUSD, ""); err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	st, err := pr.engine.BudgetStatus(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	resumed := false
	if st.Parked && !st.Exceeded {
		if resumed, err = pr.db.ResumeFromBudget(r.Context(), ini.ID, ""); err != nil {
			s.writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if resumed {
			st.Parked = false
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"budget": st, "resumed": resumed})
}

// ProjectBudgetRow is one initiative on the project budget overview.
type ProjectBudgetRow struct {
	InitiativeID string                `json:"initiative_id"`
	Title        string                `json:"title"`
	Status       string                `json:"status"`
	Budget       workflow.BudgetStatus `json:"budget"`
}

func (s *Server) handleProjectBudget(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	inis, err := pr.db.ListInitiatives(r.Context())
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	rows := make([]ProjectBudgetRow, 0, len(inis))
	for _, ini := range inis {
		st, err := pr.engine.BudgetStatus(r.Context(), ini.ID)
		if err != nil {
			s.writeErr(w, http.StatusInternalServerError, err)
			return
		}
		title := ini.ShortTitle
		if title == "" {
			title = ini.Title
		}
		rows = append(rows, ProjectBudgetRow{InitiativeID: ini.ID, Title: title, Status: string(ini.Status), Budget: st})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"default_initiative_usd": pr.cfg.Harness.Budget.DefaultInitiativeUSD,
		"initiatives":            rows,
	})
}

// handleSetProjectBudget writes budget.default_initiative_usd (0 removes the
// project-wide cap). Initiatives parked at the old cap that the new default
// leaves room for, and that have no cap of their own, are returned to where
// they stopped.
func (s *Server) handleSetProjectBudget(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		DefaultInitiativeUSD *float64 `json:"default_initiative_usd"`
	}
	if err := decodeStrict(r.Body, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.DefaultInitiativeUSD == nil || *body.DefaultInitiativeUSD < 0 || math.IsNaN(*body.DefaultInitiativeUSD) || math.IsInf(*body.DefaultInitiativeUSD, 0) {
		writeError(w, http.StatusBadRequest, "default_initiative_usd is required: a dollar amount >= 0 (0 removes the project-wide cap)")
		return
	}
	if !s.writeHarness(w, r, pr, func(h *config.HarnessConfig) error {
		h.Budget.DefaultInitiativeUSD = *body.DefaultInitiativeUSD
		return nil
	}) {
		return
	}
	pr, err = s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var resumed []string
	if inis, err := pr.db.ListInitiatives(r.Context()); err == nil {
		for _, ini := range inis {
			if ini.JudgmentReason != domain.JudgmentBudget || ini.Status != domain.InitiativeNeedsHumanJudgment {
				continue
			}
			st, err := pr.engine.BudgetStatus(r.Context(), ini.ID)
			if err != nil || st.Exceeded {
				continue
			}
			if ok, err := pr.db.ResumeFromBudget(r.Context(), ini.ID, ""); err == nil && ok {
				resumed = append(resumed, ini.ID)
			}
		}
	}
	if resumed == nil {
		resumed = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "updated", "default_initiative_usd": *body.DefaultInitiativeUSD, "resumed": resumed})
}

// writeHarness is the shared field-level write every settings handler in this
// file family uses: re-read harness.yaml from disk (never the daemon's cached
// copy, so a hand edit is not clobbered), apply mutate, write it through
// config.WriteHarness -- which refuses anything Load would refuse and keeps
// the file's comments -- and reload the project so the change takes effect
// without restarting the daemon. It writes the response itself on failure and
// reports whether the caller should go on.
func (s *Server) writeHarness(w http.ResponseWriter, r *http.Request, pr *projectRuntime, mutate func(h *config.HarnessConfig) error) bool {
	dir := filepath.Join(pr.rootPath, config.Dir)
	onDisk, err := config.Load(dir)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return false
	}
	h := onDisk.Harness
	if err := mutate(&h); err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
		return false
	}
	if err := config.WriteHarness(dir, h); err != nil {
		// A refusal by the schema or the secret sweep is the request's fault.
		var pe *config.ParseError
		if errors.As(err, &pe) {
			writeError(w, http.StatusBadRequest, pe.Error())
			return false
		}
		s.writeErr(w, http.StatusBadRequest, err)
		return false
	}
	if _, err := s.rt.ReloadProject(r.Context(), r.PathValue("projectId")); err != nil {
		s.writeReloadErr(w, err)
		return false
	}
	return true
}
