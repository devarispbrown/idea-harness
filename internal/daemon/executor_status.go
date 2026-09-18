package daemon

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/workflow"
)

// imagePresenceCache answers "is this docker image present locally" without
// shelling out to `docker image inspect` on every poll. Both the GET
// /executors status and the Build page's run_blocked_reason (the executor-
// readiness half of C3) need this on every refresh, a few seconds apart --
// asking Docker that often adds real, measurable latency to a page that is
// otherwise a pure database read.
type imagePresenceCache struct {
	mu       sync.Mutex
	entries  map[string]imagePresenceEntry
	inflight map[string]chan struct{}
	ttl      time.Duration
	// probe is swapped in tests so nothing here depends on a real docker
	// daemon being present.
	probe func(ctx context.Context, image string) imageState
}

type imagePresenceEntry struct {
	state     imageState
	checkedAt time.Time
}

// imageState is what Docker says about an image. "Docker unavailable" is kept
// apart from "not pulled": the first blocks a run, the second does not (docker
// run pulls a missing image itself), and reporting one as the other told
// users to pull an image they already had.
type imageState int

const (
	imageAbsent imageState = iota
	imagePresent
	dockerUnavailable
	// imageUnknown: Docker answered with something unrecognised, or too
	// slowly. Neither blocks a run -- the run itself will say what is wrong
	// -- and it is cached only briefly.
	imageUnknown
)

// unknownTTL is how long an inconclusive answer is reused.
const unknownTTL = 5 * time.Second

func newImagePresenceCache() *imagePresenceCache {
	return &imagePresenceCache{
		entries:  map[string]imagePresenceEntry{},
		inflight: map[string]chan struct{}{},
		ttl:      30 * time.Second,
		probe:    dockerImageState,
	}
}

func (c *imagePresenceCache) state(image string) imageState {
	// nil-safe: a Runtime built by a raw struct literal in a test (rather than
	// NewRuntime) has no cache, and a status check on it should fail closed.
	if c == nil || image == "" {
		return imageAbsent
	}
	for {
		c.mu.Lock()
		if e, ok := c.entries[image]; ok && time.Since(e.checkedAt) < c.ttlFor(e.state) {
			c.mu.Unlock()
			return e.state
		}
		// One probe per image at a time; concurrent polls wait for it rather
		// than each starting their own docker process.
		if wait, busy := c.inflight[image]; busy {
			c.mu.Unlock()
			<-wait
			continue
		}
		done := make(chan struct{})
		c.inflight[image] = done
		c.mu.Unlock()

		// Not the request's context: a client that navigates away mid-probe
		// killed `docker image inspect`, and the failure was cached for 30 s
		// as "not pulled".
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		st := c.probe(ctx, image)
		cancel()

		c.mu.Lock()
		c.entries[image] = imagePresenceEntry{state: st, checkedAt: time.Now()}
		delete(c.inflight, image)
		c.mu.Unlock()
		close(done)
		return st
	}
}

func (c *imagePresenceCache) ttlFor(st imageState) time.Duration {
	if st == imageUnknown {
		return unknownTTL
	}
	return c.ttl
}

// dockerImageState asks `docker image inspect`, the same presence check
// executor.PullImage makes before deciding whether to pull -- but never pulls.
//
// Only answers that clearly mean "no Docker" count as unavailable. Everything
// else that is not "present" used to, so Podman's "image not known" and a
// Docker Desktop still starting (slower than the probe's timeout) both greyed
// out the Run button with "Docker is not running" for 30 s.
func dockerImageState(ctx context.Context, image string) imageState {
	out, err := exec.CommandContext(ctx, "docker", "image", "inspect", image).CombinedOutput()
	return classifyInspect(err, string(out), ctx.Err())
}

func classifyInspect(err error, out string, ctxErr error) imageState {
	if err == nil {
		return imagePresent
	}
	if ctxErr != nil {
		return imageUnknown
	}
	o := strings.ToLower(out)
	switch {
	case strings.Contains(o, "no such image"), strings.Contains(o, "image not known"):
		return imageAbsent
	case errors.Is(err, exec.ErrNotFound),
		strings.Contains(o, "cannot connect to the docker daemon"),
		strings.Contains(o, "is the docker daemon running"),
		strings.Contains(o, "error during connect"),
		strings.Contains(o, "cannot connect to podman"):
		return dockerUnavailable
	default:
		return imageUnknown
	}
}

// executorReadiness is the GET /executors "status" field (C3): whether the
// coding-agent image is present, whether its key is set, and the one sentence
// a client shows instead of guessing when a run cannot start.
type executorReadiness struct {
	Image        string `json:"image"`
	ImagePresent bool   `json:"image_present"`
	// ImageState says why ImagePresent is false: "absent", "unknown" (Docker
	// answered slowly or unexpectedly) or "docker_unavailable"; "present"
	// otherwise. Empty for executors with no image.
	ImageState string `json:"image_state,omitempty"`
	KeyEnv     string `json:"key_env"`
	KeySet     bool   `json:"key_set"`
	// Problem is empty when a run can start. It is only ever computed for
	// hermes/omp; internal/container are always ready.
	Problem string `json:"problem,omitempty"`
	// Note is information that does not block a run, such as an image the
	// first run will download.
	Note string `json:"note,omitempty"`
}

// executorStatus computes executorReadiness for one executor kind. selected
// says whether kind is the project's configured executor: only then do the
// configured image and model apply; any other row describes that kind's
// defaults.
//
// The provider is resolved exactly as Execute resolves it
// (workflow.ImplementerProvider), so the Build page's "can this run" never
// disagrees with what Execute then does.
func (r *Runtime) executorStatus(pr *projectRuntime, kind string, selected bool) executorReadiness {
	if kind != string(config.ExecutorHermes) && kind != string(config.ExecutorOMP) {
		return executorReadiness{ImagePresent: true, KeySet: true}
	}
	image := ""
	if selected {
		image = pr.cfg.Harness.Executor.Image
	}
	if image == "" {
		image = executor.DefaultAgentImage(kind)
	}
	st := executorReadiness{Image: image}
	modelHint := workflow.ImplementerModelSpec(pr.cfg.Harness, pr.registry)
	switch {
	case image == "":
		st.Problem = fmt.Sprintf("%s has no default image; run: idea executors setup --kind %s --image <image> --model %s --yes",
			kind, kind, modelHint)
	default:
		ims := r.images.state(image)
		st.ImageState = map[imageState]string{imagePresent: "present", imageAbsent: "absent",
			imageUnknown: "unknown", dockerUnavailable: "docker_unavailable"}[ims]
		switch ims {
		case imagePresent:
			st.ImagePresent = true
		case dockerUnavailable:
			st.Problem = "Docker is not running or not installed; the coding agent runs in a Docker container. Start Docker and try again."
		case imageUnknown:
			st.Note = "Could not check the image just now (Docker answered slowly or unexpectedly); the run will report any problem."
		case imageAbsent:
			st.Note = fmt.Sprintf("Image %s is not pulled yet; the first run downloads it, which can take several minutes. "+
				"To pull it now: idea executors setup --kind %s --model %s --yes", image, kind, modelHint)
		}
	}

	provider, err := workflow.ImplementerProvider(pr.cfg.Harness, pr.registry)
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
