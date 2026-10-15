package daemon

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/projection"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// handleGaps answers GET …/initiatives/{id}/gaps: the deterministic gap
// analysis of one initiative's Product Graph (projection.BuildGaps), most
// severe first. ?kind=a,b narrows it; an unknown kind is a 400 naming the
// known ones, never an empty list that reads as "no gaps".
func (s *Server) handleGaps(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	kinds, err := projection.ParseGapKinds(r.URL.Query().Get("kind"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	gaps, err := s.initiativeGaps(r.Context(), pr, ini, kinds)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, domain.GapReport{InitiativeID: ini.ID, Gaps: gaps, Summary: domain.Summarize(gaps)})
}

// handleProjectGaps answers GET /projects/{projectId}/gaps: every
// initiative's gaps in one report, so a CI gate over a whole project is one
// request. Each gap carries its initiative_id.
func (s *Server) handleProjectGaps(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	kinds, err := projection.ParseGapKinds(r.URL.Query().Get("kind"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	inis, err := pr.db.ListInitiatives(r.Context())
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	all := []domain.Gap{}
	for _, ini := range inis {
		gaps, err := s.initiativeGaps(r.Context(), pr, ini, kinds)
		if err != nil {
			s.writeErr(w, http.StatusInternalServerError, err)
			return
		}
		all = append(all, gaps...)
	}
	projection.SortGaps(all)
	writeJSON(w, http.StatusOK, domain.GapReport{Gaps: all, Summary: domain.Summarize(all)})
}

// initiativeGaps runs the analysis with mapped-test evidence included when a
// verification kind is asked for -- the one input BuildGaps cannot compute
// itself, because it needs the blob stores.
func (s *Server) initiativeGaps(ctx context.Context, pr *projectRuntime, ini domain.Initiative, kinds []domain.GapKind) ([]domain.Gap, error) {
	opts := projection.GapOptions{Kinds: kinds}
	wantTests := len(kinds) == 0
	for _, k := range kinds {
		if k == domain.GapUnverifiedRequirement || k == domain.GapUntestedAcceptanceCriterion {
			wantTests = true
		}
	}
	if wantTests {
		ev, err := projection.ComputeEvidenceTests(ctx, pr.db, pr.blobs, pr.rawBlobs, ini.ID)
		if err != nil {
			return nil, err
		}
		opts.EvidenceTests = ev
	}
	return projection.BuildGaps(ctx, pr.db, ini, opts)
}

// handleSetAssertionStatus records a human's judgment on an assumption or
// hypothesis (POST /projects/{projectId}/assertions/{id}/status, body
// {"status": "accepted"|"disputed"|"retracted"}). It is what closes an
// unvalidated_assumption gap: nothing else moves an assertion out of
// `candidate`.
func (s *Server) handleSetAssertionStatus(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "body must be {\"status\": \"accepted\"|\"disputed\"|\"retracted\"}")
		return
	}
	status := domain.AssertionStatus(body.Status)
	switch status {
	case domain.AssertionAccepted, domain.AssertionDisputed, domain.AssertionRetracted:
	default:
		writeError(w, http.StatusBadRequest, "status must be one of accepted, disputed, retracted")
		return
	}
	if err := pr.db.SetAssertionStatus(r.Context(), r.PathValue("id"), status, store.HumanActor); err != nil {
		// Not found is a 404 (writeErr maps it); the only other refusal is a
		// superseded assertion, which is the state forbidding it: 409.
		s.writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": r.PathValue("id"), "status": string(status)})
}
