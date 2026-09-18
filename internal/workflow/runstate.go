package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/fsdiff"
	"github.com/devarispbrown/idea-harness/internal/risk"
	"github.com/devarispbrown/idea-harness/internal/security"
)

// ---------- live agent output ----------

// liveOutputLines is how much of a running agent's output the live view shows.
const liveOutputLines = 100

// liveOutputTTL bounds how often a viewer's poll reaches `docker logs`: every
// open tab polls, and each read is a process spawn.
const liveOutputTTL = 1500 * time.Millisecond

// liveAttempt is a running agent the UI can watch. It is registered under its
// own lock, never e.mu: Execute holds e.mu for the whole run, and a viewer
// asking for output must not wait for the run to finish.
type liveAttempt struct {
	tailer    executor.OutputTailer
	attemptID string
	redactor  *security.Redactor

	mu       sync.Mutex
	done     bool // the agent has finished; the run is in its checks
	cached   string
	cachedAt time.Time
}

func (e *Engine) setLive(initiativeID string, t executor.OutputTailer, attemptID string) {
	e.liveMu.Lock()
	defer e.liveMu.Unlock()
	if e.live == nil {
		e.live = map[string]*liveAttempt{}
	}
	e.live[initiativeID] = &liveAttempt{tailer: t, attemptID: attemptID, redactor: e.redactor}
}

// markLiveDone records that the agent has finished. Collect has removed its
// container, so there is no more output; saying "running" with an empty log
// for the length of the build and test checks read as a hang.
func (e *Engine) markLiveDone(initiativeID string) {
	e.liveMu.Lock()
	a := e.live[initiativeID]
	e.liveMu.Unlock()
	if a != nil {
		a.mu.Lock()
		a.done = true
		a.mu.Unlock()
	}
}

func (e *Engine) clearLive(initiativeID string) {
	e.liveMu.Lock()
	delete(e.live, initiativeID)
	e.liveMu.Unlock()
}

// AgentOutput returns the end of a running agent's output for an initiative,
// redacted and sanitized, and whether the agent is still running. It never
// takes e.mu.
func (e *Engine) AgentOutput(ctx context.Context, initiativeID string) (running bool, output string) {
	e.liveMu.Lock()
	a := e.live[initiativeID]
	e.liveMu.Unlock()
	if a == nil {
		return false, ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.done {
		return false, a.cached
	}
	if time.Since(a.cachedAt) < liveOutputTTL {
		return true, a.cached
	}
	out := a.tailer.OutputTail(ctx, a.attemptID, liveOutputLines)
	if a.redactor != nil {
		out = a.redactor.RedactString(out)
	} else {
		out = security.Redact(out)
	}
	a.cached, a.cachedAt = out, time.Now()
	return true, out
}

// ---------- resumable runs ----------

// partialWork is what a stopped run had written, kept so the next run of the
// same task continues from it rather than from nothing.
type partialWork struct {
	TaskID    string              `json:"task_id"`
	Files     []domain.FileChange `json:"files"`
	Dropped   string              `json:"dropped,omitempty"` // why nothing was kept
	CreatedAt time.Time           `json:"created_at"`
}

// PartialWorkInfo describes a kept partial for clients.
type PartialWorkInfo struct {
	TaskID string `json:"task_id"`
	Files  int    `json:"files"`
	// Note is set when the stopped run's files were not kept, and why.
	Note string `json:"note,omitempty"`
}

// partialPath is beside the initiative's work root, not inside it: seeding
// removes the work root before every run.
func (e *Engine) partialPath(initiativeID string) string {
	root := e.workRoot
	if root == "" {
		root = os.TempDir()
	}
	return filepath.Join(root, "work", ".partial-"+initiativeID+".json")
}

// capturePartial keeps what a run that ended without a change set had
// written. Called only after the agent's container is confirmed gone, so
// nothing is still writing. Best effort: it logs and returns on any failure,
// and never replaces the run's own error. ctx may be cancelled -- a Stop and
// a daemon shutdown are the reasons it runs -- so it works on a detached one.
func (e *Engine) capturePartial(ctx context.Context, initiativeID, taskID, workRoot string, before fsdiff.Manifest) {
	ctx = context.WithoutCancel(ctx)
	after, err := fsdiff.Snapshot(workRoot)
	if err != nil {
		e.log.Warn("could not keep the stopped run's files", "initiative", initiativeID, "err", err)
		return
	}
	files := fsdiff.Diff(before, after)
	if len(files) == 0 {
		return
	}
	pw := partialWork{TaskID: taskID, CreatedAt: time.Now()}
	// The same gate persistDiff has: content that trips it is never written to
	// the redaction-free store.
	diffText := e.renderDiffTextFromWorkRoot(workRoot, files)
	if e.detectCredential(diffText) || hasBlockingRiskSignal(risk.RiskSignals(ctx, "", diffText)) {
		pw.Dropped = "the files it wrote looked like they contained a secret, so they were not kept"
	} else if err := e.persistDiff(workRoot, files); err != nil {
		e.log.Warn("could not keep the stopped run's files", "initiative", initiativeID, "err", err)
		return
	} else {
		pw.Files = files
	}
	if err := writeJSONAtomic(e.partialPath(initiativeID), pw); err != nil {
		e.log.Warn("could not record the stopped run's files", "initiative", initiativeID, "err", err)
		return
	}
	e.log.Info("kept the stopped run's files", "initiative", initiativeID, "task", taskID, "files", len(pw.Files), "dropped", pw.Dropped)
}

// readPartial returns the initiative's partial, or nil. A corrupt or
// unreadable file is treated as absent: a torn write must not stop a run.
func (e *Engine) readPartial(initiativeID string) *partialWork {
	data, err := os.ReadFile(e.partialPath(initiativeID))
	if err != nil {
		return nil
	}
	var pw partialWork
	if err := json.Unmarshal(data, &pw); err != nil || pw.TaskID == "" {
		_ = os.Remove(e.partialPath(initiativeID))
		return nil
	}
	return &pw
}

// PartialWork describes the initiative's kept partial, or nil.
func (e *Engine) PartialWork(initiativeID string) *PartialWorkInfo {
	pw := e.readPartial(initiativeID)
	if pw == nil {
		return nil
	}
	return &PartialWorkInfo{TaskID: pw.TaskID, Files: len(pw.Files), Note: pw.Dropped}
}

// DiscardPartial throws the initiative's kept partial away. It is called by
// "Start over", and by reopening a phase or revising the plan, which can
// change the task it belongs to.
func (e *Engine) DiscardPartial(initiativeID string) error {
	err := os.Remove(e.partialPath(initiativeID))
	if err == nil {
		e.log.Info("discarded the stopped run's files", "initiative", initiativeID)
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// carryResult is what resumePartial did, for the change set's finding.
type carryResult struct {
	carried []string
	skipped []string // "path: reason"
	note    string
}

// resumePartial writes a kept partial for taskID into a freshly seeded work
// root, after the before snapshot so the carried files are part of the diff.
// A file is carried only where the seed still matches what the stopped run
// started from -- fsdiff.StaleBase's rule, applied to the seed: an add only
// if the path is absent, an edit only if the file is unchanged. Anything else
// changed in the workspace since, and carrying it would overwrite that.
func (e *Engine) resumePartial(initiativeID, taskID, workRoot string) *carryResult {
	pw := e.readPartial(initiativeID)
	if pw == nil {
		return nil
	}
	_ = os.Remove(e.partialPath(initiativeID))
	if pw.TaskID != taskID {
		e.log.Info("discarded a stopped run's files kept for a different task", "initiative", initiativeID, "task", pw.TaskID)
		return nil
	}
	res := &carryResult{note: pw.Dropped}
	for _, fc := range pw.Files {
		if stale := fsdiff.StaleBase(workRoot, []domain.FileChange{fc}); len(stale) > 0 {
			res.skipped = append(res.skipped, stale[0])
			continue
		}
		dest, err := security.ResolveWithin(workRoot, fc.Path)
		if err != nil {
			res.skipped = append(res.skipped, fmt.Sprintf("%s: %v", fc.Path, err))
			continue
		}
		if fc.IsDelete() {
			if err := os.Remove(dest); err != nil && !errors.Is(err, os.ErrNotExist) {
				res.skipped = append(res.skipped, fmt.Sprintf("%s: %v", fc.Path, err))
				continue
			}
			res.carried = append(res.carried, fc.Path+" (deleted)")
			continue
		}
		content, err := e.contentBlobs().Get(fc.Digest)
		if err == nil {
			err = os.MkdirAll(filepath.Dir(dest), 0o755)
		}
		if err == nil {
			err = os.WriteFile(dest, content, 0o644)
		}
		if err != nil {
			res.skipped = append(res.skipped, fmt.Sprintf("%s: %v", fc.Path, err))
			continue
		}
		res.carried = append(res.carried, fc.Path)
	}
	return res
}

// carriedFinding records on the change set what was carried from the stopped
// run and what was not.
func (e *Engine) carriedFinding(cs *domain.ChangeSet, res *carryResult) *domain.Finding {
	if res == nil || (len(res.carried) == 0 && len(res.skipped) == 0 && res.note == "") {
		return nil
	}
	var ev strings.Builder
	if len(res.carried) > 0 {
		fmt.Fprintf(&ev, "carried: %s\n", strings.Join(res.carried, ", "))
	}
	if len(res.skipped) > 0 {
		fmt.Fprintf(&ev, "not carried (the workspace changed there): %s\n", strings.Join(res.skipped, "; "))
	}
	if res.note != "" {
		fmt.Fprintf(&ev, "%s\n", res.note)
	}
	return &domain.Finding{
		InitiativeID:   cs.InitiativeID,
		TargetType:     "change_set",
		TargetID:       cs.ID,
		Severity:       domain.SeverityObservation,
		Confidence:     domain.ConfidenceHigh,
		Category:       "carried_over",
		Finding:        fmt.Sprintf("This run continued from the stopped run: %d file(s) carried over.", len(res.carried)),
		Evidence:       strings.TrimSpace(ev.String()),
		Impact:         "The carried files are part of this change set.",
		Recommendation: "Review them with the rest of the diff; use Start over next time to begin from the workspace instead.",
	}
}

// writeJSONAtomic writes v as JSON to path through a temp file and a rename,
// so a crash leaves the old file or the new one, never a torn one.
func writeJSONAtomic(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".partial-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
