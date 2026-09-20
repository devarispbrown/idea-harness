package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/project"
	"github.com/devarispbrown/idea-harness/internal/store"
)

func TestLaunchOperationCancellation(t *testing.T) {
	rt, err := NewRuntime(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })

	cancelled := make(chan error, 1)
	rt.launchOperation("op-1", func(ctx context.Context) {
		<-ctx.Done()
		cancelled <- ctx.Err()
	})

	// Resolve the handle through the registry, exactly as CancelOperation does.
	rt.cancelMu.Lock()
	cancel := rt.cancels["op-1"]
	rt.cancelMu.Unlock()
	if cancel == nil {
		t.Fatal("expected a live cancellation handle for op-1")
	}
	cancel()

	select {
	case err := <-cancelled:
		if err != context.Canceled {
			t.Fatalf("expected context.Canceled, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not propagate to the operation")
	}
}

// Split from a single test that asserted only `err != nil`. Once an unknown id
// started returning a *store.NotFoundError instead of the live-handle refusal,
// that test kept passing while no longer testing what its name said — the two
// conditions it conflates are exactly the ones the API could not tell apart.
func TestCancelOperationUnknownIDIsNotFound(t *testing.T) {
	rt, err := NewRuntime(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })

	err = rt.CancelOperation(context.Background(), "op-never-started")
	if err == nil {
		t.Fatal("cancelling an operation that does not exist returned no error")
	}
	var nf *store.NotFoundError
	if !errors.As(err, &nf) {
		t.Fatalf("unknown operation id returned %T (%v); want *store.NotFoundError so writeErr answers 404", err, err)
	}
}

func TestCancelOperationNoLiveHandleIsAPrecondition(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	// A real operation row that was never launched, so no cancel handle exists.
	opID, err := pr.db.CreateOperation(t.Context(), projectID, iniID, "advance_initiative")
	if err != nil {
		t.Fatal(err)
	}

	err = rt.CancelOperation(t.Context(), opID)
	if err == nil {
		t.Fatal("cancelling an operation with no live handle returned no error")
	}
	var nf *store.NotFoundError
	if errors.As(err, &nf) {
		t.Fatalf("an operation that exists was reported as missing: %v", err)
	}
	var pe *PreconditionError
	if !errors.As(err, &pe) {
		t.Fatalf("no-live-handle returned %T (%v); want *PreconditionError so writeErr answers 409", err, err)
	}
}

func TestEnsureProjectSingleFlight(t *testing.T) {
	rt, err := NewRuntime(filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })

	projDir := t.TempDir()
	if err := project.InitConfig(projDir); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pid, err := rt.global.RegisterProject(ctx, "proj", projDir, "")
	if err != nil {
		t.Fatal(err)
	}

	// Concurrent first-opens must yield the same *projectRuntime (single DB).
	const n = 8
	prs := make([]*projectRuntime, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			prs[i], _ = rt.EnsureProject(ctx, pid)
		}(i)
	}
	wg.Wait()
	for i := 1; i < n; i++ {
		if prs[i] != prs[0] {
			t.Fatalf("EnsureProject returned distinct runtimes (double-open DB): %p vs %p", prs[i], prs[0])
		}
	}
	if prs[0] == nil {
		t.Fatal("EnsureProject returned nil")
	}
}

func TestReloadProjectKeepsSameRuntime(t *testing.T) {
	rt, err := NewRuntime(filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })

	projDir := t.TempDir()
	if err := project.InitConfig(projDir); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pid, err := rt.global.RegisterProject(ctx, "proj", projDir, "")
	if err != nil {
		t.Fatal(err)
	}

	before, err := rt.EnsureProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	after, err := rt.ReloadProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	// review H4: ReloadProject now builds a fresh *projectRuntime and swaps
	// the map entry rather than mutating fields on the same one in place
	// (proven by the race detector to be unsafe -- ~20 read sites hold no
	// lock at all over cfg/registry/redactor/blobs). The engine, the DB
	// handle, and the plan-edit lock registry, which all outlive a reload by
	// design (their own internal synchronization is what "shared mutex"
	// means below), must still be the SAME instances.
	if before == after {
		t.Fatal("ReloadProject must build a new *projectRuntime, not mutate the old one in place (review H4)")
	}
	if before.engine != after.engine {
		t.Fatal("ReloadProject must keep the same engine (same engine -> shared mutex)")
	}
	if before.db != after.db {
		t.Fatal("ReloadProject must keep the same *store.DB (no second handle)")
	}
	if before.planEdit != after.planEdit {
		t.Fatal("ReloadProject must keep the same plan-edit lock registry, or a plan edit in flight on the old generation and one on the new generation could hold two different locks for the same initiative (review C2/H4)")
	}
	// Reconciliation must not re-run on reload: the reconciled flag stays set.
	rt.mu.Lock()
	reconciled := rt.reconciled[pid]
	rt.mu.Unlock()
	if !reconciled {
		t.Fatal("expected reconciled flag to remain set after reload")
	}
}

func TestCollectSecretsIncludesPresets(t *testing.T) {
	t.Setenv("GOOGLE_API_KEY", "AIzaSyTestPresetKey0123456789")
	secrets := collectSecrets(config.Config{}, nil)
	found := false
	for _, s := range secrets {
		if s == "AIzaSyTestPresetKey0123456789" {
			found = true
		}
	}
	if !found {
		t.Fatalf("preset GOOGLE_API_KEY not collected: %v", secrets)
	}
}

func TestCollectSecretsIncludesDotEnvSecrets(t *testing.T) {
	dotEnv := map[string]string{
		"AWS_SECRET_ACCESS_KEY": "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"DATABASE_URL":          "postgres://user:superSecretPass123@host:5432/db",
		"NODE_ENV":              "development",
		"PORT":                  "8080",
	}
	secrets := collectSecrets(config.Config{}, dotEnv)
	joined := strings.Join(secrets, "\n")
	if !strings.Contains(joined, "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY") {
		t.Fatalf("AWS_SECRET_ACCESS_KEY value not collected: %v", secrets)
	}
	// NODE_ENV/PORT are short/common and must NOT be treated as known secrets.
	for _, s := range secrets {
		if s == "development" || s == "8080" {
			t.Fatalf("common value %q must not be a known secret", s)
		}
	}
}

// TestEnsureProjectAutoReloadsOnHandEditedHarnessYAML is a regression test
// for review defect #4: a hand edit to .idea-harness/harness.yaml (e.g.
// test_cmd) used to be invisible to execute/checks until the daemon was
// restarted, because EnsureProject's cache-hit path never re-read the file
// and ReloadProject only ever ran when the daemon itself had just written it
// (the provider/executor settings endpoints). Every handler resolves its
// project through EnsureProject (Server.project), so this is the fix's
// single choke point: a file edited directly on disk, with no API call in
// between, must take effect on the very next request.
func TestEnsureProjectAutoReloadsOnHandEditedHarnessYAML(t *testing.T) {
	rt, err := NewRuntime(filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })

	projDir := t.TempDir()
	if err := project.InitConfig(projDir); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pid, err := rt.global.RegisterProject(ctx, "proj", projDir, "")
	if err != nil {
		t.Fatal(err)
	}

	pr, err := rt.EnsureProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if pr.cfg.Harness.TestCmd != "" {
		t.Fatalf("fixture precondition: expected no test_cmd yet, got %q", pr.cfg.Harness.TestCmd)
	}

	// Hand-edit harness.yaml directly -- no WriteHarness, no ReloadProject
	// call, exactly like a developer opening the file in an editor. Force the
	// mtime forward: on a coarse filesystem clock a same-second rewrite could
	// otherwise not register as "changed".
	harnessPath := filepath.Join(projDir, config.Dir, "harness.yaml")
	data, err := os.ReadFile(harnessPath)
	if err != nil {
		t.Fatal(err)
	}
	edited := string(data) + "\ntest_cmd: \"go test ./...\"\n"
	if err := os.WriteFile(harnessPath, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(harnessPath, future, future); err != nil {
		t.Fatal(err)
	}

	reloaded, err := rt.EnsureProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.cfg.Harness.TestCmd != "go test ./..." {
		t.Fatalf("EnsureProject did not pick up the hand-edited test_cmd: got %q", reloaded.cfg.Harness.TestCmd)
	}
	// review H4: a fresh *projectRuntime (see TestReloadProjectKeepsSameRuntime),
	// but the same underlying engine/DB/lock registry -- this must still feel
	// like a reload, not a fresh open.
	if reloaded == pr {
		t.Fatal("auto-reload must build a new *projectRuntime, not mutate the old one in place (review H4)")
	}
	if reloaded.engine != pr.engine {
		t.Fatal("auto-reload must keep the same engine (same engine -> shared mutex)")
	}

	// A second call with nothing changed on disk must not reload again (no
	// observable effect either way here, but exercises the "not stale" path
	// without error).
	again, err := rt.EnsureProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	if again.cfg.Harness.TestCmd != "go test ./..." {
		t.Fatalf("config regressed on a no-op EnsureProject call: got %q", again.cfg.Harness.TestCmd)
	}
}

// TestEnsureProjectAutoReloadSkipsWhileAnOperationIsRunning is a regression
// guard on the auto-reload path added for review defect #4: it must reuse
// ReloadProject's existing in-flight-operation guard (a pipeline goroutine
// reads the engine's config without holding its lock) rather than swapping
// config underneath a running operation, and — critically — a reload that
// cannot happen right now must not fail the request. The stale-but-valid
// config keeps serving.
func TestEnsureProjectAutoReloadSkipsWhileAnOperationIsRunning(t *testing.T) {
	rt, err := NewRuntime(filepath.Join(t.TempDir(), "state"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })

	projDir := t.TempDir()
	if err := project.InitConfig(projDir); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	pid, err := rt.global.RegisterProject(ctx, "proj", projDir, "")
	if err != nil {
		t.Fatal(err)
	}
	pr, err := rt.EnsureProject(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := pr.db.CreateOperation(ctx, pid, "", "test"); err != nil {
		t.Fatal(err)
	}

	harnessPath := filepath.Join(projDir, config.Dir, "harness.yaml")
	data, err := os.ReadFile(harnessPath)
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.WriteFile(harnessPath, append(data, []byte("\ntest_cmd: \"go test ./...\"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(harnessPath, future, future); err != nil {
		t.Fatal(err)
	}

	again, err := rt.EnsureProject(ctx, pid)
	if err != nil {
		t.Fatalf("EnsureProject must not fail the request when auto-reload cannot happen: %v", err)
	}
	if again != pr {
		t.Fatal("expected the same runtime to keep serving when auto-reload is skipped")
	}
	if again.cfg.Harness.TestCmd != "" {
		t.Fatalf("config must not change while an operation is running, got test_cmd=%q", again.cfg.Harness.TestCmd)
	}
}
