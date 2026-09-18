package daemon

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/executor"
)

// imagePresenceCache answers "is this docker image present locally" without
// shelling out to `docker image inspect` on every poll. Both the GET
// /executors status and the Build page's run_blocked_reason (the executor-
// readiness half of C3) need this on every refresh, a few seconds apart --
// asking Docker that often adds real, measurable latency to a page that is
// otherwise a pure database read.
type imagePresenceCache struct {
	mu      sync.Mutex
	entries map[string]imagePresenceEntry
	ttl     time.Duration
	// probe is swapped in tests so nothing here depends on a real docker
	// daemon being present.
	probe func(ctx context.Context, image string) bool
}

type imagePresenceEntry struct {
	present   bool
	checkedAt time.Time
}

func newImagePresenceCache() *imagePresenceCache {
	return &imagePresenceCache{
		entries: map[string]imagePresenceEntry{},
		ttl:     30 * time.Second,
		probe:   dockerImagePresent,
	}
}

func (c *imagePresenceCache) present(ctx context.Context, image string) bool {
	// nil-safe: a Runtime built by a raw struct literal in a test (rather than
	// NewRuntime) has no cache, and a status check on it should fail closed to
	// "not present" rather than panic.
	if c == nil || image == "" {
		return false
	}
	c.mu.Lock()
	if e, ok := c.entries[image]; ok && time.Since(e.checkedAt) < c.ttl {
		c.mu.Unlock()
		return e.present
	}
	c.mu.Unlock()

	present := c.probe(ctx, image)

	c.mu.Lock()
	c.entries[image] = imagePresenceEntry{present: present, checkedAt: time.Now()}
	c.mu.Unlock()
	return present
}

// dockerImagePresent shells out to `docker image inspect`, the same
// presence check executor.PullImage makes before deciding whether to pull --
// but never pulls.
func dockerImagePresent(ctx context.Context, image string) bool {
	return exec.CommandContext(ctx, "docker", "image", "inspect", image).Run() == nil
}

// executorReadiness is the GET /executors "status" field (C3): whether the
// selected coding-agent image is present, whether its key is set, and the one
// sentence a client shows instead of guessing when it is not ready.
type executorReadiness struct {
	Image        string `json:"image"`
	ImagePresent bool   `json:"image_present"`
	KeyEnv       string `json:"key_env"`
	KeySet       bool   `json:"key_set"`
	// Problem is empty when ready. It is only ever computed for hermes/omp;
	// internal/container are always ready (no image to pull, no key to set).
	Problem string `json:"problem,omitempty"`
}

// executorStatus computes executorReadiness for one executor entry. kind is
// the executor's own name ("internal", "container", "omp", "hermes"); image
// and modelSpec are the project's configured executor.image/executor.model
// (meaningful only for the selected kind, same as the sibling "image"/"model"
// fields GET /executors already returns per row).
func (r *Runtime) executorStatus(ctx context.Context, pr *projectRuntime, kind, image, modelSpec string) executorReadiness {
	if kind != string(config.ExecutorHermes) && kind != string(config.ExecutorOMP) {
		// internal/container run no external agent and forward no key, so
		// there is nothing to be unready about (C3).
		return executorReadiness{ImagePresent: true, KeySet: true}
	}
	if image == "" {
		image = executor.DefaultAgentImage(kind)
	}
	st := executorReadiness{Image: image}
	st.ImagePresent = r.images.present(ctx, image)
	if !st.ImagePresent {
		modelHint := modelSpec
		if modelHint == "" {
			modelHint = "<model>"
		}
		if image == "" {
			st.Problem = fmt.Sprintf(
				"%s has no default image; run: idea executors setup --kind %s --image <image> --model %s --yes",
				kind, kind, modelHint)
		} else {
			st.Problem = fmt.Sprintf(
				"Image %s is not pulled. Run: idea executors setup --kind %s --model %s --yes",
				image, kind, modelHint)
		}
	}

	provider, err := executor.ResolveAgentProvider(modelSpec, pr.cfg.Harness.DefaultModel, pr.cfg.Harness.Providers)
	if err != nil {
		if st.Problem == "" {
			st.Problem = strings.TrimPrefix(err.Error(), "executor: ")
		}
		return st
	}
	st.KeyEnv = provider.KeyEnv
	st.KeySet = provider.KeyEnv == "" || lookupEnv(provider.KeyEnv, pr.dotEnv) != ""
	if st.Problem == "" && !st.KeySet {
		st.Problem = fmt.Sprintf(
			"%s is not set in the daemon's environment or the project's .env; set it and run idea daemon stop",
			provider.KeyEnv)
	}
	return st
}
