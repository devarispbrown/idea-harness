package daemon

import (
	"context"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
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

// An operation launched once shutdown has begun -- by a handler still running
// when the HTTP drain gave up -- must not run uncancelled past the stop.
func TestOperationLaunchedDuringShutdownStartsCancelled(t *testing.T) {
	rt, err := NewRuntime(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	rt.beginShutdown()
	cancelled := make(chan bool, 1)
	rt.launchOperation("late", func(ctx context.Context) { cancelled <- ctx.Err() != nil })
	select {
	case c := <-cancelled:
		if !c {
			t.Error("an operation launched during shutdown ran with a live context")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the late operation never ran")
	}
}

// Operations wait for the startup sweep, so the sweep can never remove one of
// this daemon's own agents.
func TestOperationsWaitForTheStartupSweep(t *testing.T) {
	rt, err := NewRuntime(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	rt.holdOperationsUntilSwept()
	started := make(chan struct{})
	rt.launchOperation("op", func(ctx context.Context) { close(started) })
	select {
	case <-started:
		t.Fatal("an operation started before the sweep finished")
	case <-time.After(100 * time.Millisecond):
	}
	rt.markSwept()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("the operation never started after the sweep")
	}
}

// A second daemon must fail on the taken address BEFORE it does anything that
// assumes it is the only one: it used to sweep the running daemon's agents
// and overwrite its pidfile first.
func TestSecondDaemonTouchesNothing(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	state := t.TempDir()
	marker := filepath.Join(state, "projects", "p1", "work", ".agent-home-live")
	if err := os.MkdirAll(marker, 0o700); err != nil {
		t.Fatal(err)
	}
	ready := false
	err = Run(ln.Addr().String(), state, slog.New(slog.NewTextHandler(io.Discard, nil)), func() { ready = true })
	if err == nil {
		t.Fatal("a second daemon started on a taken address")
	}
	if ready {
		t.Error("the second daemon claimed readiness (and would have written its pid)")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the second daemon swept the running daemon's agent: %v", err)
	}
}

// Two daemons on different addresses must not share one state directory.
func TestStateDirIsLocked(t *testing.T) {
	state := t.TempDir()
	unlock, err := lockStateDir(state)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := lockStateDir(state); err == nil {
		t.Error("a second lock on the same state directory succeeded")
	}
}
