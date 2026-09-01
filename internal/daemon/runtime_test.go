package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/idea-harness/idea-harness/internal/project"
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

func TestCancelOperationNoLiveHandle(t *testing.T) {
	rt, err := NewRuntime(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })

	if err := rt.CancelOperation(context.Background(), "op-never-started"); err == nil {
		t.Fatal("expected an error for an operation with no live cancellation handle")
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
		t.Fatal("ReloadProject opened a second runtime (double DB handle)")
	}
	// Reconciliation must not re-run on reload: the reconciled flag stays set.
	rt.mu.Lock()
	reconciled := rt.reconciled[pid]
	rt.mu.Unlock()
	if !reconciled {
		t.Fatal("expected reconciled flag to remain set after reload")
	}
}
