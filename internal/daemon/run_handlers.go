package daemon

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/workflow"
)

// opExecutorSmoke is the operation type of a smoke test started from the UI.
const opExecutorSmoke = "executor_smoke"

// smokeTimeout bounds a smoke test; a real run is bounded at 15 minutes too.
const smokeTimeout = 15 * time.Minute

// smokeClaims is the per-project "a smoke test is running" claim. Taken
// atomically before the operation is created: listing running operations and
// then creating one is the race this daemon has had to fix at every other
// entry point that starts work.
type smokeClaims struct {
	mu      sync.Mutex
	running map[string]bool
}

func (c *smokeClaims) claim(projectID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running == nil {
		c.running = map[string]bool{}
	}
	if c.running[projectID] {
		return false
	}
	c.running[projectID] = true
	return true
}

func (c *smokeClaims) release(projectID string) {
	c.mu.Lock()
	delete(c.running, projectID)
	c.mu.Unlock()
}

// smokeResult is the last smoke test, shown in Settings after a reload.
type smokeResult struct {
	Status     domain.OperationStatus `json:"status"`
	FinishedAt *time.Time             `json:"finished_at,omitempty"`
	Error      string                 `json:"error,omitempty"`
}

// handleExecutorSmoke runs the smoke test -- a trivial task through the real
// coding agent, which must write a file -- as an operation, with exactly what
// a run would use: the configured kind and image, the implementer's model and
// provider, and the daemon's own keys. `idea executors setup --smoke` runs it
// from the CLI, in the CLI's environment, which is not always the daemon's.
func (s *Server) handleExecutorSmoke(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	projectID := r.PathValue("projectId")
	kind := string(pr.cfg.Harness.Executor.Kind)
	if kind != string(config.ExecutorHermes) && kind != string(config.ExecutorOMP) {
		writeError(w, http.StatusBadRequest,
			"the smoke test runs the coding agent; select the hermes or omp executor first")
		return
	}
	image := pr.cfg.Harness.Executor.Image
	if image == "" {
		image = executor.DefaultAgentImage(kind)
	}
	if image == "" {
		writeError(w, http.StatusBadRequest, kind+" has no default image; set one first")
		return
	}
	provider, err := workflow.ImplementerProvider(pr.cfg.Harness, pr.registry)
	if err != nil {
		s.writeErr(w, http.StatusBadRequest, err)
		return
	}
	model := workflow.ImplementerModelSpec(pr.cfg.Harness, pr.registry)
	if !s.rt.smokes.claim(projectID) {
		writeError(w, http.StatusConflict, "a smoke test is already running for this project")
		return
	}
	opID, err := pr.db.CreateOperation(r.Context(), projectID, "", opExecutorSmoke)
	if err != nil {
		s.rt.smokes.release(projectID)
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Under the daemon's state root, which Docker can see; the system temp
	// directory is not visible to snap-installed Docker or Colima.
	workDir := filepath.Join(s.rt.stateRoot, "projects", projectID, "smoke")
	dotEnv := pr.dotEnv
	lookup := func(name string) string { return lookupEnv(name, dotEnv) }
	s.rt.launchOperation(opID, func(ctx context.Context) {
		defer s.rt.smokes.release(projectID)
		sctx, cancel := context.WithTimeout(ctx, smokeTimeout)
		defer cancel()
		bg := context.WithoutCancel(ctx)
		if err := executor.SmokeAgent(sctx, kind, image, model, provider, lookup, workDir); err != nil {
			status := domain.OperationFailed
			if ctx.Err() != nil {
				status = domain.OperationCancelled
			}
			_ = pr.db.SetOperationFailed(bg, opID, status, operationErrorMessage(pr, err.Error()))
			return
		}
		_ = pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, "")
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
}

// lastSmoke is the project's newest smoke test, or nil.
func lastSmoke(ctx context.Context, pr *projectRuntime, projectID string) *smokeResult {
	op, err := pr.db.LatestOperationOfType(ctx, projectID, opExecutorSmoke)
	if err != nil || op == nil {
		return nil
	}
	return &smokeResult{Status: op.Status, FinishedAt: op.FinishedAt, Error: op.Error}
}

// handleAgentOutput returns the end of the running coding agent's output for
// an initiative, for the running card. It never waits on the run itself.
func (s *Server) handleAgentOutput(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	running, output := pr.engine.AgentOutput(r.Context(), ini.ID)
	writeJSON(w, http.StatusOK, map[string]any{"running": running, "output": output})
}

// handleDiscardPartialWork is "Start over": the next run begins from the
// workspace instead of from what a stopped run wrote.
func (s *Server) handleDiscardPartialWork(w http.ResponseWriter, r *http.Request) {
	pr, ini, err := s.initiative(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	if err := pr.engine.DiscardPartial(ini.ID); err != nil {
		s.writeErr(w, http.StatusInternalServerError, fmt.Errorf("discard the stopped run's files: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "discarded"})
}
