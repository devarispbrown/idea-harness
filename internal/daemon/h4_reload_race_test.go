package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/project"
)

// TestReloadProjectDoesNotRaceConcurrentReaders is review H4's regression
// test.
//
// ReloadProject used to mutate a *projectRuntime's fields (cfg, dotEnv,
// registry, blobs, redactor, cfgMTime) in place, under r.mu, while roughly
// twenty read sites across the daemon package (executor_status.go's
// executorStatus, handlers.go's provider/model/queue/evidence/redaction call
// sites, ...) read pr.cfg/pr.registry/pr.redactor/pr.blobs/pr.dotEnv
// directly off a *projectRuntime returned by EnsureProject, with no lock at
// all -- the doc comment on ReloadProject already claimed a pointer swap
// that the code did not perform. This is exactly the shape the review's own
// race detector run caught: a write to these fields racing a read of them
// with no synchronization between the two, and it does not depend on timing
// to reproduce -- go test -race flags ANY interleaving where a write and an
// unsynchronized read touch the same memory concurrently, not just the rare
// ones that also happen to observe a wrong value.
//
// One goroutine hammers ReloadProject; the other hammers EnsureProject and
// reads every field a real handler reads, exactly the way handlers do (no
// lock of their own -- r.mu only ever protected the map, never a
// *projectRuntime's own fields). Run under `go test -race`, this is
// deterministic: it does not matter whether the race detector happens to
// interleave them close enough to flag it "sometimes" -- for the in-place
// mutation this replaces, every run does.
func TestReloadProjectDoesNotRaceConcurrentReaders(t *testing.T) {
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
	if _, err := rt.EnsureProject(ctx, pid); err != nil {
		t.Fatal(err)
	}

	var stop atomic.Bool
	var reloadErrs, readErrs int64
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for !stop.Load() {
			if _, err := rt.ReloadProject(ctx, pid); err != nil {
				atomic.AddInt64(&reloadErrs, 1)
			}
		}
	}()

	go func() {
		defer wg.Done()
		for !stop.Load() {
			pr, err := rt.EnsureProject(ctx, pid)
			if err != nil {
				atomic.AddInt64(&readErrs, 1)
				continue
			}
			// Exactly what executorStatus, buildProvidersView, BuildQueue,
			// ComputeEvidenceTests and every redactor call site in
			// handlers.go do: read these fields straight off pr, with no
			// lock of the caller's own.
			_ = pr.cfg.Harness.Executor.Kind
			_ = pr.registry
			_ = pr.redactor
			_ = pr.blobs
			_ = pr.rawBlobs
			_ = pr.dotEnv
			_ = pr.cfgMTime
		}
	}()

	time.Sleep(150 * time.Millisecond)
	stop.Store(true)
	wg.Wait()

	if reloadErrs > 0 {
		t.Fatalf("ReloadProject returned %d error(s) under concurrent load", reloadErrs)
	}
	if readErrs > 0 {
		t.Fatalf("EnsureProject returned %d error(s) under concurrent load", readErrs)
	}
}
