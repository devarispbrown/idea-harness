package daemon

import (
	"context"
	"testing"
	"time"
)

// `idea daemon stop` sends SIGTERM, and Go used to exit at once: no operation
// was cancelled, so a running agent container was never removed and kept the
// provider key. Shutdown cancels every operation and waits for it to unwind.
func TestShutdownCancelsAndWaitsForOperations(t *testing.T) {
	rt, err := NewRuntime(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	unwound := make(chan struct{})
	rt.launchOperation("op-1", func(ctx context.Context) {
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond) // the cleanup a cancelled execute does
		close(unwound)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rt.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-unwound:
	default:
		t.Fatal("Shutdown returned before the operation finished cleaning up")
	}
}

// Bounded: an operation that ignores cancellation must not hold the stop open.
func TestShutdownGivesUpAtItsDeadline(t *testing.T) {
	rt, err := NewRuntime(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	release := make(chan struct{})
	defer close(release)
	rt.launchOperation("op-stuck", func(ctx context.Context) { <-release })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := rt.Shutdown(ctx); err == nil {
		t.Fatal("Shutdown reported success with an operation still running")
	}
}
