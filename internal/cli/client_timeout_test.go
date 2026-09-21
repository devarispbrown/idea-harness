package cli

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// hangingListener accepts TCP connections and never answers them -- the shape
// a wedged/overloaded daemon presents to a client, and the deterministic way
// to trigger a client-side HTTP timeout fast in a test, without needing a
// real slow handler.
//
// review LOW #12: the accepted conn used to be a bare loop-local variable,
// discarded (`_ = c`) rather than kept alive -- once nothing referenced it,
// it became eligible for GC at any point, and *net.TCPConn's finalizer
// closes the underlying file descriptor when collected. A close that lands
// before the client's 50ms timeout fires sends it a TCP RST instead: the
// client observes "connection reset by peer", not the deadline-exceeded
// timeout this test exists to translate, and fails without hanging or a
// wrong-looking diff, plausibly once in several thousand runs (GC timing
// dependent) -- ICP re-run reported roughly 1 in 7 full-suite runs, which a
// full suite's added GC pressure from every other package's tests running
// concurrently makes far likelier to actually observe. Keeping every
// accepted conn referenced until the test itself closes it in Cleanup makes
// the collection nondeterminism impossible, not just unlikely.
func hangingListener(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var accepted []net.Conn
	t.Cleanup(func() {
		ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range accepted {
			c.Close()
		}
	})
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			accepted = append(accepted, c) // kept alive (and explicitly closed) until Cleanup, never GC-finalized mid-test
			mu.Unlock()
		}
	}()
	return ln
}

// TestClientTimeoutNamesTheActionAndHowToCheck is ICP re-run item C: a
// client-side HTTP timeout used to surface as a bare "context deadline
// exceeded" (net/http's own wording) while the daemon kept the action running
// server-side -- leaving no clue that anything was still in flight, let alone
// how to check. The message must name what timed out and how to check on it.
func TestClientTimeoutNamesTheActionAndHowToCheck(t *testing.T) {
	ln := hangingListener(t)
	c := &client{
		baseURL: "http://" + ln.Addr().String(),
		token:   "t",
		http:    &http.Client{Timeout: 50 * time.Millisecond},
	}

	var out map[string]string
	err := c.do(http.MethodPost, "/api/v1/projects/p/change-sets/CHG-1/review", map[string]any{"verdict": "approved"}, &out)
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	msg := err.Error()
	if strings.Contains(msg, "context deadline exceeded") && !strings.Contains(msg, "still running") {
		t.Fatalf("timeout error was not translated into a helpful message: %q", msg)
	}
	if !strings.Contains(msg, "/api/v1/projects/p/change-sets/CHG-1/review") {
		t.Errorf("timeout message does not name what was in flight: %q", msg)
	}
	if !strings.Contains(msg, "idea status") && !strings.Contains(msg, "idea watch") {
		t.Errorf("timeout message does not say how to check whether it finished: %q", msg)
	}

	// doRaw goes through the same translation.
	if _, err := c.doRaw(http.MethodGet, "/api/v1/health"); err == nil {
		t.Fatal("expected a timeout error from doRaw")
	} else if !strings.Contains(err.Error(), "still running") {
		t.Errorf("doRaw's timeout error was not translated: %v", err)
	}
}

// TestNonTimeoutErrorsAreUnaffected proves timeoutHint is a no-op for any
// other error (e.g. connection refused) -- it must not misreport a wedged
// daemon as "not reachable" or vice versa.
func TestNonTimeoutErrorsAreUnaffected(t *testing.T) {
	// Nothing listens here: connection refused, not a timeout.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // now refusing connections

	c := &client{baseURL: "http://" + addr, token: "t", http: &http.Client{Timeout: 5 * time.Second}}
	var out map[string]string
	err = c.do(http.MethodGet, "/api/v1/health", nil, &out)
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if strings.Contains(err.Error(), "still running") {
		t.Errorf("a connection-refused error was mislabeled as a timeout: %v", err)
	}
}
