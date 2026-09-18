package daemon

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
)

func fakeImageCache(st imageState) *imagePresenceCache {
	c := newImagePresenceCache()
	c.probe = func(context.Context, string) imageState { return st }
	return c
}

// hermesProject is a project whose selected executor is hermes with model.
func hermesProject(model string, dotEnv map[string]string) *projectRuntime {
	cfg := config.Config{}
	cfg.Harness.Executor = config.ExecutorConfig{Kind: config.ExecutorHermes, Model: model, Image: "nousresearch/hermes-agent:v2026.5.16"}
	return &projectRuntime{cfg: cfg, dotEnv: dotEnv}
}

// internal/container run no external agent and forward no key: always ready.
func TestExecutorStatusIsAlwaysReadyForInternalAndContainer(t *testing.T) {
	rt := &Runtime{images: fakeImageCache(imageAbsent)}
	for _, kind := range []string{"internal", "container"} {
		st := rt.executorStatus(&projectRuntime{}, kind, true)
		if !st.ImagePresent || !st.KeySet || st.Problem != "" {
			t.Errorf("%s status = %+v, want ready", kind, st)
		}
	}
}

// A missing image does not block a run -- `docker run` pulls it -- so it is a
// note naming the image and the command to pull it now, not a problem. As a
// problem it greyed out runs that would have succeeded.
func TestAMissingImageIsANoteNotABlocker(t *testing.T) {
	rt := &Runtime{images: fakeImageCache(imageAbsent)}
	st := rt.executorStatus(hermesProject("anthropic/claude-sonnet-5", map[string]string{"ANTHROPIC_API_KEY": "k"}), "hermes", true)
	if st.ImagePresent {
		t.Fatal("ImagePresent = true, want false")
	}
	if st.Problem != "" {
		t.Errorf("a missing image blocks the run: %q", st.Problem)
	}
	if !strings.Contains(st.Note, "nousresearch/hermes-agent:v2026.5.16") || !strings.Contains(st.Note, "idea executors setup --kind hermes") {
		t.Errorf("note does not name the image and the command: %q", st.Note)
	}
}

// Docker being down is not "image not pulled": it blocks, and says so.
func TestDockerUnavailableIsItsOwnProblem(t *testing.T) {
	rt := &Runtime{images: fakeImageCache(dockerUnavailable)}
	st := rt.executorStatus(hermesProject("anthropic/claude-sonnet-5", map[string]string{"ANTHROPIC_API_KEY": "k"}), "hermes", true)
	if !strings.Contains(st.Problem, "Docker is not running") {
		t.Errorf("problem = %q, want it to say Docker is not running", st.Problem)
	}
}

func TestExecutorStatusReportsAMissingKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	rt := &Runtime{images: fakeImageCache(imagePresent)}
	st := rt.executorStatus(hermesProject("anthropic/claude-sonnet-5", map[string]string{}), "hermes", true)
	if st.KeySet || st.KeyEnv != "ANTHROPIC_API_KEY" {
		t.Fatalf("status = %+v, want the key reported missing", st)
	}
	if !strings.Contains(st.Problem, "ANTHROPIC_API_KEY") || !strings.Contains(st.Problem, "idea daemon stop") {
		t.Errorf("problem does not name the key and the fix: %q", st.Problem)
	}
}

func TestExecutorStatusKeySetFromProjectDotEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	rt := &Runtime{images: fakeImageCache(imagePresent)}
	st := rt.executorStatus(hermesProject("anthropic/claude-sonnet-5", map[string]string{"ANTHROPIC_API_KEY": "sk-ant-x"}), "hermes", true)
	if !st.KeySet || st.Problem != "" {
		t.Errorf("status = %+v, want ready from .env", st)
	}
}

func TestExecutorStatusReportsAnUnresolvableModel(t *testing.T) {
	rt := &Runtime{images: fakeImageCache(imagePresent)}
	st := rt.executorStatus(hermesProject("typo-provider/some-model", nil), "hermes", true)
	if st.Problem == "" || strings.HasPrefix(st.Problem, "executor: ") {
		t.Errorf("problem = %q, want a user-facing refusal", st.Problem)
	}
}

// models.implementer outranks executor.model in Execute, so it must here too:
// checking executor.model alone blocked a run on a key Execute never needed.
func TestExecutorStatusUsesTheModelExecuteWillRun(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	pr := hermesProject("openai/gpt-5.6-luna", map[string]string{"ANTHROPIC_API_KEY": "k"})
	pr.cfg.Harness.Models = map[string]string{"implementer": "anthropic/claude-sonnet-5"}
	rt := &Runtime{images: fakeImageCache(imagePresent)}
	st := rt.executorStatus(pr, "hermes", true)
	if st.KeyEnv != "ANTHROPIC_API_KEY" || st.Problem != "" {
		t.Errorf("status = %+v, want it to check the implementer's anthropic key", st)
	}
}

// A row for a kind that is not selected describes that kind's defaults, not
// the selected kind's image.
func TestAnUnselectedRowUsesItsOwnDefaultImage(t *testing.T) {
	rt := &Runtime{images: fakeImageCache(imagePresent)}
	pr := hermesProject("anthropic/claude-sonnet-5", map[string]string{"ANTHROPIC_API_KEY": "k"})
	pr.cfg.Harness.Executor.Image = "my/custom:1"
	if st := rt.executorStatus(pr, "hermes", false); st.Image == "my/custom:1" {
		t.Error("an unselected row reported the selected executor's image")
	}
}

// One Docker probe per image per TTL, however many polls ask.
func TestImageCacheProbesOncePerTTL(t *testing.T) {
	var calls atomic.Int32
	c := newImagePresenceCache()
	c.probe = func(context.Context, string) imageState { calls.Add(1); return imagePresent }
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() { defer wg.Done(); c.state("some/image:tag") }()
	}
	wg.Wait()
	if n := calls.Load(); n != 1 {
		t.Errorf("docker was probed %d times for 10 concurrent polls, want 1", n)
	}
}

// The probe does not use a context the caller can cancel: a client navigating
// away mid-probe used to kill it and cache "not pulled" for 30 s.
func TestImageProbeOutlivesTheRequest(t *testing.T) {
	c := newImagePresenceCache()
	c.probe = func(ctx context.Context, _ string) imageState {
		if ctx.Err() != nil {
			return imageAbsent
		}
		return imagePresent
	}
	if got := c.state("some/image:tag"); got != imagePresent {
		t.Errorf("state = %v, want present", got)
	}
}
