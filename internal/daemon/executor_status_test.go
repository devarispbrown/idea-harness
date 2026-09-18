package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// fakeImageCache lets a test control docker image presence deterministically,
// and counts probe calls so the 30s cache guard can be checked without a real
// clock.
func fakeImageCache(present bool) *imagePresenceCache {
	c := newImagePresenceCache()
	c.probe = func(ctx context.Context, image string) bool { return present }
	return c
}

// TestExecutorStatusIsAlwaysReadyForInternalAndContainer is C3's scope rule:
// status is only meaningful for hermes/omp. internal/container run no
// external agent and forward no key, so they must always report ready.
func TestExecutorStatusIsAlwaysReadyForInternalAndContainer(t *testing.T) {
	rt := &Runtime{images: fakeImageCache(false)}
	pr := &projectRuntime{cfg: config.Config{}}
	for _, kind := range []string{"internal", "container"} {
		st := rt.executorStatus(t.Context(), pr, kind, "", "")
		if !st.ImagePresent || !st.KeySet || st.Problem != "" {
			t.Errorf("%s status = %+v, want image_present=true key_set=true problem=\"\"", kind, st)
		}
	}
}

// TestExecutorStatusReportsAMissingImage covers the exact wording the audit's
// fix hinges on: the problem must name the image and the setup command, not
// just say "not ready".
func TestExecutorStatusReportsAMissingImage(t *testing.T) {
	rt := &Runtime{images: fakeImageCache(false)}
	pr := &projectRuntime{cfg: config.Config{}}
	st := rt.executorStatus(t.Context(), pr, "hermes", "nousresearch/hermes-agent:v2026.5.16", "anthropic/claude-sonnet-4-5-20250929")
	if st.ImagePresent {
		t.Fatal("ImagePresent = true, want false")
	}
	if !strings.Contains(st.Problem, "nousresearch/hermes-agent:v2026.5.16") {
		t.Errorf("problem does not name the image: %q", st.Problem)
	}
	if !strings.Contains(st.Problem, "idea executors setup --kind hermes") {
		t.Errorf("problem does not name the fix command: %q", st.Problem)
	}
}

// TestExecutorStatusReportsAMissingKey: an image that IS present but whose
// provider key is unset must still report not-ready, naming the variable and
// the fix (F1's message, reused here for the same underlying fact).
func TestExecutorStatusReportsAMissingKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	rt := &Runtime{images: fakeImageCache(true)}
	pr := &projectRuntime{cfg: config.Config{}, dotEnv: map[string]string{}}
	st := rt.executorStatus(t.Context(), pr, "hermes", "nousresearch/hermes-agent:v2026.5.16", "anthropic/claude-sonnet-4-5-20250929")
	if !st.ImagePresent {
		t.Fatal("ImagePresent = false, want true")
	}
	if st.KeySet {
		t.Fatal("KeySet = true, want false (no key in env or .env)")
	}
	if st.KeyEnv != "ANTHROPIC_API_KEY" {
		t.Errorf("KeyEnv = %q, want ANTHROPIC_API_KEY", st.KeyEnv)
	}
	if !strings.Contains(st.Problem, "ANTHROPIC_API_KEY") || !strings.Contains(st.Problem, "idea daemon stop") {
		t.Errorf("problem does not name the key and the fix: %q", st.Problem)
	}
}

// TestExecutorStatusKeySetFromProjectDotEnv: the project's .env counts as much
// as the daemon's own environment (C3's stated rule).
func TestExecutorStatusKeySetFromProjectDotEnv(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	rt := &Runtime{images: fakeImageCache(true)}
	pr := &projectRuntime{cfg: config.Config{}, dotEnv: map[string]string{"ANTHROPIC_API_KEY": "sk-ant-from-dotenv"}}
	st := rt.executorStatus(t.Context(), pr, "hermes", "nousresearch/hermes-agent:v2026.5.16", "anthropic/claude-sonnet-4-5-20250929")
	if !st.KeySet {
		t.Fatal("KeySet = false, want true: the key is in the project's .env")
	}
	if st.Problem != "" {
		t.Errorf("problem = %q, want none: image present and key set", st.Problem)
	}
}

// TestExecutorStatusReportsAnUnresolvableModel: a model that cannot resolve to
// a provider at all is a problem too, distinct from a missing key.
func TestExecutorStatusReportsAnUnresolvableModel(t *testing.T) {
	rt := &Runtime{images: fakeImageCache(true)}
	pr := &projectRuntime{cfg: config.Config{}}
	st := rt.executorStatus(t.Context(), pr, "omp", "", "typo-provider/some-model")
	if st.Problem == "" {
		t.Fatal("an unresolvable model produced no problem")
	}
	if strings.HasPrefix(st.Problem, "executor: ") {
		t.Errorf("problem leaks the internal package prefix: %q", st.Problem)
	}
}

// TestExecutorStatusReadyWhenImageAndKeyAreBothFine is the all-clear case.
func TestExecutorStatusReadyWhenImageAndKeyAreBothFine(t *testing.T) {
	rt := &Runtime{images: fakeImageCache(true)}
	pr := &projectRuntime{cfg: config.Config{}, dotEnv: map[string]string{"ANTHROPIC_API_KEY": "sk-ant-x"}}
	st := rt.executorStatus(t.Context(), pr, "hermes", "nousresearch/hermes-agent:v2026.5.16", "anthropic/claude-sonnet-4-5-20250929")
	if !st.ImagePresent || !st.KeySet || st.Problem != "" {
		t.Errorf("status = %+v, want fully ready", st)
	}
}

// TestImagePresenceCacheDoesNotCallDockerOnEveryPoll pins the 30s cache rule:
// repeated checks of the same image within the TTL must hit the cache, not
// shell out to `docker image inspect` again -- a per-poll Docker call was
// measured to add real latency to a page refreshed every few seconds.
func TestImagePresenceCacheDoesNotCallDockerOnEveryPoll(t *testing.T) {
	calls := 0
	c := newImagePresenceCache()
	c.probe = func(ctx context.Context, image string) bool {
		calls++
		return true
	}
	for i := 0; i < 5; i++ {
		if !c.present(context.Background(), "some/image:tag") {
			t.Fatal("present() = false, want true")
		}
	}
	if calls != 1 {
		t.Errorf("docker was probed %d times for 5 polls within the TTL, want 1", calls)
	}
}
