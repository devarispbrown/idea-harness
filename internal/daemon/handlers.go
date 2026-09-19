package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/devarispbrown/idea-harness/internal/buildinfo"
	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/docparse"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/projection"
	"github.com/devarispbrown/idea-harness/internal/risk"
	"github.com/devarispbrown/idea-harness/internal/security"
	"github.com/devarispbrown/idea-harness/internal/store"
	"github.com/devarispbrown/idea-harness/internal/textdiff"
	"github.com/devarispbrown/idea-harness/internal/workflow"
)

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": buildinfo.Get().Version})
}

func (s *Server) handleVersion(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, buildinfo.Get())
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

// handleGuidance returns the Recommended Next Move, extended with the Build
// contract (backend-2/web-build): `next_task` (the task Execute would run
// next) and `run_blocked_reason` (empty when a run can start now). Both are
// composed here rather than inside BuildGuidance, which stays a pure function
// of Understanding with no engine/task-store access.
func (s *Server) handleGuidance(w http.ResponseWriter, r *http.Request) {
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
	// Review C6: BuildGuidance's own signature change. Errors are swallowed to
	// nil -- an initiative with no change sets yet (or a store hiccup) must
	// not turn "recommended next move" into a 500; it just gets the old,
	// change-set-agnostic guidance.
	//
	// M7: reduced to the latest change set per ACTIVE task before being
	// handed to BuildGuidance, not the raw list -- see
	// LatestChangeSetsPerActiveTask's and latestChangeSetOutcome's doc
	// comments for why passing the raw list was wrong.
	allChangeSets, _ := pr.db.ListChangeSets(r.Context(), ini.ID)
	tasks, _ := pr.db.ListTasks(r.Context(), ini.ID)
	recentChangeSets := projection.LatestChangeSetsPerActiveTask(allChangeSets, tasks)
	g := projection.BuildGuidance(ini, u, recentChangeSets)

	if nt, err := pr.engine.NextReadyTask(r.Context(), ini.ID); err == nil && nt != nil {
		g.NextTask = &projection.NextTaskRef{ID: nt.ID, HumanID: nt.HumanID, Title: nt.Title}
	}
	if reason, err := pr.engine.RunBlockedReason(r.Context(), ini.ID); err == nil {
		g.RunBlockedReason = reason
	}
	// The executor-readiness half (C3): only worth a Docker-backed check (cached,
	// see imagePresenceCache) when the workflow-level checks already say a run
	// could start and there is actually a task to run it on.
	if pw := pr.engine.PartialWork(ini.ID); pw != nil {
		ref := &projection.PartialWorkRef{TaskID: pw.TaskID, Files: pw.Files, Note: pw.Note}
		if t, err := pr.db.GetTask(r.Context(), pw.TaskID); err == nil {
			ref.TaskHumanID = t.Code
		}
		g.PartialWork = ref
	}
	if g.RunBlockedReason == "" && g.NextTask != nil {
		st := s.rt.executorStatus(pr, string(pr.cfg.Harness.Executor.Kind), true)
		g.RunBlockedReason = st.Problem
	}
	// Review A4 #5: "N items may be invalid since DEC-n changed", with the
	// re-plan/review path named, plus the itemized list for the UI. Composed
	// here for the same reason NextTask/RunBlockedReason/PartialWork are:
	// BuildGuidance has no store access.
	if items, err := pr.db.ListStaleItems(r.Context(), ini.ID); err == nil && len(items) > 0 {
		refs := make([]projection.StaleItemRef, 0, len(items))
		var codes []string
		seenCode := map[string]bool{}
		for _, it := range items {
			label := s.staleItemLabel(r.Context(), pr, ini.ID, it)
			refs = append(refs, projection.StaleItemRef{StaleID: it.ID, Type: it.TargetType, ID: it.TargetID, Label: label, Reason: it.Reason})
			code, _, _ := strings.Cut(it.Reason, " ")
			if code != "" && !seenCode[code] {
				seenCode[code] = true
				codes = append(codes, code)
			}
		}
		g.StaleItems = refs
		// review M5: `idea revise-plan` refuses once any change set holds work
		// (workflow.changeSetsHoldingWork), so telling the user to run it here
		// would just send them at a guaranteed refusal. Point at what is
		// actually available instead: acknowledge the stale items, or re-run
		// the affected tasks so their change sets are regenerated fresh.
		advice := "Run `idea revise-plan` and review before continuing, or acknowledge the items."
		if cses, err := pr.db.ListChangeSets(r.Context(), ini.ID); err == nil {
			for _, cs := range cses {
				if cs.Status.HoldsWork() {
					advice = "`idea revise-plan` is unavailable once a change set holds work; " +
						"acknowledge the items, or re-run the affected tasks so their change sets are regenerated."
					break
				}
			}
		}
		g.Why = fmt.Sprintf("%s %d item(s) may be invalid since %s changed. %s",
			g.Why, len(items), strings.Join(codes, ", "), advice)
	}
	// ICP re-run defect #5: changeSetFailureWhy (BuildGuidance, no store
	// access) can only say "a check or the review found a problem it would
	// not pass" for a rejected change set -- true, but not what a human
	// needs to act. When the rejection is a secret_sweep denial, name the
	// actual cause here, where the findings are available: which file (or
	// "in the output of <check>") and kind of secret, and the "not a secret"
	// disposition route when the match is overridable.
	for _, cs := range allChangeSets {
		if cs.Status != domain.ChangeSetRejected || g.Action != "Resolve "+cs.HumanID {
			continue
		}
		findings, ferr := pr.db.ListFindingsByTarget(r.Context(), "change_set", cs.ID)
		if ferr != nil {
			break
		}
		if detail := secretSweepGuidanceDetail(findings); detail != "" {
			g.Why += " " + detail
		}
		break
	}

	writeJSON(w, http.StatusOK, g)
}

// secretSweepGuidanceDetail names the actual cause a rejected change set's
// generic guidance ("a check or the review found a problem it would not
// pass") otherwise leaves unsaid (ICP re-run defect #5): the secret sweep's
// own finding already names path:line (or "in the output of <check>") and
// the kind of secret, never the value itself, and an overridable
// (pattern-shaped) match still has the not_a_secret route out. Returns "" --
// leaving the generic message alone -- when the change set was not rejected
// by the secret sweep at all (a failing build/test check, say).
func secretSweepGuidanceDetail(findings []domain.Finding) string {
	for _, f := range findings {
		if f.Category != "secret_sweep" || f.Code != domain.DenyCodeSecretSweep {
			continue
		}
		detail := fmt.Sprintf("The secret sweep denied it: %s.", f.Finding)
		if f.SecretOverridable {
			detail += " If this is not really a secret, `idea reviews` lets you disposition it \"not a secret\" to allow it."
		}
		return detail
	}
	return ""
}

// staleItemLabel resolves a human-readable label for a stale item, best-effort
// (an empty label is omitted by the JSON tag, not an error).
func (s *Server) staleItemLabel(ctx context.Context, pr *projectRuntime, initiativeID string, it domain.StaleItem) string {
	switch it.TargetType {
	case "requirement":
		if req, err := pr.db.GetRequirementByRef(ctx, initiativeID, it.TargetID); err == nil {
			return req.Code
		}
	case "task":
		if t, err := pr.db.GetTask(ctx, it.TargetID); err == nil {
			return t.Code
		}
	case "change_set":
		if cs, err := pr.db.GetChangeSet(ctx, it.TargetID); err == nil {
			return cs.HumanID
		}
	}
	return ""
}

// handleAckStale explicitly acknowledges one stale item, clearing it (review
// A4 #5: "explicitly acknowledging the item clears the stale state").
func (s *Server) handleAckStale(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	if err := pr.db.ClearStaleItem(r.Context(), ini.ID, r.PathValue("staleId"), store.HumanActor); err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cleared": true})
}

func (s *Server) handleChanged(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	// M1, review fix: a per-initiative cursor ("ui:<iniID>"), not the single
	// project-wide "ui" cursor. Marking initiative A seen used to advance the
	// one shared cursor past initiative B's own lower, still-unseen sequence
	// numbers, hiding B's changes the next time its own What Changed loaded.
	since := int64(0)
	cursor, err := pr.db.GetReadCursor(r.Context(), seenCursorKey(ini.ID))
	if err == nil {
		since = cursor
	}
	// Scoped to this initiative (C1/JTBD 6) -- see BuildChanged's doc comment.
	// The response shape is unchanged ({groups, through}); web/DecisionPage.tsx
	// still reads this endpoint that way.
	changed, err := projection.BuildChanged(r.Context(), pr.db, ini.ID, since, 200)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, changed)
}

// handleTimeline returns an initiative's human-language "what changed"
// timeline (C1, wave 3 contract): change sets applied/rejected/denied/
// reverted, decisions changed, stale marks, run failures/stops, and completed
// phases -- newest first. `idea changed` uses this, not handleChanged above
// (which stays byte-compatible for the web What Changed widget).
func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	since := int64(0)
	if raw := r.URL.Query().Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be RFC3339")
			return
		}
		since = t.UnixMilli()
	}
	entries, err := projection.BuildTimeline(r.Context(), pr.db, ini.ID, since, 0)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleCosts returns an initiative's spend/time summary (C3, wave 3
// contract): per-phase model spend/time from the run records, and the
// coding-agent run count/duration/spend separately.
func (s *Server) handleCosts(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	costs, err := projection.BuildCosts(r.Context(), pr.db, ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, costs)
}

// handleAttention returns a cross-initiative rollup (phase-derivation inputs
// plus open decision/question/finding counts) for the product home and the
// sidebar inbox in one call, instead of one round trip per initiative per entity.
func (s *Server) handleAttention(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	rollups, err := pr.db.ListInitiativeRollups(r.Context())
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rollups})
}

// handleQueue returns the cross-initiative "needs you" queue (D-b, wave 3
// contract): open blocking decisions, change sets awaiting review, conflicts,
// denials, stale items, and verifications ready to confirm, ranked blocking
// first and then by age.
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	items, err := projection.BuildQueue(r.Context(), pr.db, pr.blobs)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

// seenCursorKey is the read-cursor key for one initiative's What Changed
// "seen" mark (M1, review fix): "ui:<iniID>", never the bare project-wide
// "ui" key handleMarkSeen still writes for a caller that sends no
// initiative_id at all.
func seenCursorKey(initiativeID string) string {
	return "ui:" + initiativeID
}

// handleMarkSeen writes a read cursor to the sequence the client actually
// rendered (echoed from /changed's `through`), so What Changed clears
// exactly what was shown and never events that arrived mid-read.
//
// initiative_id (M1, review fix) scopes the cursor to that one initiative
// ("ui:<iniID>") -- required to fix the bug where marking one initiative
// seen silently advanced a single project-wide cursor past another
// initiative's own lower, still-unseen sequence numbers. Optional and
// backward compatible: a caller that sends none (the project-wide "ui" key)
// keeps behaving exactly as before.
func (s *Server) handleMarkSeen(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		Through      int64  `json:"through"`
		InitiativeID string `json:"initiative_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Through < 0 {
		writeError(w, http.StatusBadRequest, "through must be a non-negative integer")
		return
	}
	maxSeq, err := pr.db.MaxEventSeq(r.Context())
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if body.Through > maxSeq {
		body.Through = maxSeq
	}
	key := "ui"
	if body.InitiativeID != "" {
		key = seenCursorKey(body.InitiativeID)
	}
	if err := pr.db.SetReadCursor(r.Context(), key, body.Through); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"seen_seq": body.Through})
}

func (s *Server) handleResume(w http.ResponseWriter, r *http.Request) {
	_, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	status, err := s.rt.ResumeInitiative(r.Context(), r.PathValue("projectId"), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": string(status)})
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	_, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	status, err := s.rt.ApproveInitiative(r.Context(), r.PathValue("projectId"), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusConflict, err)
		return
	}
	// Still a literal, one frame up in ApproveInitiative -- approve has exactly
	// one success status. What changed is that a refusal is now a 409 instead of
	// this line reporting "approved" over an engine error mapped to 500.
	writeJSON(w, http.StatusOK, map[string]string{"status": string(status)})
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

// handleRetry re-runs a failed initiative's workflow with a chosen model.
func (s *Server) handleRetry(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	initiativeID := r.PathValue("id")
	// `note` (D-a, wave 3 contract) steers this one retry: joins the framing
	// prompt that restarts the pipeline and is recorded on that run. Decoded
	// leniently like `fresh` on /execute (M4, review fix: this used to reject
	// `{"note":"x"}` with 400 "model is required" even though this very
	// comment already promised the lenient decode) -- an old caller that
	// never sends a body at all must keep working unchanged (an absent model
	// falls back to the initiative's configured default, same as before),
	// and a caller that sends only `note` must not have it read as
	// "model": "" and rejected as if it never sent one.
	var body struct {
		Model string `json:"model"`
		Note  string `json:"note"`
	}
	if raw, _ := io.ReadAll(io.LimitReader(r.Body, 4096)); len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	opID, err := s.rt.RetryInitiativeWithNote(r.Context(), projectID, initiativeID, strings.TrimSpace(body.Model), body.Note)
	if err != nil {
		s.writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
}

// handleApprovePlan accepts the execution plan as it stands (human approval gate).
func (s *Server) handleApprovePlan(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	initiativeID := r.PathValue("id")
	opID, err := s.rt.ApprovePlanInitiative(r.Context(), projectID, initiativeID)
	if err != nil {
		s.writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
}

// handleReopenPhase re-runs a design phase and everything downstream (cascade).
func (s *Server) handleReopenPhase(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	initiativeID := r.PathValue("id")
	var body struct {
		Phase string `json:"phase"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	switch body.Phase {
	case "architecture", "product_design", "verification_design":
	default:
		writeError(w, http.StatusBadRequest, "phase must be architecture, product_design, or verification_design")
		return
	}
	opID, err := s.rt.ReopenPhaseInitiative(r.Context(), projectID, initiativeID, body.Phase)
	if err != nil {
		s.writeErr(w, http.StatusConflict, err)
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
		s.writeErr(w, http.StatusConflict, err)
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

// touchedConfigFields returns the harness.yaml field paths a provider write is
// responsible for: the provider entries in the body, plus default_model and the
// per-role model specs when the body sets them. It deliberately excludes
// harness.executor.model — this endpoint cannot write it, so an error there
// belongs to somebody else's edit and is reported rather than rejected.
func touchedConfigFields(defaultModel string, providers map[string]config.ProviderConfig, models map[string]string) map[string]bool {
	touched := map[string]bool{}
	if defaultModel != "" {
		touched["harness.default_model"] = true
	}
	for name := range providers {
		touched["harness.providers."+name] = true
	}
	for role := range models {
		// A cleared role (empty spec) is removed from the candidate and so has no
		// problem to attribute; including it costs nothing and keeps the rule
		// simple: every key in the body is this request's responsibility.
		touched["harness.models."+role] = true
	}
	return touched
}

// problemIsAttributable reports whether a config problem belongs to a field this
// request wrote. Problems carry the harness.yaml path they came from
// ("harness.default_model", "harness.providers.groq",
// "harness.providers.groq.base_url"), so an exact match, or a match on a path
// boundary, attributes a sub-field problem to the entry that was written.
func problemIsAttributable(p config.Problem, touched map[string]bool) bool {
	for field := range touched {
		if p.Field == field || strings.HasPrefix(p.Field, field+".") {
			return true
		}
	}
	return false
}

// sortedProviderNames orders a providers payload so validation is deterministic.
func sortedProviderNames(providers map[string]config.ProviderConfig) []string {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
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
		// GitAllowDefaultBranch/GitBranchPerInitiative/ReviewRigor (wave 3
		// contract): harness.yaml git.allow_default_branch,
		// git.branch_per_initiative and review.rigor, alongside git_commit.
		GitAllowDefaultBranch  *bool   `json:"git_allow_default_branch"`
		GitBranchPerInitiative *bool   `json:"git_branch_per_initiative"`
		ReviewRigor            *string `json:"review_rigor"`
	}
	if err := decodeStrict(r.Body, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Re-read from disk, not from pr.cfg — the daemon caches a project's config
	// for its whole lifetime, so anything hand-edited since it started is
	// invisible here and this handler writes the whole struct back. Editing
	// harness.yaml and then running `idea model set` silently reverted
	// max_cycles, research.urls and git_commit, and reported success at exit 0.
	//
	// handleUpdateExecutors has always done this correctly and says why in the
	// same words. The two handlers sit 1800 lines apart and only one was fixed;
	// that asymmetry shipped in v0.1.0 and survived four audits.
	onDisk, err := config.Load(filepath.Join(pr.rootPath, config.Dir))
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// config.Load returns fresh maps, so these clones are no longer what keeps a
	// rejected write from mutating the running daemon — the disk read already
	// does. Kept as defence, and because the next person to change the source of
	// `onDisk` back to a shared value would otherwise reintroduce that bug
	// silently.
	cfg := onDisk.Harness
	cfg.Providers = maps.Clone(onDisk.Harness.Providers)
	cfg.Models = maps.Clone(onDisk.Harness.Models)
	if body.DefaultModel != "" {
		cfg.DefaultModel = body.DefaultModel
	}
	// A provider entry decides where the API key and the full prompt context are
	// sent, and buildRegistry registers it over the preset default — so it is
	// validated before it is stored, not when it is first used. Sorted so a body
	// with several bad entries always reports the same one.
	if cfg.Providers == nil {
		cfg.Providers = map[string]config.ProviderConfig{}
	}
	for _, name := range sortedProviderNames(body.Providers) {
		pc := body.Providers[name]
		if err := config.ValidateProvider(name, pc); err != nil {
			s.writeErr(w, http.StatusBadRequest, err)
			return
		}
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
	if body.GitAllowDefaultBranch != nil {
		cfg.Git.AllowDefaultBranch = *body.GitAllowDefaultBranch
	}
	if body.GitBranchPerInitiative != nil {
		cfg.Git.BranchPerInitiative = *body.GitBranchPerInitiative
	}
	if body.ReviewRigor != nil {
		if !config.ValidRigor(*body.ReviewRigor) {
			writeError(w, http.StatusBadRequest, "review_rigor must be one of "+strings.Join(config.RigorLevels, "|"))
			return
		}
		cfg.Review.Rigor = *body.ReviewRigor
	}

	// A model spec naming a provider that does not exist is rejected here rather
	// than accepted and discovered at the first run: `idea model set
	// bogus/model` used to answer "✓ default model set to bogus/model", and
	// executor.model is the field whose unresolvable provider used to make the
	// engine scavenge an API key.
	//
	// Scoped to what THIS request writes. Rejecting on any error anywhere in the
	// candidate config locked an operator out of the only command that could fix
	// the config: a harness.yaml carrying an unrelated pre-existing problem (a
	// `models.<role>` naming an unknown provider, say) made every `idea model
	// set` and every UI Settings save fail on a message about a field the request
	// never touched. Pre-existing problems are still reported — `idea config
	// validate`, `idea doctor`, and the daemon log below — they just no longer
	// block an unrelated write.
	candidate := config.Config{Project: pr.cfg.Project, Harness: cfg}
	problems := candidate.ProblemsWithEnv(pr.dotEnv)
	touched := touchedConfigFields(body.DefaultModel, body.Providers, body.Models)
	// Warnings attributable to what this request changed are returned rather
	// than dropped. Everything non-error used to be skipped here, so a catalog
	// mismatch on `idea model set` reached nobody and the command reported plain
	// success -- which is how nine dead model ids shipped in the docs.
	var warnings []string
	for _, p := range problems {
		if p.Severity == config.SeverityWarn && problemIsAttributable(p, touched) {
			warnings = append(warnings, p.Message)
		}
		if p.Severity != config.SeverityError {
			continue
		}
		if problemIsAttributable(p, touched) {
			writeError(w, http.StatusBadRequest, p.Message)
			return
		}
		s.rt.log.Warn("pre-existing config problem left in place by a provider write",
			"project", pr.rootPath, "field", p.Field, "problem", p.Message)
	}

	// Reject inline secrets (§47) and write harness.yaml.
	if err := config.WriteHarness(filepath.Join(pr.rootPath, config.Dir), cfg); err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Reload so the new registry/default model takes effect.
	if _, err := s.rt.ReloadProject(r.Context(), r.PathValue("projectId")); err != nil {
		// writeReloadErr, not writeErr: the config file WAS written, and
		// writeErr's storage branch masks that to "internal error (correlation
		// id)". The executors endpoint has always used it; this one did not, so
		// the same failure told the caller two different things depending on
		// which config they were editing.
		s.writeReloadErr(w, err)
		return
	}
	out := map[string]any{"status": "updated"}
	if len(warnings) > 0 {
		out["warnings"] = warnings
	}
	writeJSON(w, http.StatusOK, out)
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
	models, _ := listModelsCached(r.Context(), pr.registry, provider)
	if models == nil {
		// The documented behaviour for an unreachable provider is "200 with an
		// empty models array" -- and the error is deliberately dropped above to
		// get it. A nil slice marshals as null, which is not an empty array, so
		// the one case the spec calls out was the one it did not produce.
		models = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"provider": provider, "models": models})
}

// ---------- operations ----------

func (s *Server) handleGetOperation(w http.ResponseWriter, r *http.Request) {
	// Operation lives in whichever project's DB; we do not know it from the
	// path, so search the registry (M1: single project common case). Shared with
	// the cancel route, which had the same search inline and its own wording for
	// a miss.
	_, op, err := s.rt.findOperation(r.Context(), r.PathValue("id"))
	if err != nil {
		// Only a genuine miss is a 404. findOperation also surfaces
		// ListProjects failures, and a cancelled request context is neither a
		// driver error nor sql.ErrNoRows, so it would have rendered as
		// `404 context canceled`.
		status := http.StatusInternalServerError
		var nf *store.NotFoundError
		if errors.As(err, &nf) {
			status = http.StatusNotFound
		}
		s.writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, op)
}

// handleCancelOperation interrupts a running operation (M3, §16).
func (s *Server) handleCancelOperation(w http.ResponseWriter, r *http.Request) {
	opID := r.PathValue("id")
	if err := s.rt.CancelOperation(r.Context(), opID); err != nil {
		// 409 is the fallback for a state refusal; writeErr upgrades a
		// *store.NotFoundError to 404 on its own, which is what an unknown id
		// now returns.
		s.writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"operation_id": opID, "status": string(domain.OperationCancelled)})
}

// handleListOperations returns an initiative's operations (most recent first)
// so the UI can surface the running one with elapsed time and a Stop action.
func (s *Server) handleListOperations(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	list, err := pr.db.ListOperationsByInitiative(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// ---------- decisions / questions ----------

func (s *Server) handleListDecisions(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	list, err := pr.db.ListDecisions(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// A revise cycle re-lists a decision it already asked; the UI showed the
	// same question five times while guidance counted one. Collapsed here as
	// in the projection; ?all=true returns every row.
	if r.URL.Query().Get("all") != "true" {
		list = projection.DedupDecisions(list)
	}
	writeJSON(w, http.StatusOK, list)
}

// optionRefRe matches a recommendation that names an option by position
// instead of by text: "Option B", "Choice 2", "option #3", with optional
// surrounding punctuation ("(B)", "2.", "B:"). Group 1 is the letter or
// number. Used by matchRecommendationOption's index-reference path (review
// #2, ICP re-run: the model's recommendation prose never matched an option
// verbatim, so accept-recommendations skipped every decision that had
// options).
var optionRefRe = regexp.MustCompile(`(?i)^\(?(?:option|choice)\b\s*#?\s*([a-z]|[0-9]+)\b\)?\s*[.:)\-–—]?\s*.*$`)

// bareIndexRe matches a recommendation that is NOTHING BUT a letter or number
// reference ("B", "2", "(b)", "3.") -- accepted only when the option-keyword
// form above didn't match, and only as the entire trimmed string, so an
// option whose real text happens to be a single word is never misread as an
// index.
var bareIndexRe = regexp.MustCompile(`(?i)^\(?([a-z]|[0-9]+)\)?[.:]?$`)

// optionIndexFromRef resolves a letter ("a".."z") or 1-based number token to
// a 0-based index into an n-long options list, or (-1, false) if it names
// nothing in range.
func optionIndexFromRef(token string, n int) (int, bool) {
	if len(token) == 1 && unicode.IsLetter(rune(token[0])) {
		idx := int(unicode.ToUpper(rune(token[0])) - 'A')
		if idx >= 0 && idx < n {
			return idx, true
		}
		return -1, false
	}
	num, err := strconv.Atoi(token)
	if err != nil {
		return -1, false
	}
	idx := num - 1 // options are named 1-based ("Option 1") in prose.
	if idx >= 0 && idx < n {
		return idx, true
	}
	return -1, false
}

// normalizeForMatch folds recommendation/option text for a loose comparison:
// lowercased, trimmed, internal whitespace collapsed, and every rune that is
// neither a letter, digit nor space dropped -- so "Use Postgres." and "use
// postgres" (or "use, postgres!") compare equal.
func normalizeForMatch(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastSpace = false
		case unicode.IsSpace(r):
			if !lastSpace {
				b.WriteRune(' ')
				lastSpace = true
			}
		default:
			// Punctuation is dropped, not replaced by a space, so "don't" and
			// "dont" compare equal along with "Use Postgres." and "use postgres".
		}
	}
	return strings.TrimSpace(b.String())
}

// matchRecommendationOption maps a decision's free-text Recommendation onto
// one of its Options for accept-recommendations (review M3, and its ICP
// re-run follow-up review #2): the single-resolve path (handleResolveDecision)
// already refuses, via offers(), to record an answer that is not one of a
// decision's options, and batch-accepting recommendations must not silently
// bypass that guard just because there is no human present to notice.
//
// Tried in order, the first that resolves uniquely wins:
//  1. An exact, verbatim match.
//  2. An "Option B" / "Choice 2" / "(3)" style reference naming an option by
//     position (optionRefRe) -- what the schema now asks the model to prefer.
//  3. A bare letter/number ("B", "2") naming an option by position, when
//     nothing else in the recommendation could itself be prose.
//  4. Case/whitespace/punctuation-normalized equality (the common case: the
//     model's own recommendation text drifts in casing/whitespace/punctuation
//     from the option list it produced in the same response).
//  5. One option containing the other after the same normalization -- the
//     recommendation restates an option with extra framing ("I'd go with
//     using Postgres for this" containing option "Use Postgres"), or an
//     option is itself a prefix of a longer recommendation.
//
// Anything else -- no candidate at any step, or more than one -- is not
// resolved automatically; reason explains why (empty when ok is true).
func matchRecommendationOption(options []string, recommendation string) (matched string, ok bool, reason string) {
	rec := strings.TrimSpace(recommendation)
	if rec == "" {
		return "", false, "no recommendation was given"
	}
	for _, o := range options {
		if o == rec {
			return o, true, ""
		}
	}
	if m := optionRefRe.FindStringSubmatch(rec); m != nil {
		if idx, ok := optionIndexFromRef(m[1], len(options)); ok {
			return options[idx], true, ""
		}
	}
	if m := bareIndexRe.FindStringSubmatch(rec); m != nil {
		if idx, ok := optionIndexFromRef(m[1], len(options)); ok {
			return options[idx], true, ""
		}
	}
	normRec := normalizeForMatch(rec)
	var exact []string
	for _, o := range options {
		if normalizeForMatch(o) == normRec {
			exact = append(exact, o)
		}
	}
	if len(exact) == 1 {
		return exact[0], true, ""
	}
	if len(exact) > 1 {
		return "", false, "the recommendation matches more than one option once case/punctuation is ignored"
	}
	var contained []string
	for _, o := range options {
		no := normalizeForMatch(o)
		if no == "" || normRec == "" {
			continue
		}
		if strings.Contains(normRec, no) || strings.Contains(no, normRec) {
			contained = append(contained, o)
		}
	}
	if len(contained) == 1 {
		return contained[0], true, ""
	}
	if len(contained) > 1 {
		return "", false, "the recommendation is ambiguous between more than one option"
	}
	return "", false, "the recommendation does not match any option"
}

// skippedDecision names one decision handleAcceptRecommendations left open,
// and why -- so a human (or the UI) can tell "no recommendation was given"
// apart from "the recommendation didn't match any option" apart from "this
// was resolved by someone else while the batch ran" (review #2, ICP re-run:
// the endpoint used to return skipped as bare codes with no reason at all).
type skippedDecision struct {
	Code   string `json:"code"`
	Reason string `json:"reason"`
}

// handleAcceptRecommendations resolves every open decision on an initiative
// with its own recommendation, actor human (review B4, adopted #8): the "53
// things need your judgment" problem the ICP round found, answered in one
// call instead of one click per decision. A decision with no recommendation
// (the model raised it without one) is left open and named in `skipped`,
// with a reason.
//
// M3: when a decision has Options, the recommendation must map onto one of
// them (matchRecommendationOption) -- recording it verbatim regardless
// would record a choice the decision never offered, exactly what the
// single-resolve path's offers() guard exists to prevent. And a decision
// that a CONCURRENT single-resolve call answers between this handler's own
// ListDecisions snapshot and its ResolveDecision call is treated as
// skipped, not a 500 that aborts every decision after it in the loop.
func (s *Server) handleAcceptRecommendations(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	decs, err := pr.db.ListDecisions(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	resolved := []string{}
	skipped := []skippedDecision{}
	for _, d := range decs {
		if d.Status != domain.DecisionOpen {
			continue
		}
		didResolve, reason, err := s.acceptOneRecommendation(r.Context(), pr, d)
		if err != nil {
			s.writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if didResolve {
			resolved = append(resolved, d.Code)
		} else {
			skipped = append(skipped, skippedDecision{Code: d.Code, Reason: reason})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"resolved": resolved, "skipped": skipped})
}

// acceptOneRecommendation resolves one decision (already known Open as of
// the caller's snapshot) with its own recommendation, for
// handleAcceptRecommendations (review M3). Returns (true, "", nil) when
// resolved; (false, reason, nil) when skipped -- no recommendation, a
// recommendation that maps to no unique option, or a decision a CONCURRENT
// request already resolved between the caller's snapshot and this call
// (ResolveDecision then fails with "not open"; re-checked here via
// GetDecision rather than treated as a hard failure that would abort the
// rest of the batch) -- and a non-nil error only for a genuine store failure.
func (s *Server) acceptOneRecommendation(ctx context.Context, pr *projectRuntime, d domain.Decision) (bool, string, error) {
	rec := strings.TrimSpace(d.Recommendation)
	if rec == "" {
		return false, "no recommendation was given", nil
	}
	resolution := rec
	if len(d.Options) > 0 {
		matched, ok, reason := matchRecommendationOption(d.Options, rec)
		if !ok {
			return false, reason, nil
		}
		resolution = matched
	}
	if err := pr.db.ResolveDecision(ctx, d.ID, resolution, store.HumanActor); err != nil {
		if cur, gerr := pr.db.GetDecision(ctx, d.ID); gerr == nil && cur.Status != domain.DecisionOpen {
			return false, "resolved by someone else while this request ran", nil
		}
		return false, "", err
	}
	return true, "", nil
}

// handleTriage returns the ranked "what needs your judgment first" view
// (review B4, adopted #8): the top 3 open decisions/questions/blocking
// findings by impact, the rest labelled optional.
func (s *Server) handleTriage(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	ctx := r.Context()
	decs, err := pr.db.ListDecisions(ctx, ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	questions, err := pr.db.ListQuestions(ctx, ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	findings, err := pr.db.ListFindingsByInitiative(ctx, ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Dependents: how many requirements cite each open decision, via the
	// requirement --derived_from--> decision edge runProductDefinition wires.
	// Questions have no such edge today, so they default to 0 dependents
	// (they still rank by blocking/severity -- neither applies to a question,
	// so this only affects tie-breaking among questions themselves).
	dependents := map[string]int{}
	for _, d := range decs {
		if d.Status != domain.DecisionOpen {
			continue
		}
		edges, err := pr.db.ListGraphEdgesTo(ctx, ini.ID, "decision", d.ID)
		if err != nil {
			continue
		}
		n := 0
		for _, e := range edges {
			if e.EdgeType == domain.EdgeDerivedFrom {
				n++
			}
		}
		dependents[d.ID] = n
	}
	items := projection.BuildTriage(decs, questions, findings, dependents)
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
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
	decisionID := r.PathValue("id")
	if err := pr.db.ResolveDecision(r.Context(), decisionID, body.Resolution, store.HumanActor); err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	// The same question asked again in the same initiative is answered by
	// this answer. Otherwise the list, which shows one row per question,
	// brought an older open copy back the moment the newest was answered, and
	// the open-decision count kept every copy.
	if d, err := pr.db.GetDecision(r.Context(), decisionID); err == nil {
		if all, err := pr.db.ListDecisions(r.Context(), d.InitiativeID); err == nil {
			for _, other := range all {
				// Same type too, and only an answer that decision offers: the
				// question match ignores leading tags, and copying an answer
				// that is not one of a decision's options would record a
				// choice nobody was offered.
				if other.ID != d.ID && other.Status == domain.DecisionOpen && other.Type == d.Type &&
					projection.SameQuestion(other.Question, d.Question) && offers(other.Options, body.Resolution) {
					_ = pr.db.ResolveDecision(r.Context(), other.ID, body.Resolution, store.HumanActor)
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "decided"})
}

// handleReopenDecision undoes a resolved decision, returning it to open.
func (s *Server) handleReopenDecision(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	if err := pr.db.ReopenDecision(r.Context(), r.PathValue("id"), store.HumanActor); err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
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
	// ListDecisionDiscussions is a plain SELECT over child rows, so an unknown
	// parent is indistinguishable from an empty thread: this answered `200 []`
	// for a decision that does not exist, and the spec has declared a 404 it
	// could never produce. Every other id-taking read 404s.
	if _, err := pr.db.GetDecision(r.Context(), r.PathValue("id")); err != nil {
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
	pr, ini, err := s.initiative(r)
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
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resolved"})
}

// ---------- requirements / assertions / artifacts ----------

func (s *Server) handleListRequirements(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	list, err := pr.db.ListRequirements(r.Context(), ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// evidence_tests (C2, wave 3 contract): computed at read time, never
	// persisted, so it is always as fresh as the last check run.
	evidence, err := projection.ComputeEvidenceTests(r.Context(), pr.db, pr.blobs, ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for i := range list {
		list[i].EvidenceTests = evidence[list[i].ID]
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleListAssertions(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
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
	if err := pr.db.AttachTaskEdges(r.Context(), ini.ID, list); err != nil {
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
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	// M5: serializes the guard check through every write below against every
	// other Plan-editing endpoint on this same initiative.
	defer pr.lockPlanEdit(ini.ID)()
	if err := s.taskEditableGuard(r.Context(), pr, ini, nil); err != nil {
		s.writeErr(w, http.StatusConflict, err)
		return
	}
	var body struct {
		Title          string   `json:"title"`
		Statement      string   `json:"statement"`
		Priority       string   `json:"priority"`
		Estimate       string   `json:"estimate"`
		Implements     string   `json:"implements"`
		RequirementIDs []string `json:"requirement_ids"`
		DependsOn      []string `json:"depends_on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Title) == "" {
		writeError(w, http.StatusBadRequest, "title is required")
		return
	}

	// Resolve the requirement reference(s): `implements` (single, kept for
	// compatibility) plus `requirements_ids` (N:M, review A4 #3/#4). A task
	// added after the plan is approved must name at least one -- unlike the
	// planner's own unresolved-reference finding, this is refused synchronously
	// rather than parked, since there is no plan-review cycle to catch it here.
	var reqRefs []string
	if body.Implements != "" {
		reqRefs = append(reqRefs, body.Implements)
	}
	reqRefs = append(reqRefs, body.RequirementIDs...)
	if len(reqRefs) == 0 {
		writeError(w, http.StatusBadRequest, "requirement_ids is required (at least one)")
		return
	}
	implementsIDs, err := s.resolveRequirementRefs(r.Context(), pr, ini.ID, reqRefs)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}

	// Resolve depends_on titles to existing task ids.
	depIDs, err := s.resolveTaskRefs(r.Context(), pr, ini.ID, body.DependsOn)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
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
	if err := pr.db.CreateTaskMulti(r.Context(), task, implementsIDs, depIDs); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	task.RequirementIDs, task.DependsOn = nonNilStrings(implementsIDs), nonNilStrings(depIDs)
	writeJSON(w, http.StatusOK, task)
}

// nonNilStrings returns s, or an empty (non-nil) slice when s is nil, so a
// Task's requirement_ids/depends_on always serialize as `[]`, never `null`.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// resolveRequirementRefs resolves each ref to a Requirement.ID, matching by
// exact id or by NormalizeRequirementRef'd code (review A4 #4: trim,
// case-insensitive, "REQ-3: title" forms). Duplicates are dropped; any ref
// that resolves to nothing is a 400 naming it, mirroring `depends_on`'s own
// unknown-reference refusal.
func (s *Server) resolveRequirementRefs(ctx context.Context, pr *projectRuntime, initiativeID string, refs []string) ([]string, error) {
	reqs, err := pr.db.ListRequirements(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	byCode := make(map[string]domain.Requirement, len(reqs))
	byID := make(map[string]domain.Requirement, len(reqs))
	for _, req := range reqs {
		byCode[workflow.NormalizeRequirementRef(req.Code)] = req
		byID[req.ID] = req
	}
	var out []string
	seen := map[string]bool{}
	for _, ref := range refs {
		req, ok := byID[ref]
		if !ok {
			req, ok = byCode[workflow.NormalizeRequirementRef(ref)]
		}
		if !ok {
			return nil, fmt.Errorf("requirement_ids references an unknown requirement %q", ref)
		}
		// review low-priority item: a superseded requirement is refused, not
		// silently wired to a task as if it were still live.
		//
		// Requirement.SupersededBy is provenance (a run label like
		// "human-revise" or "retry" -- see store.SupersedeRequirements), not a
		// pointer to whatever requirement replaced it: there is no tracked 1:1
		// successor link. The closest available signal is an active
		// requirement with the IDENTICAL statement text, which is what a
		// revise that regenerates the same requirement produces in practice;
		// named when found, so the caller is not left to search for it by
		// hand, but this is a heuristic, not a guarantee -- reworded
		// requirements have no traceable successor at all.
		if req.SupersededBy != "" {
			if live := liveRequirementWithSameStatement(reqs, req); live != "" {
				return nil, fmt.Errorf("requirement_ids references %q, which is superseded; %s appears to be its live continuation (same statement); use that instead", ref, live)
			}
			return nil, fmt.Errorf("requirement_ids references %q, which is superseded and has no active continuation", ref)
		}
		if !seen[req.ID] {
			seen[req.ID] = true
			out = append(out, req.ID)
		}
	}
	return out, nil
}

// liveRequirementWithSameStatement returns the Code of an active requirement
// (not removed, not itself superseded) among all whose Statement exactly
// matches superseded's -- the closest available signal to "what replaced
// this", given SupersededBy carries no successor id (see the caller). Returns
// "" when no such requirement exists.
func liveRequirementWithSameStatement(all []domain.Requirement, superseded domain.Requirement) string {
	for _, r := range all {
		if r.ID == superseded.ID || r.Status == domain.RequirementRemoved || r.SupersededBy != "" {
			continue
		}
		if r.Statement == superseded.Statement {
			return r.Code
		}
	}
	return ""
}

func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	// M5: serializes the guard check through every write below against every
	// other Plan-editing endpoint on this same initiative.
	defer pr.lockPlanEdit(ini.ID)()
	task, err := pr.db.GetTask(r.Context(), r.PathValue("taskId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err := s.taskEditableGuard(r.Context(), pr, ini, task); err != nil {
		s.writeErr(w, http.StatusConflict, err)
		return
	}
	var body struct {
		Title          string   `json:"title"`
		Statement      string   `json:"statement"`
		Priority       string   `json:"priority"`
		Estimate       string   `json:"estimate"`
		RequirementIDs []string `json:"requirement_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	// Everything here is validated (and, for requirement_ids, resolved to real
	// ids) BEFORE either write below runs (review low-priority item): this
	// handler used to replace the task's requirement edges FIRST and only
	// validate priority afterward, so a bad `priority` value in the same
	// request returned 400 after the edges were already replaced -- a
	// half-applied PATCH the client's 400 gave no reason to suspect.
	//
	// requirement_ids is optional on PATCH: omitted (nil) leaves the task's
	// existing requirement links untouched. Present, it replaces them
	// entirely, and (review A4 #3) must name at least one -- clearing a task's
	// last requirement is refused the same way adding one with none is.
	var implementsIDs []string
	replaceRequirements := body.RequirementIDs != nil
	if replaceRequirements {
		if len(body.RequirementIDs) == 0 {
			writeError(w, http.StatusBadRequest, "requirement_ids is required (at least one)")
			return
		}
		implementsIDs, err = s.resolveRequirementRefs(r.Context(), pr, ini.ID, body.RequirementIDs)
		if err != nil {
			s.writeErr(w, http.StatusBadRequest, err)
			return
		}
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

	// Writes: only now, after every field above validated cleanly.
	if replaceRequirements {
		if err := pr.db.ReplaceTaskRequirements(r.Context(), ini.ID, task.ID, implementsIDs); err != nil {
			s.writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := pr.db.UpdateTask(r.Context(), ini.ID, task.ID, title, statement, priority, body.Estimate); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Files a stopped run wrote for the old wording must not be carried into
	// the new one; the task id alone does not tell them apart.
	if title != task.Title || statement != task.Statement {
		if pw := pr.engine.PartialWork(ini.ID); pw != nil && pw.TaskID == task.ID {
			_ = pr.engine.DiscardPartial(ini.ID)
		}
	}
	updated, _ := pr.db.GetTask(r.Context(), task.ID)
	if updated != nil {
		reqs, deps, _ := pr.db.TaskEdges(r.Context(), ini.ID, updated.ID)
		updated.RequirementIDs, updated.DependsOn = nonNilStrings(reqs), nonNilStrings(deps)
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteTask(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	// M5: serializes the guard check through every write below against every
	// other Plan-editing endpoint on this same initiative.
	defer pr.lockPlanEdit(ini.ID)()
	task, err := pr.db.GetTask(r.Context(), r.PathValue("taskId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err := s.taskEditableGuard(r.Context(), pr, ini, task); err != nil {
		s.writeErr(w, http.StatusConflict, err)
		return
	}
	// Refuse if any other task depends on this one, naming which ones (review
	// B3: the message was raw `{"error":"another task depends on this
	// task…"}` -- true, and useless, since it never said which task or what
	// to do about it).
	if dependents, err := s.taskDependents(r.Context(), pr, ini.ID, task.ID); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	} else if len(dependents) > 0 {
		writeError(w, http.StatusConflict, fmt.Sprintf(
			"%s cannot be deleted: %s depend on it. Remove those dependencies first, or delete them too.",
			task.Code, strings.Join(dependents, ", ")))
		return
	}
	if err := pr.db.DeleteTask(r.Context(), ini.ID, task.ID); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleCancelTask(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	// M5: serializes the guard check through every write below against every
	// other Plan-editing endpoint on this same initiative.
	defer pr.lockPlanEdit(ini.ID)()
	task, err := pr.db.GetTask(r.Context(), r.PathValue("taskId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err := s.taskEditableGuard(r.Context(), pr, ini, task); err != nil {
		s.writeErr(w, http.StatusConflict, err)
		return
	}
	if err := pr.db.SetTaskStatus(r.Context(), task.ID, domain.TaskCancelled); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": string(domain.TaskCancelled)})
}

func (s *Server) handleReorderTasks(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	// M5: serializes the guard check through every write below against every
	// other Plan-editing endpoint on this same initiative.
	defer pr.lockPlanEdit(ini.ID)()
	if err := s.taskEditableGuard(r.Context(), pr, ini, nil); err != nil {
		s.writeErr(w, http.StatusConflict, err)
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
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	list, _ := pr.db.ListTasks(r.Context(), ini.ID)
	writeJSON(w, http.StatusOK, list)
}

// handleSetTaskDependsOn replaces a task's depends_on wholesale (review B3:
// "set a task's depends_on (validate for cycles)"), refusing a set that would
// create a dependency cycle. Unlike PATCH (which leaves depends_on untouched
// when omitted), this endpoint's whole purpose is depends_on, so an empty
// list is a legitimate request -- "make this task depend on nothing".
func (s *Server) handleSetTaskDependsOn(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	// M5: serializes the guard check, wouldCycle, and the write against
	// every other Plan-editing endpoint on this same initiative -- two
	// concurrent set-depends_on requests (A->B and B->A) each used to pass
	// wouldCycle against the graph as it stood before either write landed,
	// and together create a cycle.
	defer pr.lockPlanEdit(ini.ID)()
	task, err := pr.db.GetTask(r.Context(), r.PathValue("taskId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err := s.taskEditableGuard(r.Context(), pr, ini, task); err != nil {
		s.writeErr(w, http.StatusConflict, err)
		return
	}
	// DependsOn is a pointer (review low-priority item): {} or a misspelled
	// field name decodes to a nil field, which must be refused rather than
	// silently treated as depends_on: [] and clearing every existing
	// dependency. An explicitly empty array, {"depends_on": []}, decodes to
	// a non-nil pointer to an empty slice and is still the legitimate "make
	// this task depend on nothing" request the doc comment above describes.
	var body struct {
		DependsOn *[]string `json:"depends_on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if body.DependsOn == nil {
		writeError(w, http.StatusBadRequest, "depends_on is required (use [] to clear all dependencies)")
		return
	}
	depIDs, err := s.resolveTaskRefs(r.Context(), pr, ini.ID, *body.DependsOn)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	for _, id := range depIDs {
		if id == task.ID {
			writeError(w, http.StatusBadRequest, "a task cannot depend on itself")
			return
		}
	}
	if cycle, err := s.wouldCycle(r.Context(), pr, ini.ID, task.ID, depIDs); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	} else if cycle != "" {
		writeError(w, http.StatusBadRequest, "that dependency would create a cycle: "+cycle)
		return
	}
	// M5: SetTaskDependencies re-checks for a cycle INSIDE the same
	// transaction as the write, defense-in-depth beyond the pre-check above
	// and lockPlanEdit's serialization (for any path to this write that
	// isn't the pre-check's own wouldCycle call).
	if err := pr.db.SetTaskDependencies(r.Context(), ini.ID, task.ID, depIDs); err != nil {
		if errors.Is(err, store.ErrDependsOnCycle) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	updated, _ := pr.db.GetTask(r.Context(), task.ID)
	if updated != nil {
		reqs, deps, _ := pr.db.TaskEdges(r.Context(), ini.ID, updated.ID)
		updated.RequirementIDs, updated.DependsOn = nonNilStrings(reqs), nonNilStrings(deps)
	}
	writeJSON(w, http.StatusOK, updated)
}

// wouldCycle reports whether replacing taskID's depends_on with newDeps would
// introduce a dependency cycle anywhere in the initiative's task graph. It
// builds the FULL graph (every task's current depends_on) with taskID's edges
// swapped for newDeps, so a cycle introduced through some OTHER task is caught
// too, not just a direct A-depends-on-A/B-depends-on-A-depends-on-B case.
func (s *Server) wouldCycle(ctx context.Context, pr *projectRuntime, initiativeID, taskID string, newDeps []string) (string, error) {
	tasks, err := pr.db.ListTasks(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	deps := map[string][]string{}
	for _, t := range tasks {
		if t.ID == taskID {
			deps[t.ID] = newDeps
			continue
		}
		edges, err := pr.db.ListGraphEdgesFrom(ctx, initiativeID, "task", t.ID)
		if err != nil {
			return "", err
		}
		for _, e := range edges {
			if e.EdgeType == domain.EdgeDependsOn {
				deps[t.ID] = append(deps[t.ID], e.ToID)
			}
		}
	}
	return store.DetectTaskCycle(deps), nil
}

// handleMergeTasks merges two tasks into one (review B3): the requirements of
// both are unioned onto the surviving (first-named) task, their statements
// are concatenated, every dependency edge either task held is rewired onto
// the survivor, and the second task is deleted.
func (s *Server) handleMergeTasks(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	// M5: serializes the guard checks and MergeTasks's own cycle check
	// against every other Plan-editing endpoint on this same initiative.
	defer pr.lockPlanEdit(ini.ID)()
	var body struct {
		TaskIDs []string `json:"task_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.TaskIDs) != 2 {
		writeError(w, http.StatusBadRequest, "task_ids must name exactly 2 tasks")
		return
	}
	// H4: refuse a self-merge before even looking the tasks up -- task_ids:
	// [A, A] used to pass both existence guards and then DeleteTask(A)
	// deleted the survivor, leaving a null response.
	if body.TaskIDs[0] == body.TaskIDs[1] {
		writeError(w, http.StatusBadRequest, "task_ids must name two distinct tasks")
		return
	}
	keep, err := pr.db.GetTask(r.Context(), body.TaskIDs[0])
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found: "+body.TaskIDs[0])
		return
	}
	merge, err := pr.db.GetTask(r.Context(), body.TaskIDs[1])
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found: "+body.TaskIDs[1])
		return
	}
	if err := s.taskEditableGuard(r.Context(), pr, ini, keep); err != nil {
		s.writeErr(w, http.StatusConflict, err)
		return
	}
	if err := s.taskEditableGuard(r.Context(), pr, ini, merge); err != nil {
		s.writeErr(w, http.StatusConflict, err)
		return
	}

	// H4: the union/concat/rewire/delete below is now MergeTasks, ONE store
	// transaction that also runs the cycle check against the FINAL graph
	// before writing anything (a failure partway, or a concurrent request,
	// used to leave a half-merged plan; there was also no cycle check at
	// all, so A->B->C plus merging (A, C) could silently create A->B->A).
	updated, err := pr.db.MergeTasks(r.Context(), ini.ID, keep.ID, merge.ID)
	if err != nil {
		switch {
		case errors.Is(err, store.ErrSelfMerge):
			writeError(w, http.StatusBadRequest, err.Error())
		case errors.Is(err, store.ErrMergeCycle):
			writeError(w, http.StatusBadRequest, err.Error())
		default:
			s.writeErr(w, http.StatusInternalServerError, err)
		}
		return
	}
	if updated != nil {
		reqs, deps, _ := pr.db.TaskEdges(r.Context(), ini.ID, updated.ID)
		updated.RequirementIDs, updated.DependsOn = nonNilStrings(reqs), nonNilStrings(deps)
	}
	writeJSON(w, http.StatusOK, updated)
}

// taskRequirementIDs returns the union of the requirement ids two tasks each
// implement (`implements` edges), for a merge (review B3).
func (s *Server) taskRequirementIDs(ctx context.Context, pr *projectRuntime, initiativeID string, taskIDs ...string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, id := range taskIDs {
		edges, err := pr.db.ListGraphEdgesFrom(ctx, initiativeID, "task", id)
		if err != nil {
			return nil, err
		}
		for _, e := range edges {
			if e.EdgeType == domain.EdgeImplements && !seen[e.ToID] {
				seen[e.ToID] = true
				out = append(out, e.ToID)
			}
		}
	}
	return out, nil
}

// unionTaskDeps returns the union of two tasks' own outgoing depends_on
// (each other and self-references dropped), for a merge (review B3).
func (s *Server) unionTaskDeps(ctx context.Context, pr *projectRuntime, initiativeID string, taskIDs ...string) ([]string, error) {
	exclude := map[string]bool{}
	for _, id := range taskIDs {
		exclude[id] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range taskIDs {
		edges, err := pr.db.ListGraphEdgesFrom(ctx, initiativeID, "task", id)
		if err != nil {
			return nil, err
		}
		for _, e := range edges {
			if e.EdgeType == domain.EdgeDependsOn && !exclude[e.ToID] && !seen[e.ToID] {
				seen[e.ToID] = true
				out = append(out, e.ToID)
			}
		}
	}
	return out, nil
}

// handleSplitTask splits one task into two or more (review B3). Each new
// task's requirement_ids defaults to the original task's own requirements
// when omitted. Every new task inherits the original's outgoing depends_on,
// and anything that depended on the original now depends on ALL the new
// tasks (a safe superset -- narrowing it is a follow-up `set depends_on`).
// The original task is then deleted.
func (s *Server) handleSplitTask(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	// M5: serializes the guard check through every write below against every
	// other Plan-editing endpoint on this same initiative.
	defer pr.lockPlanEdit(ini.ID)()
	task, err := pr.db.GetTask(r.Context(), r.PathValue("taskId"))
	if err != nil {
		writeError(w, http.StatusNotFound, "task not found")
		return
	}
	if err := s.taskEditableGuard(r.Context(), pr, ini, task); err != nil {
		s.writeErr(w, http.StatusConflict, err)
		return
	}
	var body struct {
		Tasks []struct {
			Title          string   `json:"title"`
			Statement      string   `json:"statement"`
			RequirementIDs []string `json:"requirement_ids"`
		} `json:"tasks"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Tasks) < 2 {
		writeError(w, http.StatusBadRequest, "tasks must name at least 2 replacement tasks")
		return
	}

	ctx := r.Context()
	originalReqs, err := s.taskRequirementIDs(ctx, pr, ini.ID, task.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	originalDeps, err := s.unionTaskDeps(ctx, pr, ini.ID, task.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// M4: validate and resolve EVERY replacement task before any write. The
	// old handler validated a spec's title and resolved its
	// requirement_ids inside the same loop that created tasks one at a
	// time, so an invalid second (or third...) spec returned 400 after the
	// first replacement was already created and the original not yet
	// deleted -- a half-applied split, the same bug this branch had already
	// fixed for PATCH.
	specs := make([]store.SplitTaskSpec, 0, len(body.Tasks))
	for i, spec := range body.Tasks {
		title := strings.TrimSpace(spec.Title)
		if title == "" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("replacement task %d needs a title", i+1))
			return
		}
		reqIDs := originalReqs
		if spec.RequirementIDs != nil {
			reqIDs, err = s.resolveRequirementRefs(ctx, pr, ini.ID, spec.RequirementIDs)
			if err != nil {
				s.writeErr(w, http.StatusBadRequest, err)
				return
			}
		}
		// M4: requirement_ids: [] (explicitly empty, non-nil) must not
		// silently produce a task with no requirement -- "Added tasks take
		// a requirement" -- and neither may originalReqs being empty (the
		// original itself had none).
		if len(reqIDs) == 0 {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("replacement task %d (%q) needs at least one requirement", i+1, title))
			return
		}
		specs = append(specs, store.SplitTaskSpec{
			Title:          pr.redactor.RedactString(title),
			Statement:      pr.redactor.RedactString(spec.Statement),
			Priority:       task.Priority,
			RequirementIDs: reqIDs,
		})
	}

	// M4: every replacement's creation, the dependent rewiring, and the
	// original's deletion now run inside SplitTask's one transaction.
	created, err := pr.db.SplitTask(ctx, ini.ID, task.ID, specs, originalDeps)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	for _, nt := range created {
		reqs, deps, err := pr.db.TaskEdges(ctx, ini.ID, nt.ID)
		if err != nil {
			s.writeErr(w, http.StatusInternalServerError, err)
			return
		}
		nt.RequirementIDs, nt.DependsOn = nonNilStrings(reqs), nonNilStrings(deps)
	}
	writeJSON(w, http.StatusOK, created)
}

// handleGenerateVerificationTask creates a plan task whose purpose is verifying
// a requirement (wired with a `verifies` edge), for a requirement that has no
// human or automated evidence yet.
func (s *Server) handleGenerateVerificationTask(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	req, err := pr.db.GetRequirementByRef(r.Context(), ini.ID, r.PathValue("reqId"))
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	task := &domain.Task{
		InitiativeID: ini.ID,
		Title:        "Verify " + req.Code,
		Statement:    verificationStatement(req),
		Priority:     req.Priority,
	}
	if err := pr.db.CreateVerificationTask(r.Context(), task, req.ID); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

// verificationStatement renders a requirement's acceptance criteria as a short
// task statement for a generated verification task.
func verificationStatement(r *domain.Requirement) string {
	var b strings.Builder
	for _, ac := range r.AcceptanceCriteria {
		fmt.Fprintf(&b, "- %s %s (%s)\n", ac.Metric, ac.Target, ac.Condition)
	}
	return strings.TrimSpace(b.String())
}

// taskDependents returns the human-readable codes of tasks whose depends_on
// names taskID, for a readable delete refusal (review B3).
func (s *Server) taskDependents(ctx context.Context, pr *projectRuntime, initiativeID, taskID string) ([]string, error) {
	edges, err := pr.db.ListGraphEdgesTo(ctx, initiativeID, "task", taskID)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range edges {
		if e.EdgeType != domain.EdgeDependsOn {
			continue
		}
		if t, err := pr.db.GetTask(ctx, e.FromID); err == nil {
			out = append(out, t.Code)
		} else {
			out = append(out, e.FromID)
		}
	}
	return out, nil
}

// resolveTaskRefs resolves task references (title, code, or id) to task ids,
// as documented (docs/api.md's depends-on entry: "titles, codes, or ids").
// Matched by exact id FIRST -- an id is never run through the
// title/whitespace-folding normalization below, so a task whose id happens to
// collide with another task's normalized title/code (astronomically unlikely,
// but exact-id is unambiguous regardless) always resolves to itself -- then
// falls back to a normalized title/code match. Before this, a task's own id
// was never in byNorm at all: depends_on documented "titles, codes, or ids"
// but only the first two worked, so `idea task depends-on <task-id>` (or any
// caller passing ids, the least ambiguous ref a client could use) failed with
// "unknown task" for every id (review #3, ICP re-run).
func (s *Server) resolveTaskRefs(ctx context.Context, pr *projectRuntime, initiativeID string, refs []string) ([]string, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	tasks, err := pr.db.ListTasks(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	byID := make(map[string]string, len(tasks))
	byNorm := map[string]string{}
	for _, t := range tasks {
		byID[t.ID] = t.ID
		byNorm[norm(t.Title)] = t.ID
		byNorm[norm(t.Code)] = t.ID
	}
	var out []string
	for _, ref := range refs {
		id, ok := byID[ref]
		if !ok {
			id, ok = byNorm[norm(ref)]
		}
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
	if objType != "" && !workflow.ValidNodeType(objType) {
		// An unknown type used to traverse nothing and answer 200 with an empty
		// result, so `idea why requirements REQ-1` -- one character wrong --
		// reported "No provenance edges found." and exited 0. A bad id was
		// caught; a bad type turned a provenance question into a confident
		// false negative.
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown node type %q; use one of: %s",
			objType, strings.Join(workflow.GraphNodeTypes, ", ")))
		return
	}
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

// maxImpactBatchNodes caps the number of nodes a single impact-counts request
// may ask about, bounding worst-case load (each node is an independent BFS).
const maxImpactBatchNodes = 200

// handleImpactCounts returns "blocks N" counts for a batch of graph nodes,
// addressed as comma-separated `type:id` (e.g. decision:D-004,finding:F-012), so
// the UI can render impact counts without one Impact call per node.
func (s *Server) handleImpactCounts(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	raw := r.URL.Query().Get("nodes")
	if raw == "" {
		writeError(w, http.StatusBadRequest, "nodes query param is required (comma-separated type:id)")
		return
	}
	var nodes []workflow.NodeRef
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		typ, id, ok := strings.Cut(part, ":")
		if !ok || typ == "" || id == "" {
			writeError(w, http.StatusBadRequest, "each node must be type:id")
			return
		}
		nodes = append(nodes, workflow.NodeRef{Type: typ, ID: id})
	}
	if len(nodes) == 0 {
		writeError(w, http.StatusBadRequest, "nodes query param is required")
		return
	}
	if len(nodes) > maxImpactBatchNodes {
		writeError(w, http.StatusBadRequest, "too many nodes in one impact-counts request")
		return
	}
	counts, err := pr.engine.ImpactCounts(r.Context(), ini.ID, nodes)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts})
}

func (s *Server) handleWhy(w http.ResponseWriter, r *http.Request) { s.handleTraceObject(w, r, "why") }
func (s *Server) handleImpact(w http.ResponseWriter, r *http.Request) {
	s.handleTraceObject(w, r, "impact")
}
func (s *Server) handleTrace(w http.ResponseWriter, r *http.Request) {
	s.handleTraceObject(w, r, "trace")
}

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
	pr, ini, err := s.initiative(r)
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
	pr, ini, err := s.initiative(r)
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

// handleArtifactDiff returns a line diff between two artifact versions of the
// same artifact family, addressed by version id (from → to).
func (s *Server) handleArtifactDiff(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	fromID := r.URL.Query().Get("from")
	toID := r.URL.Query().Get("to")
	if fromID == "" || toID == "" {
		writeError(w, http.StatusBadRequest, "from and to version ids are required")
		return
	}
	from, err := pr.db.GetArtifactVersion(r.Context(), fromID)
	if err != nil {
		writeError(w, http.StatusNotFound, "from version not found")
		return
	}
	to, err := pr.db.GetArtifactVersion(r.Context(), toID)
	if err != nil {
		writeError(w, http.StatusNotFound, "to version not found")
		return
	}
	if from.ArtifactID != to.ArtifactID {
		writeError(w, http.StatusBadRequest, "versions belong to different artifacts")
		return
	}
	old, err := pr.blobs.Get(from.BodyDigest)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "from body missing")
		return
	}
	newBody, err := pr.blobs.Get(to.BodyDigest)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "to body missing")
		return
	}
	// Guard the O(lines²) diff table against pathological inputs. Artifact bodies
	// are model-output-bounded, but the endpoint still refuses absurd sizes.
	if len(old) > maxArtifactDiffBytes || len(newBody) > maxArtifactDiffBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "artifact too large to diff")
		return
	}
	if strings.Count(string(old), "\n") > maxArtifactDiffLines || strings.Count(string(newBody), "\n") > maxArtifactDiffLines {
		writeError(w, http.StatusRequestEntityTooLarge, "artifact has too many lines to diff")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"kind":         to.Kind,
		"from_version": from.Version,
		"to_version":   to.Version,
		"diff":         textdiff.Unified(string(old), string(newBody)),
	})
}

const (
	// maxArtifactDiffBytes caps a single artifact body considered for diffing.
	maxArtifactDiffBytes = 1 << 20 // 1 MiB
	// maxArtifactDiffLines caps the line count fed to the O(lines²) LCS diff.
	maxArtifactDiffLines = 20000
)

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

// handleListDocComments returns the comments on an artifact version.
func (s *Server) handleListDocComments(w http.ResponseWriter, r *http.Request) {
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
	list, err := pr.db.ListDocComments(r.Context(), av.VersionID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// handleCreateDocComment adds a human comment to an artifact version section.
func (s *Server) handleCreateDocComment(w http.ResponseWriter, r *http.Request) {
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
	var body struct {
		Section string `json:"section"`
		Text    string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Text) == "" {
		writeError(w, http.StatusBadRequest, "text is required")
		return
	}
	c := &domain.DocComment{
		InitiativeID:      av.InitiativeID,
		ArtifactVersionID: av.VersionID,
		Section:           pr.redactor.RedactString(strings.TrimSpace(body.Section)),
		Text:              pr.redactor.RedactString(strings.TrimSpace(body.Text)),
	}
	if err := pr.db.CreateDocComment(r.Context(), c); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

// handlePromoteDocComment promotes a comment into an open Question.
func (s *Server) handlePromoteDocComment(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	q, err := pr.engine.PromoteDocComment(r.Context(), r.PathValue("id"))
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, q)
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
	pr, ini, err := s.initiative(r)
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
	findingID := r.PathValue("id")
	if err := pr.db.SetFindingDisposition(r.Context(), findingID, disp, store.HumanActor); err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	resp := map[string]string{"status": "dispositioned"}
	// rejected resolves a reviewer's own judgment call (Code == "") the same
	// as fixed/accepted_risk (Finding.ResolvesBlocking, ICP re-run defect
	// #2), but a finding carrying a system deny code still blocks
	// afterward -- "system deny beats human approve" -- so a 200 here would
	// otherwise say nothing about that (the ICP re-run's own reported
	// "returns 200 with no warning"). Best-effort: a failed re-read must not
	// turn a successful disposition into an error response.
	if disp == domain.DispositionRejected {
		if f, err := pr.db.GetFinding(r.Context(), findingID); err == nil && f.Code != "" {
			resp["warning"] = fmt.Sprintf(
				"this finding carries system deny code %q; rejected does not clear it -- only fixing the underlying problem does",
				f.Code)
		}
	}
	writeJSON(w, http.StatusOK, resp)
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
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "noted"})
}

// handleDoctor audits the initiative's product-graph edges and returns any
// traceability gaps (untraced tasks/objects, dependency cycles).
func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
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
	// Optional body: {"fresh": true} is "Start over". An empty body is the
	// ordinary run, which continues a stopped run's kept files. Decoded
	// leniently on purpose: this endpoint has always ignored its body, and
	// refusing one it used to accept would be a breaking change.
	//
	// `note` (D-a, wave 3 contract) steers this one rerun: appended to the
	// implementer's brief and recorded on the run. Decoded just as leniently
	// as `fresh` -- an old CLI/UI that never sends it must keep working
	// unchanged.
	var body struct {
		Fresh bool   `json:"fresh"`
		Note  string `json:"note"`
	}
	if raw, _ := io.ReadAll(io.LimitReader(r.Body, 4096)); len(bytes.TrimSpace(raw)) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	opID, err := s.rt.ExecuteInitiativeFreshWithNote(r.Context(), projectID, initiativeID, body.Fresh, body.Note)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"operation_id":  opID,
		"executor_kind": string(pr.cfg.Harness.Executor.Kind),
	})
}

// handleVerify runs the verification gate and, when a requirement + disposition
// is supplied, records a human judgment (pass/fail/waived) first (§70, review
// B4/B10). `pass` is retained for backward compatibility; `fail` and `waived`
// are the new dispositions.
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	initiativeID := r.PathValue("id")
	pr, err := s.rt.EnsureProject(r.Context(), projectID)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	// The initiative, not just the project. Without this, a bogus initiative id
	// with a plausible requirement name answered `no requirement matching
	// "REQ-1"` -- a 404 about a requirement of an initiative that does not
	// exist. Every sibling route resolves the initiative first.
	if _, err := pr.db.GetInitiative(r.Context(), initiativeID); err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		Requirement string `json:"requirement"`
		Pass        bool   `json:"pass"`
		Fail        bool   `json:"fail"`
		Waived      bool   `json:"waived"`
		Note        string `json:"note"`
		// ConfirmPassingTests (C2, wire contract) confirms every criterion
		// whose mapped tests all passed, via the same human-evidence path,
		// actor human. Never inferred -- only this explicit request does it.
		ConfirmPassingTests bool `json:"confirm_passing_tests"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var disposition domain.VerificationStatus
	switch {
	case body.Fail:
		disposition = domain.VerificationFail
	case body.Waived:
		disposition = domain.VerificationWaived
	case body.Pass:
		disposition = domain.VerificationPass
	}
	// A requirement with no disposition used to be dropped on the floor: the CLI
	// sent {} and this handler never saw the name, so `idea verify REQ-99`
	// reported the whole-initiative gate and never mentioned REQ-99. The name is
	// resolved inside RunVerificationGate, alongside the state check, so the two
	// refusals have one precedence rather than depending on which field was set.
	status, coverage, confirmResult, err := s.rt.RunVerificationGateConfirmingTests(
		r.Context(), projectID, initiativeID, body.Requirement, disposition, body.Note, body.ConfirmPassingTests)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	resp := map[string]any{"status": status, "coverage": coverage}
	// confirmed/skipped (C3/H2, wire contract addition) are present only when
	// confirm_passing_tests was requested -- an ordinary pass/fail/waive call
	// never touches them, so its response shape is unchanged.
	if confirmResult != nil {
		resp["confirmed"] = confirmResult.Confirmed
		resp["skipped"] = confirmResult.Skipped
	}
	writeJSON(w, http.StatusOK, resp)
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
	resp := map[string]string{"action": action}
	if action == "failed" {
		// The token alone is not actionable -- "it failed" without the reason
		// sends the reader to `idea logs` for something the daemon is already
		// holding. One extra read, only on this branch.
		//
		// A read error here is swallowed on purpose: NextAction read the same
		// row microseconds ago, so a failure now is a storage fault, and
		// turning a correct action token into a 500 would cost the caller the
		// diagnosis to report a fault writeErr would mask behind a correlation
		// id anyway.
		if ini, err := pr.db.GetInitiative(r.Context(), initiativeID); err == nil {
			resp["failure_reason"] = ini.FailureReason
		} else {
			// Logged rather than passed over in silence: the difference between
			// "we reasoned this is unreachable" and "we could never tell whether
			// it happened" is one line.
			s.rt.log.Warn("could not read the failure reason for a failed initiative",
				"initiative", initiativeID, "error", err)
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleListChangeSets returns the change sets for an initiative.
func (s *Server) handleListChangeSets(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
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
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	findings, err := pr.db.ListFindingsByTarget(r.Context(), "change_set", cs.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
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
		s.writeErr(w, http.StatusNotFound, err)
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
			s.writeErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": string(domain.ChangeSetRejected)})
	case "retry":
		opID, err := s.rt.RetryConflictedApply(r.Context(), projectID, cs.InitiativeID, cs.ID)
		if err != nil {
			s.writeErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
	default:
		writeError(w, http.StatusBadRequest, "resolution must be retry or reject")
	}
}

// handleChangeSetDiff serves the change set's patch as a readable diff: the
// flat `patch` string plus a per-file `files` list. Content is redacted at this
// display surface (RenderPatch/StructuredDiff), never at blob-write time — the
// raw store stays redaction-free for byte-for-byte apply/revert.
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
	// Bounded (review defect #2: "the diff endpoint that didn't respond in
	// 100s"): RenderPatchPage/StructuredDiffPage cap the per-file redact-and-
	// diff work at maxDiffEndpointFiles rather than processing every file of
	// an arbitrarily large change set synchronously in this request.
	// total_files/truncated are additive fields; a client reading only
	// patch/files behaves exactly as before.
	patch, total := pr.engine.RenderPatchPage(cs)
	files, _ := pr.engine.StructuredDiffPage(cs)
	writeJSON(w, http.StatusOK, map[string]any{
		"patch":       patch,
		"files":       files,
		"total_files": total,
		"truncated":   total > len(files),
	})
}
func (s *Server) handleReviewChangeSet(w http.ResponseWriter, r *http.Request) {
	pr, cs, err := s.changeSet(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		Verdict string `json:"verdict"`
		// AcceptFindings signs off the reviewer's open findings on this change
		// set and approves without re-reviewing. Only the reviewer's: a finding
		// carrying a deny code is untouched and still refuses.
		AcceptFindings bool `json:"accept_findings"`
	}
	// decodeStrict, so a NEW CLI against an OLD daemon gets
	// `400 json: unknown field "accept_findings"` rather than having the flag
	// silently dropped and being told, while passing the documented escape, that
	// approval is blocked. `idea daemon` is long-lived, so that is a realistic
	// first experience.
	if err := decodeStrict(r.Body, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.Verdict == "" {
		writeError(w, http.StatusBadRequest, "verdict is required")
		return
	}
	review := pr.engine.ReviewChangeSet
	if body.AcceptFindings {
		review = pr.engine.ReviewChangeSetAccepting
	}
	// E1: an "approved" verdict runs the adversarial reviewer synchronously,
	// inside this request. That used to share the request's own context, so
	// closing the browser tab -- which cancels it -- cancelled the review
	// mid-flight and left "context canceled" in daemon.log with the change set
	// stuck wherever it was. context.WithoutCancel detaches from the client's
	// disconnect; the timeout is this process's own bound, generous enough
	// for a real model call without hanging forever on a wedged provider.
	//
	// It derives from the runtime's lifetime, not from nothing: detached
	// from everything, a review kept the engine lock through `idea daemon
	// stop`, so the stop waited out its whole budget and the process exited
	// mid-review without recording the run.
	ctx, cancel := s.rt.detachedContext()
	defer cancel()
	if err := review(ctx, cs.InitiativeID, cs.ID, body.Verdict); err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
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
	// Apply is asynchronous, so a status that cannot be applied has to be
	// caught here: launching the operation and failing in the background made
	// `idea apply CHG-1` on an already-applied change set answer "✓ apply
	// started" with the failure only visible in the log.
	if cs.Status != domain.ChangeSetApproved {
		writeError(w, http.StatusConflict, applyStateMessage(cs))
		return
	}
	opID, err := s.rt.ApplyChangeSetInitiative(r.Context(), projectID, cs.InitiativeID, cs.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
}

// applyStateMessage explains why a change set cannot be applied, in terms of
// what the reader should do next.
func applyStateMessage(cs *domain.ChangeSet) string {
	switch cs.Status {
	case domain.ChangeSetApplied:
		return "change set " + cs.HumanID + " is already applied; `idea revert " + cs.HumanID + "` rolls it back"
	case domain.ChangeSetApplying:
		return "change set " + cs.HumanID + " is being applied; `idea changes` shows when it lands"
	case domain.ChangeSetReviewRequired:
		return "change set " + cs.HumanID + " needs approval first: `idea review " + cs.HumanID + " approve`"
	case domain.ChangeSetRejected, domain.ChangeSetChangesRequested:
		return "change set " + cs.HumanID + " was " + string(cs.Status) + "; `idea reviews` shows why"
	default:
		return "change set " + cs.HumanID + " cannot be applied from status " + string(cs.Status)
	}
}

// revertStateMessage explains why a change set cannot be reverted, in terms of
// what the reader should do next -- the counterpart to applyStateMessage.
//
// `applied` is the complete revertible set. Every failure path in ApplyChangeSet
// restores the pre-image and then marks the change set `conflicted`, so a
// conflicted one has nothing left to roll back; and reconcileStaleOperations
// moves a crashed `applying` to `conflicted`, or back to `approved` when no
// bytes were written. So neither is ever a stable revertible state.
func revertStateMessage(cs *domain.ChangeSet) string {
	switch cs.Status {
	case domain.ChangeSetReverted:
		return "change set " + cs.HumanID + " is already reverted; `idea changes` shows the current state"
	case domain.ChangeSetApproved, domain.ChangeSetReviewRequired:
		return "change set " + cs.HumanID + " was never applied, so there is nothing to roll back"
	case domain.ChangeSetApplying:
		return "change set " + cs.HumanID + " is still being applied; `idea changes` shows when it lands"
	case domain.ChangeSetConflicted:
		return "change set " + cs.HumanID + " conflicted during apply and was already rolled back"
	default:
		return "change set " + cs.HumanID + " cannot be reverted from status " + string(cs.Status)
	}
}

// handleRevertChangeSet records a rolled-back journal entry asynchronously.
func (s *Server) handleRevertChangeSet(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	_, cs, err := s.changeSet(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	// Revert is asynchronous, so a status that cannot be reverted has to be
	// caught here -- the same reason handleApplyChangeSet checks. That guard was
	// added for apply and never propagated: `idea revert CHG-1` on an
	// already-reverted change set answered "✓ revert started" and exited 0, with
	// the real failure visible only in the daemon log (audit finding b).
	if cs.Status != domain.ChangeSetApplied {
		writeError(w, http.StatusConflict, revertStateMessage(cs))
		return
	}
	opID, err := s.rt.RevertChangeSetInitiative(r.Context(), projectID, cs.InitiativeID, cs.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
}

// handleCreatePR pushes an initiative's dedicated branch (git.branch_per_initiative)
// and opens a pull request via `gh pr create` (review C4 piece 3). Synchronous,
// unlike apply/execute/revert: it touches only git/GitHub state, not the
// workspace tree, so there is nothing here that needs a background operation
// or a claim on the initiative. Idempotent: a repeat call answers the same
// `pr_url` rather than opening a second PR.
func (s *Server) handleCreatePR(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	_, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	url, err := s.rt.CreatePRForInitiative(r.Context(), projectID, ini.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"pr_url": url})
}

// handlePRChecks reports the latest CI status of a change set's initiative's
// pull request via `gh pr checks` (review C4 piece 3). Scoped by change set
// id since that is where the CLI and UI show it, though every change set of
// one initiative shares the same PR and so the same result.
func (s *Server) handlePRChecks(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	_, cs, err := s.changeSet(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	res, err := s.rt.PRChecksForChangeSet(r.Context(), projectID, cs.ID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleRisk computes the deterministic risk profile for an initiative from its
// produced patches.
func (s *Server) handleRisk(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
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
	// This is a GET and it stays one. It used to call SaveRiskProfile, which is
	// append-only ("every computed profile is retained"), so every read grew the
	// database without bound -- and because the save goes through db.Apply, each
	// read also appended a risk_profile.saved event that handleActivity returns
	// and handleEvents streams over SSE. Polling the UI's risk panel, a browser
	// prefetch, or `idea risk --json` in a read-only reporting script all wrote.
	//
	// Nothing read any of it: risk_profiles has no SELECT anywhere in the tree,
	// and both consumers of risk data use this response. The comment that
	// justified the write claimed durability was needed so the tier floor could
	// "be re-derived from past scans" -- but ComputeRiskProfile is a pure
	// function of the patches, so it is recomputable here for free. A durable
	// profile needs a reader first.
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
		// Capability listing only: these are never Start()ed here, so the
		// credential and endpoint are deliberately empty rather than resolved.
		executor.NewOMPExecutor(pr.blobs, pr.cfg.Harness.Executor.Model, pr.cfg.Harness.Executor.Image, executor.AgentProvider{}),
		executor.NewHermesExecutor(pr.blobs, pr.cfg.Harness.Executor.Model, pr.cfg.Harness.Executor.Image, executor.AgentProvider{}),
	}
	list := make([]map[string]any, 0, len(execs))
	for _, exec := range execs {
		caps := exec.Capabilities(r.Context())
		// C3: status is only meaningful for hermes/omp; executorStatus reports
		// internal/container as always-ready.
		selected := exec.Name() == string(pr.cfg.Harness.Executor.Kind)
		status := s.rt.executorStatus(pr, exec.Name(), selected)
		if selected {
			status.LastSmoke = lastSmoke(r.Context(), pr, r.PathValue("projectId"))
		}
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
			"status":        status,
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
	if err := decodeStrict(r.Body, &body); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
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

	// C2: for hermes/omp, a configured model must actually resolve to a
	// provider before it is saved. Without this, a typo or an unconfigured
	// provider name was written to harness.yaml, forwarded into the agent
	// container at execute time, and failed there -- minutes later, nowhere
	// near the settings page that caused it, with `selectExecutor`'s own
	// fail-closed check as the only thing standing between the typo and a
	// leaked credential.
	if cfg.Harness.Executor.Kind == config.ExecutorHermes || cfg.Harness.Executor.Kind == config.ExecutorOMP {
		if cfg.Harness.Executor.Model != "" {
			if _, err := executor.ResolveAgentProvider(cfg.Harness.Executor.Model, cfg.Harness.DefaultModel, cfg.Harness.Providers); err != nil {
				writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "executor: "))
				return
			}
		}
	}
	// C1: hermes/omp with no image stores the pinned default, not an empty
	// string the runtime would otherwise have to guess at. agent.go's own
	// fallback (DefaultAgentImage, not alpine:3.20) still exists for a
	// pre-existing or hand-edited harness.yaml; the write path should not
	// manufacture new instances of the empty-image case that fixes.
	if cfg.Harness.Executor.Image == "" {
		if def := executor.DefaultAgentImage(string(cfg.Harness.Executor.Kind)); def != "" {
			cfg.Harness.Executor.Image = def
		}
	}
	if err := config.WriteHarness(filepath.Join(pr.rootPath, config.Dir), cfg.Harness); err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.rt.ReloadProject(r.Context(), r.PathValue("projectId")); err != nil {
		s.writeReloadErr(w, err)
		return
	}
	out := map[string]any{"status": "updated"}
	// The same catalog warning PUT /providers returns. This path was missed the
	// first time and it is the one that matters most: `idea executors setup
	// --model <dead id>` forwards that id into the agent container, where the
	// 404 surfaces as an agent that produced nothing rather than as a config
	// error.
	if body.Model != nil && *body.Model != "" {
		var warnings []string
		for _, pb := range config.UnknownModelProblems("harness.executor.model", *body.Model) {
			warnings = append(warnings, pb.Message)
		}
		if len(warnings) > 0 {
			out["warnings"] = warnings
		}
	}
	writeJSON(w, http.StatusOK, out)
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
	id := r.PathValue("id")
	if id == "" {
		// Unreachable through the mux, which routes these handlers only on
		// patterns carrying {id}. Explicit rather than a nil-value lookup, so a
		// future route registered under a different wildcard name fails loudly
		// here instead of silently answering "not found" forever.
		return pr, domain.Initiative{}, errors.New("route is missing an initiative id")
	}
	ini, err := pr.db.GetInitiative(r.Context(), id)
	return pr, ini, err
}

// offers reports whether answer is one of options, or options is empty (a
// free-text decision).
func offers(options []string, answer string) bool {
	if len(options) == 0 {
		return true
	}
	for _, o := range options {
		if o == answer {
			return true
		}
	}
	return false
}
