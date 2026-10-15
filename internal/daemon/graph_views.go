package daemon

import (
	"net/http"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// Read-only views over records the daemon already keeps but did not expose:
// the review loops' convergence telemetry and the quality-measurement
// history. Both exist so the web UI can show the Product Graph's machinery
// (docs/ui.md "Review convergence", "Quality") instead of only its latest
// verdict.

// handleReviewTelemetry answers GET …/review-telemetry: every review cycle's
// convergence record (findings raised/new/re-raised/resolved by severity,
// demotions by rule, open blocking count, outcome), oldest first. ?review=prd
// or ?review=execution_plan narrows it to one loop. The records live on the
// reviewer runs' manifests (store.ListReviewTelemetry); before this route the
// only reader was the eval harness, so "did not converge" was all a human
// ever saw of a stalled loop.
func (s *Server) handleReviewTelemetry(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	review := strings.TrimSpace(r.URL.Query().Get("review"))
	if review != "" && review != "prd" && review != "execution_plan" {
		writeError(w, http.StatusBadRequest, "review must be prd or execution_plan")
		return
	}
	all, err := pr.db.ListReviewTelemetry(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Non-nil so "no review has run" is [] rather than null.
	out := []domain.ReviewTelemetry{}
	for _, t := range all {
		if review == "" || t.Review == review {
			out = append(out, t)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleQualityHistory answers GET …/quality/history: every recorded quality
// result, oldest first, each with its linked requirement codes. ?dimension=
// narrows it to one dimension; an unknown dimension is a 400 naming the
// known ones rather than an empty list that reads as "never measured".
func (s *Server) handleQualityHistory(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	dim := domain.QualityDimension(strings.TrimSpace(r.URL.Query().Get("dimension")))
	if dim != "" && !dim.Valid() {
		names := make([]string, len(domain.QualityDimensions))
		for i, d := range domain.QualityDimensions {
			names[i] = string(d)
		}
		writeError(w, http.StatusBadRequest, "unknown dimension "+string(dim)+"; use one of: "+strings.Join(names, ", "))
		return
	}
	all, err := pr.db.QualityHistory(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := []domain.QualityResult{}
	for _, q := range all {
		if dim == "" || q.Dimension == dim {
			out = append(out, q)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
