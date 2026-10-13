package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// modernMeta is the _meta every 2026-07-28 request carries.
var modernMeta = map[string]any{
	"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
	"io.modelcontextprotocol/clientInfo":         map[string]any{"name": "test", "version": "1"},
	"io.modelcontextprotocol/clientCapabilities": map[string]any{},
}

func testServer() *Server {
	return &Server{
		Info:         Implementation{Name: "idea-harness", Version: "test"},
		Instructions: "test instructions",
		Withheld:     map[string]string{"apply_thing": "not enabled: restart with --allow-apply"},
		Tools: []Tool{
			{
				Name:        "echo",
				Title:       "Echo",
				Description: "echoes",
				InputSchema: map[string]any{
					"type":                 "object",
					"properties":           map[string]any{"msg": map[string]any{"type": "string"}},
					"required":             []string{"msg"},
					"additionalProperties": false,
				},
				Annotations: Annotations{ReadOnlyHint: true},
				Handler: func(_ context.Context, args json.RawMessage) (Result, error) {
					var a struct{ Msg string }
					_ = json.Unmarshal(args, &a)
					return Result{Text: "echo: " + a.Msg, Structured: map[string]any{"msg": a.Msg}}, nil
				},
			},
			{
				Name:        "fails",
				Description: "always fails",
				InputSchema: map[string]any{"type": "object", "additionalProperties": false},
				Handler: func(context.Context, json.RawMessage) (Result, error) {
					return Result{}, errors.New("change set CHG-9 does not exist")
				},
			},
			{
				Name:        "panics",
				Description: "panics",
				InputSchema: map[string]any{"type": "object"},
				Handler: func(context.Context, json.RawMessage) (Result, error) {
					panic("boom")
				},
			},
			{
				Name:        "structured_only",
				Description: "no text",
				InputSchema: map[string]any{"type": "object"},
				Handler: func(context.Context, json.RawMessage) (Result, error) {
					return Result{Structured: map[string]any{"n": 1}}, nil
				},
			},
		},
		ResourceTemplates: []ResourceTemplate{{URITemplate: "idea://x/{id}", Name: "x"}},
		ListResources: func(context.Context) ([]Resource, error) {
			return nil, errors.New("no project registered here")
		},
		ReadResource: func(_ context.Context, uri string) ([]ResourceContents, error) {
			switch uri {
			case "idea://x/1":
				return []ResourceContents{{URI: uri, MimeType: "text/markdown", Text: "# one"}}, nil
			case "idea://x/empty":
				return nil, nil
			case "idea://x/broken":
				return nil, errors.New("daemon unreachable")
			}
			return nil, fmt.Errorf("%w: %s", ErrResourceNotFound, uri)
		},
	}
}

// exchange writes each line to a fresh server and returns every line it
// wrote back, decoded.
func exchange(t *testing.T, s *Server, lines ...string) []map[string]any {
	t.Helper()
	var out bytes.Buffer
	if err := s.Serve(context.Background(), strings.NewReader(strings.Join(lines, "\n")+"\n"), &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	var msgs []map[string]any
	for _, l := range strings.Split(strings.TrimRight(out.String(), "\n"), "\n") {
		if l == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("server wrote a non-JSON line %q: %v", l, err)
		}
		msgs = append(msgs, m)
	}
	return msgs
}

func req(id any, method string, params map[string]any) string {
	m := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		m["params"] = params
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func modern(params map[string]any) map[string]any {
	if params == nil {
		params = map[string]any{}
	}
	params["_meta"] = modernMeta
	return params
}

func one(t *testing.T, s *Server, line string) map[string]any {
	t.Helper()
	msgs := exchange(t, s, line)
	if len(msgs) != 1 {
		t.Fatalf("got %d responses, want 1: %v", len(msgs), msgs)
	}
	return msgs[0]
}

func errCode(t *testing.T, m map[string]any) int {
	t.Helper()
	e, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected a JSON-RPC error, got %v", m)
	}
	return int(e["code"].(float64))
}

func result(t *testing.T, m map[string]any) map[string]any {
	t.Helper()
	r, ok := m["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected a result, got %v", m)
	}
	return r
}

func TestInitializeNegotiatesLegacyVersion(t *testing.T) {
	for _, tc := range []struct{ asked, want string }{
		{"2025-06-18", "2025-06-18"},
		{"2025-11-25", "2025-11-25"},
		// Unknown: offer the newest legacy version and let the client decide.
		{"1999-01-01", "2025-11-25"},
	} {
		r := result(t, one(t, testServer(), req(1, "initialize", map[string]any{
			"protocolVersion": tc.asked, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "c", "version": "1"},
		})))
		if r["protocolVersion"] != tc.want {
			t.Errorf("asked %s: protocolVersion = %v, want %s", tc.asked, r["protocolVersion"], tc.want)
		}
		if r["serverInfo"].(map[string]any)["name"] != "idea-harness" {
			t.Errorf("serverInfo = %v", r["serverInfo"])
		}
		caps := r["capabilities"].(map[string]any)
		if _, ok := caps["tools"]; !ok {
			t.Errorf("capabilities lack tools: %v", caps)
		}
		if _, ok := caps["resources"]; !ok {
			t.Errorf("capabilities lack resources: %v", caps)
		}
		if r["instructions"] != "test instructions" {
			t.Errorf("instructions = %v", r["instructions"])
		}
	}
}

func TestLegacySessionServesToolsWithoutMeta(t *testing.T) {
	msgs := exchange(t, testServer(),
		req(1, "initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}}),
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		req(2, "tools/list", nil),
	)
	// The notification gets no response: two requests, two answers.
	if len(msgs) != 2 {
		t.Fatalf("got %d messages, want 2: %v", len(msgs), msgs)
	}
	r := result(t, msgs[1])
	if r["resultType"] != "complete" {
		t.Errorf("resultType = %v", r["resultType"])
	}
	if _, ok := r["_meta"]; ok {
		t.Errorf("a legacy result carried modern _meta: %v", r["_meta"])
	}
}

func TestServerDiscover(t *testing.T) {
	r := result(t, one(t, testServer(), req("d", "server/discover", modern(nil))))
	vs := r["supportedVersions"].([]any)
	if vs[0] != "2026-07-28" {
		t.Errorf("supportedVersions[0] = %v, want the modern version first", vs[0])
	}
	if r["resultType"] != "complete" {
		t.Errorf("resultType = %v", r["resultType"])
	}
	info := r["_meta"].(map[string]any)["io.modelcontextprotocol/serverInfo"].(map[string]any)
	if info["name"] != "idea-harness" {
		t.Errorf("serverInfo = %v", info)
	}
}

// A dual-era client probes with server/discover and falls back to
// initialize on any non-modern error; a modern server must answer a probe
// with an unsupported version with -32022 naming what it does support, or the
// client falls back wrongly.
func TestUnsupportedModernVersion(t *testing.T) {
	m := one(t, testServer(), req(1, "tools/list", map[string]any{"_meta": map[string]any{
		"io.modelcontextprotocol/protocolVersion":    "2099-01-01",
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}}))
	if got := errCode(t, m); got != -32022 {
		t.Fatalf("code = %d, want -32022", got)
	}
	data := m["error"].(map[string]any)["data"].(map[string]any)
	if data["requested"] != "2099-01-01" {
		t.Errorf("data.requested = %v", data["requested"])
	}
	if !strings.Contains(fmt.Sprint(data["supported"]), "2026-07-28") {
		t.Errorf("data.supported = %v", data["supported"])
	}
}

func TestModernRequestMissingCapabilities(t *testing.T) {
	m := one(t, testServer(), req(1, "tools/list", map[string]any{"_meta": map[string]any{
		"io.modelcontextprotocol/protocolVersion": "2026-07-28",
	}}))
	if got := errCode(t, m); got != -32602 {
		t.Fatalf("code = %d, want -32602", got)
	}
}

func TestDiscoverWithoutMetaIsInvalidParams(t *testing.T) {
	if got := errCode(t, one(t, testServer(), req(1, "server/discover", nil))); got != -32602 {
		t.Fatalf("code = %d, want -32602", got)
	}
}

func TestToolsListAnnotationsAreExplicit(t *testing.T) {
	r := result(t, one(t, testServer(), req(1, "tools/list", modern(nil))))
	tools := r["tools"].([]any)
	if len(tools) != 4 {
		t.Fatalf("got %d tools", len(tools))
	}
	echo := tools[0].(map[string]any)
	if echo["name"] != "echo" || echo["title"] != "Echo" {
		t.Errorf("first tool = %v; the list must keep the table's order", echo)
	}
	ann := echo["annotations"].(map[string]any)
	// destructiveHint defaults to TRUE when absent, so a false one has to be
	// on the wire, not omitted.
	for _, k := range []string{"readOnlyHint", "destructiveHint", "idempotentHint", "openWorldHint"} {
		if _, ok := ann[k]; !ok {
			t.Errorf("annotations lack %s: %v", k, ann)
		}
	}
	if ann["destructiveHint"] != false || ann["readOnlyHint"] != true {
		t.Errorf("annotations = %v", ann)
	}
	if ann["title"] != "Echo" {
		t.Errorf("annotations.title = %v, want the tool title", ann["title"])
	}
	if echo["inputSchema"].(map[string]any)["type"] != "object" {
		t.Errorf("inputSchema = %v", echo["inputSchema"])
	}
}

func call(name string, args any) map[string]any {
	p := map[string]any{"name": name}
	if args != nil {
		p["arguments"] = args
	}
	return modern(p)
}

func TestToolCallSuccess(t *testing.T) {
	r := result(t, one(t, testServer(), req(1, "tools/call", call("echo", map[string]any{"msg": "hi"}))))
	if r["isError"] != false {
		t.Errorf("isError = %v", r["isError"])
	}
	content := r["content"].([]any)[0].(map[string]any)
	if content["type"] != "text" || content["text"] != "echo: hi" {
		t.Errorf("content = %v", content)
	}
	if r["structuredContent"].(map[string]any)["msg"] != "hi" {
		t.Errorf("structuredContent = %v", r["structuredContent"])
	}
}

func TestToolCallStructuredOnlyGetsSerializedText(t *testing.T) {
	r := result(t, one(t, testServer(), req(1, "tools/call", call("structured_only", map[string]any{}))))
	if text := r["content"].([]any)[0].(map[string]any)["text"]; text != `{"n":1}` {
		t.Errorf("text = %v, want the serialized structured content", text)
	}
}

// Error mapping: what the model can fix is a tool result with isError;
// what the client's software got wrong is a JSON-RPC error.
func TestToolErrorMapping(t *testing.T) {
	t.Run("schema violation is isError", func(t *testing.T) {
		r := result(t, one(t, testServer(), req(1, "tools/call", call("echo", map[string]any{"msg": 3}))))
		if r["isError"] != true {
			t.Fatalf("isError = %v", r["isError"])
		}
		text := r["content"].([]any)[0].(map[string]any)["text"].(string)
		if !strings.Contains(text, "invalid arguments for echo") || strings.Contains(text, "idea-harness.local") {
			t.Errorf("text = %q; want a readable validation message without the schema's internal URL", text)
		}
	})
	t.Run("missing required argument is isError", func(t *testing.T) {
		r := result(t, one(t, testServer(), req(1, "tools/call", call("echo", nil))))
		if r["isError"] != true {
			t.Fatalf("isError = %v", r["isError"])
		}
		if text := r["content"].([]any)[0].(map[string]any)["text"].(string); !strings.Contains(text, "msg") {
			t.Errorf("text = %q; want it to name the missing property", text)
		}
	})
	t.Run("unexpected argument is isError", func(t *testing.T) {
		r := result(t, one(t, testServer(), req(1, "tools/call", call("fails", map[string]any{"x": 1}))))
		if r["isError"] != true {
			t.Fatalf("isError = %v", r["isError"])
		}
	})
	t.Run("handler error is isError with its message", func(t *testing.T) {
		r := result(t, one(t, testServer(), req(1, "tools/call", call("fails", map[string]any{}))))
		if r["isError"] != true {
			t.Fatalf("isError = %v", r["isError"])
		}
		if text := r["content"].([]any)[0].(map[string]any)["text"]; text != "change set CHG-9 does not exist" {
			t.Errorf("text = %v", text)
		}
	})
	t.Run("unknown tool is -32602", func(t *testing.T) {
		if got := errCode(t, one(t, testServer(), req(1, "tools/call", call("nope", map[string]any{})))); got != -32602 {
			t.Fatalf("code = %d", got)
		}
	})
	t.Run("withheld tool says how to enable it", func(t *testing.T) {
		m := one(t, testServer(), req(1, "tools/call", call("apply_thing", map[string]any{})))
		if got := errCode(t, m); got != -32602 {
			t.Fatalf("code = %d", got)
		}
		if msg := m["error"].(map[string]any)["message"].(string); !strings.Contains(msg, "--allow-apply") {
			t.Errorf("message = %q", msg)
		}
	})
	t.Run("non-object arguments are -32602", func(t *testing.T) {
		if got := errCode(t, one(t, testServer(), req(1, "tools/call", call("echo", []any{"hi"})))); got != -32602 {
			t.Fatalf("code = %d", got)
		}
	})
	t.Run("missing name is -32602", func(t *testing.T) {
		if got := errCode(t, one(t, testServer(), req(1, "tools/call", modern(nil)))); got != -32602 {
			t.Fatalf("code = %d", got)
		}
	})
	t.Run("panic is -32603 and the server keeps serving", func(t *testing.T) {
		msgs := exchange(t, testServer(),
			req(1, "tools/call", call("panics", map[string]any{})),
			req(2, "ping", modern(nil)),
		)
		if len(msgs) != 2 {
			t.Fatalf("got %d messages", len(msgs))
		}
		if got := errCode(t, msgs[0]); got != -32603 {
			t.Fatalf("code = %d", got)
		}
		result(t, msgs[1])
	})
}

func TestJSONRPCErrors(t *testing.T) {
	t.Run("parse error has a null id", func(t *testing.T) {
		m := one(t, testServer(), `{not json`)
		if got := errCode(t, m); got != -32700 {
			t.Fatalf("code = %d", got)
		}
		if id, ok := m["id"]; !ok || id != nil {
			t.Errorf("id = %v (present %v), want null", id, ok)
		}
	})
	t.Run("batch is refused", func(t *testing.T) {
		if got := errCode(t, one(t, testServer(), `[`+req(1, "ping", nil)+`]`)); got != -32600 {
			t.Fatalf("code = %d", got)
		}
	})
	t.Run("wrong jsonrpc version", func(t *testing.T) {
		if got := errCode(t, one(t, testServer(), `{"jsonrpc":"1.0","id":1,"method":"ping"}`)); got != -32600 {
			t.Fatalf("code = %d", got)
		}
	})
	t.Run("unknown method", func(t *testing.T) {
		m := one(t, testServer(), req(7, "prompts/list", modern(nil)))
		if got := errCode(t, m); got != -32601 {
			t.Fatalf("code = %d", got)
		}
		if m["id"].(float64) != 7 {
			t.Errorf("id = %v, want the request's", m["id"])
		}
	})
	t.Run("string ids round-trip", func(t *testing.T) {
		if m := one(t, testServer(), req("abc", "ping", modern(nil))); m["id"] != "abc" {
			t.Errorf("id = %v", m["id"])
		}
	})
	t.Run("unknown notifications are silent", func(t *testing.T) {
		if msgs := exchange(t, testServer(), `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`); len(msgs) != 0 {
			t.Fatalf("a notification was answered: %v", msgs)
		}
	})
}

func TestResources(t *testing.T) {
	t.Run("list degrades to empty when the backend cannot answer", func(t *testing.T) {
		var logged []string
		s := testServer()
		s.Logf = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
		r := result(t, one(t, s, req(1, "resources/list", modern(nil))))
		if list := r["resources"].([]any); len(list) != 0 {
			t.Errorf("resources = %v", list)
		}
		if len(logged) != 1 || !strings.Contains(logged[0], "no project registered") {
			t.Errorf("logged = %v; the reason belongs on stderr", logged)
		}
	})
	t.Run("templates", func(t *testing.T) {
		r := result(t, one(t, testServer(), req(1, "resources/templates/list", modern(nil))))
		if tmpl := r["resourceTemplates"].([]any); len(tmpl) != 1 || tmpl[0].(map[string]any)["uriTemplate"] != "idea://x/{id}" {
			t.Errorf("resourceTemplates = %v", tmpl)
		}
	})
	t.Run("read", func(t *testing.T) {
		r := result(t, one(t, testServer(), req(1, "resources/read", modern(map[string]any{"uri": "idea://x/1"}))))
		c := r["contents"].([]any)[0].(map[string]any)
		if c["text"] != "# one" || c["mimeType"] != "text/markdown" || c["uri"] != "idea://x/1" {
			t.Errorf("contents = %v", c)
		}
	})
	for uri, code := range map[string]int{
		"idea://x/missing": -32602,
		// An empty contents array would be ambiguous; the spec forbids it.
		"idea://x/empty":  -32602,
		"idea://x/broken": -32603,
	} {
		t.Run("read "+uri, func(t *testing.T) {
			m := one(t, testServer(), req(1, "resources/read", modern(map[string]any{"uri": uri})))
			if got := errCode(t, m); got != code {
				t.Fatalf("code = %d, want %d", got, code)
			}
		})
	}
}

func TestDuplicateToolNamesRefuseToServe(t *testing.T) {
	s := testServer()
	s.Tools = append(s.Tools, s.Tools[0])
	if err := s.Serve(context.Background(), strings.NewReader(""), io.Discard); err == nil {
		t.Fatal("Serve accepted two tools with one name")
	}
}

// Over a real pipe: responses come back one line at a time while the input
// stays open, and closing stdin -- the stdio shutdown signal -- ends Serve.
func TestServeOverPipe(t *testing.T) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- testServer().Serve(context.Background(), inR, outW)
		outW.Close()
	}()
	rd := bufio.NewReader(outR)
	for i := 1; i <= 2; i++ {
		if _, err := io.WriteString(inW, req(i, "tools/call", call("echo", map[string]any{"msg": "line\nbreak"}))+"\n"); err != nil {
			t.Fatal(err)
		}
		line, err := rd.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %q: %v", line, err)
		}
		if m["id"].(float64) != float64(i) {
			t.Errorf("id = %v, want %d", m["id"], i)
		}
	}
	inW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after stdin closed")
	}
}
