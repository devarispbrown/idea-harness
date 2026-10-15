package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// fakeSlack is an in-process stand-in for both halves of Slack: the Web API
// (chat.postMessage, auth.test, apps.connections.open, views.open), the
// response_url endpoint, and a Socket Mode websocket the test drives frame
// by frame.
type fakeSlack struct {
	t   *testing.T
	srv *httptest.Server

	mu         sync.Mutex
	posts      []map[string]any
	views      []map[string]any
	responses  []map[string]any
	opens      int
	authHeader []string
	nextTS     int
	// postError, when set, is returned as {"ok":false,"error":postError}.
	postError string
	// rateLimit makes the next chat.postMessage answer 429.
	rateLimit bool

	// sockets receives each accepted websocket connection.
	sockets chan *fakeSocket
}

type fakeSocket struct {
	conn *websocket.Conn
	acks chan map[string]any
}

func newFakeSlack(t *testing.T) *fakeSlack {
	f := &fakeSlack{t: t, sockets: make(chan *fakeSocket, 8), nextTS: 1}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/", f.api)
	mux.HandleFunc("/response/", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.responses = append(f.responses, body)
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/socket", f.socket)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeSlack) apiBase() string     { return f.srv.URL + "/api/" }
func (f *fakeSlack) responseURL() string { return f.srv.URL + "/response/1" }

func (f *fakeSlack) api(w http.ResponseWriter, r *http.Request) {
	method := strings.TrimPrefix(r.URL.Path, "/api/")
	body, _ := io.ReadAll(r.Body)
	var req map[string]any
	_ = json.Unmarshal(body, &req)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.authHeader = append(f.authHeader, r.Header.Get("Authorization"))
	w.Header().Set("Content-Type", "application/json")
	switch method {
	case "chat.postMessage":
		if f.rateLimit {
			f.rateLimit = false
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if f.postError != "" {
			fmt.Fprintf(w, `{"ok":false,"error":%q}`, f.postError)
			return
		}
		f.posts = append(f.posts, req)
		ts := fmt.Sprintf("1700000000.%06d", f.nextTS)
		f.nextTS++
		fmt.Fprintf(w, `{"ok":true,"ts":%q,"channel":%q}`, ts, req["channel"])
	case "auth.test":
		fmt.Fprint(w, `{"ok":true,"team":"Acme","user":"ideabot","user_id":"UBOT","bot_id":"B1"}`)
	case "apps.connections.open":
		f.opens++
		fmt.Fprintf(w, `{"ok":true,"url":%q}`, "ws"+strings.TrimPrefix(f.srv.URL, "http")+"/socket")
	case "views.open":
		f.views = append(f.views, req)
		fmt.Fprint(w, `{"ok":true}`)
	default:
		fmt.Fprint(w, `{"ok":false,"error":"unknown_method"}`)
	}
}

func (f *fakeSlack) socket(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c.SetReadLimit(4 << 20)
	fs := &fakeSocket{conn: c, acks: make(chan map[string]any, 16)}
	ctx := context.Background()
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"hello","num_connections":1}`))
	f.sockets <- fs
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		var m map[string]any
		_ = json.Unmarshal(data, &m)
		fs.acks <- m
	}
}

// send writes one envelope and returns its ack.
func (s *fakeSocket) send(t *testing.T, env map[string]any) map[string]any {
	t.Helper()
	data, _ := json.Marshal(env)
	if err := s.conn.Write(context.Background(), websocket.MessageText, data); err != nil {
		t.Fatalf("socket write: %v", err)
	}
	select {
	case a := <-s.acks:
		return a
	case <-time.After(5 * time.Second):
		t.Fatal("no ack within 5s (Slack's own window is 3s)")
	}
	return nil
}

func (f *fakeSlack) waitSocket(t *testing.T) *fakeSocket {
	t.Helper()
	select {
	case s := <-f.sockets:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("client never connected")
	}
	return nil
}

func (f *fakeSlack) waitResponses(t *testing.T, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		got := append([]map[string]any(nil), f.responses...)
		f.mu.Unlock()
		if len(got) >= n {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("want %d response_url replies, got %d: %v", n, len(got), got)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *fakeSlack) postsSnapshot() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.posts...)
}
