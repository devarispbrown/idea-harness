package daemon

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/projection"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// Objectives & outcome metrics (the outcome end of the Product Graph). Every
// write here is a human edit -- the PRD stage writes its own through the
// engine -- so the actor is store.HumanActor throughout.
//
// Path ids ({objectiveId}, {metricId}) accept the code (OBJ-1, MET-2) or the
// id, and are looked up before the body is decoded, so an unknown one is a
// 404 whatever the body says.

func (s *Server) handleListObjectives(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	objs, err := pr.db.ListObjectives(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]domain.Objective, 0, len(objs))
	for _, o := range objs {
		if o.SupersededBy == "" {
			out = append(out, o)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCreateObjective(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		Statement string `json:"statement"`
		Rationale string `json:"rationale"`
		Owner     string `json:"owner"`
		Status    string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	o := &domain.Objective{
		InitiativeID: ini.ID, Statement: strings.TrimSpace(body.Statement), Rationale: strings.TrimSpace(body.Rationale),
		Owner: strings.TrimSpace(body.Owner), Status: domain.ObjectiveStatus(body.Status),
	}
	if err := pr.db.CreateObjective(r.Context(), o, store.HumanActor); err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, o)
}

func (s *Server) handleUpdateObjective(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	o, err := pr.db.GetObjectiveByRef(r.Context(), ini.ID, r.PathValue("objectiveId"))
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		Statement *string `json:"statement"`
		Rationale *string `json:"rationale"`
		Owner     *string `json:"owner"`
		Status    *string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Statement != nil {
		o.Statement = strings.TrimSpace(*body.Statement)
	}
	if body.Rationale != nil {
		o.Rationale = strings.TrimSpace(*body.Rationale)
	}
	if body.Owner != nil {
		o.Owner = strings.TrimSpace(*body.Owner)
	}
	if body.Status != nil {
		o.Status = domain.ObjectiveStatus(*body.Status)
	}
	o.ProducedByRun = "" // a human edit adds no produced_by edge
	if err := pr.db.UpdateObjective(r.Context(), o, store.HumanActor); err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	updated, err := pr.db.GetObjectiveByRef(r.Context(), ini.ID, o.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// handleLinkObjectiveRequirement links (or, with unlink, unlinks) a
// requirement to an objective: requirement --serves--> objective.
func (s *Server) handleLinkObjectiveRequirement(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	o, err := pr.db.GetObjectiveByRef(r.Context(), ini.ID, r.PathValue("objectiveId"))
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		Requirement string `json:"requirement"`
		Unlink      bool   `json:"unlink"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Requirement) == "" {
		writeError(w, http.StatusBadRequest, "requirement is required")
		return
	}
	req, err := pr.db.GetRequirementByRef(r.Context(), ini.ID, strings.TrimSpace(body.Requirement))
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	if body.Unlink {
		err = pr.db.UnlinkRequirementObjective(r.Context(), ini.ID, req.ID, o.ID, store.HumanActor)
	} else {
		if req.Status == domain.RequirementRemoved || req.SupersededBy != "" {
			writeError(w, http.StatusConflict, req.Code+" has been superseded; link a live requirement")
			return
		}
		err = pr.db.LinkRequirementObjective(r.Context(), ini.ID, req.ID, o.ID, store.HumanActor)
	}
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	updated, err := pr.db.GetObjectiveByRef(r.Context(), ini.ID, o.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleListMetrics(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	ms, err := pr.db.ListMetrics(r.Context(), ini.ID, time.Now())
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, ms)
}

// metricSourceBody is the request shape of a metric's source.
type metricSourceBody struct {
	Kind    string `json:"kind"`
	Command string `json:"command"`
	URL     string `json:"url"`
	Pointer string `json:"pointer"`
}

func (b *metricSourceBody) source() domain.MetricSource {
	if b == nil {
		return domain.MetricSource{Kind: domain.MetricSourceManual}
	}
	return domain.MetricSource{Kind: domain.MetricSourceKind(b.Kind), Command: b.Command, URL: strings.TrimSpace(b.URL), Pointer: b.Pointer}
}

func (s *Server) handleCreateMetric(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		Objective  string            `json:"objective"`
		Name       string            `json:"name"`
		Definition string            `json:"definition"`
		Unit       string            `json:"unit"`
		Direction  string            `json:"direction"`
		Baseline   *float64          `json:"baseline"`
		Target     *float64          `json:"target"`
		Guardrail  *float64          `json:"guardrail"`
		Cadence    string            `json:"cadence"`
		Source     *metricSourceBody `json:"source"`
		Hypothesis string            `json:"hypothesis"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if strings.TrimSpace(body.Objective) == "" || body.Target == nil {
		writeError(w, http.StatusBadRequest, "objective and target are required")
		return
	}
	o, err := pr.db.GetObjectiveByRef(r.Context(), ini.ID, strings.TrimSpace(body.Objective))
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	m := &domain.OutcomeMetric{
		InitiativeID: ini.ID, ObjectiveID: o.ID, Name: strings.TrimSpace(body.Name), Definition: strings.TrimSpace(body.Definition),
		Unit: strings.TrimSpace(body.Unit), Direction: domain.MetricDirection(body.Direction), Baseline: body.Baseline,
		Target: *body.Target, Guardrail: body.Guardrail, Cadence: strings.TrimSpace(body.Cadence), Source: body.Source.source(),
	}
	if err := pr.db.CreateMetric(r.Context(), m, store.HumanActor); err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	if h := strings.TrimSpace(body.Hypothesis); h != "" {
		if _, err := pr.db.CreateMetricHypothesis(r.Context(), ini.ID, m.ID, h, "human", ""); err != nil {
			s.writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	created, err := pr.db.GetMetricByRef(r.Context(), ini.ID, m.ID, time.Now())
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateMetric(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	m, err := pr.db.GetMetricByRef(r.Context(), ini.ID, r.PathValue("metricId"), time.Now())
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		Objective      *string           `json:"objective"`
		Name           *string           `json:"name"`
		Definition     *string           `json:"definition"`
		Unit           *string           `json:"unit"`
		Direction      *string           `json:"direction"`
		Baseline       *float64          `json:"baseline"`
		ClearBaseline  bool              `json:"clear_baseline"`
		Target         *float64          `json:"target"`
		Guardrail      *float64          `json:"guardrail"`
		ClearGuardrail bool              `json:"clear_guardrail"`
		Cadence        *string           `json:"cadence"`
		Source         *metricSourceBody `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Objective != nil {
		o, err := pr.db.GetObjectiveByRef(r.Context(), ini.ID, strings.TrimSpace(*body.Objective))
		if err != nil {
			s.writeErr(w, http.StatusNotFound, err)
			return
		}
		m.ObjectiveID = o.ID
	}
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	set(&m.Name, body.Name)
	set(&m.Definition, body.Definition)
	set(&m.Unit, body.Unit)
	set(&m.Cadence, body.Cadence)
	if body.Direction != nil {
		m.Direction = domain.MetricDirection(*body.Direction)
	}
	if body.Target != nil {
		m.Target = *body.Target
	}
	switch {
	case body.ClearBaseline:
		m.Baseline = nil
	case body.Baseline != nil:
		m.Baseline = body.Baseline
	}
	switch {
	case body.ClearGuardrail:
		m.Guardrail = nil
	case body.Guardrail != nil:
		m.Guardrail = body.Guardrail
	}
	if body.Source != nil {
		m.Source = body.Source.source()
	}
	m.ProducedByRun, m.Status = "", nil
	if err := pr.db.UpdateMetric(r.Context(), m, store.HumanActor); err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	updated, err := pr.db.GetMetricByRef(r.Context(), ini.ID, m.ID, time.Now())
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleListObservations(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	m, err := pr.db.GetMetricByRef(r.Context(), ini.ID, r.PathValue("metricId"), time.Now())
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	obs, err := pr.db.ListObservations(r.Context(), m.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, obs)
}

// handleRecordObservation records a human-supplied value (`idea metric
// record`, the MCP record_metric tool).
func (s *Server) handleRecordObservation(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	m, err := pr.db.GetMetricByRef(r.Context(), ini.ID, r.PathValue("metricId"), time.Now())
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		Value      *float64 `json:"value"`
		Note       string   `json:"note"`
		ObservedAt string   `json:"observed_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Value == nil {
		writeError(w, http.StatusBadRequest, "value is required")
		return
	}
	var at time.Time
	if body.ObservedAt != "" {
		if at, err = time.Parse(time.RFC3339, body.ObservedAt); err != nil {
			writeError(w, http.StatusBadRequest, "observed_at must be RFC 3339, e.g. 2026-10-14T09:00:00Z")
			return
		}
		if at.After(time.Now().Add(time.Minute)) {
			writeError(w, http.StatusBadRequest, "observed_at is in the future")
			return
		}
	}
	obs, st, err := pr.engine.RecordMetric(r.Context(), ini.ID, m.ID, *body.Value, body.Note, store.HumanActor, at)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"observation": obs, "status": st})
}

// handleMeasureMetrics runs command/url sources (one metric, or every one
// that has a source) and records what they read. Synchronous: each command is
// bounded by outcomes.measure_timeout_seconds.
func (s *Server) handleMeasureMetrics(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		Metric string `json:"metric"`
	}
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	// A metric command's scratch root is under the state root, which the
	// startup sweep's recovery may still be clearing. Fails only when the
	// client has gone, so there is no one to answer.
	if s.rt.waitSwept(r.Context()) != nil {
		return
	}
	res, err := pr.engine.MeasureMetrics(r.Context(), ini.ID, strings.TrimSpace(body.Metric), store.HumanActor)
	if err != nil {
		// writeErr answers 404 for an unknown metric (a store NotFoundError)
		// whatever status is passed; anything else here is the request's
		// fault -- e.g. measuring a manual metric.
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": res})
}

func (s *Server) handleOutcomes(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	rep, err := projection.BuildOutcomes(r.Context(), pr.db, ini.ID, time.Now())
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
