package daemon

import (
	"context"
	"testing"
	"time"
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
