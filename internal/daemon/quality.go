package daemon

import (
	"context"
	"net/http"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// handleGetQuality returns the initiative's quality-dimension scorecard
// (verify.dimensions) without measuring anything: each dimension's mode,
// its latest result and whether that result is stale, and which gated
// dimensions keep the verification gate from passing.
func (s *Server) handleGetQuality(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	sc, err := pr.engine.QualityScorecard(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, sc)
}

// handleMeasureQuality starts a measurement of every enabled quality
// dimension. Asynchronous: a reproducibility run in the sandbox can take
// minutes, longer than any client should hold a request open.
func (s *Server) handleMeasureQuality(w http.ResponseWriter, r *http.Request) {
	opID, err := s.rt.MeasureQualityInitiative(r.Context(), r.PathValue("projectId"), r.PathValue("id"))
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
}

// MeasureQualityInitiative launches a measure_quality operation. It does not
// claim the initiative: measuring only reads the workspace (under the
// engine's own lock for the snapshot) and appends results, so it may run
// beside anything -- including on a complete or failed initiative, where the
// scorecard is still worth having.
func (r *Runtime) MeasureQualityInitiative(ctx context.Context, projectID, initiativeID string) (string, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	if _, err := pr.db.GetInitiative(ctx, initiativeID); err != nil {
		return "", err
	}
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "measure_quality")
	if err != nil {
		return "", err
	}
	r.launchOperation(opID, func(bg context.Context) {
		sc, runErr := pr.engine.MeasureQuality(bg, initiativeID)
		if runErr != nil {
			r.failOperation(opID, initiativeID, "quality measurement failed", bg, pr, runErr, false)
			return
		}
		pr.db.SetOperationStatus(context.WithoutCancel(bg), opID, domain.OperationSucceeded, initiativeID)
		r.log.Info("quality measured", "initiative", initiativeID, "gate_unmet", len(sc.GateUnmet))
	})
	return opID, nil
}
