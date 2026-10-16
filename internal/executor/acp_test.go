package executor

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// The ACP client is tested against a fake ACP agent: this test binary,
// re-executed as a child process (TestACPFakeAgentProcess), speaking the agent
// side of the protocol over its stdin/stdout. The transport is the only thing
// swapped -- production always launches the agent with `docker run -i` -- so
// the handshake, the fs confinement, the permission answers, the timeout and
// the cancel path all run for real, with no Docker and no real agent.

const fakeAgentEnv = "IDEA_ACP_FAKE_AGENT"

// localLaunch runs the fake agent as a local process in scenario.
func localLaunch(t *testing.T, scenario string) func(context.Context, acpLaunch) (*acpProc, error) {
	t.Helper()
	return func(_ context.Context, l acpLaunch) (*acpProc, error) {
		cmd := exec.Command(os.Args[0], "-test.run=^TestACPFakeAgentProcess$")
		cmd.Env = append(os.Environ(), fakeAgentEnv+"=1", "ACP_SCENARIO="+scenario, "ACP_ARGV="+strings.Join(l.Argv, " "))
		for k, v := range l.Secrets {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		return startProc(cmd, l.Stderr, func() {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
			}
		})
	}
}

func newTestACP(t *testing.T, scenario string, opts ACPOptions, provider AgentProvider, env map[string]string) *ACPExecutor {
	t.Helper()
	if opts.Command == nil {
		opts.Command = []string{"fake-acp", "--model", "{model}", "--bare", "{bare_model}"}
	}
	if opts.Model == "" {
		opts.Model = "openai/gpt-test"
	}
	e := NewACPExecutor(opts, provider)
	e.launch = localLaunch(t, scenario)
	e.cancelGrace = 2 * time.Second
	e.exitGrace = 2 * time.Second
	e.SetKeyLookup(func(name string) string { return env[name] })
	return e
}

// waitACP polls Status until the attempt leaves running.
func waitACP(t *testing.T, e *ACPExecutor, id string, within time.Duration) Status {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		st, err := e.Status(context.Background(), id)
		if err != nil {
			t.Fatalf("status: %v", err)
		}
		if st.Status != "running" {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("attempt still running after %s", within)
	return Status{}
}

func workRootFor(t *testing.T) string {
	t.Helper()
	// Under a parent the test owns, as in production (state/projects/x/work/ini),
	// because the agent home is created next to the work root.
	return filepath.Join(t.TempDir(), "work", "ini")
}

func TestACPFullHandshakeWritesFilesAndLogsPermissions(t *testing.T) {
	const key = "sk-test-0123456789abcdef"
	e := newTestACP(t, "write", ACPOptions{KeyEnv: "AGENT_KEY"},
		AgentProvider{Name: "openai", KeyEnv: "OPENAI_API_KEY"}, map[string]string{"OPENAI_API_KEY": key})
	work := workRootFor(t)
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "existing.txt"), []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	att, err := e.Start(context.Background(), Request{TaskTitle: "t", Brief: "write hello.txt", WorkRoot: work})
	if err != nil {
		t.Fatal(err)
	}
	if att.Executor != "acp" {
		t.Fatalf("executor = %q", att.Executor)
	}
	st := waitACP(t, e, att.ID, 20*time.Second)
	tail := e.OutputTail(context.Background(), att.ID, 200)
	if st.Status != "succeeded" {
		t.Fatalf("status = %+v\n%s", st, tail)
	}

	got, err := os.ReadFile(filepath.Join(work, "src", "hello.txt"))
	if err != nil {
		t.Fatalf("the agent's fs/write_text_file did not land in the work root: %v\n%s", err, tail)
	}
	// The fake agent writes back what it read with line=2, limit=1, and the
	// argv it was started with, proving both reached it intact.
	want := "read=two\n|argv=fake-acp --model openai/gpt-test --bare gpt-test|cwd=/work|key=" + key
	if string(got) != want {
		t.Fatalf("hello.txt = %q, want %q", got, want)
	}

	perms := e.PermissionLog(att.ID)
	if len(perms) != 2 {
		t.Fatalf("permission log = %+v, want 2 entries", perms)
	}
	if perms[0].Decision != "allow_once" || perms[0].OptionID != "once" || perms[0].Title != "Write src/hello.txt" || perms[0].ToolKind != "edit" {
		t.Fatalf("first permission = %+v, want allow_once/once for the write", perms[0])
	}
	// The second request offers only reject options: nothing to allow with,
	// so the answer is reject_once rather than a fabricated allow.
	if perms[1].Decision != "reject_once" {
		t.Fatalf("second permission = %+v, want reject_once", perms[1])
	}

	for _, want := range []string{"agent: fake-acp", "→ Write src/hello.txt", "permission: allow_once Write src/hello.txt", "wrote /work/src/hello.txt", "done writing"} {
		if !strings.Contains(tail, want) {
			t.Errorf("output tail is missing %q:\n%s", want, tail)
		}
	}
	if strings.Contains(tail, key) {
		t.Fatalf("the provider key reached the output tail unredacted:\n%s", tail)
	}
	if strings.Contains(tail, "secret thought") {
		t.Fatalf("an agent_thought_chunk reached the output tail:\n%s", tail)
	}

	if _, err := e.Collect(context.Background(), att.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Status(context.Background(), att.ID); err == nil {
		t.Fatal("Collect did not forget the attempt")
	}
	homes, _ := filepath.Glob(filepath.Join(filepath.Dir(work), agentHomePrefix+"*"))
	if len(homes) != 0 {
		t.Fatalf("agent home left behind: %v", homes)
	}
}

func TestACPRefusesPathEscapes(t *testing.T) {
	e := newTestACP(t, "escape", ACPOptions{}, AgentProvider{Name: "ollama"}, nil)
	base := t.TempDir()
	work := filepath.Join(base, "work", "ini")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{work, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("do not read"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the work root pointing out of it: cleaning the path
	// cannot see this one, only resolving it can.
	if err := os.Symlink(outside, filepath.Join(work, "link")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ACP_OUTSIDE", outside)

	att, err := e.Start(context.Background(), Request{Brief: "escape", WorkRoot: work})
	if err != nil {
		t.Fatal(err)
	}
	st := waitACP(t, e, att.ID, 20*time.Second)
	tail := e.OutputTail(context.Background(), att.ID, 200)
	if st.Status != "succeeded" {
		t.Fatalf("status = %+v\n%s", st, tail)
	}
	// The fake agent reports each attempt's error code; every one must have
	// been refused with invalid params, and none may have touched the disk.
	for _, p := range []string{"/work/../escaped.txt", "/etc/hosts", "relative.txt", "/work/link/planted.txt", "/work/link/secret.txt", "/workother/x.txt"} {
		if !strings.Contains(tail, "refused "+p+" code=-32602") {
			t.Errorf("%s was not refused with -32602:\n%s", p, tail)
		}
	}
	for _, p := range []string{filepath.Join(base, "work", "escaped.txt"), filepath.Join(outside, "planted.txt")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("an escaping write landed at %s", p)
		}
	}
	if strings.Contains(tail, "do not read") {
		t.Fatalf("a read through the symlink leaked the outside file:\n%s", tail)
	}
	// terminal/* is not advertised, and an agent that calls it anyway gets
	// method-not-found rather than a host shell.
	if !strings.Contains(tail, "terminal/create code=-32601") {
		t.Errorf("terminal/create was not answered method-not-found:\n%s", tail)
	}
	_ = e.Cancel(context.Background(), att.ID)
}

func TestACPTimeoutSendsSessionCancel(t *testing.T) {
	// The timeout covers starting the fake agent too, so it must leave room
	// for a slow runner to reach session/prompt; otherwise the run times out
	// before there is a session to cancel.
	e := newTestACP(t, "hang", ACPOptions{Timeout: 2 * time.Second}, AgentProvider{Name: "ollama"}, nil)
	work := workRootFor(t)
	att, err := e.Start(context.Background(), Request{Brief: "hang", WorkRoot: work})
	if err != nil {
		t.Fatal(err)
	}
	st := waitACP(t, e, att.ID, 10*time.Second)
	if st.Status != "failed" || !strings.Contains(st.Action, "timed out after 2s") {
		t.Fatalf("status = %+v, want failed with a timeout", st)
	}
	// The fake agent prints this to stderr only when it receives
	// session/cancel, so its presence proves the clean cancel was sent. Its
	// stderr is copied asynchronously, so give the line a moment to land.
	var tail string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if tail = e.OutputTail(context.Background(), att.ID, 100); strings.Contains(tail, "fake: got session/cancel") {
			break
		}
	}
	if !strings.Contains(tail, "fake: got session/cancel") {
		t.Fatalf("session/cancel never reached the agent:\n%s", tail)
	}
	_ = e.Cancel(context.Background(), att.ID)
}

func TestACPAgentIgnoringCancelIsKilled(t *testing.T) {
	e := newTestACP(t, "hang-ignore-cancel", ACPOptions{Timeout: 200 * time.Millisecond}, AgentProvider{Name: "ollama"}, nil)
	e.cancelGrace = 300 * time.Millisecond
	att, err := e.Start(context.Background(), Request{Brief: "hang", WorkRoot: workRootFor(t)})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	st := waitACP(t, e, att.ID, 10*time.Second)
	if st.Status != "failed" {
		t.Fatalf("status = %+v", st)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("an agent ignoring session/cancel was not killed promptly (%s)", time.Since(start))
	}
	_ = e.Cancel(context.Background(), att.ID)
}

func TestACPCancelEndsTheTurn(t *testing.T) {
	e := newTestACP(t, "hang", ACPOptions{}, AgentProvider{Name: "ollama"}, nil)
	att, err := e.Start(context.Background(), Request{Brief: "hang", WorkRoot: workRootFor(t)})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- e.Cancel(context.Background(), att.ID) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Cancel did not return")
	}
	if _, err := e.Status(context.Background(), att.ID); err == nil {
		t.Fatal("Cancel did not forget the attempt")
	}
}

func TestACPAgentCrashFailsFast(t *testing.T) {
	e := newTestACP(t, "crash", ACPOptions{}, AgentProvider{Name: "ollama"}, nil)
	att, err := e.Start(context.Background(), Request{Brief: "x", WorkRoot: workRootFor(t)})
	if err != nil {
		t.Fatal(err)
	}
	st := waitACP(t, e, att.ID, 5*time.Second)
	if st.Status != "failed" || !strings.Contains(st.Action, "closed its output") {
		t.Fatalf("status = %+v, want failed: the agent closed its output", st)
	}
	if tail := e.OutputTail(context.Background(), att.ID, 50); !strings.Contains(tail, "fake: crashing") {
		t.Fatalf("the agent's stderr is not in the tail:\n%s", tail)
	}
	_ = e.Cancel(context.Background(), att.ID)
}

func TestACPRefusesAnotherProtocolVersion(t *testing.T) {
	e := newTestACP(t, "badversion", ACPOptions{}, AgentProvider{Name: "ollama"}, nil)
	att, err := e.Start(context.Background(), Request{Brief: "x", WorkRoot: workRootFor(t)})
	if err != nil {
		t.Fatal(err)
	}
	st := waitACP(t, e, att.ID, 5*time.Second)
	if st.Status != "failed" || !strings.Contains(st.Action, "protocol version 2") {
		t.Fatalf("status = %+v", st)
	}
	_ = e.Cancel(context.Background(), att.ID)
}

func TestACPPassesMCPServersInSessionNew(t *testing.T) {
	const token = "ghp_0123456789abcdefghijklmnopqrstuvwxyz"
	servers := []config.MCPServerConfig{
		{Name: "gh", Command: "github-mcp", Args: []string{"stdio"}, Env: []config.MCPNameValue{
			{Name: "GITHUB_TOKEN", ValueEnv: "MY_GH_TOKEN"},
			{Name: "LOG_LEVEL", Value: "debug"},
		}},
	}
	e := newTestACP(t, "mcp", ACPOptions{MCPServers: servers}, AgentProvider{Name: "ollama"}, map[string]string{"MY_GH_TOKEN": token})
	work := workRootFor(t)
	att, err := e.Start(context.Background(), Request{Brief: "x", WorkRoot: work})
	if err != nil {
		t.Fatal(err)
	}
	st := waitACP(t, e, att.ID, 10*time.Second)
	if st.Status != "succeeded" {
		t.Fatalf("status = %+v\n%s", st, e.OutputTail(context.Background(), att.ID, 50))
	}
	got, err := os.ReadFile(filepath.Join(work, "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"args":["stdio"],"command":"github-mcp","env":[{"name":"GITHUB_TOKEN","value":"` + token + `"},{"name":"LOG_LEVEL","value":"debug"}],"name":"gh"}]`
	if string(got) != want {
		t.Fatalf("mcpServers =\n%s\nwant\n%s", got, want)
	}
	if strings.Contains(e.OutputTail(context.Background(), att.ID, 50), token) {
		t.Fatal("an MCP value_env value reached the output tail unredacted")
	}
	_ = e.Cancel(context.Background(), att.ID)
}

func TestACPMissingMCPValueEnvStopsBeforeLaunch(t *testing.T) {
	servers := []config.MCPServerConfig{{Name: "gh", Command: "x", Env: []config.MCPNameValue{{Name: "GITHUB_TOKEN", ValueEnv: "UNSET_VAR_FOR_TEST"}}}}
	e := newTestACP(t, "write", ACPOptions{MCPServers: servers}, AgentProvider{Name: "ollama"}, nil)
	launched := false
	e.launch = func(context.Context, acpLaunch) (*acpProc, error) {
		launched = true
		return nil, fmt.Errorf("unreachable")
	}
	_, err := e.Start(context.Background(), Request{Brief: "x", WorkRoot: workRootFor(t)})
	if err == nil || !strings.Contains(err.Error(), "UNSET_VAR_FOR_TEST") {
		t.Fatalf("err = %v, want one naming the unset variable", err)
	}
	if launched {
		t.Fatal("the agent was launched with an unresolved MCP secret")
	}
}

func TestACPRefusesHTTPMCPServerTheAgentCannotUse(t *testing.T) {
	servers := []config.MCPServerConfig{{Name: "docs", Type: "http", URL: "https://example.com/mcp"}}
	e := newTestACP(t, "write", ACPOptions{MCPServers: servers}, AgentProvider{Name: "ollama"}, nil)
	att, err := e.Start(context.Background(), Request{Brief: "x", WorkRoot: workRootFor(t)})
	if err != nil {
		t.Fatal(err)
	}
	st := waitACP(t, e, att.ID, 10*time.Second)
	if st.Status != "failed" || !strings.Contains(st.Action, "does not support HTTP MCP servers") {
		t.Fatalf("status = %+v", st)
	}
	_ = e.Cancel(context.Background(), att.ID)
}

// TestACPContainerArgsAreHardened pins the production transport's argv: the
// same hardening as hermes/omp, attached (-i), and the secrets passed by name
// only, never by value.
func TestACPContainerArgsAreHardened(t *testing.T) {
	e := NewACPExecutor(ACPOptions{Image: "ghcr.io/x/claude-acp@sha256:abc", Command: []string{"claude-code-acp"}}, AgentProvider{})
	const key = "sk-should-never-be-in-argv-123456"
	args := e.containerArgs(acpLaunch{AttemptID: "A1", WorkRoot: "/s/projects/p/work/ini", AgentHome: "/s/projects/p/work/.agent-home-A1",
		Argv: []string{"claude-code-acp"}, Secrets: map[string]string{"ANTHROPIC_API_KEY": key}})
	joined := strings.Join(args, " ")
	for _, want := range []string{"run -i --rm", "--read-only", "--cap-drop ALL", "--security-opt no-new-privileges",
		"--memory 2g", "--pids-limit 256", "--tmpfs /tmp", "-v /s/projects/p/work/ini:/work", "-w /work",
		"--name idea-agent-A1", "--label " + agentLabel + "=/s/projects/p/work",
		"-v /s/projects/p/work/.agent-home-A1:" + containerAgentHome, "-e HOME=" + containerAgentHome,
		"-e ANTHROPIC_API_KEY ghcr.io/x/claude-acp@sha256:abc claude-code-acp"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv is missing %q:\n%s", want, joined)
		}
	}
	if !hasFlag(args, "--user") {
		t.Error("argv has no --user")
	}
	if strings.Contains(joined, key) {
		t.Fatal("the provider key is in the docker argv")
	}
	if hasFlag(args, "--network") {
		t.Error("agent egress is documented as unrestricted; the ACP argv now sets --network")
	}
	if hasFlag(args, "-t") {
		t.Error("a TTY would corrupt the JSON-RPC stream")
	}
}

func TestACPHostPath(t *testing.T) {
	work := t.TempDir()
	for _, tc := range []struct {
		in string
		ok bool
	}{
		{"/work/a.txt", true},
		{"/work/sub/../b.txt", true},
		{"/work", true},
		{"/work/../x", false},
		{"/workx/a", false},
		{"/etc/passwd", false},
		{"a.txt", false},
		{"", false},
	} {
		_, err := acpHostPath(work, tc.in)
		if (err == nil) != tc.ok {
			t.Errorf("acpHostPath(%q) err = %v, want ok=%v", tc.in, err, tc.ok)
		}
	}
}

func TestChoosePermissionPrefersAllowOnce(t *testing.T) {
	var p acpPermissionRequest
	_ = json.Unmarshal([]byte(`{"toolCall":{"toolCallId":"c1","title":"Run tests","kind":"execute"},"options":[
		{"optionId":"always","name":"Always","kind":"allow_always"},
		{"optionId":"once","name":"Once","kind":"allow_once"},
		{"optionId":"no","name":"No","kind":"reject_once"}]}`), &p)
	rec, out := choosePermission(p)
	if rec.Decision != "allow_once" || out["optionId"] != "once" || out["outcome"] != "selected" {
		t.Fatalf("rec=%+v out=%v", rec, out)
	}
	p.Options = nil
	rec, out = choosePermission(p)
	if rec.Decision != "cancelled" || out["outcome"] != "cancelled" {
		t.Fatalf("no options: rec=%+v out=%v", rec, out)
	}
}

// ---------- the fake agent ----------

// TestACPFakeAgentProcess is not a test: it is the fake ACP agent, run as a
// child process by localLaunch. It does nothing unless that env var is set.
func TestACPFakeAgentProcess(t *testing.T) {
	if os.Getenv(fakeAgentEnv) != "1" {
		t.Skip("helper process for the ACP tests")
	}
	runFakeAgent(os.Getenv("ACP_SCENARIO"))
	os.Exit(0)
}

type fakePeer struct {
	sc     *bufio.Scanner
	nextID int
	// cancelled is set when session/cancel arrives while waiting.
	cancelled bool
}

func (p *fakePeer) send(m map[string]any) {
	m["jsonrpc"] = "2.0"
	b, _ := json.Marshal(m)
	fmt.Fprintf(os.Stdout, "%s\n", b)
}

// read returns the next message, noting a session/cancel along the way.
func (p *fakePeer) read() (map[string]json.RawMessage, bool) {
	for p.sc.Scan() {
		var m map[string]json.RawMessage
		if json.Unmarshal(p.sc.Bytes(), &m) != nil {
			continue
		}
		if string(m["method"]) == `"session/cancel"` {
			p.cancelled = true
			fmt.Fprintln(os.Stderr, "fake: got session/cancel")
		}
		return m, true
	}
	return nil, false
}

// request sends a request to the client and returns its response.
func (p *fakePeer) request(method string, params any) (json.RawMessage, *rpcError) {
	p.nextID++
	id := fmt.Sprintf("agent-%d", p.nextID)
	p.send(map[string]any{"id": id, "method": method, "params": params})
	for {
		m, ok := p.read()
		if !ok {
			os.Exit(2)
		}
		if string(m["id"]) == `"`+id+`"` {
			if e, ok := m["error"]; ok {
				var re rpcError
				_ = json.Unmarshal(e, &re)
				return nil, &re
			}
			return m["result"], nil
		}
	}
}

func (p *fakePeer) update(sessionID string, u map[string]any) {
	p.send(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": sessionID, "update": u}})
}

func (p *fakePeer) say(sessionID, text string) {
	p.update(sessionID, map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": text}})
}

func runFakeAgent(scenario string) {
	if scenario == "crash-at-init" {
		fmt.Fprintln(os.Stderr, "fake: crashing")
		os.Exit(3)
	}
	p := &fakePeer{sc: bufio.NewScanner(os.Stdin)}
	p.sc.Buffer(make([]byte, 1<<20), 1<<24)
	var cwd string
	var mcp json.RawMessage
	for {
		m, ok := p.read()
		if !ok {
			return
		}
		var method string
		_ = json.Unmarshal(m["method"], &method)
		id := m["id"]
		reply := func(result any) { p.send(map[string]any{"id": id, "result": result}) }
		switch method {
		case "initialize":
			var req acpInitializeRequest
			_ = json.Unmarshal(m["params"], &req)
			if req.ClientCapabilities.Terminal || !req.ClientCapabilities.FS.ReadTextFile || !req.ClientCapabilities.FS.WriteTextFile {
				fmt.Fprintln(os.Stderr, "fake: client advertised the wrong capabilities")
				os.Exit(4)
			}
			v := 1
			if scenario == "badversion" {
				v = 2
			}
			reply(map[string]any{"protocolVersion": v, "agentCapabilities": map[string]any{"mcpCapabilities": map[string]bool{"http": false, "sse": false}},
				"agentInfo": map[string]string{"name": "fake-acp", "version": "0"}})
		case "session/new":
			var params struct {
				Cwd        string          `json:"cwd"`
				MCPServers json.RawMessage `json:"mcpServers"`
			}
			_ = json.Unmarshal(m["params"], &params)
			cwd, mcp = params.Cwd, params.MCPServers
			reply(map[string]string{"sessionId": "s1"})
		case "session/prompt":
			var params struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(m["params"], &params)
			fakeTurn++
			fakePromptText = ""
			if len(params.Prompt) > 0 {
				fakePromptText = params.Prompt[0].Text
			}
			stop := fakePrompt(p, scenario, "s1", cwd, mcp)
			if stop == "" {
				return
			}
			if scenario == "events" {
				// An ACP draft extension: usage on the prompt response.
				reply(map[string]any{"stopReason": stop, "usage": map[string]int{"inputTokens": 100, "outputTokens": 20}})
				continue
			}
			reply(map[string]string{"stopReason": stop})
		}
	}
}

// fakeTurn counts the session/prompt turns the fake agent has received, and
// fakePromptText is the current turn's prompt (the steering scenarios answer
// differently on a later turn).
var (
	fakeTurn       int
	fakePromptText string
)

func fakePrompt(p *fakePeer, scenario, sid, cwd string, mcp json.RawMessage) string {
	switch scenario {
	case "events":
		// One of each session/update the run-event mapping handles.
		p.update(sid, map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]string{"type": "text", "text": "private reasoning one"}})
		p.update(sid, map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]string{"type": "text", "text": "private reasoning two"}})
		p.say(sid, "I will edit ")
		p.say(sid, "src/a.txt.")
		p.update(sid, map[string]any{"sessionUpdate": "plan", "entries": []map[string]string{
			{"content": "read", "status": "completed"}, {"content": "edit", "status": "in_progress"}, {"content": "test", "status": "pending"}}})
		p.update(sid, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "Run tests", "kind": "execute", "status": "pending",
			"rawInput": map[string]any{"command": []string{"npm", "test"}}})
		p.update(sid, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": "failed",
			"content": []map[string]any{{"type": "content", "content": map[string]string{"type": "text", "text": "1 test failed"}}}})
		p.update(sid, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t2", "title": "Edit src/b.txt", "kind": "edit",
			"locations": []map[string]string{{"path": "/work/src/b.txt"}}})
		p.update(sid, map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t2", "status": "completed",
			"content": []map[string]any{{"type": "diff", "path": "/work/src/b.txt", "oldText": "x\ny\n", "newText": "x\nY\nz\n"}}})
		p.update(sid, map[string]any{"sessionUpdate": "usage_update", "used": 1000, "size": 200000, "cost": map[string]any{"amount": 0.01, "currency": "USD"}})
		p.update(sid, map[string]any{"sessionUpdate": "usage_update", "used": 2000, "size": 200000, "cost": map[string]any{"amount": 0.03, "currency": "USD"}})
		_, _ = p.request("session/request_permission", map[string]any{"sessionId": sid,
			"toolCall": map[string]string{"toolCallId": "t3", "title": "Write src/a.txt", "kind": "edit"},
			"options":  []map[string]string{{"optionId": "once", "name": "Once", "kind": "allow_once"}}})
		_, _ = p.request("fs/write_text_file", map[string]any{"sessionId": sid, "path": "/work/src/a.txt", "content": "one\nTWO\nthree\n"})
		p.update(sid, map[string]any{"sessionUpdate": "available_commands_update", "availableCommands": []any{}})
		return "end_turn"
	case "steer":
		// Turn 1 works for a while without reading stdin (so a note can only
		// arrive as a later turn); turn 2 writes the prompt it got.
		if fakeTurn == 1 {
			time.Sleep(1500 * time.Millisecond)
			return "end_turn"
		}
		_, _ = p.request("fs/write_text_file", map[string]any{"sessionId": sid, "path": fmt.Sprintf("/work/turn%d.txt", fakeTurn), "content": fakePromptText})
		return "end_turn"
	case "steer-interrupt":
		// Turn 1 hangs until session/cancel and answers "cancelled", as ACP
		// requires; turn 2 writes the prompt it got.
		if fakeTurn == 1 {
			for {
				m, ok := p.read()
				if !ok {
					return ""
				}
				if string(m["method"]) == `"session/cancel"` {
					return "cancelled"
				}
			}
		}
		_, _ = p.request("fs/write_text_file", map[string]any{"sessionId": sid, "path": "/work/turn2.txt", "content": fakePromptText})
		return "end_turn"
	case "write":
		p.update(sid, map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]string{"type": "text", "text": "secret thought"}})
		p.say(sid, "working on it, key is "+os.Getenv("AGENT_KEY")+"\n")
		p.update(sid, map[string]any{"sessionUpdate": "tool_call", "toolCallId": "c1", "title": "Write src/hello.txt", "kind": "edit", "status": "pending"})
		res, rerr := p.request("session/request_permission", map[string]any{"sessionId": sid,
			"toolCall": map[string]string{"toolCallId": "c1", "title": "Write src/hello.txt", "kind": "edit"},
			"options": []map[string]string{{"optionId": "always", "name": "Always", "kind": "allow_always"},
				{"optionId": "once", "name": "Once", "kind": "allow_once"}, {"optionId": "no", "name": "No", "kind": "reject_once"}}})
		if rerr != nil || !strings.Contains(string(res), `"optionId":"once"`) {
			fmt.Fprintf(os.Stderr, "fake: unexpected permission answer %s %v\n", res, rerr)
			return "refusal"
		}
		res, rerr = p.request("fs/read_text_file", map[string]any{"sessionId": sid, "path": "/work/existing.txt", "line": 2, "limit": 1})
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "fake: read failed %v\n", rerr)
			return "refusal"
		}
		var rd struct {
			Content string `json:"content"`
		}
		_ = json.Unmarshal(res, &rd)
		content := "read=" + rd.Content + "|argv=" + os.Getenv("ACP_ARGV") + "|cwd=" + cwd + "|key=" + os.Getenv("AGENT_KEY")
		if _, rerr := p.request("fs/write_text_file", map[string]any{"sessionId": sid, "path": "/work/src/hello.txt", "content": content}); rerr != nil {
			fmt.Fprintf(os.Stderr, "fake: write failed %v\n", rerr)
			return "refusal"
		}
		_, _ = p.request("session/request_permission", map[string]any{"sessionId": sid,
			"toolCall": map[string]string{"toolCallId": "c2", "title": "Delete everything"},
			"options":  []map[string]string{{"optionId": "nope", "name": "No", "kind": "reject_once"}}})
		p.say(sid, "done ")
		p.say(sid, "writing\n")
		return "end_turn"
	case "escape":
		outside := os.Getenv("ACP_OUTSIDE")
		_ = outside
		for _, path := range []string{"/work/../escaped.txt", "/etc/hosts", "relative.txt", "/work/link/planted.txt", "/workother/x.txt"} {
			_, rerr := p.request("fs/write_text_file", map[string]any{"sessionId": sid, "path": path, "content": "x"})
			report(p, sid, path, rerr)
		}
		res, rerr := p.request("fs/read_text_file", map[string]any{"sessionId": sid, "path": "/work/link/secret.txt"})
		if rerr == nil {
			p.say(sid, "read leaked: "+string(res)+"\n")
		}
		report(p, sid, "/work/link/secret.txt", rerr)
		_, rerr = p.request("terminal/create", map[string]any{"sessionId": sid, "command": "sh"})
		report(p, sid, "terminal/create", rerr)
		return "end_turn"
	case "hang":
		for {
			m, ok := p.read()
			if !ok {
				return ""
			}
			if string(m["method"]) == `"session/cancel"` {
				return "cancelled"
			}
		}
	case "hang-ignore-cancel":
		for {
			if _, ok := p.read(); !ok {
				return ""
			}
		}
	case "crash":
		fmt.Fprintln(os.Stderr, "fake: crashing")
		os.Exit(3)
	case "mcp":
		if _, rerr := p.request("fs/write_text_file", map[string]any{"sessionId": sid, "path": "/work/mcp.json", "content": string(mcp)}); rerr != nil {
			return "refusal"
		}
		return "end_turn"
	}
	return "end_turn"
}

func report(p *fakePeer, sid, path string, rerr *rpcError) {
	if rerr == nil {
		p.say(sid, "accepted "+path+"\n")
		return
	}
	p.say(sid, fmt.Sprintf("refused %s code=%d\n", path, rerr.Code))
}

func TestProbeACPHandshake(t *testing.T) {
	for _, tc := range []struct {
		scenario, want string
	}{{"write", "fake-acp 0"}, {"badversion", "protocol version 2"}, {"crash-at-init", "closed its output"}} {
		proc, err := localLaunch(t, tc.scenario)(context.Background(), acpLaunch{})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		name, err := probeACPProc(ctx, proc)
		cancel()
		got := name
		if err != nil {
			got = err.Error()
		}
		if !strings.Contains(got, tc.want) {
			t.Errorf("%s: probe = %q, want %q", tc.scenario, got, tc.want)
		}
	}
}
