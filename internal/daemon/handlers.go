package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/idea-harness/idea-harness/internal/config"
	"github.com/idea-harness/idea-harness/internal/docparse"
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
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"project_id": projectID})
}

func (s *Server) handleListProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := s.rt.ListProjects(r.Context())
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, projects)
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	if _, err := s.rt.EnsureProject(r.Context(), r.PathValue("projectId")); err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	rec, err := s.rt.global.GetProject(r.Context(), r.PathValue("projectId"))
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
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
		Docs        []struct {
			Content string `json:"content"`
			Kind    string `json:"kind"`
			Title   string `json:"title"`
		} `json:"docs"`
	}
	// Bound the create-with-docs body (title + description + up to N docs, each
	// ≤ maxDocFileBytes) so a non-CLI client cannot force unbounded decode.
	r.Body = http.MaxBytesReader(w, r.Body, maxDocBodyBytes*maxContextDocsPerInitiative)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Title == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	var docs []InitiativeDocInput
	for _, d := range body.Docs {
		docs = append(docs, InitiativeDocInput{Content: d.Content, Kind: d.Kind, Title: d.Title})
	}
	iniID, opID, err := s.rt.StartInitiativeWithDocs(r.Context(), projectID, body.Title, body.Description, docs)
	if err != nil {
		var ie ingestError
		if errors.As(err, &ie) {
			writeError(w, ie.status, ie.msg)
			return
		}
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"initiative_id": iniID, "operation_id": opID})
}

func (s *Server) handleListInitiatives(w http.ResponseWriter, r *http.Request) {
	pr, err := s.rt.EnsureProject(r.Context(), r.PathValue("projectId"))
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	list, err := pr.db.ListInitiatives(r.Context())
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetInitiative(w http.ResponseWriter, r *http.Request) {
	pr, err := s.rt.EnsureProject(r.Context(), r.PathValue("projectId"))
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
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
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	u, err := projection.BuildUnderstanding(r.Context(), pr.db, ini)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleNextMove(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	u, err := projection.BuildUnderstanding(r.Context(), pr.db, ini)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, projection.BuildNextMove(ini, u))
}

func (s *Server) handleChanged(w http.ResponseWriter, r *http.Request) {
	pr, _, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	since := int64(0)
	cursor, err := pr.db.GetReadCursor(r.Context(), "ui")
	if err == nil {
		since = cursor
	}
	changed, err := projection.BuildChanged(r.Context(), pr.db, since, 200)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, changed)
}

// handleMarkSeen writes the project's "ui" read cursor to the sequence the
// client actually rendered (echoed from /changed's `through`), so What Changed
// clears exactly what was shown and never events that arrived mid-read.
func (s *Server) handleMarkSeen(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	var body struct {
		Through int64 `json:"through"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Through < 0 {
		writeError(w, http.StatusBadRequest, "through must be a non-negative integer")
		return
	}
	maxSeq, err := pr.db.MaxEventSeq(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.Through > maxSeq {
		body.Through = maxSeq
	}
	if err := pr.db.SetReadCursor(r.Context(), "ui", body.Through); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"seen_seq": body.Through})
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	status, err := pr.engine.Resume(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": string(status)})
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	if err := pr.engine.Approve(r.Context(), ini.ID); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
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
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
}

// handleRevisePlan regenerates the execution plan + review (keeps PRD/design).
func (s *Server) handleRevisePlan(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	initiativeID := r.PathValue("id")
	opID, err := s.rt.RevisePlanInitiative(r.Context(), projectID, initiativeID)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
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
		s.writeErr(w, http.StatusInternalServerError, err)
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
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, buildProvidersView(r.Context(), pr.cfg, pr.dotEnv, pr.registry))
}

func (s *Server) handleUpdateProviders(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		DefaultModel string                           `json:"default_model"`
		Providers    map[string]config.ProviderConfig `json:"providers"`
		Models       map[string]string                `json:"models"`
		GitCommit    *bool                            `json:"git_commit"`
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

	if body.GitCommit != nil {
		cfg.GitCommit = *body.GitCommit
	}

	// Reject inline secrets (§47) and write harness.yaml.
	if err := config.WriteHarness(filepath.Join(pr.rootPath, config.Dir), cfg); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Reload so the new registry/default model takes effect.
	if _, err := s.rt.ReloadProject(r.Context(), r.PathValue("projectId")); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

// ---------- live model listing ----------

func (s *Server) handleListModels(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
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
		s.writeErr(w, http.StatusInternalServerError, err)
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
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	list, err := pr.db.ListDecisions(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleResolveDecision(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
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

// handleReopenDecision undoes a resolved decision, returning it to open.
func (s *Server) handleReopenDecision(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := pr.db.ReopenDecision(r.Context(), r.PathValue("id"), store.HumanActor); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "open"})
}

// handleDecisionDiscuss runs one human→AI discussion turn on a decision.
func (s *Server) handleDecisionDiscuss(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
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
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"response": response})
}

// handleDecisionDiscussion lists a decision's discussion thread.
func (s *Server) handleDecisionDiscussion(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	thread, err := pr.db.ListDecisionDiscussions(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, thread)
}

func (s *Server) handleListQuestions(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiativeFromQuery(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	list, err := pr.db.ListQuestions(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleAnswerQuestion(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
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
		s.writeErr(w, http.StatusInternalServerError, err)
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
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	list, err := pr.db.ListRequirements(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleListAssertions(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiativeFromQuery(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	list, err := pr.db.ListAssertions(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
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
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	list, err := pr.db.ListTasks(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// ---------- task mutations (Plan editing) ----------

// taskEditableGuard validates that an initiative's tasks are editable and (when
// task is non-nil) that the specific task is mutable. It returns a user-facing
// error otherwise.
func (s *Server) taskEditableGuard(ctx context.Context, pr *projectRuntime, ini domain.Initiative, task *domain.Task) error {
	if ini.Status != domain.InitiativePlanApproved && ini.Status != domain.InitiativeNeedsHumanJudgment {
		return fmt.Errorf("tasks are editable only after the plan is drafted and before it is executed")
	}
	ops, err := pr.db.ListUnfinishedOperations(ctx)
	if err != nil {
		return err
	}
	for _, op := range ops {
		if op.InitiativeID == ini.ID {
			return fmt.Errorf("the initiative has work in progress; wait for it to finish")
		}
	}
	if task == nil {
		return nil
	}
	if task.InitiativeID != ini.ID {
		return fmt.Errorf("task does not belong to this initiative")
	}
	if task.Status == domain.TaskDone || task.Status == domain.TaskCancelled {
		return fmt.Errorf("task is %s and cannot be edited", task.Status)
	}
	cses, err := pr.db.ListChangeSetsByTask(ctx, task.ID)
	if err != nil {
		return err
	}
	if len(cses) > 0 {
		return fmt.Errorf("task already has change sets and cannot be edited")
	}
	return nil
}

func (s *Server) handleAddTask(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.taskEditableGuard(r.Context(), pr, ini, nil); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	var body struct {
		Title      string   `json:"title"`
		Statement  string   `json:"statement"`
		Priority   string   `json:"priority"`
		Estimate   string   `json:"estimate"`
		Implements string   `json:"implements"`
		DependsOn  []string `json:"depends_on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Title) == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}

	// Resolve the implements requirement (by code or id) if given.
	implementsID := ""
	if body.Implements != "" {
		reqs, err := pr.db.ListRequirements(r.Context(), ini.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		for _, req := range reqs {
			if req.Code == body.Implements || req.ID == body.Implements {
				implementsID = req.ID
				break
			}
		}
		if implementsID == "" {
			writeError(w, http.StatusBadRequest, "implements references an unknown requirement")
			return
		}
	}

	// Resolve depends_on titles to existing task ids.
	depIDs, err := s.resolveTaskRefs(r.Context(), pr, ini.ID, body.DependsOn)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	priority := domain.RequirementPriority(body.Priority)
	if !priority.Valid() {
		priority = domain.PriorityShould
	}
	task := &domain.Task{
		InitiativeID: ini.ID,
		Title:        pr.redactor.RedactString(strings.TrimSpace(body.Title)),
		Statement:    pr.redactor.RedactString(body.Statement),
		Priority:     priority,
		Estimate:     body.Estimate,
	}
	if err := pr.db.CreateTask(r.Context(), task, implementsID, depIDs); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	task, err := pr.db.GetTask(r.Context(), r.PathValue("taskId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err := s.taskEditableGuard(r.Context(), pr, ini, task); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	var body struct {
		Title     string `json:"title"`
		Statement string `json:"statement"`
		Priority  string `json:"priority"`
		Estimate  string `json:"estimate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	priority := task.Priority
	if body.Priority != "" {
		p := domain.RequirementPriority(body.Priority)
		if !p.Valid() {
			writeError(w, http.StatusBadRequest, "invalid priority")
			return
		}
		priority = p
	}
	title := task.Title
	if strings.TrimSpace(body.Title) != "" {
		title = pr.redactor.RedactString(strings.TrimSpace(body.Title))
	}
	statement := task.Statement
	if body.Statement != "" {
		statement = pr.redactor.RedactString(body.Statement)
	}
	if err := pr.db.UpdateTask(r.Context(), ini.ID, task.ID, title, statement, priority, body.Estimate); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	updated, _ := pr.db.GetTask(r.Context(), task.ID)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	task, err := pr.db.GetTask(r.Context(), r.PathValue("taskId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err := s.taskEditableGuard(r.Context(), pr, ini, task); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	// Refuse if any other task depends on this one.
	edges, err := pr.db.ListGraphEdgesTo(r.Context(), ini.ID, "task", task.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, e := range edges {
		if e.EdgeType == domain.EdgeDependsOn {
			writeError(w, http.StatusConflict, "another task depends on this task; remove that dependency first")
			return
		}
	}
	if err := pr.db.DeleteTask(r.Context(), ini.ID, task.ID); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	task, err := pr.db.GetTask(r.Context(), r.PathValue("taskId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err := s.taskEditableGuard(r.Context(), pr, ini, task); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err := pr.db.SetTaskStatus(r.Context(), task.ID, domain.TaskCancelled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": string(domain.TaskCancelled)})
}

func (s *Server) handleReorderTasks(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.taskEditableGuard(r.Context(), pr, ini, nil); err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	var body struct {
		TaskIDs []string `json:"task_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.TaskIDs) == 0 {
		writeError(w, http.StatusBadRequest, "task_ids is required")
		return
	}
	if err := pr.db.ReorderTasks(r.Context(), ini.ID, body.TaskIDs); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	list, _ := pr.db.ListTasks(r.Context(), ini.ID)
	writeJSON(w, http.StatusOK, list)
}

// resolveTaskRefs resolves task references (title, code, or id) to task ids.
func (s *Server) resolveTaskRefs(ctx context.Context, pr *projectRuntime, initiativeID string, refs []string) ([]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	tasks, err := pr.db.ListTasks(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	byNorm := map[string]string{}
	for _, t := range tasks {
		byNorm[norm(t.Title)] = t.ID
		byNorm[norm(t.Code)] = t.ID
	}
	var out []string
	for _, ref := range refs {
		id, ok := byNorm[norm(ref)]
		if !ok {
			return nil, fmt.Errorf("depends_on references an unknown task %q", ref)
		}
		out = append(out, id)
	}
	return out, nil
}

// handleTraceObject dispatches why / impact / trace for a graph node.
func (s *Server) handleTraceObject(w http.ResponseWriter, r *http.Request, mode string) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
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
			s.writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	case "impact":
		items, err := pr.engine.Impact(r.Context(), ini.ID, objType, objID)
		if err != nil {
			s.writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, items)
	case "trace":
		why, err := pr.engine.Why(r.Context(), ini.ID, objType, objID)
		if err != nil {
			s.writeErr(w, http.StatusInternalServerError, err)
			return
		}
		impact, err := pr.engine.Impact(r.Context(), ini.ID, objType, objID)
		if err != nil {
			s.writeErr(w, http.StatusInternalServerError, err)
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
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	d, err := pr.engine.SemanticDiff(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
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
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
}

// handleListSuggestedAnswers returns the persisted AI-suggested answers.
func (s *Server) handleListSuggestedAnswers(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiativeFromQuery(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	answers, err := pr.db.ListSuggestedAnswers(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, answers)
}

func (s *Server) handleListArtifacts(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiativeFromQuery(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	list, err := pr.db.ListArtifacts(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleArtifactBody(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
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
		s.writeErr(w, http.StatusNotFound, err)
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

// ---------- context docs ----------

// maxDocFileBytes caps a single uploaded document's file CONTENT at 2 MiB.
// maxDocBodyBytes is the total request-body budget — slightly larger so
// multipart framing (boundary, part headers, kind/title fields) and JSON
// syntax overhead do not reject a boundary-sized file with a spurious 413.
const (
	maxDocFileBytes = 2 << 20
	maxDocBodyBytes = maxDocFileBytes + 64<<10
	// maxContextDocsPerInitiative bounds the number of docs (and thus image
	// description calls) a single initiative can accumulate.
	maxContextDocsPerInitiative = 100
)

// ingestError is a user-facing (4xx) ingest rejection with an explicit status,
// distinguished from internal errors so the handler can pick the right code.
type ingestError struct {
	status int
	msg    string
}

func (e ingestError) Error() string { return e.msg }

// handleAddDoc ingests a context doc for an initiative: credential-sweep →
// redact → content-addressed blob → row. Two request shapes are accepted:
//   - JSON (`content`/`kind`/`title`) — the paste-text path.
//   - multipart/form-data (`file` + optional `kind`/`title`) — the upload path;
//     the daemon sniffs the format and extracts text from .md/.txt/.pdf/.docx/
//     .xlsx, or stores an image (.jpg/.png/…) for the vision-describe path.
func (s *Server) handleAddDoc(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	if r.ContentLength > maxDocBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "document too large")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDocBodyBytes)

	var content, kind, title string
	var imageData []byte
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		data, filename, k, t, err := s.readMultipartFile(w, r)
		if err != nil {
			return // readMultipartFile already wrote the error
		}
		kind, title = k, t
		switch docparse.Sniff(filename, data) {
		case "image":
			imageData = data
		default:
			extracted, err := docparse.ExtractText(filename, data)
			if err != nil {
				writeError(w, http.StatusBadRequest, "could not parse document")
				return
			}
			content = extracted
		}
	} else {
		var body struct {
			Content string `json:"content"`
			Kind    string `json:"kind"`
			Title   string `json:"title"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				writeError(w, http.StatusRequestEntityTooLarge, "document too large")
				return
			}
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
		content, kind, title = body.Content, body.Kind, body.Title
	}

	var doc *domain.ContextDoc
	if imageData != nil {
		doc, err = s.ingestImageDoc(r, pr, ini, imageData, kind, title)
	} else {
		doc, err = s.ingestDoc(r, pr, ini, content, kind, title)
	}
	if err != nil {
		var ie ingestError
		if errors.As(err, &ie) {
			writeError(w, ie.status, ie.msg)
			return
		}
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if doc.Format == "image" {
		// Describe the image asynchronously so the upload returns immediately;
		// the description then flows into contextDocs for subsequent runs.
		go func() {
			if derr := pr.engine.DescribeImages(context.Background(), ini.ID); derr != nil {
				s.rt.log.Warn("describe images failed", "initiative", ini.ID, "err", derr)
			}
		}()
	}
	writeJSON(w, http.StatusOK, doc)
}

// readMultipartFile parses a multipart upload and returns the raw file bytes,
// filename, and kind/title. On failure it writes the error response and returns
// a non-nil err.
func (s *Server) readMultipartFile(w http.ResponseWriter, r *http.Request) ([]byte, string, string, string, error) {
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusRequestEntityTooLarge, "document too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid multipart body")
		}
		return nil, "", "", "", err
	}
	defer r.MultipartForm.RemoveAll()

	kind := strings.TrimSpace(r.FormValue("kind"))
	title := strings.TrimSpace(r.FormValue("title"))
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeError(w, http.StatusBadRequest, "file is required")
		return nil, "", "", "", err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, maxDocFileBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read file")
		return nil, "", "", "", err
	}
	if len(data) > maxDocFileBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "document too large")
		return nil, "", "", "", err
	}
	if kind == "" {
		kind = docparse.InferKind(hdr.Filename)
	}
	if title == "" {
		title = filepath.Base(hdr.Filename)
	}
	return data, hdr.Filename, kind, title, nil
}

// ingestImageDoc stores an image upload byte-for-byte in the redaction-free
// store and records a context_docs row with Format="image". The vision
// description is produced asynchronously and stored separately.
func (s *Server) ingestImageDoc(r *http.Request, pr *projectRuntime, ini domain.Initiative, data []byte, kind, title string) (*domain.ContextDoc, error) {
	if err := s.checkDocCap(r, pr, ini); err != nil {
		return nil, err
	}
	kind = truncateRunes(pr.redactor.RedactString(strings.TrimSpace(kind)), 64)
	if kind == "" {
		kind = "other"
	}
	title = truncateRunes(pr.redactor.RedactString(strings.TrimSpace(title)), 200)
	if title == "" {
		title = "untitled"
	}
	digest, err := pr.rawBlobs.Put(data)
	if err != nil {
		return nil, err
	}
	doc := &domain.ContextDoc{
		InitiativeID: ini.ID,
		Kind:         kind,
		Title:        title,
		Digest:       digest,
		Format:       "image",
	}
	if err := pr.db.CreateContextDoc(r.Context(), doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// ingestDoc applies the shared ingest pipeline (cap check + ingestTextDoc) for
// the /docs handler. It returns an ingestError for user rejections and a plain
// error for internal failures.
func (s *Server) ingestDoc(r *http.Request, pr *projectRuntime, ini domain.Initiative, content, kind, title string) (*domain.ContextDoc, error) {
	if err := s.checkDocCap(r, pr, ini); err != nil {
		return nil, err
	}
	return pr.ingestTextDoc(r.Context(), ini.ID, content, kind, title)
}

// ingestTextDoc runs the shared text-doc pipeline: trim → credential sweep →
// redact (kind/title/body) → content-addressed blob → row. It is shared by the
// /docs handler and the create-with-docs (idea new --doc) path.
func (pr *projectRuntime) ingestTextDoc(ctx context.Context, iniID, content, kind, title string) (*domain.ContextDoc, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return nil, ingestError{http.StatusBadRequest, "content is required"}
	}
	// Enforce the per-doc size cap server-side so no caller (CLI or otherwise)
	// can persist an oversized doc regardless of transport framing.
	if len(content) > maxDocFileBytes {
		return nil, ingestError{http.StatusRequestEntityTooLarge, "document too large"}
	}
	// Hard-reject on credentials with a non-echoing message (§45).
	if pr.redactor.ContainsCredential(content) {
		return nil, ingestError{http.StatusBadRequest, "document contains a credential and was rejected"}
	}
	// Title and kind are egress (returned by list + embedded in the prompt
	// header), so they must be redacted and bounded like the body.
	kind = truncateRunes(pr.redactor.RedactString(strings.TrimSpace(kind)), 64)
	if kind == "" {
		kind = "other"
	}
	title = truncateRunes(pr.redactor.RedactString(strings.TrimSpace(title)), 200)
	if title == "" {
		title = "untitled"
	}
	redacted := pr.redactor.RedactString(content)
	digest, err := pr.blobs.Put([]byte(redacted))
	if err != nil {
		return nil, err
	}
	doc := &domain.ContextDoc{
		InitiativeID: iniID,
		Kind:         kind,
		Title:        title,
		Digest:       digest,
		Preview:      truncateRunes(redacted, 200),
	}
	if err := pr.db.CreateContextDoc(ctx, doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// checkDocCap rejects an ingest when the initiative already holds the maximum
// number of context docs (bounds image-description work too).
func (s *Server) checkDocCap(r *http.Request, pr *projectRuntime, ini domain.Initiative) error {
	docs, err := pr.db.ListContextDocs(r.Context(), ini.ID)
	if err != nil {
		return err
	}
	if len(docs) >= maxContextDocsPerInitiative {
		return ingestError{http.StatusBadRequest, "too many context docs"}
	}
	return nil
}

// handleListDocs lists an initiative's ingested context docs.
func (s *Server) handleListDocs(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	docs, err := pr.db.ListContextDocs(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, docs)
}

// handleGetDoc downloads one context doc's stored content: image docs serve the
// raw bytes from the redaction-free store; text docs serve the redacted text.
func (s *Server) handleGetDoc(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	doc, err := pr.db.GetContextDoc(r.Context(), r.PathValue("docId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "doc not found")
		return
	}
	if doc.InitiativeID != ini.ID {
		writeError(w, http.StatusNotFound, "doc not found")
		return
	}

	var content []byte
	contentType := "text/plain; charset=utf-8"
	if doc.Format == "image" {
		content, err = pr.rawBlobs.Get(doc.Digest)
		contentType = docparse.ImageMediaType(content)
		if contentType == "" {
			contentType = "application/octet-stream"
		}
	} else {
		content, err = pr.blobs.Get(doc.Digest)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "doc content missing")
		return
	}

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": doc.Title}))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
}

// truncateRunes truncates s to at most n runes.
func truncateRunes(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n])
}

// ---------- findings ----------

func (s *Server) handleListFindings(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiativeFromQuery(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	findings, err := pr.db.ListFindingsByInitiative(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, findings)
}

func (s *Server) handleFindingDisposition(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
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
		s.writeErr(w, http.StatusNotFound, err)
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
	if err := pr.db.SetFindingNote(r.Context(), r.PathValue("id"), pr.redactor.RedactString(strings.TrimSpace(body.Note)), store.HumanActor); err != nil {
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
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	findings, err := pr.db.AuditGraphEdges(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, findings)
}

// ---------- change sets / execute / risk ----------

// handleExecute launches the M3 execute pipeline for the next ready task.
func (s *Server) handleExecute(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	initiativeID := r.PathValue("id")
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	opID, err := s.rt.ExecuteInitiative(r.Context(), projectID, initiativeID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"operation_id":  opID,
		"executor_kind": string(pr.cfg.Harness.Executor.Kind),
	})
}

// handleVerify runs the verification gate and, when a requirement + pass is
// supplied, records a human pass first (§70, review B4/B10).
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	initiativeID := r.PathValue("id")
	pr, err := s.rt.EnsureProject(r.Context(), projectID)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		Requirement string `json:"requirement"`
		Pass        bool   `json:"pass"`
		Note        string `json:"note"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
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
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	coverage, err := pr.engine.Coverage(r.Context(), initiativeID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
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
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	action, err := pr.engine.NextAction(r.Context(), initiativeID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"action": action})
}

// handleListChangeSets returns the change sets for an initiative.
func (s *Server) handleListChangeSets(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiativeFromQuery(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	list, err := pr.db.ListChangeSets(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleGetChangeSet returns one change set by id.
func (s *Server) handleGetChangeSet(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	cs, err := pr.db.GetChangeSetByRef(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, cs)
}

// handleListChecks returns the deterministic check results for a change set,
// projected from its change_set-targeted findings (patch_applies/build/tests).
func (s *Server) handleListChecks(w http.ResponseWriter, r *http.Request) {
	pr, cs, err := s.changeSet(r)
	if err != nil {
		writeError(w, http.StatusNotFound, changeSetRefError(err))
		return
	}
	findings, err := pr.db.ListFindingsByTarget(r.Context(), "change_set", cs.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	type check struct {
		Name     string `json:"name"`
		Status   string `json:"status"`
		Summary  string `json:"summary"`
		Evidence string `json:"evidence,omitempty"`
		DenyCode string `json:"deny_code,omitempty"`
	}
	checks := make([]check, 0, 3)
	pass, fail, na := 0, 0, 0
	for _, f := range findings {
		if f.Category != "patch_applies" && f.Category != "build" && f.Category != "tests" {
			continue
		}
		status := "pass"
		switch {
		case f.Disposition == domain.DispositionNotApplicable:
			status = "not_applicable"
		case f.Severity == domain.SeverityHigh || f.Severity == domain.SeverityCritical:
			status = "fail"
		}
		switch status {
		case "pass":
			pass++
		case "fail":
			fail++
		default:
			na++
		}
		checks = append(checks, check{Name: f.Category, Status: status, Summary: f.Finding, Evidence: f.Evidence, DenyCode: string(f.Code)})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"checks": checks, "pass": pass, "fail": fail, "not_applicable": na,
	})
}

// handleResolveConflict resolves a conflicted change set: retry (reset to
// approved and re-apply async) or reject (sync).
func (s *Server) handleResolveConflict(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	pr, cs, err := s.changeSet(r)
	if err != nil {
		writeError(w, http.StatusNotFound, changeSetRefError(err))
		return
	}
	var body struct {
		Resolution string `json:"resolution"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	switch body.Resolution {
	case "reject":
		if err := pr.engine.RejectConflicted(r.Context(), cs.InitiativeID, cs.ID); err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": string(domain.ChangeSetRejected)})
	case "retry":
		opID, err := s.rt.RetryConflictedApply(r.Context(), projectID, cs.InitiativeID, cs.ID)
		if err != nil {
			writeError(w, http.StatusConflict, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
	default:
		writeError(w, http.StatusBadRequest, "resolution must be retry or reject")
	}
}

// handleChangeSetDiff serves the change set's patch as a readable diff (path +
// content per file). Content blobs are already redacted at write time (§43).
func (s *Server) handleChangeSetDiff(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	cs, err := pr.db.GetChangeSetByRef(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"patch": pr.engine.RenderPatch(cs)})
}
func (s *Server) handleReviewChangeSet(w http.ResponseWriter, r *http.Request) {
	pr, cs, err := s.changeSet(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
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
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	opID, err := s.rt.ApplyChangeSetInitiative(r.Context(), projectID, cs.InitiativeID, cs.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
}

// handleRevertChangeSet records a rolled-back journal entry asynchronously.
func (s *Server) handleRevertChangeSet(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	_, cs, err := s.changeSet(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	opID, err := s.rt.RevertChangeSetInitiative(r.Context(), projectID, cs.InitiativeID, cs.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
}

// handleRisk computes the deterministic risk profile for an initiative from its
// produced patches.
func (s *Server) handleRisk(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiativeFromQuery(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	list, err := pr.db.ListChangeSets(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
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
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, profile)
}

// handleListExecutors returns the available executors and their declared
// capabilities, plus which one the harness is configured to use.
func (s *Server) handleListExecutors(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	execs := []executor.Executor{
		executor.NewInternalExecutor(pr.blobs),
		executor.NewContainerExecutor(pr.blobs, pr.cfg.Harness.Executor.Image),
		executor.NewOMPExecutor(pr.blobs, pr.cfg.Harness.Executor.Model, pr.cfg.Harness.Executor.Image, ""),
		executor.NewHermesExecutor(pr.blobs, pr.cfg.Harness.Executor.Model, pr.cfg.Harness.Executor.Image, ""),
	}
	list := make([]map[string]any, 0, len(execs))
	for _, exec := range execs {
		caps := exec.Capabilities(r.Context())
		list = append(list, map[string]any{
			"name":          exec.Name(),
			"trust":         caps.Trust,
			"sandbox_mode":  caps.SandboxMode,
			"network":       caps.Network,
			"capabilities":  caps.Capabilities,
			"selected":      string(pr.cfg.Harness.Executor.Kind) == exec.Name(),
			"image":         pr.cfg.Harness.Executor.Image,
			"model":         pr.cfg.Harness.Executor.Model,
			"default_image": executor.DefaultAgentImage(exec.Name()),
		})
	}
	writeJSON(w, http.StatusOK, list)
}

// handleUpdateExecutors writes executor.kind/image/model (field-level merge) and
// reloads the runtime so selectExecutor picks up the change. It re-reads config
// from disk to avoid overlaying onto a stale cache, and validates kind/image.
func (s *Server) handleUpdateExecutors(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		Kind  string  `json:"kind"`
		Image *string `json:"image"`
		Model *string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.Kind != "" && !config.ExecutorKind(body.Kind).Valid() {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid executor kind %q", body.Kind))
		return
	}
	if body.Image != nil && strings.ContainsAny(*body.Image, " \t\n\r") {
		writeError(w, http.StatusBadRequest, "invalid executor image")
		return
	}

	// Re-read from disk (not the daemon's cached copy) so a hand-edited
	// harness.yaml is not clobbered, then merge only the supplied fields.
	cfg, err := config.Load(filepath.Join(pr.rootPath, config.Dir))
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if body.Kind != "" {
		cfg.Harness.Executor.Kind = config.ExecutorKind(body.Kind)
	}
	if body.Image != nil {
		cfg.Harness.Executor.Image = *body.Image // empty clears
	}
	if body.Model != nil {
		cfg.Harness.Executor.Model = *body.Model // empty clears
	}
	if err := config.WriteHarness(filepath.Join(pr.rootPath, config.Dir), cfg.Harness); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := s.rt.ReloadProject(r.Context(), r.PathValue("projectId")); err != nil {
		writeError(w, http.StatusConflict, "config written but not applied (reload failed): "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
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

// ---------- activity / events ----------

func (s *Server) handleActivity(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	events, err := pr.db.ListEvents(r.Context(), 0, 200)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
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
