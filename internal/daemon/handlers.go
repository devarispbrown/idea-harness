package daemon

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/idea-harness/idea-harness/internal/config"
	"github.com/idea-harness/idea-harness/internal/domain"
	"github.com/idea-harness/idea-harness/internal/executor"
	"github.com/idea-harness/idea-harness/internal/projection"
	"github.com/idea-harness/idea-harness/internal/risk"
	"github.com/idea-harness/idea-harness/internal/security"
	"github.com/idea-harness/idea-harness/internal/store"
)

const version = "0.1.0"

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": version})
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": version})
}

// ---------- projects ----------

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RootPath string `json:"root_path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.RootPath == "" {
		writeError(w, http.StatusBadRequest, "root_path is required")
		return
	}
	projectID, err := s.rt.CreateProject(r.Context(), body.RootPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"project_id": projectID})
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.rt.ListProjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	if _, err := s.rt.EnsureProject(r.Context(), r.PathValue("projectId")); err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	rec, err := s.rt.global.GetProject(r.Context(), r.PathValue("projectId"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rec)
}

// ---------- initiatives ----------

func (s *Server) handleCreateInitiative(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	var body struct {
		Title       string `json:"title"`
		Description string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	iniID, opID, err := s.rt.StartInitiative(r.Context(), projectID, body.Title, body.Description)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"initiative_id": iniID, "operation_id": opID})
}

func (s *Server) handleListInitiatives(w http.ResponseWriter, r *http.Request) {
	pr, err := s.rt.EnsureProject(r.Context(), r.PathValue("projectId"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	list, err := pr.db.ListInitiatives(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetInitiative(w http.ResponseWriter, r *http.Request) {
	pr, err := s.rt.EnsureProject(r.Context(), r.PathValue("projectId"))
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	ini, err := pr.db.GetInitiative(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "initiative not found")
		return
	}
	writeJSON(w, http.StatusOK, ini)
}

func (s *Server) handleUnderstanding(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	u, err := projection.BuildUnderstanding(r.Context(), pr.db, ini)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleNextMove(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	u, err := projection.BuildUnderstanding(r.Context(), pr.db, ini)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, projection.BuildNextMove(ini, u))
}

func (s *Server) handleChanged(w http.ResponseWriter, r *http.Request) {
	pr, _, err := s.initiative(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	since := int64(0)
	cursor, err := pr.db.GetReadCursor(r.Context(), "ui")
	if err == nil {
		since = cursor
	}
	changed, err := projection.BuildChanged(r.Context(), pr.db, since, 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, changed)
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	status, err := pr.engine.Resume(r.Context(), ini.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": string(status)})
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := pr.engine.Approve(r.Context(), ini.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": string(domain.InitiativeApproved)})
}

// handleRevise applies the human's decisions/answers and regenerates the PRD.
func (s *Server) handleRevise(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	initiativeID := r.PathValue("id")
	opID, err := s.rt.ReviseInitiative(r.Context(), projectID, initiativeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
}

// handleAdvance launches the M2 Design → Execution Plan pipeline.
func (s *Server) handleAdvance(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	initiativeID := r.PathValue("id")
	opID, err := s.rt.AdvanceInitiative(r.Context(), projectID, initiativeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
}

// handleLogs returns the tail of the daemon log (global, token-authenticated).
func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	n := 100
	if v := r.URL.Query().Get("n"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 2000 {
			n = parsed
		}
	}
	data, err := os.ReadFile(filepath.Join(s.rt.StateRoot(), "daemon.log"))
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"path": "", "lines": []string{}})
		return
	}
	all := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(all) > n {
		all = all[len(all)-n:]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":  filepath.Join(s.rt.StateRoot(), "daemon.log"),
		"lines": all,
	})
}

// ---------- providers / config ----------

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, buildProvidersView(r.Context(), pr.cfg, pr.dotEnv, pr.registry))
}

func (s *Server) handleUpdateProviders(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct {
		DefaultModel string                           `json:"default_model"`
		Providers    map[string]config.ProviderConfig `json:"providers"`
		Models       map[string]string                `json:"models"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}

	cfg := pr.cfg.Harness
	if body.DefaultModel != "" {
		cfg.DefaultModel = body.DefaultModel
	}
	for name, pc := range body.Providers {
		cfg.Providers[name] = pc
	}
	if body.Models != nil {
		if cfg.Models == nil {
			cfg.Models = map[string]string{}
		}
		for role, spec := range body.Models {
			if spec == "" {
				delete(cfg.Models, role) // empty clears a role override
			} else {
				cfg.Models[role] = spec
			}
		}
	}

	// Reject inline secrets (§47) and write harness.yaml.
	if err := config.WriteHarness(filepath.Join(pr.rootPath, config.Dir), cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Reload so the new registry/default model takes effect.
	if _, err := s.rt.ReloadProject(r.Context(), r.PathValue("projectId")); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ---------- live model listing ----------

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	provider := r.URL.Query().Get("provider")
	if provider == "" {
		if p, _ := config.ParseModelSpec(pr.cfg.Harness.DefaultModel); p != "" {
			provider = p
		}
	}
	if provider == "" {
		provider = "openai"
	}
	models, _ := pr.registry.ListModels(r.Context(), provider)
	writeJSON(w, http.StatusOK, map[string]any{"provider": provider, "models": models})
}

// ---------- operations ----------

func (s *Server) handleGetOperation(w http.ResponseWriter, r *http.Request) {
	// Operation lives in whichever project's DB; we do not know it from the
	// path, so search the registry (M1: single project common case).
	projects, err := s.rt.ListProjects(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, p := range projects {
		pr, err := s.rt.EnsureProject(r.Context(), p.ProjectID)
		if err != nil {
			continue
		}
		op, err := pr.db.GetOperation(r.Context(), r.PathValue("id"))
		if err == nil {
			writeJSON(w, http.StatusOK, op)
			return
		}
	}
	writeError(w, http.StatusNotFound, "operation not found")
}

// handleCancelOperation interrupts a running operation (M3, §16).
func (s *Server) handleCancelOperation(w http.ResponseWriter, r *http.Request) {
	opID := r.PathValue("id")
	if err := s.rt.CancelOperation(r.Context(), opID); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"operation_id": opID, "status": string(domain.OperationCancelled)})
}

// ---------- decisions / questions ----------

func (s *Server) handleListDecisions(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiativeFromQuery(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	list, err := pr.db.ListDecisions(r.Context(), ini.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleResolveDecision(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct {
		Resolution string `json:"resolution"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Resolution == "" {
		writeError(w, http.StatusBadRequest, "resolution is required")
		return
	}
	if err := pr.db.ResolveDecision(r.Context(), r.PathValue("id"), body.Resolution, store.HumanActor); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "decided"})
}

// handleDecisionDiscuss runs one human→AI discussion turn on a decision.
func (s *Server) handleDecisionDiscuss(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Message) == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}
	response, err := pr.engine.DiscussDecision(r.Context(), r.PathValue("id"), strings.TrimSpace(body.Message))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"response": response})
}

// handleDecisionDiscussion lists a decision's discussion thread.
func (s *Server) handleDecisionDiscussion(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	thread, err := pr.db.ListDecisionDiscussions(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, thread)
}

func (s *Server) handleListQuestions(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiativeFromQuery(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	list, err := pr.db.ListQuestions(r.Context(), ini.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleAnswerQuestion(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Answer == "" {
		writeError(w, http.StatusBadRequest, "answer is required")
		return
	}
	digest, err := pr.blobs.Put([]byte(body.Answer))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := pr.db.AnswerQuestion(r.Context(), r.PathValue("id"), digest, store.HumanActor); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

// ---------- requirements / assertions / artifacts ----------

func (s *Server) handleListRequirements(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiativeFromQuery(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	list, err := pr.db.ListRequirements(r.Context(), ini.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleListAssertions(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiativeFromQuery(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	list, err := pr.db.ListAssertions(r.Context(), ini.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleListTasks returns the execution-plan tasks for an initiative. The
// initiative is taken from the {id} path parameter (review: full-platform
// UX/API — it previously read ?initiative_id=/oldest-initiative fallback).
func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	list, err := pr.db.ListTasks(r.Context(), ini.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleTraceObject dispatches why / impact / trace for a graph node.
func (s *Server) handleTraceObject(w http.ResponseWriter, r *http.Request, mode string) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	objType := r.URL.Query().Get("type")
	objID := r.URL.Query().Get("id")
	if objType == "" || objID == "" {
		writeError(w, http.StatusBadRequest, "type and id query params are required")
		return
	}
	switch mode {
	case "why":
		items, err := pr.engine.Why(r.Context(), ini.ID, objType, objID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, items)
	case "impact":
		items, err := pr.engine.Impact(r.Context(), ini.ID, objType, objID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, items)
	case "trace":
		why, err := pr.engine.Why(r.Context(), ini.ID, objType, objID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		impact, err := pr.engine.Impact(r.Context(), ini.ID, objType, objID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"why": why, "impact": impact})
	}
}

func (s *Server) handleWhy(w http.ResponseWriter, r *http.Request)    { s.handleTraceObject(w, r, "why") }
func (s *Server) handleImpact(w http.ResponseWriter, r *http.Request)  { s.handleTraceObject(w, r, "impact") }
func (s *Server) handleTrace(w http.ResponseWriter, r *http.Request)   { s.handleTraceObject(w, r, "trace") }

// handleDiff returns the structured requirement/decision diff for an initiative.
func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	d, err := pr.engine.SemanticDiff(r.Context(), ini.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// handleSuggestAnswers proposes AI answers for open questions.
func (s *Server) handleSuggestAnswers(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	initiativeID := r.PathValue("id")
	opID, err := s.rt.SuggestAnswersInitiative(r.Context(), projectID, initiativeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
}

// handleListSuggestedAnswers returns the persisted AI-suggested answers.
func (s *Server) handleListSuggestedAnswers(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiativeFromQuery(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	answers, err := pr.db.ListSuggestedAnswers(r.Context(), ini.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, answers)
}

func (s *Server) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiativeFromQuery(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	list, err := pr.db.ListArtifacts(r.Context(), ini.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleArtifactBody(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	av, err := pr.db.GetArtifactVersion(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "artifact not found")
		return
	}
	content, err := pr.blobs.Get(av.BodyDigest)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "artifact body missing")
		return
	}
	// Sanitize before serving (§15).
	sanitized := security.SanitizeMarkdown(string(content))
	writeJSON(w, http.StatusOK, map[string]any{
		"kind":       av.Kind,
		"version":    av.Version,
		"body":       sanitized,
		"created_at": av.CreatedAt,
	})
}

// handleArtifactRaw returns the raw markdown body for download/export (§20).
func (s *Server) handleArtifactRaw(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	av, err := pr.db.GetArtifactVersion(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "artifact not found")
		return
	}
	content, err := pr.blobs.Get(av.BodyDigest)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "artifact body missing")
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s-v%d.md", av.Kind, av.Version))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

// ---------- findings ----------

func (s *Server) handleListFindings(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiativeFromQuery(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	findings, err := pr.db.ListFindingsByInitiative(r.Context(), ini.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, findings)
}

func (s *Server) handleFindingDisposition(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct {
		Disposition string `json:"disposition"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "disposition is required")
		return
	}
	disp := domain.Disposition(body.Disposition)
	if err := pr.db.SetFindingDisposition(r.Context(), r.PathValue("id"), disp, store.HumanActor); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "dispositioned"})
}

// handleFindingNote records a free-text human note on a finding ("discuss").
func (s *Server) handleFindingNote(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct {
		Note string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "note is required")
		return
	}
	if strings.TrimSpace(body.Note) == "" {
		writeError(w, http.StatusBadRequest, "note is required")
		return
	}
	if err := pr.db.SetFindingNote(r.Context(), r.PathValue("id"), strings.TrimSpace(body.Note), store.HumanActor); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "noted"})
}

// handleDoctor audits the initiative's product-graph edges and returns any
// traceability gaps (untraced tasks/objects, dependency cycles).
func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiativeFromQuery(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	findings, err := pr.db.AuditGraphEdges(r.Context(), ini.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, findings)
}

// ---------- change sets / execute / risk ----------

// handleExecute launches the M3 execute pipeline for the next ready task.
func (s *Server) handleExecute(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	initiativeID := r.PathValue("id")
	opID, err := s.rt.ExecuteInitiative(r.Context(), projectID, initiativeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
}

// handleVerify runs the verification gate and, when a requirement + pass is
// supplied, records a human pass first (§70, review B4/B10).
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	initiativeID := r.PathValue("id")
	pr, err := s.rt.EnsureProject(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct {
		Requirement string `json:"requirement"`
		Pass        bool   `json:"pass"`
		Note        string `json:"note"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)

	if body.Requirement != "" && body.Pass {
		if err := pr.engine.RecordHumanPass(r.Context(), initiativeID, body.Requirement, body.Note, ""); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := pr.engine.VerifyInitiative(r.Context(), initiativeID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ini, err := pr.db.GetInitiative(r.Context(), initiativeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	coverage, err := pr.engine.Coverage(r.Context(), initiativeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": ini.Status, "coverage": coverage})
}

// handleNext returns the deterministic next step for an initiative (§52).
func (s *Server) handleNext(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	initiativeID := r.PathValue("id")
	pr, err := s.rt.EnsureProject(r.Context(), projectID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	action, err := pr.engine.NextAction(r.Context(), initiativeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"action": action})
}

// handleListChangeSets returns the change sets for an initiative.
func (s *Server) handleListChangeSets(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiativeFromQuery(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	list, err := pr.db.ListChangeSets(r.Context(), ini.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleGetChangeSet returns one change set by id.
func (s *Server) handleGetChangeSet(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	cs, err := pr.db.GetChangeSetByRef(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, changeSetRefError(err))
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

// handleChangeSetDiff serves the change set's patch as a readable diff (path +
// content per file). Content blobs are already redacted at write time (§43).
func (s *Server) handleChangeSetDiff(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	cs, err := pr.db.GetChangeSetByRef(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, changeSetRefError(err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"patch": pr.engine.RenderPatch(cs)})
}
func (s *Server) handleReviewChangeSet(w http.ResponseWriter, r *http.Request) {
	pr, cs, err := s.changeSet(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct {
		Verdict string `json:"verdict"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Verdict == "" {
		writeError(w, http.StatusBadRequest, "verdict is required")
		return
	}
	if err := pr.engine.ReviewChangeSet(r.Context(), cs.InitiativeID, cs.ID, body.Verdict); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reviewed"})
}

// handleApplyChangeSet applies an approved change set asynchronously.
func (s *Server) handleApplyChangeSet(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	_, cs, err := s.changeSet(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	opID, err := s.rt.ApplyChangeSetInitiative(r.Context(), projectID, cs.InitiativeID, cs.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
}

// handleRevertChangeSet records a rolled-back journal entry asynchronously.
func (s *Server) handleRevertChangeSet(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	_, cs, err := s.changeSet(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	opID, err := s.rt.RevertChangeSetInitiative(r.Context(), projectID, cs.InitiativeID, cs.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
}

// handleRisk computes the deterministic risk profile for an initiative from its
// produced patches.
func (s *Server) handleRisk(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiativeFromQuery(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	list, err := pr.db.ListChangeSets(r.Context(), ini.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var diff strings.Builder
	for _, cs := range list {
		if cs.PatchDigest == "" {
			continue
		}
		diff.WriteString(pr.engine.RenderPatch(&cs))
		diff.WriteByte('\n')
	}
	signals := risk.RiskSignals(r.Context(), "", diff.String())
	profile := risk.ComputeRiskProfile(ini.ID, signals)
	// Persist the profile so the deterministic tier floor is durable and can be
	// re-derived from past scans, not just computed on demand and lost.
	if err := pr.db.SaveRiskProfile(r.Context(), &profile); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

// handleListExecutors returns the available executors and their declared
// capabilities, plus which one the harness is configured to use.
func (s *Server) handleListExecutors(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	execs := []executor.Executor{
		executor.NewInternalExecutor(pr.blobs),
		executor.NewContainerExecutor(pr.blobs, pr.cfg.Harness.Executor.Image),
		executor.NewOMPExecutor(pr.blobs, pr.cfg.Harness.Executor.Model, pr.cfg.Harness.Executor.Image),
		executor.NewHermesExecutor(pr.blobs, pr.cfg.Harness.Executor.Model, pr.cfg.Harness.Executor.Image),
	}
	list := make([]map[string]any, 0, len(execs))
	for _, exec := range execs {
		caps := exec.Capabilities(r.Context())
		list = append(list, map[string]any{
			"name":         exec.Name(),
			"trust":        caps.Trust,
			"sandbox_mode": caps.SandboxMode,
			"network":      caps.Network,
			"capabilities": caps.Capabilities,
			"selected":     string(pr.cfg.Harness.Executor.Kind) == exec.Name(),
		})
	}
	writeJSON(w, http.StatusOK, list)
}

// changeSet resolves the project runtime + a change set by its {id} path value.
func (s *Server) changeSet(r *http.Request) (*projectRuntime, *domain.ChangeSet, error) {
	pr, err := s.project(r)
	if err != nil {
		return nil, nil, err
	}
	cs, err := pr.db.GetChangeSetByRef(r.Context(), r.PathValue("id"))
	return pr, cs, err
}

// changeSetRefError maps a change-set resolution error to a user-facing message,
// preserving the ambiguity error while keeping not-found friendly.
func changeSetRefError(err error) string {
	if errors.Is(err, sql.ErrNoRows) {
		return "change set not found"
	}
	return err.Error()
}

// ---------- activity / events ----------

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	events, err := pr.db.ListEvents(r.Context(), 0, 200)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, events)
}

// ---------- helpers ----------

func (s *Server) project(r *http.Request) (*projectRuntime, error) {
	return s.rt.EnsureProject(r.Context(), r.PathValue("projectId"))
}

func (s *Server) initiative(r *http.Request) (*projectRuntime, domain.Initiative, error) {
	pr, err := s.project(r)
	if err != nil {
		return nil, domain.Initiative{}, err
	}
	ini, err := pr.db.GetInitiative(r.Context(), r.PathValue("id"))
	return pr, ini, err
}

// initiativeFromQuery resolves an initiative from the ?initiative_id= query or
// falls back to the single initiative in the project.
func (s *Server) initiativeFromQuery(r *http.Request) (*projectRuntime, domain.Initiative, error) {
	pr, err := s.project(r)
	if err != nil {
		return nil, domain.Initiative{}, err
	}
	if id := r.URL.Query().Get("initiative_id"); id != "" {
		ini, err := pr.db.GetInitiative(r.Context(), id)
		return pr, ini, err
	}
	list, err := pr.db.ListInitiatives(r.Context())
	if err != nil || len(list) == 0 {
		return pr, domain.Initiative{}, errors.New("no initiatives in project")
	}
	return pr, list[0], nil
}
