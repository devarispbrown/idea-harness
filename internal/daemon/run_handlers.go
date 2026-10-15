package daemon

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
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
	isOH := kind == string(config.ExecutorOpenHands)
	if kind != string(config.ExecutorHermes) && kind != string(config.ExecutorOMP) && kind != string(config.ExecutorACP) && !isOH {
		writeError(w, http.StatusBadRequest,
			"the smoke test runs the coding agent; select the hermes, omp, acp or openhands executor first")
		return
	}
	if isOH && pr.cfg.Harness.Executor.OpenHands.URL == "" {
		writeError(w, http.StatusBadRequest, "openhands has no executor.openhands.url; set the Agent Server first")
		return
	}
	if kind == string(config.ExecutorACP) && len(pr.cfg.Harness.Executor.ACP.Command) == 0 {
		writeError(w, http.StatusBadRequest, "acp has no executor.acp.command; set the agent's argv first")
		return
	}
	image := pr.cfg.Harness.Executor.Image
	if image == "" {
		image = executor.DefaultAgentImage(kind)
	}
	if image == "" && !isOH {
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
	signature := smokeSignature(kind, smokeImageKey(pr, kind, image), model)
	// Under the daemon's state root, which Docker can see; the system temp
	// directory is not visible to snap-installed Docker or Colima.
	workDir := filepath.Join(s.rt.stateRoot, "projects", projectID, "smoke")
	dotEnv := pr.dotEnv
	lookup := func(name string) string { return lookupEnv(name, dotEnv) }
	acpOpts := executor.ACPOptionsFrom(pr.cfg.Harness.Executor, model)
	ohOpts := executor.OpenHandsOptionsFrom(pr.cfg.Harness.Executor, model)
	s.rt.launchOperation(opID, func(ctx context.Context) {
		defer s.rt.smokes.release(projectID)
		sctx, cancel := context.WithTimeout(ctx, smokeTimeout)
		defer cancel()
		bg := context.WithoutCancel(ctx)
		smoke := func() error {
			if isOH {
				return executor.SmokeOpenHands(sctx, ohOpts, provider, lookup, workDir)
			}
			if kind == string(config.ExecutorACP) {
				return executor.SmokeACPAgent(sctx, acpOpts, provider, lookup, workDir)
			}
			return executor.SmokeAgent(sctx, kind, image, model, provider, lookup, workDir)
		}
		if err := smoke(); err != nil {
			status := domain.OperationFailed
			if ctx.Err() != nil {
				status = domain.OperationCancelled
			}
			_ = pr.db.SetOperationFailed(bg, opID, status, operationErrorMessage(pr, err.Error()))
			_ = pr.db.SetOperationResultRef(bg, opID, signature)
			return
		}
		_ = pr.db.SetOperationStatus(bg, opID, domain.OperationSucceeded, signature)
	})
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
}

// smokeSignature is what a smoke test tested, stored on its operation.
func smokeSignature(kind, image, model string) string {
	return kind + "|" + image + "|" + model
}

// smokeImageKey is the image half of a smoke signature. For acp the agent's
// argv is as much a part of "what was tested" as the image: the same image
// started with a different command is a different agent.
//
// For openhands there is no image: what was tested is the server and the
// agent it ran.
func smokeImageKey(pr *projectRuntime, kind, image string) string {
	if kind == string(config.ExecutorOpenHands) {
		oh := pr.cfg.Harness.Executor.OpenHands
		return oh.URL + " " + oh.EffectiveAgent() + " " + strings.Join(oh.ACPCommand, " ")
	}
	if kind == string(config.ExecutorACP) {
		return image + " " + strings.Join(pr.cfg.Harness.Executor.ACP.Command, " ")
	}
	return image
}

// lastSmoke is the project's newest smoke test, or nil -- and nil too when it
// tested something other than the current setup. "Passed 5 min ago" after the
// model or image changed described a configuration that no longer runs.
func lastSmoke(ctx context.Context, pr *projectRuntime, projectID string) *smokeResult {
	op, err := pr.db.LatestOperationOfType(ctx, projectID, opExecutorSmoke)
	if err != nil || op == nil {
		return nil
	}
	if op.Status == domain.OperationSucceeded || op.Status == domain.OperationFailed || op.Status == domain.OperationCancelled {
		kind := string(pr.cfg.Harness.Executor.Kind)
		image := pr.cfg.Harness.Executor.Image
		if image == "" {
			image = executor.DefaultAgentImage(kind)
		}
		if op.ResultRef != smokeSignature(kind, smokeImageKey(pr, kind, image), workflow.ImplementerModelSpec(pr.cfg.Harness, pr.registry)) {
			return nil
		}
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
	state, output := pr.engine.AgentOutput(r.Context(), ini.ID)
	writeJSON(w, http.StatusOK, map[string]any{
		"state":   state,
		"running": state == workflow.LiveRunning,
		"output":  output,
	})
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
