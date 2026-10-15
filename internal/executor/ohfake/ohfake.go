// Package ohfake is a fake OpenHands Agent Server for tests: the subset of
// the v1.54.0 API the openhands executor uses, over a real directory standing
// in for the server's filesystem. It lives outside _test.go files so the
// executor's tests and the workflow's end-to-end tests share one fake.
package ohfake

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// Server implements the subset of the OpenHands Agent Server API
// (v1.54.0) the executor uses, over a real directory standing in for the
// server's filesystem. The "agent" is a Go func the test supplies; it runs
// when the conversation starts and can block on a channel so a test can act
// mid-run (restart the daemon, cancel, delete the conversation).
type Server struct {
	t    *testing.T
	srv  *httptest.Server
	root string // the server's "/" on disk
	key  string // required X-Session-API-Key ("" = no auth)

	mu      sync.Mutex
	convs   map[string]*Conv
	calls   map[string]int // "METHOD /path" -> count
	fail    map[string]int // "METHOD /path" -> remaining 503s
	creates []map[string]any
	// excluded names are left out of archives, as the real route's default
	// excludes are (a file of that name is still downloadable on its own).
	excluded map[string]bool
	// agent is what runs when a conversation starts.
	Agent func(c *Conv, dir string)
}

type Conv struct {
	srv     *Server
	id      string
	dir     string
	mu      sync.Mutex
	status  string
	events  []map[string]any
	subs    []chan struct{}
	release chan struct{} // closed to let a blocking agent finish
}

func New(t *testing.T, key string) *Server {
	t.Helper()
	f := &Server{t: t, root: t.TempDir(), key: key, convs: map[string]*Conv{},
		calls: map[string]int{}, fail: map[string]int{}, excluded: map[string]bool{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(func() {
		f.mu.Lock()
		for _, c := range f.convs {
			c.finishRelease()
		}
		f.mu.Unlock()
		f.srv.Close()
	})
	return f
}

func (f *Server) URL() string { return f.srv.URL }

func (f *Server) local(remote string) string {
	return filepath.Join(f.root, filepath.FromSlash(remote))
}

func (f *Server) Count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[key]
}

func (f *Server) FailNext(key string, n int) {
	f.mu.Lock()
	f.fail[key] = n
	f.mu.Unlock()
}

func (f *Server) Conv(id string) *Conv {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.convs[id]
}

func (f *Server) OnlyConv(t *testing.T) *Conv {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.convs) != 1 {
		t.Fatalf("server has %d conversations, want 1", len(f.convs))
	}
	for _, c := range f.convs {
		return c
	}
	return nil
}

func (f *Server) DeleteConv(id string) {
	f.mu.Lock()
	c := f.convs[id]
	delete(f.convs, id)
	f.mu.Unlock()
	if c != nil {
		c.finishRelease()
	}
}

// Released is closed when the conversation is interrupted, paused or
// deleted, or the server shuts down: a blocking Agent func should select on
// it so it never outlives the test.
func (c *Conv) Released() <-chan struct{} { return c.release }

// ID is the conversation id.
func (c *Conv) ID() string { return c.id }

// Creates returns every POST /api/conversations body received.
func (f *Server) Creates() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.creates...)
}

// Exclude leaves files and directories with this base name out of archives,
// as the real archive route's default excludes do.
func (f *Server) Exclude(name string) {
	f.mu.Lock()
	f.excluded[name] = true
	f.mu.Unlock()
}

func (c *Conv) finishRelease() {
	c.mu.Lock()
	defer c.mu.Unlock()
	select {
	case <-c.release:
	default:
		close(c.release)
	}
}

func (c *Conv) SetStatus(s string) {
	c.mu.Lock()
	c.status = s
	c.mu.Unlock()
	c.Emit(map[string]any{"kind": "ConversationStateUpdateEvent", "source": "environment", "key": "execution_status", "value": s})
}

func (c *Conv) Status() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// emit appends an event and wakes websocket subscribers.
func (c *Conv) Emit(ev map[string]any) {
	c.mu.Lock()
	ev["id"] = fmt.Sprintf("ev-%04d", len(c.events)+1)
	ev["timestamp"] = time.Now().UTC().Format("2006-01-02T15:04:05.000000")
	c.events = append(c.events, ev)
	subs := append([]chan struct{}(nil), c.subs...)
	c.mu.Unlock()
	for _, s := range subs {
		select {
		case s <- struct{}{}:
		default:
		}
	}
}

func (c *Conv) Say(text string) {
	c.Emit(map[string]any{"kind": "MessageEvent", "source": "agent",
		"llm_message": map[string]any{"role": "assistant", "content": []map[string]string{{"type": "text", "text": text}}}})
}

func (c *Conv) EventCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.events)
}

func (f *Server) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

var unpackRe = regexp.MustCompile(`^set -e; rm -rf '([^']+)'; mkdir -p '([^']+)'; tar -xzf '([^']+)' -C '([^']+)'; rm -f '([^']+)'$`)

func (f *Server) serve(w http.ResponseWriter, r *http.Request) {
	route := r.Method + " " + r.URL.Path
	if strings.HasPrefix(r.URL.Path, "/api/conversations/") {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/conversations/"), "/")
		if parts[0] != "count" && parts[0] != "search" {
			parts[0] = "{id}"
		}
		route = r.Method + " /api/conversations/" + strings.Join(parts, "/")
	}
	if strings.HasPrefix(r.URL.Path, "/sockets/events/") {
		route = "GET /sockets/events/{id}"
	}
	f.mu.Lock()
	f.calls[route]++
	if f.fail[route] > 0 {
		f.fail[route]--
		f.mu.Unlock()
		http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
		return
	}
	f.mu.Unlock()

	if route == "GET /sockets/events/{id}" {
		f.socket(w, r)
		return
	}
	if r.URL.Path == "/server_info" {
		f.writeJSON(w, 200, map[string]any{"title": "OpenHands Agent Server", "version": "1.54.0",
			"capabilities": []string{"idempotent_conversation_create_v1"}})
		return
	}
	if f.key != "" && r.Header.Get("X-Session-API-Key") != f.key {
		f.writeJSON(w, 401, map[string]string{"detail": "Unauthorized"})
		return
	}
	switch {
	case route == "GET /api/conversations/count":
		f.mu.Lock()
		n := len(f.convs)
		f.mu.Unlock()
		f.writeJSON(w, 200, n)
	case route == "POST /api/file/upload":
		f.upload(w, r)
	case route == "POST /api/bash/execute_bash_command":
		f.bash(w, r)
	case route == "GET /api/file/archive":
		f.archive(w, r)
	case route == "GET /api/file/download":
		p := r.URL.Query().Get("path")
		data, err := os.ReadFile(f.local(p))
		if err != nil {
			f.writeJSON(w, 404, map[string]string{"detail": "not found"})
			return
		}
		_, _ = w.Write(data)
	case route == "POST /api/conversations":
		f.start(w, r)
	default:
		f.conversationRoute(w, r)
	}
}

func (f *Server) upload(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	file, _, err := r.FormFile("file")
	if err != nil || !strings.HasPrefix(p, "/") {
		f.writeJSON(w, 400, map[string]string{"detail": "bad upload"})
		return
	}
	data, _ := io.ReadAll(file)
	_ = os.MkdirAll(filepath.Dir(f.local(p)), 0o755)
	if err := os.WriteFile(f.local(p), data, 0o644); err != nil {
		f.writeJSON(w, 500, map[string]string{"detail": err.Error()})
		return
	}
	f.writeJSON(w, 200, map[string]bool{"success": true})
}

// bash understands exactly one command: the executor's unpack.
func (f *Server) bash(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Command string `json:"command"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	m := unpackRe.FindStringSubmatch(body.Command)
	exit := func(code int, stderr string) {
		f.writeJSON(w, 200, map[string]any{"kind": "BashOutput", "exit_code": code, "stdout": "", "stderr": stderr})
	}
	if m == nil {
		exit(127, "fake server: unknown command")
		return
	}
	dir, archive := f.local(m[1]), f.local(m[3])
	_ = os.RemoveAll(dir)
	_ = os.MkdirAll(dir, 0o755)
	data, err := os.ReadFile(archive)
	if err != nil {
		exit(2, "tar: "+m[3]+": Cannot open: No such file or directory")
		return
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		exit(2, "tar: not gzip")
		return
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			exit(2, err.Error())
			return
		}
		dst := filepath.Join(dir, filepath.FromSlash(h.Name))
		_ = os.MkdirAll(filepath.Dir(dst), 0o755)
		b, _ := io.ReadAll(tr)
		_ = os.WriteFile(dst, b, os.FileMode(h.Mode))
	}
	_ = os.Remove(archive)
	exit(0, "")
}

// archive tars the directory the way the real v1.54.0 route does: entries
// rooted under the directory's base name, and an archive_manifest.json of
// metadata appended at the root. f.excluded is left out.
func (f *Server) archive(w http.ResponseWriter, r *http.Request) {
	dir := f.local(r.URL.Query().Get("path"))
	if _, err := os.Stat(dir); err != nil {
		f.writeJSON(w, 404, map[string]string{"detail": "not found"})
		return
	}
	base := filepath.Base(dir)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		f.mu.Lock()
		skip := f.excluded[filepath.Base(p)]
		f.mu.Unlock()
		if skip {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, _ := d.Info()
		if d.IsDir() {
			return tw.WriteHeader(&tar.Header{Name: base + "/" + rel + "/", Typeflag: tar.TypeDir, Mode: 0o755})
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, _ := os.Readlink(p)
			return tw.WriteHeader(&tar.Header{Name: base + "/" + rel, Typeflag: tar.TypeSymlink, Linkname: target})
		}
		data, _ := os.ReadFile(p)
		if err := tw.WriteHeader(&tar.Header{Name: base + "/" + rel, Typeflag: tar.TypeReg, Mode: int64(info.Mode().Perm()), Size: int64(len(data))}); err != nil {
			return err
		}
		_, err = tw.Write(data)
		return err
	})
	meta := []byte(`{"format":"tar.gz","excludes":["node_modules/"]}`)
	_ = tw.WriteHeader(&tar.Header{Name: base + "/archive_manifest.json", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(meta))})
	_, _ = tw.Write(meta)
	_ = tw.Close()
	_ = gz.Close()
	w.Header().Set("Content-Type", "application/gzip")
	_, _ = w.Write(buf.Bytes())
}

func (f *Server) start(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		f.writeJSON(w, 422, map[string]string{"detail": err.Error()})
		return
	}
	id, _ := body["conversation_id"].(string)
	ws, _ := body["workspace"].(map[string]any)
	dir, _ := ws["working_dir"].(string)
	f.mu.Lock()
	f.creates = append(f.creates, body)
	if c, ok := f.convs[id]; ok && id != "" {
		f.mu.Unlock()
		f.writeJSON(w, 200, map[string]any{"id": c.id, "execution_status": c.Status()})
		return
	}
	c := &Conv{srv: f, id: id, dir: dir, status: "idle", release: make(chan struct{})}
	f.convs[id] = c
	agent := f.Agent
	f.mu.Unlock()
	c.Emit(map[string]any{"kind": "MessageEvent", "source": "user",
		"llm_message": map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": "the brief"}}}})
	go func() {
		c.SetStatus("running")
		if agent != nil {
			agent(c, f.local(dir))
		}
		if c.Status() == "running" {
			c.SetStatus("finished")
		}
	}()
	f.writeJSON(w, 201, map[string]any{"id": id, "execution_status": "idle"})
}

func (f *Server) conversationRoute(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/conversations/"), "/")
	c := f.Conv(parts[0])
	if c == nil {
		f.writeJSON(w, 404, map[string]string{"detail": "Conversation not found"})
		return
	}
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		f.writeJSON(w, 200, map[string]any{"id": c.id, "execution_status": c.Status()})
	case len(parts) == 2 && (parts[1] == "interrupt" || parts[1] == "pause") && r.Method == http.MethodPost:
		c.SetStatus("paused")
		c.Emit(map[string]any{"kind": "PauseEvent", "source": "user"})
		c.finishRelease()
		f.writeJSON(w, 200, map[string]bool{"success": true})
	case len(parts) == 3 && parts[1] == "events" && parts[2] == "search":
		f.searchEvents(w, r, c)
	default:
		f.writeJSON(w, 404, map[string]string{"detail": "no route"})
	}
}

// searchEvents pages with inclusive page_id semantics, as the executor
// assumes: a page starts AT page_id.
func (f *Server) searchEvents(w http.ResponseWriter, r *http.Request, c *Conv) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	c.mu.Lock()
	events := append([]map[string]any(nil), c.events...)
	c.mu.Unlock()
	start := 0
	if pid := r.URL.Query().Get("page_id"); pid != "" {
		start = -1
		for i, ev := range events {
			if ev["id"] == pid {
				start = i
				break
			}
		}
		if start < 0 {
			f.writeJSON(w, 400, map[string]string{"detail": "unknown page_id"})
			return
		}
	}
	end := start + limit
	var next any
	if end < len(events) {
		next = events[end]["id"]
	} else {
		end = len(events)
	}
	f.writeJSON(w, 200, map[string]any{"items": events[start:end], "next_page_id": next})
}

// socket implements /sockets/events/{id} with first-frame auth. It forwards
// a frame per new event.
func (f *Server) socket(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/sockets/events/")
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := r.Context()
	if f.key != "" {
		rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		_, data, err := conn.Read(rctx)
		cancel()
		var auth struct {
			Type string `json:"type"`
			Key  string `json:"session_api_key"`
		}
		if err != nil || json.Unmarshal(data, &auth) != nil || auth.Type != "auth" || auth.Key != f.key {
			conn.Close(4001, "Authentication failed")
			return
		}
	}
	c := f.Conv(id)
	if c == nil {
		conn.Close(4004, "Conversation not found")
		return
	}
	ch := make(chan struct{}, 1)
	c.mu.Lock()
	c.subs = append(c.subs, ch)
	c.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			if err := conn.Write(ctx, websocket.MessageText, []byte(`{"kind":"ConversationStateUpdateEvent"}`)); err != nil {
				return
			}
		}
	}
}
