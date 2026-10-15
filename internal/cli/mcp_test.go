package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// These tests drive `idea mcp` the way an MCP host does -- JSON-RPC lines in
// on stdin, lines out on stdout -- against the fake daemon, so what is asserted
// is the whole path: dispatch, flag tiers, the HTTP calls each tool makes, and
// how daemon failures surface.

// mcpSession is a running `idea mcp` with its stdin and stdout as pipes.
type mcpSession struct {
	t    *testing.T
	in   *io.PipeWriter
	out  *bufio.Reader
	done chan error
	next int
}

// startMCP runs `idea mcp <flags>` through the real command dispatch.
func startMCP(t *testing.T, flags ...string) *mcpSession {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	prev := mcpStdin
	mcpStdin = inR
	t.Cleanup(func() { mcpStdin = prev })

	s := &mcpSession{t: t, in: inW, out: bufio.NewReader(outR), done: make(chan error, 1)}
	go func() {
		err := Run(append([]string{"mcp"}, flags...), outW, io.Discard)
		outW.CloseWithError(fmt.Errorf("server exited: %v", err))
		s.done <- err
	}()
	t.Cleanup(func() {
		inW.Close()
		select {
		case <-s.done:
		case <-time.After(5 * time.Second):
			t.Error("idea mcp did not exit after stdin closed")
		}
	})
	// A legacy host's opening, which is what Claude Code, Cursor and Codex send.
	init := s.request("initialize", map[string]any{
		"protocolVersion": "2025-06-18",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "test-host", "version": "1"},
	})
	if init["result"].(map[string]any)["protocolVersion"] != "2025-06-18" {
		t.Fatalf("initialize = %v", init)
	}
	s.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	return s
}

func (s *mcpSession) send(line string) {
	s.t.Helper()
	if _, err := io.WriteString(s.in, line+"\n"); err != nil {
		s.t.Fatalf("write: %v", err)
	}
}

func (s *mcpSession) request(method string, params map[string]any) map[string]any {
	s.t.Helper()
	s.next++
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": s.next, "method": method, "params": params})
	s.send(string(b))
	line, err := s.out.ReadString('\n')
	if err != nil {
		s.t.Fatalf("read response to %s: %v", method, err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		s.t.Fatalf("stdout carried a non-JSON line %q: %v", line, err)
	}
	if m["id"].(float64) != float64(s.next) {
		s.t.Fatalf("response id %v, want %d", m["id"], s.next)
	}
	return m
}

func (s *mcpSession) toolNames() []string {
	s.t.Helper()
	res := s.request("tools/list", map[string]any{})["result"].(map[string]any)
	var names []string
	for _, tl := range res["tools"].([]any) {
		names = append(names, tl.(map[string]any)["name"].(string))
	}
	return names
}

// callTool returns the tools/call result, failing on a JSON-RPC error.
func (s *mcpSession) callTool(name string, args map[string]any) (text string, structured map[string]any, isError bool) {
	s.t.Helper()
	m := s.request("tools/call", map[string]any{"name": name, "arguments": args})
	res, ok := m["result"].(map[string]any)
	if !ok {
		s.t.Fatalf("tools/call %s: JSON-RPC error %v", name, m["error"])
	}
	text = res["content"].([]any)[0].(map[string]any)["text"].(string)
	structured, _ = res["structuredContent"].(map[string]any)
	return text, structured, res["isError"] == true
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// seedMCP registers what the MCP tools read beyond seedInitiative.
func (f *fakeDaemon) seedMCP() {
	f.seedInitiative()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}", map[string]any{"id": "ini-1", "title": "Build usage-based billing", "status": "needs_human_judgment"})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/next", map[string]any{"action": "review CHG-1"})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/guidance", map[string]any{"action": "Review CHG-1", "why": "the agent finished TASK-1"})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/decisions", []map[string]any{
		{"id": "dec-1", "code": "DEC-1", "question": "Which meter?", "options": []string{"events", "seats"}, "status": "open", "recommendation": "events"},
	})
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/artifacts", []map[string]any{
		{"id": "art-1", "kind": "prd", "version": 1, "status": "superseded"},
		{"id": "art-2", "kind": "prd", "version": 2, "status": "approved"},
	})
	f.json("GET /api/v1/projects/{projectId}/artifacts/{id}/body", map[string]any{"kind": "prd", "version": 2, "body": "# Billing PRD\n\nBill by usage."})
	f.json("GET /api/v1/projects/{projectId}/change-sets/{id}/diff", map[string]any{
		"patch": "--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n", "files": []map[string]any{{"path": "x"}}, "total_files": 1, "truncated": false,
	})
	f.mux.HandleFunc("GET /api/v1/projects/{projectId}/change-sets/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != "CHG-1" {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": `no change set matching "` + r.PathValue("id") + `"`})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "cs-1", "human_id": "CHG-1", "status": "review_required", "review_completed_at": "2026-10-01T00:00:00Z"})
	})
}

func TestMCPToolTiers(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedMCP()

	def := startMCP(t).toolNames()
	for _, want := range []string{"status", "list_decisions", "why", "show_change_set", "answer_question", "review_change_set", "verify", "new_initiative"} {
		if !contains(def, want) {
			t.Errorf("default tier lacks %s: %v", want, def)
		}
	}
	for _, gated := range []string{"apply_change_set", "revert_change_set"} {
		if contains(def, gated) {
			t.Errorf("default tier exposes %s without --allow-apply", gated)
		}
	}

	ro := startMCP(t, "--read-only").toolNames()
	for _, gated := range []string{"answer_question", "resolve_decision", "review_change_set", "new_initiative", "verify", "apply_change_set"} {
		if contains(ro, gated) {
			t.Errorf("--read-only exposes %s", gated)
		}
	}
	if !contains(ro, "status") || !contains(ro, "show_prd") {
		t.Errorf("--read-only lost the read tools: %v", ro)
	}

	all := startMCP(t, "--allow-apply").toolNames()
	if !contains(all, "apply_change_set") || !contains(all, "revert_change_set") || !contains(all, "review_change_set") {
		t.Errorf("--allow-apply tools = %v", all)
	}
}

// The hints a host shows its user before a call: everything that writes the
// working tree or overrides a recorded state is destructive, and the reads say
// they only read.
func TestMCPToolAnnotations(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedMCP()
	res := startMCP(t, "--allow-apply").request("tools/list", map[string]any{})["result"].(map[string]any)
	ann := map[string]map[string]any{}
	for _, tl := range res["tools"].([]any) {
		m := tl.(map[string]any)
		ann[m["name"].(string)] = m["annotations"].(map[string]any)
		if m["description"] == "" || m["inputSchema"].(map[string]any)["type"] != "object" {
			t.Errorf("%s: description/inputSchema missing: %v", m["name"], m)
		}
	}
	for _, name := range []string{"apply_change_set", "revert_change_set", "review_change_set"} {
		if ann[name]["destructiveHint"] != true || ann[name]["readOnlyHint"] != false {
			t.Errorf("%s annotations = %v, want destructive", name, ann[name])
		}
	}
	for _, name := range []string{"list_initiatives", "status", "next_step", "show_prd", "list_findings", "why", "impact", "trace", "gaps", "changed", "list_questions", "list_decisions", "list_change_sets", "show_change_set", "get_operation"} {
		if ann[name]["readOnlyHint"] != true || ann[name]["destructiveHint"] != false {
			t.Errorf("%s annotations = %v, want read-only", name, ann[name])
		}
	}
	for _, name := range []string{"answer_question", "resolve_decision", "new_initiative", "verify"} {
		if ann[name]["readOnlyHint"] != false || ann[name]["destructiveHint"] != false {
			t.Errorf("%s annotations = %v, want a non-destructive write", name, ann[name])
		}
	}
}

func TestMCPReadTools(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedMCP()
	f.seedTrace()
	s := startMCP(t)

	text, st, isErr := s.callTool("status", map[string]any{})
	if isErr {
		t.Fatalf("status isError: %s", text)
	}
	if !strings.Contains(text, "Build usage-based billing") || !strings.Contains(text, "review CHG-1") {
		t.Errorf("status text = %q", text)
	}
	if st["next"].(map[string]any)["action"] != "review CHG-1" {
		t.Errorf("status structured = %v", st)
	}

	text, st, _ = s.callTool("next_step", map[string]any{})
	if !strings.Contains(text, "review_change_set") {
		t.Errorf("next_step should name the tool that moves it: %q", text)
	}
	if st["action"] != "review CHG-1" {
		t.Errorf("next_step structured = %v", st)
	}

	text, st, _ = s.callTool("list_decisions", map[string]any{})
	if !strings.Contains(text, "DEC-1 [open] Which meter?") || !strings.Contains(text, "1. events") {
		t.Errorf("list_decisions text = %q", text)
	}
	if len(st["decisions"].([]any)) != 1 {
		t.Errorf("list_decisions structured = %v", st)
	}

	text, _, _ = s.callTool("show_prd", map[string]any{})
	if !strings.HasPrefix(text, "# Billing PRD") {
		t.Errorf("show_prd text = %q", text)
	}
	if !f.asked("/artifacts/art-2/body") {
		t.Errorf("show_prd did not read the LATEST prd; requests: %v", f.requests)
	}

	text, st, _ = s.callTool("trace", map[string]any{"type": "requirement", "ref": "REQ-1"})
	if !strings.Contains(text, "EV-1 —derives_from→ REQ-1") || !strings.Contains(text, "[fresh] via depends_on") {
		t.Errorf("trace text = %q", text)
	}
	if st["why"] == nil || st["impact"] == nil {
		t.Errorf("trace structured = %v", st)
	}
	if !f.asked("/why?id=req-1&type=requirement") {
		t.Errorf("trace did not resolve REQ-1 to its id; requests: %v", f.requests)
	}

	text, st, _ = s.callTool("show_change_set", map[string]any{"change_set": "CHG-1"})
	if !strings.Contains(text, "+b") || st["change_set"].(map[string]any)["human_id"] != "CHG-1" {
		t.Errorf("show_change_set = %q / %v", text, st)
	}

	// An empty list is [] in structuredContent, not null.
	_, st, _ = s.callTool("changed", map[string]any{})
	if entries, ok := st["entries"].([]any); !ok || len(entries) != 0 {
		t.Errorf("changed entries = %#v", st["entries"])
	}
}

// Every daemon refusal reaches the model as an isError result carrying the
// daemon's own message, which is what lets it correct itself.
func TestMCPDaemonErrorsAreToolErrors(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedMCP()
	f.fail("POST /api/v1/projects/{projectId}/change-sets/{id}/review", http.StatusConflict,
		"CHG-1 never finished its adversarial review; `idea review CHG-1 rerun` runs it")
	s := startMCP(t)

	text, _, isErr := s.callTool("review_change_set", map[string]any{"change_set": "CHG-1", "verdict": "approve"})
	if !isErr || !strings.Contains(text, "never finished its adversarial review") {
		t.Errorf("review 409: isError=%v text=%q", isErr, text)
	}

	text, _, isErr = s.callTool("show_change_set", map[string]any{"change_set": "CHG-9"})
	if !isErr || !strings.Contains(text, `no change set matching "CHG-9"`) {
		t.Errorf("unknown change set: isError=%v text=%q", isErr, text)
	}

	text, _, isErr = s.callTool("status", map[string]any{"initiative": "ini-nope"})
	if !isErr || !strings.Contains(text, `no initiative matching "ini-nope"`) {
		t.Errorf("unknown initiative: isError=%v text=%q", isErr, text)
	}

	// Schema-level: a verdict outside the enum never reaches the daemon.
	before := len(f.requests)
	text, _, isErr = s.callTool("review_change_set", map[string]any{"change_set": "CHG-1", "verdict": "lgtm"})
	if !isErr || !strings.Contains(text, "invalid arguments") {
		t.Errorf("bad verdict: isError=%v text=%q", isErr, text)
	}
	for _, r := range f.requests[before:] {
		if strings.Contains(r, "/review") {
			t.Errorf("an invalid verdict reached the daemon: %s", r)
		}
	}

	text, _, isErr = s.callTool("review_change_set", map[string]any{"change_set": "CHG-1", "verdict": "reject", "accept_findings": true})
	if !isErr || !strings.Contains(text, "accept_findings only applies") {
		t.Errorf("accept_findings with reject: isError=%v text=%q", isErr, text)
	}
}

func TestMCPApplyIsGatedByFlag(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedMCP()
	f.json("POST /api/v1/projects/{projectId}/change-sets/{id}/apply", map[string]string{"operation_id": "op-7"})

	// Default tier: not a tool at all. A protocol error, and the message says
	// how to enable it rather than implying a typo.
	m := startMCP(t).request("tools/call", map[string]any{"name": "apply_change_set", "arguments": map[string]any{"change_set": "CHG-1"}})
	e, ok := m["error"].(map[string]any)
	if !ok {
		t.Fatalf("apply without --allow-apply returned %v, want a JSON-RPC error", m)
	}
	if e["code"].(float64) != -32602 || !strings.Contains(e["message"].(string), "--allow-apply") {
		t.Errorf("error = %v", e)
	}
	if f.asked("/apply") {
		t.Fatal("apply reached the daemon without --allow-apply")
	}

	text, st, isErr := startMCP(t, "--allow-apply").callTool("apply_change_set", map[string]any{"change_set": "CHG-1"})
	if isErr || st["operation_id"] != "op-7" || !strings.Contains(text, "get_operation") {
		t.Errorf("apply: isError=%v text=%q structured=%v", isErr, text, st)
	}
	if !f.asked("POST /api/v1/projects/p1/change-sets/CHG-1/apply") {
		t.Errorf("apply did not POST; requests: %v", f.requests)
	}
}

func TestMCPJudgmentTools(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedMCP()
	var resolved map[string]string
	f.mux.HandleFunc("POST /api/v1/projects/{projectId}/decisions/{id}/resolve", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&resolved)
		w.WriteHeader(http.StatusOK)
	})
	f.json("POST /api/v1/projects/{projectId}/change-sets/{id}/review", map[string]string{})
	s := startMCP(t)

	// An option number means the option's text, exactly as `idea resolve`.
	text, st, isErr := s.callTool("resolve_decision", map[string]any{"decision": "DEC-1", "resolution": "2"})
	if isErr || resolved["resolution"] != "seats" || st["resolution"] != "seats" {
		t.Errorf("resolve: isError=%v text=%q body=%v", isErr, text, resolved)
	}

	text, st, isErr = s.callTool("review_change_set", map[string]any{"change_set": "CHG-1", "verdict": "approve"})
	if isErr || st["verdict"] != "approved" {
		t.Errorf("review approve: isError=%v text=%q structured=%v", isErr, text, st)
	}
}

func TestMCPVerifyReportsUnmetWithoutFailing(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedMCP()
	f.verifyCoverage = map[string]any{"status": "complete", "coverage": []map[string]any{{"code": "REQ-1", "pass": 0, "total": 1}}}
	text, st, isErr := startMCP(t).callTool("verify", map[string]any{"requirement": "REQ-1"})
	if isErr {
		t.Fatalf("an unmet gate is an answer, not a failed call: %q", text)
	}
	if st["verified"] != false || !strings.Contains(text, "REQ-1 0/1 verified") {
		t.Errorf("verify = %q / %v", text, st)
	}
	// The requirement is sent, and no evidence is: the tool cannot sign off.
	if len(f.verifyBodies) != 1 || f.verifyBodies[0] != `{"requirement":"REQ-1"}` {
		t.Errorf("verify bodies = %v", f.verifyBodies)
	}
}

func TestMCPResources(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedMCP()
	s := startMCP(t, "--read-only")

	list := s.request("resources/list", map[string]any{})["result"].(map[string]any)["resources"].([]any)
	if len(list) != 1 || list[0].(map[string]any)["uri"] != "idea://initiative/ini-1/prd" {
		t.Errorf("resources = %v (one per kind, latest wins)", list)
	}

	read := s.request("resources/read", map[string]any{"uri": "idea://initiative/ini-1/prd"})
	c := read["result"].(map[string]any)["contents"].([]any)[0].(map[string]any)
	if !strings.HasPrefix(c["text"].(string), "# Billing PRD") || c["mimeType"] != "text/markdown" {
		t.Errorf("read = %v", c)
	}

	for _, uri := range []string{"idea://initiative/ini-1/threat_model", "idea://initiative/ini-9/prd", "idea://other/x"} {
		m := s.request("resources/read", map[string]any{"uri": uri})
		e, ok := m["error"].(map[string]any)
		if !ok || e["code"].(float64) != -32602 {
			t.Errorf("read %s = %v, want -32602", uri, m)
		}
	}
}

func TestMCPFlagErrors(t *testing.T) {
	newFakeDaemon(t)
	if _, _, err := run(t, "mcp", "--read-only", "--allow-apply"); err == nil || !strings.Contains(err.Error(), "contradict") {
		t.Errorf("--read-only --allow-apply: err = %v", err)
	}
	if _, _, err := run(t, "mcp", "serve"); err == nil || !strings.Contains(err.Error(), "takes no arguments") {
		t.Errorf("positional: err = %v", err)
	}
	if _, _, err := run(t, "mcp", "--dir", "/does/not/exist"); err == nil || !strings.Contains(err.Error(), "--dir") {
		t.Errorf("bad --dir: err = %v", err)
	}
}

// Nothing but protocol messages may reach stdout -- including when the
// project is not initialized, where every tool has to fail as a tool.
func TestMCPOutsideAProjectFailsAsToolError(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedMCP()
	t.Chdir(t.TempDir())
	text, _, isErr := startMCP(t).callTool("status", map[string]any{})
	if !isErr || !strings.Contains(text, "idea init") {
		t.Errorf("status outside a project: isError=%v text=%q", isErr, text)
	}
}

// gaps is a read tool over GET …/gaps: the kind filter reaches the daemon as
// ?kind=, the text names each gap's closing command, and an empty report is
// [] rather than null in structuredContent.
func TestMCPGapsTool(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedMCP()
	f.json("GET /api/v1/projects/{projectId}/initiatives/{id}/gaps", map[string]any{
		"initiative_id": "ini-1",
		"gaps": []map[string]any{{
			"kind": "unverified_requirement", "state": "evidence_stale", "severity": "medium",
			"nodes":      []map[string]any{{"type": "requirement", "id": "req-1", "code": "REQ-1"}},
			"why":        "pass evidence for REQ-1/AC-1 predates CHG-2 changing the code it verified",
			"next":       "idea verify REQ-1 --pass",
			"dependents": 0, "initiative_id": "ini-1",
		}},
		"summary": map[string]any{"total": 1, "by_severity": map[string]int{"medium": 1}, "by_kind": map[string]int{"unverified_requirement": 1}},
	})
	s := startMCP(t, "--read-only")

	text, st, isErr := s.callTool("gaps", map[string]any{"kind": "unverified_requirement"})
	if isErr {
		t.Fatalf("gaps isError: %s", text)
	}
	if !strings.Contains(text, "1 gap(s): 1 medium") || !strings.Contains(text, "unverified_requirement REQ-1") ||
		!strings.Contains(text, "→ idea verify REQ-1 --pass") {
		t.Errorf("gaps text = %q", text)
	}
	if gaps, ok := st["gaps"].([]any); !ok || len(gaps) != 1 {
		t.Errorf("gaps structured = %v", st)
	}
	if !f.asked("/initiatives/ini-1/gaps?kind=unverified_requirement") {
		t.Errorf("kind did not reach the daemon; requests: %v", f.requests)
	}
}
