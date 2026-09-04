package daemon

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestAPanickingOperationDoesNotKillTheDaemon pins the recovery added with the
// initiative claim.
//
// launchOperation's goroutines are detached, so net/http's per-request recovery
// never reaches them: one panic in a pipeline took the whole process down,
// including any other operation sitting between committing files and marking a
// change set applied. With the atomic claim in place it would also have stranded
// the initiative in "running" with no way out.
//
// The assertion is that the process survives and the deferred cleanup still
// runs. Without the recover, this test crashes the test binary rather than
// failing -- which is the point.
func TestAPanickingOperationDoesNotKillTheDaemon(t *testing.T) {
	rt, _, _ := newTestServer(t)

	var wg sync.WaitGroup
	wg.Add(1)
	rt.launchOperation("op-that-panics", func(context.Context) {
		defer wg.Done()
		panic("boom from a pipeline goroutine")
	})

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the panicking operation never returned")
	}

	// The cancel handle must be released, or every later cancel leaks. Polled
	// rather than read once: wg.Done() is deferred inside fn, so it fires before
	// launchOperation's own deferred cleanup.
	deadline := time.Now().Add(5 * time.Second)
	for {
		rt.cancelMu.Lock()
		_, stillRegistered := rt.cancels["op-that-panics"]
		rt.cancelMu.Unlock()
		if !stillRegistered {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the cancel handle survived the panic; deferred cleanup did not run")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
