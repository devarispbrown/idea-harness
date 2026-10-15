// Package mcp is a small, dependency-light Model Context Protocol server: the
// stdio transport (newline-delimited JSON-RPC 2.0), tools, and read-only
// resources. It knows nothing about idea-harness; internal/cli supplies the
// tools and resources and wires them to the daemon's REST API.
//
// Why in-house rather than an SDK: the surface this binary needs is one
// transport and a handful of methods (server/discover, initialize, ping,
// tools/list, tools/call, resources/list, resources/templates/list,
// resources/read). That is a few hundred lines with no new module, versus a
// dependency whose tracking of the protocol's 2026-07-28 stateless revision we
// would have to verify on every bump. The input-schema validator is the
// JSON Schema 2020-12 implementation the module already ships for config
// validation, so no new code path validates untrusted input.
//
// # Protocol eras
//
// The server is dual-era, as the 2026-07-28 revision's compatibility section
// describes:
//
//   - A request whose params._meta carries io.modelcontextprotocol/protocolVersion
//     is "modern": it is served statelessly, its version is checked against
//     ModernVersions, and a version this server does not speak is refused with
//     UnsupportedProtocolVersion (-32022) naming the versions it does.
//   - An `initialize` request is "legacy": the handshake answers with a
//     negotiated LegacyVersions entry, and later requests without modern _meta
//     are served under it. MCP hosts shipping today (Claude Code, Cursor,
//     Codex) still open with `initialize`, so refusing it would make the server
//     unusable from exactly the clients it exists for.
//
// Every result carries resultType "complete": modern clients require it and
// legacy clients ignore unknown fields.
//
// # Errors
//
// The two mechanisms the spec distinguishes are kept apart deliberately:
//
//   - JSON-RPC errors for things the caller's software got wrong -- a parse
//     failure, an unknown method, an unknown (or disabled) tool, arguments that
//     are not an object, an unknown resource.
//   - Tool results with isError: true for everything a model can act on --
//     arguments that fail the tool's input schema, and any error the tool
//     itself returns (a daemon 404/409, "no project registered here"). Those
//     reach the model, which is the point: "change set CHG-9 does not exist"
//     is something it can fix; a protocol error usually is not shown to it.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// ModernVersions are the per-request-metadata protocol revisions served.
var ModernVersions = []string{"2026-07-28"}

// LegacyVersions are the initialize-handshake revisions served, newest first.
// Older ones are accepted because the subset used here -- tools with
// inputSchema, text content, resources -- has not changed shape since
// 2024-11-05; newer fields (annotations, title, structuredContent) are
// additive and an older client ignores them.
var LegacyVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

// JSON-RPC and MCP error codes used by this server.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternalError  = -32603
	// codeUnsupportedProtocolVersion is defined by the 2026-07-28 revision.
	codeUnsupportedProtocolVersion = -32022
)

// Reserved _meta keys from the 2026-07-28 revision.
const (
	metaProtocolVersion    = "io.modelcontextprotocol/protocolVersion"
	metaClientCapabilities = "io.modelcontextprotocol/clientCapabilities"
	metaServerInfo         = "io.modelcontextprotocol/serverInfo"
)

// Implementation identifies the server (serverInfo).
type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// Annotations are the spec's ToolAnnotations. Every hint is always emitted
// rather than omitted when false: the spec's defaults for an absent hint are
// the pessimistic ones (destructiveHint true, openWorldHint true), so omitting
// a false destructiveHint would tell a client the opposite of what was meant.
type Annotations struct {
	Title           string `json:"title,omitempty"`
	ReadOnlyHint    bool   `json:"readOnlyHint"`
	DestructiveHint bool   `json:"destructiveHint"`
	IdempotentHint  bool   `json:"idempotentHint"`
	OpenWorldHint   bool   `json:"openWorldHint"`
}

// Result is what a tool handler returns on success.
type Result struct {
	// Text is the concise, human-and-model-readable answer.
	Text string
	// Structured is the machine form, sent as structuredContent. It must
	// marshal to a JSON object: the 2025-06-18 and 2025-11-25 revisions
	// require an object there, and a list is wrapped ({"items": [...]}) by the
	// caller so every era reads the same shape. Nil sends none.
	Structured any
}

// Handler runs a tool. args is the already schema-validated arguments object.
// A returned error becomes an isError result whose text is the error message.
type Handler func(ctx context.Context, args json.RawMessage) (Result, error)

// Tool is one callable tool.
type Tool struct {
	Name        string
	Title       string
	Description string
	// InputSchema is a JSON Schema (2020-12) object for the arguments.
	InputSchema map[string]any
	Annotations Annotations
	Handler     Handler
}

// Resource is one concrete readable resource.
type Resource struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ResourceTemplate is a parameterized resource (RFC 6570 URI template).
type ResourceTemplate struct {
	URITemplate string `json:"uriTemplate"`
	Name        string `json:"name"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ResourceContents is one item of a resources/read answer.
type ResourceContents struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType,omitempty"`
	Text     string `json:"text"`
}

// ErrResourceNotFound is returned (wrapped) by ReadResource for a URI that
// names nothing. It maps to -32602, which the 2026-07-28 revision mandates in
// place of the earlier -32002.
var ErrResourceNotFound = errors.New("resource not found")

// Server is a configured MCP server. The zero value is not usable; fill Info
// and Tools at least.
type Server struct {
	Info         Implementation
	Instructions string
	Tools        []Tool

	// Withheld names tools that exist but were not enabled by this server's
	// configuration, mapped to how to enable them. A call to one is still an
	// unknown tool (a JSON-RPC error, since it is absent from tools/list), but
	// the message says why instead of implying a typo.
	Withheld map[string]string

	ResourceTemplates []ResourceTemplate
	ListResources     func(ctx context.Context) ([]Resource, error)
	ReadResource      func(ctx context.Context, uri string) ([]ResourceContents, error)

	// Logf receives diagnostics. stdout is the protocol channel, so nothing
	// else may ever be written there; this is normally stderr.
	Logf func(format string, args ...any)

	compileOnce sync.Once
	compiled    map[string]*jsonschema.Schema
	compileErr  error
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// maxMessageBytes bounds one inbound line. Tool arguments here are short
// strings; a client sending more than this is broken, and an unbounded
// scanner buffer would let it take the process's memory with it.
const maxMessageBytes = 8 << 20

// Serve reads newline-delimited JSON-RPC messages from in and writes responses
// to out until in reaches EOF (the stdio transport's shutdown signal) or ctx
// is cancelled.
//
// Requests are handled one at a time, in order. Every tool here is one bounded
// HTTP round trip to a local daemon -- long work (an apply, a review rerun)
// returns an operation handle rather than waiting -- so concurrency would buy
// nothing and would make the daemon auto-start racy.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	if err := s.compile(); err != nil {
		return err
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), maxMessageBytes)
	enc := json.NewEncoder(out)
	enc.SetEscapeHTML(false)
	for sc.Scan() {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		resp := s.handleLine(ctx, line)
		if resp == nil {
			continue
		}
		// json.Encoder writes exactly one line per value, which is the
		// transport's framing: no embedded newlines, one message per line.
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return sc.Err()
}

func (s *Server) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// compile prepares each tool's input-schema validator once.
func (s *Server) compile() error {
	s.compileOnce.Do(func() {
		s.compiled = map[string]*jsonschema.Schema{}
		seen := map[string]bool{}
		for _, t := range s.Tools {
			if seen[t.Name] {
				s.compileErr = fmt.Errorf("mcp: duplicate tool %q", t.Name)
				return
			}
			seen[t.Name] = true
			raw, err := json.Marshal(t.InputSchema)
			if err != nil {
				s.compileErr = fmt.Errorf("mcp: tool %s: %w", t.Name, err)
				return
			}
			doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
			if err != nil {
				s.compileErr = fmt.Errorf("mcp: tool %s: %w", t.Name, err)
				return
			}
			c := jsonschema.NewCompiler()
			url := "https://idea-harness.local/mcp/tools/" + t.Name
			if err := c.AddResource(url, doc); err != nil {
				s.compileErr = fmt.Errorf("mcp: tool %s: %w", t.Name, err)
				return
			}
			sch, err := c.Compile(url)
			if err != nil {
				s.compileErr = fmt.Errorf("mcp: tool %s: %w", t.Name, err)
				return
			}
			s.compiled[t.Name] = sch
		}
	})
	return s.compileErr
}

// handleLine processes one inbound message and returns the response to write,
// or nil for a notification (or a client response, which this server never
// solicits).
func (s *Server) handleLine(ctx context.Context, line []byte) *response {
	if line[0] == '[' {
		// JSON-RPC batching was removed from MCP in 2025-06-18.
		return errResp(json.RawMessage("null"), codeInvalidRequest, "batch requests are not supported", nil)
	}
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return errResp(json.RawMessage("null"), codeParseError, "parse error: "+err.Error(), nil)
	}
	isNotification := len(req.ID) == 0
	if req.Method == "" {
		if isNotification {
			// A response from the client, or noise. Nothing was asked, so
			// nothing is owed.
			return nil
		}
		return errResp(req.ID, codeInvalidRequest, "missing method", nil)
	}
	if req.JSONRPC != "2.0" {
		if isNotification {
			return nil
		}
		return errResp(req.ID, codeInvalidRequest, `jsonrpc must be "2.0"`, nil)
	}
	if isNotification {
		// notifications/initialized, notifications/cancelled: requests are
		// handled synchronously, so a cancel always arrives after the answer
		// and there is nothing to stop.
		return nil
	}
	if string(req.ID) == "null" {
		return errResp(json.RawMessage("null"), codeInvalidRequest, "id must not be null", nil)
	}

	result, rerr := s.dispatch(ctx, req)
	if rerr != nil {
		return &response{JSONRPC: "2.0", ID: req.ID, Error: rerr}
	}
	return &response{JSONRPC: "2.0", ID: req.ID, Result: result}
}

func errResp(id json.RawMessage, code int, msg string, data any) *response {
	return &response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: msg, Data: data}}
}

// requestMeta extracts the modern per-request fields.
type requestMeta struct {
	modern       bool
	version      string
	capabilities json.RawMessage
}

func parseMeta(params json.RawMessage) requestMeta {
	var p struct {
		Meta map[string]json.RawMessage `json:"_meta"`
	}
	if len(params) == 0 || json.Unmarshal(params, &p) != nil || p.Meta == nil {
		return requestMeta{}
	}
	raw, ok := p.Meta[metaProtocolVersion]
	if !ok {
		return requestMeta{}
	}
	var v string
	_ = json.Unmarshal(raw, &v)
	return requestMeta{modern: true, version: v, capabilities: p.Meta[metaClientCapabilities]}
}

func (s *Server) dispatch(ctx context.Context, req request) (result map[string]any, rerr *rpcError) {
	defer func() {
		if r := recover(); r != nil {
			s.logf("mcp: panic handling %s: %v", req.Method, r)
			result, rerr = nil, &rpcError{Code: codeInternalError, Message: fmt.Sprintf("internal error handling %s", req.Method)}
		}
	}()

	if req.Method == "initialize" {
		return s.initialize(req.Params)
	}

	meta := parseMeta(req.Params)
	if meta.modern {
		if !slices.Contains(ModernVersions, meta.version) {
			return nil, &rpcError{
				Code:    codeUnsupportedProtocolVersion,
				Message: "Unsupported protocol version",
				Data:    map[string]any{"supported": s.supportedVersions(), "requested": meta.version},
			}
		}
		if len(meta.capabilities) == 0 {
			return nil, &rpcError{Code: codeInvalidParams, Message: "missing required _meta field " + metaClientCapabilities}
		}
	} else if req.Method == "server/discover" {
		// Discovery is modern-only; a request without the version field is
		// malformed under the revision that defines the method.
		return nil, &rpcError{Code: codeInvalidParams, Message: "missing required _meta field " + metaProtocolVersion}
	}

	var res map[string]any
	switch req.Method {
	case "server/discover":
		res = map[string]any{
			"supportedVersions": s.supportedVersions(),
			"capabilities":      s.capabilities(),
		}
		if s.Instructions != "" {
			res["instructions"] = s.Instructions
		}
	case "ping":
		res = map[string]any{}
	case "tools/list":
		res = map[string]any{"tools": s.toolList()}
	case "tools/call":
		res, rerr = s.callTool(ctx, req.Params)
	case "resources/list":
		res, rerr = s.listResources(ctx)
	case "resources/templates/list":
		tmpl := s.ResourceTemplates
		if tmpl == nil {
			tmpl = []ResourceTemplate{}
		}
		res = map[string]any{"resourceTemplates": tmpl}
	case "resources/read":
		res, rerr = s.readResource(ctx, req.Params)
	default:
		return nil, &rpcError{Code: codeMethodNotFound, Message: "method not found: " + req.Method}
	}
	if rerr != nil {
		return nil, rerr
	}
	res["resultType"] = "complete"
	if meta.modern {
		res["_meta"] = map[string]any{metaServerInfo: s.Info}
	}
	return res, nil
}

// supportedVersions lists every version served, modern first.
func (s *Server) supportedVersions() []string {
	return append(slices.Clone(ModernVersions), LegacyVersions...)
}

func (s *Server) capabilities() map[string]any {
	caps := map[string]any{"tools": map[string]any{"listChanged": false}}
	if s.ListResources != nil || s.ReadResource != nil || len(s.ResourceTemplates) > 0 {
		caps["resources"] = map[string]any{}
	}
	return caps
}

// initialize answers the legacy handshake.
func (s *Server) initialize(params json.RawMessage) (map[string]any, *rpcError) {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpcError{Code: codeInvalidParams, Message: "invalid initialize params: " + err.Error()}
		}
	}
	// The legacy rule: echo the client's version when supported, otherwise
	// offer the newest this server speaks and let the client decide.
	//
	// Nothing is remembered from the handshake: every version served here
	// answers every method identically, so there is no per-connection state
	// for a later request to depend on.
	version := LegacyVersions[0]
	if slices.Contains(LegacyVersions, p.ProtocolVersion) {
		version = p.ProtocolVersion
	}
	res := map[string]any{
		"protocolVersion": version,
		"capabilities":    s.capabilities(),
		"serverInfo":      s.Info,
		"resultType":      "complete",
	}
	if s.Instructions != "" {
		res["instructions"] = s.Instructions
	}
	return res, nil
}

type toolDescriptor struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations Annotations    `json:"annotations"`
}

// toolList is deterministic -- the table's own order -- so a client can cache
// it and a model's prompt cache stays warm, as the spec recommends.
func (s *Server) toolList() []toolDescriptor {
	out := make([]toolDescriptor, 0, len(s.Tools))
	for _, t := range s.Tools {
		a := t.Annotations
		if a.Title == "" {
			a.Title = t.Title
		}
		out = append(out, toolDescriptor{Name: t.Name, Title: t.Title, Description: t.Description, InputSchema: t.InputSchema, Annotations: a})
	}
	return out
}

func (s *Server) findTool(name string) *Tool {
	for i := range s.Tools {
		if s.Tools[i].Name == name {
			return &s.Tools[i]
		}
	}
	return nil
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage) (map[string]any, *rpcError) {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if len(params) == 0 || json.Unmarshal(params, &p) != nil || p.Name == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call needs params.name"}
	}
	t := s.findTool(p.Name)
	if t == nil {
		msg := "Unknown tool: " + p.Name
		if why, ok := s.Withheld[p.Name]; ok {
			msg += " (" + why + ")"
		}
		return nil, &rpcError{Code: codeInvalidParams, Message: msg}
	}
	args := bytes.TrimSpace(p.Arguments)
	if len(args) == 0 || string(args) == "null" {
		args = []byte("{}")
	}
	if args[0] != '{' {
		// Not an object is a malformed CallToolRequest, not a value a model
		// picked badly, so it is a protocol error.
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call arguments must be a JSON object"}
	}
	var inst any
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.UseNumber()
	if err := dec.Decode(&inst); err != nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "tools/call arguments are not valid JSON: " + err.Error()}
	}
	if err := s.compiled[t.Name].Validate(inst); err != nil {
		// An input validation error is a tool execution error by the spec's
		// own example: the model chose the value and can choose again.
		return toolError("invalid arguments for " + t.Name + ": " + validationMessage(err)), nil
	}
	r, err := t.Handler(ctx, args)
	if err != nil {
		return toolError(err.Error()), nil
	}
	return toolResult(r), nil
}

// validationMessage flattens the validator's multi-line report to one line a
// model can read. The report's first line names the schema's internal URL,
// which means nothing to a caller; the "- at '<path>': <problem>" lines are
// the content.
func validationMessage(err error) string {
	var parts []string
	for _, l := range strings.Split(err.Error(), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "- ") {
			parts = append(parts, strings.TrimPrefix(l, "- "))
		}
	}
	if len(parts) == 0 {
		return err.Error()
	}
	return strings.Join(parts, "; ")
}

func toolError(msg string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": msg}},
		"isError": true,
	}
}

func toolResult(r Result) map[string]any {
	text := r.Text
	if text == "" && r.Structured != nil {
		// The spec asks for the serialized JSON in a text block when there is
		// no better rendering, for clients that ignore structuredContent.
		if b, err := json.Marshal(r.Structured); err == nil {
			text = string(b)
		}
	}
	res := map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": false,
	}
	if r.Structured != nil {
		res["structuredContent"] = r.Structured
	}
	return res
}

func (s *Server) listResources(ctx context.Context) (map[string]any, *rpcError) {
	if s.ListResources == nil {
		return map[string]any{"resources": []Resource{}}, nil
	}
	list, err := s.ListResources(ctx)
	if err != nil {
		// The list is advisory (templates still work), and a host lists
		// resources eagerly at startup -- before the user has necessarily run
		// `idea init` here. An error would surface as a broken server; an empty
		// list is the truth about what can be read right now.
		s.logf("mcp: resources/list: %v", err)
		list = nil
	}
	if list == nil {
		list = []Resource{}
	}
	return map[string]any{"resources": list}, nil
}

func (s *Server) readResource(ctx context.Context, params json.RawMessage) (map[string]any, *rpcError) {
	var p struct {
		URI string `json:"uri"`
	}
	if len(params) == 0 || json.Unmarshal(params, &p) != nil || p.URI == "" {
		return nil, &rpcError{Code: codeInvalidParams, Message: "resources/read needs params.uri"}
	}
	if s.ReadResource == nil {
		return nil, &rpcError{Code: codeInvalidParams, Message: "Resource not found", Data: map[string]any{"uri": p.URI}}
	}
	contents, err := s.ReadResource(ctx, p.URI)
	if err != nil {
		if errors.Is(err, ErrResourceNotFound) {
			return nil, &rpcError{Code: codeInvalidParams, Message: err.Error(), Data: map[string]any{"uri": p.URI}}
		}
		return nil, &rpcError{Code: codeInternalError, Message: err.Error(), Data: map[string]any{"uri": p.URI}}
	}
	if len(contents) == 0 {
		// The spec forbids an empty contents array for a missing resource.
		return nil, &rpcError{Code: codeInvalidParams, Message: "Resource not found", Data: map[string]any{"uri": p.URI}}
	}
	return map[string]any{"contents": contents}, nil
}
