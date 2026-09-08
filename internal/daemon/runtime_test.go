package daemon

import (
	"context"
	"errors"
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
	if before != after {
		t.Fatal("ReloadProject must keep the same runtime (same engine -> shared mutex)")
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
