package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestNewHTTPServerHasSensibleTimeoutsButNotWriteTimeout is review LOW #9:
// the daemon's *http.Server used to leave ReadHeaderTimeout/ReadTimeout/
// WriteTimeout/IdleTimeout all at their zero value (unbounded), so a client
// that opened a connection and never finished sending headers (or a body)
// tied up a goroutine and a file descriptor forever.
//
// WriteTimeout is asserted to stay exactly zero: GET .../events (sse.go)
// streams for as long as a browser tab is open, and Go's WriteTimeout is one
// hard wall-clock deadline covering an entire response write with no
// per-handler override -- any nonzero value would eventually cut that
// stream.
func TestNewHTTPServerHasSensibleTimeoutsButNotWriteTimeout(t *testing.T) {
	srv := newHTTPServer(http.NotFoundHandler(), func(net.Listener) context.Context { return context.Background() })

	if srv.ReadHeaderTimeout <= 0 {
		t.Error("ReadHeaderTimeout is unbounded")
	}
	if srv.ReadTimeout <= 0 {
		t.Error("ReadTimeout is unbounded")
	}
	if srv.IdleTimeout <= 0 {
		t.Error("IdleTimeout is unbounded")
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout = %v, want 0 (unbounded): it would eventually sever the /events SSE stream", srv.WriteTimeout)
	}
	// Sane relative ordering: reading headers should not itself be allowed
	// to take as long as the whole request.
	if srv.ReadHeaderTimeout > srv.ReadTimeout {
		t.Errorf("ReadHeaderTimeout (%v) > ReadTimeout (%v)", srv.ReadHeaderTimeout, srv.ReadTimeout)
	}
}

// TestEventsStreamSurvivesPastReadHeaderTimeout is an end-to-end check
// against a REAL http.Server built by newHTTPServer (not httptest's own
// server, which the rest of this package's tests use and which never
// exercises Run's timeout configuration): a GET .../events connection held
// open past ReadHeaderTimeout (shrunk here so the test does not need a
// multi-second sleep) must still be alive and still deliver a real event,
// proving that timeout is scoped to reading the NEXT request's headers, not
// to the lifetime of an already-established streaming response.
func TestEventsStreamSurvivesPastReadHeaderTimeout(t *testing.T) {
	rt, err := NewRuntime(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := newHTTPServer(NewServer(rt).Handler(), func(net.Listener) context.Context { return base })
	// Shrunk so the test proves survival past it without an actual
	// multi-second sleep; unrelated to WriteTimeout, which stays 0.
	srv.ReadHeaderTimeout = 200 * time.Millisecond
	go srv.Serve(ln)
	defer srv.Close()

	baseURL := "http://" + ln.Addr().String()
	token := rt.Token()

	dir := filepath.Join(t.TempDir(), "proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	createResp := httpDo(t, http.MethodPost, baseURL+"/api/v1/projects", token, `{"root_path":"`+dir+`"}`)
	var created struct {
		ProjectID string `json:"project_id"`
	}
	if err := json.NewDecoder(createResp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	createResp.Body.Close()

	req, err := http.NewRequest(http.MethodGet, baseURL+"/api/v1/projects/"+created.ProjectID+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Idea-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET .../events: status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	// Outlive ReadHeaderTimeout, then prove the connection is still usable
	// by triggering a real event and reading it off the still-open stream.
	time.Sleep(3 * srv.ReadHeaderTimeout)

	pr, err := rt.EnsureProject(context.Background(), created.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := pr.db.CreateInitiative(context.Background(), ini); err != nil {
		t.Fatal(err)
	}

	line := make(chan string, 1)
	go func() {
		buf := make([]byte, 4096)
		n, _ := resp.Body.Read(buf)
		line <- string(buf[:n])
	}()
	select {
	case got := <-line:
		if !strings.Contains(got, "data:") {
			t.Fatalf("stream output = %q, want an SSE data line for the new initiative", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event arrived on the stream (held open past ReadHeaderTimeout) within 5s -- the timeout may have severed the connection")
	}
}

// httpDo issues a request with the daemon's auth header, failing the test on
// a transport error (not on a non-2xx status, which some callers assert
// themselves).
func httpDo(t *testing.T, method, url, token, body string) *http.Response {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Idea-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
