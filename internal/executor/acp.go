package executor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/devarispbrown/idea-harness/internal/buildinfo"
	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/id"
	"github.com/devarispbrown/idea-harness/internal/security"
)

// ACPExecutor drives any Agent Client Protocol agent -- Claude Code
// (claude-code-acp), Codex (codex-acp), Gemini CLI (`gemini
// --experimental-acp`), OpenHands (`openhands acp`), OpenCode -- as the coding
// executor. The agent runs in the same hardened container as hermes/omp
// (read-only root, --cap-drop ALL, no-new-privileges, host uid, memory and pid
// limits, only the work root and a fresh home writable, only its own
// provider's key inside), attached with `docker run -i`; the harness speaks
// JSON-RPC to it over that container's stdin/stdout.
//
// What the harness implements as the ACP client, and advertises -- nothing
// more:
//
//   - fs/read_text_file and fs/write_text_file, confined to the work root:
//     a path outside /work (after cleaning, and after resolving symlinks on
//     the host side) is refused, never followed.
//   - session/request_permission, answered allow_once (allow_always only when
//     the agent offers no allow_once) without asking anyone. The container is
//     the sandbox boundary, exactly as hermes's --yolo and omp's
//     --auto-approve are; every request and its answer is kept and recorded on
//     the run manifest so nothing approved on the operator's behalf is
//     invisible.
//   - terminal/* is NOT advertised (clientCapabilities.terminal is false): the
//     agent runs its own commands inside its own container, and a client-side
//     terminal would run them on the host.
//
// A prompt turn is bounded by ACPOptions.Timeout. When it expires, or the run
// is cancelled, the client sends session/cancel, waits acpCancelGrace for the
// agent to stop cleanly, then kills the container.
type ACPExecutor struct {
	mu        sync.Mutex
	attempts  map[string]*acpAttempt
	opts      ACPOptions
	provider  AgentProvider
	lookupKey func(name string) string
	// dockerPath is the Docker CLI. launch is the transport: production
	// always runs the agent in a container (dockerLaunch); tests swap in a
	// local process speaking the same protocol, so the protocol handling --
	// handshake, fs confinement, permissions, timeout -- is tested without
	// Docker or a real agent.
	dockerPath string
	launch     func(ctx context.Context, l acpLaunch) (*acpProc, error)
	// cancelGrace and exitGrace are acpCancelGrace and acpExitGrace; fields
	// so a test of the kill path need not wait ten seconds for it.
	cancelGrace time.Duration
	exitGrace   time.Duration
}

// ACPOptions is the acp kind's configuration, from harness.yaml's executor
// block.
type ACPOptions struct {
	Image   string
	Model   string
	Command []string
	// KeyEnv is the name the provider key is exposed under inside the
	// container ("" keeps the provider's own variable name).
	KeyEnv     string
	Timeout    time.Duration
	MCPServers []config.MCPServerConfig
}

// ACPOptionsFrom builds ACPOptions from harness.yaml's executor block.
func ACPOptionsFrom(ec config.ExecutorConfig, model string) ACPOptions {
	o := ACPOptions{
		Image:      ec.Image,
		Model:      model,
		Command:    ec.ACP.Command,
		KeyEnv:     ec.ACP.KeyEnv,
		MCPServers: ec.MCPServers,
	}
	if ec.ACP.TimeoutSeconds > 0 {
		o.Timeout = time.Duration(ec.ACP.TimeoutSeconds) * time.Second
	}
	return o
}

const (
	// acpProtocolVersion is the ACP major version this client speaks.
	acpProtocolVersion = 1
	// acpWorkDir is where the work root is mounted in the container, and the
	// cwd every session is created with.
	acpWorkDir = "/work"
	// defaultACPTimeout is just under waitForCompletion's 15-minute ceiling,
	// so an over-long turn ends with a clean session/cancel rather than the
	// engine's hard stop.
	defaultACPTimeout = 840 * time.Second
	// acpCancelGrace is how long a cancelled agent gets to answer its prompt
	// with stopReason "cancelled" before the container is killed.
	acpCancelGrace = 10 * time.Second
	// acpExitGrace is how long an agent whose turn ended gets to exit after
	// its stdin is closed.
	acpExitGrace = 5 * time.Second
	// acpMaxReadFileBytes bounds one fs/read_text_file. Source files are far
	// smaller; this refuses a read of something that is not one.
	acpMaxReadFileBytes = 8 << 20
	// acpOutputLines is how many lines of agent output are kept per attempt.
	acpOutputLines = 2000
)

// acpLaunch is what a transport needs to start the agent.
type acpLaunch struct {
	AttemptID string
	WorkRoot  string
	AgentHome string
	Argv      []string
	// Secrets are env values the agent needs: its provider key and any
	// value_env an MCP server names. Never put in argv.
	Secrets map[string]string
	// Stderr receives the process's stderr (the agent's own log, and
	// docker's pull/start errors), line by line, for the output tail.
	Stderr io.Writer
}

// acpProc is a started agent process.
type acpProc struct {
	stdin  io.WriteCloser
	stdout io.Reader
	wait   func() error
	// kill stops the agent for good. For a container that is `docker rm -f`
	// as well as killing the attached CLI, which on its own would leave the
	// container running.
	kill func()
}

// acpAttempt is one running or finished prompt turn.
type acpAttempt struct {
	mu          sync.Mutex
	status      string // running|succeeded|failed|cancelled
	phase       string
	action      string
	permissions []domain.ACPPermission
	out         outputRing
	cancelled   bool

	done      chan struct{}
	cancel    context.CancelFunc
	proc      *acpProc
	agentHome string
	grant     *mountGrant
	secrets   []string

	// events maps session/update, fs writes and permission answers onto
	// normalized run events (acp_events.go).
	events *acpEvents

	// Steering (guarded by mu): notes a human sent while a turn ran, sent as
	// the next prompt turn; interrupted asks the running turn to end first
	// (interruptCh wakes it). steerClosed is set once no further turn will
	// be sent, after which Steer refuses rather than accept a note nobody
	// will read.
	notes       []string
	interrupted bool
	steerClosed bool
	interruptCh chan struct{}
}

// takeNotes returns and clears the pending steering notes. When there are
// none the session is about to end, and steering closes in the same
// critical section, so a note cannot slip in between "none pending" and the
// end of the session.
func (a *acpAttempt) takeNotes() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := a.notes
	a.notes = nil
	if len(n) == 0 {
		a.steerClosed = true
	}
	return n
}

// takeInterrupt reports and clears a pending interrupt request.
func (a *acpAttempt) takeInterrupt() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	v := a.interrupted
	a.interrupted = false
	select {
	case <-a.interruptCh:
	default:
	}
	return v
}

// closeSteering refuses further notes: the session is ending.
func (a *acpAttempt) closeSteering() {
	a.mu.Lock()
	a.steerClosed = true
	a.mu.Unlock()
}

func (a *acpAttempt) set(status, phase, action string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.status, a.phase, a.action = status, phase, action
}

// NewACPExecutor returns an ACP executor pinned to provider, which the engine
// resolves with ResolveAgentProvider.
func NewACPExecutor(opts ACPOptions, provider AgentProvider) *ACPExecutor {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultACPTimeout
	}
	e := &ACPExecutor{
		attempts:    map[string]*acpAttempt{},
		opts:        opts,
		provider:    provider,
		lookupKey:   os.Getenv,
		dockerPath:  "docker",
		cancelGrace: acpCancelGrace,
		exitGrace:   acpExitGrace,
	}
	e.launch = e.dockerLaunch
	return e
}

// SetKeyLookup sets where the provider key and MCP value_env values are read
// from (the daemon's environment, then the project's .env).
func (e *ACPExecutor) SetKeyLookup(lookup func(name string) string) {
	if lookup != nil {
		e.lookupKey = lookup
	}
}

// Name reports the executor's registered name.
func (e *ACPExecutor) Name() string { return "acp" }

// Capabilities matches the other agent kinds: real filesystem/process
// isolation, network egress for the model API.
func (e *ACPExecutor) Capabilities(ctx context.Context) Capabilities {
	return Capabilities{
		Trust:        domain.ExecutorLocalExternal,
		SandboxMode:  domain.SandboxContainer,
		Network:      true,
		Capabilities: []string{"fs.write"},
	}
}

// agentArgv substitutes {model} and {bare_model} into the configured command.
func (e *ACPExecutor) agentArgv() []string {
	out := make([]string, len(e.opts.Command))
	for i, a := range e.opts.Command {
		a = strings.ReplaceAll(a, "{bare_model}", bareModel(e.opts.Model))
		out[i] = strings.ReplaceAll(a, "{model}", e.opts.Model)
	}
	return out
}

// secrets resolves the env values the agent receives: the provider key
// (under ACPOptions.KeyEnv when set) and every MCP value_env. A value_env set
// nowhere is an error here, before the container starts, naming the variable.
func (e *ACPExecutor) secrets() (map[string]string, error) {
	s := map[string]string{}
	if e.provider.KeyEnv != "" {
		if v := e.lookupKey(e.provider.KeyEnv); v != "" {
			name := e.provider.KeyEnv
			if e.opts.KeyEnv != "" {
				name = e.opts.KeyEnv
			}
			s[name] = v
		}
	}
	for _, m := range e.opts.MCPServers {
		for _, nv := range append(append([]config.MCPNameValue{}, m.Env...), m.Headers...) {
			if nv.ValueEnv == "" {
				continue
			}
			if _, ok := config.ResolveMCPValue(nv, e.lookupKey); !ok {
				return nil, fmt.Errorf("executor: acp: MCP server %q needs %s (for %s), which is set neither in the daemon's environment nor in the project's .env", m.Name, nv.ValueEnv, nv.Name)
			}
		}
	}
	return s, nil
}

// Start launches the agent and runs one prompt turn in the background. Start
// returns once the process is running; Status reports the turn's progress.
func (e *ACPExecutor) Start(ctx context.Context, req Request) (Attempt, error) {
	if len(e.opts.Command) == 0 {
		return Attempt{}, fmt.Errorf("executor: acp: executor.acp.command is empty; set the agent's argv inside the image, e.g. [claude-code-acp]")
	}
	if err := os.MkdirAll(req.WorkRoot, 0o755); err != nil {
		return Attempt{}, fmt.Errorf("executor: acp: mkdir work root: %w", err)
	}
	secrets, err := e.secrets()
	if err != nil {
		return Attempt{}, err
	}
	mcp, err := e.mcpServers()
	if err != nil {
		return Attempt{}, err
	}
	attemptID := id.New()
	home, err := makeAgentHome(req.WorkRoot, attemptID)
	if err != nil {
		return Attempt{}, err
	}
	// A root daemon runs the agent as ContainerUID: hand it what it writes,
	// and take it back in finish (containeruser.go).
	grant, err := grantMounts(req.WorkRoot, home)
	if err != nil {
		removeAgentHome(home)
		return Attempt{}, err
	}
	a := &acpAttempt{status: "running", phase: "Planning", action: "starting the agent",
		done: make(chan struct{}), agentHome: home, grant: grant, interruptCh: make(chan struct{}, 1),
		events: newACPEvents(req.Events)}
	a.out.max = acpOutputLines
	// Everything the agent was handed that is secret, for OutputTail to
	// redact: an agent can echo its environment or its MCP configuration.
	for _, v := range secrets {
		a.secrets = append(a.secrets, v)
	}
	for _, s := range e.opts.MCPServers {
		for _, nv := range append(append([]config.MCPNameValue{}, s.Env...), s.Headers...) {
			if nv.ValueEnv != "" {
				if v, ok := config.ResolveMCPValue(nv, e.lookupKey); ok {
					a.secrets = append(a.secrets, v)
				}
			}
		}
	}

	proc, err := e.launch(ctx, acpLaunch{AttemptID: attemptID, WorkRoot: req.WorkRoot, AgentHome: home,
		Argv: e.agentArgv(), Secrets: secrets, Stderr: &lineWriter{ring: &a.out}})
	if err != nil {
		_ = grant.Restore()
		removeAgentHome(home)
		return Attempt{}, fmt.Errorf("executor: acp: start agent: %w", err)
	}
	a.proc = proc

	// Not bound to ctx: the turn outlives Start, like hermes's detached
	// container, and ends through Cancel or its own timeout.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.opts.Timeout)
	a.cancel = cancel
	e.mu.Lock()
	e.attempts[attemptID] = a
	e.mu.Unlock()

	go e.run(sctx, a, req, mcp)
	return Attempt{ID: attemptID, Executor: "acp", Status: "running", Phase: "Planning"}, nil
}

// run is one whole session: initialize, session/new, session/prompt.
func (e *ACPExecutor) run(ctx context.Context, a *acpAttempt, req Request, mcp []any) {
	defer close(a.done)
	defer a.cancel()

	conn := newACPConn(a.proc.stdin,
		func(method string, params json.RawMessage) (any, *rpcError) {
			return e.handle(a, req.WorkRoot, method, params)
		},
		func(method string, params json.RawMessage) { a.onNotification(method, params) },
		func(line string) { a.out.addLine(line) })
	go conn.readLoop(a.proc.stdout)

	stop, reason := e.session(ctx, conn, a, req, mcp)
	a.out.flush()

	// End the process: close stdin and give it a moment, then kill. A
	// killed docker CLI does not stop its container, so kill is `docker rm
	// -f` as well.
	_ = a.proc.stdin.Close()
	exited := make(chan error, 1)
	go func() { exited <- a.proc.wait() }()
	select {
	case <-exited:
	case <-time.After(e.exitGrace):
		a.proc.kill()
		<-exited
	}

	a.mu.Lock()
	cancelled := a.cancelled
	a.mu.Unlock()
	switch {
	case stop == "end_turn":
		a.set("succeeded", "Finished", "complete")
	case cancelled:
		a.set("cancelled", "Finished", "cancelled")
	default:
		a.set("failed", "Finished", reason)
	}
}

// session runs the protocol and returns the prompt's stopReason, or "" and a
// reason when the turn did not end normally.
func (e *ACPExecutor) session(ctx context.Context, conn *acpConn, a *acpAttempt, req Request, mcp []any) (string, string) {
	fail := func(what string, err error) (string, string) {
		if errors.Is(err, context.DeadlineExceeded) {
			return "", fmt.Sprintf("timed out after %s during %s", e.opts.Timeout, what)
		}
		if errors.Is(err, context.Canceled) {
			return "", "cancelled during " + what
		}
		return "", fmt.Sprintf("%s: %v", what, err)
	}

	a.set("running", "Planning", "initialize")
	var initRes acpInitializeResponse
	if err := conn.call(ctx, "initialize", acpInitializeRequest{
		ProtocolVersion: acpProtocolVersion,
		ClientCapabilities: acpClientCapabilities{
			FS:       acpFSCapabilities{ReadTextFile: true, WriteTextFile: true},
			Terminal: false,
		},
		ClientInfo: &acpImplementation{Name: "idea-harness", Version: buildinfo.Get().String()},
	}, &initRes); err != nil {
		return fail("initialize", err)
	}
	if initRes.ProtocolVersion != acpProtocolVersion {
		return "", fmt.Sprintf("the agent speaks ACP protocol version %d; idea-harness speaks %d", initRes.ProtocolVersion, acpProtocolVersion)
	}
	if err := checkMCPCapabilities(e.opts.MCPServers, initRes.AgentCapabilities.MCPCapabilities); err != nil {
		return "", err.Error()
	}
	if initRes.AgentInfo != nil && initRes.AgentInfo.Name != "" {
		a.out.addLine(fmt.Sprintf("agent: %s %s", initRes.AgentInfo.Name, initRes.AgentInfo.Version))
		a.events.emit(domain.RunEvent{Kind: domain.RunEventStatus, Status: "running", Source: domain.RunEventSourceHarness,
			Title: strings.TrimSpace(fmt.Sprintf("Connected to %s %s", initRes.AgentInfo.Name, initRes.AgentInfo.Version))})
	}

	var newRes struct {
		SessionID string `json:"sessionId"`
	}
	if err := conn.call(ctx, "session/new", map[string]any{"cwd": acpWorkDir, "mcpServers": mcp}, &newRes); err != nil {
		return fail("session/new", err)
	}
	if newRes.SessionID == "" {
		return "", "session/new returned no sessionId"
	}

	stmt := req.Brief
	if stmt == "" {
		stmt = req.TaskStatement
	}
	if stmt == "" {
		stmt = req.TaskTitle
	}
	// The first turn is the brief; each later one is the steering notes a
	// human sent while the previous turn ran (Steer). ACP defines no
	// concurrent prompts in one session -- "once a prompt turn completes,
	// the Client may send another session/prompt" -- so a note is never
	// sent mid-turn: it waits for the turn to end, or (interrupt) ends the
	// turn with session/cancel first, which the agent MUST answer with
	// stopReason "cancelled".
	text := stmt
	for {
		a.set("running", "Editing", "working")
		stop, why, perr := e.turn(ctx, conn, a, newRes.SessionID, text)
		if why != "" {
			return "", why
		}
		if perr != nil {
			a.closeSteering()
			return fail("session/prompt", perr)
		}
		interrupted := a.takeInterrupt()
		if stop != "end_turn" && !(stop == "cancelled" && interrupted) {
			a.closeSteering()
			return stop, "the agent stopped: " + stop
		}
		notes := a.takeNotes()
		if len(notes) == 0 {
			if stop == "cancelled" {
				return stop, "the agent stopped: " + stop
			}
			return "end_turn", ""
		}
		text = steeringPrompt(notes)
		a.out.addLine("idea-harness: sent a steering note as the next prompt turn")
	}
}

// steeringPrompt is the prompt turn a human's steering notes become.
func steeringPrompt(notes []string) string {
	return "A note from the human watching this run. Take it into account and carry on with the task:\n\n" + strings.Join(notes, "\n\n")
}

// turn runs one session/prompt. why is non-empty when the run's context
// ended (timeout or Stop): session/cancel was sent and the turn is over.
func (e *ACPExecutor) turn(ctx context.Context, conn *acpConn, a *acpAttempt, sessionID, text string) (stop, why string, err error) {
	type promptResult struct {
		stop  string
		usage *acpUsage
		err   error
	}
	resCh := make(chan promptResult, 1)
	go func() {
		var res struct {
			StopReason string    `json:"stopReason"`
			Usage      *acpUsage `json:"usage"`
		}
		// Not ctx: on cancel the agent is expected to answer this very call
		// with stopReason "cancelled", and that answer is still wanted.
		err := conn.call(context.Background(), "session/prompt", map[string]any{
			"sessionId": sessionID,
			"prompt":    []map[string]string{{"type": "text", "text": text}},
		}, &res)
		resCh <- promptResult{res.StopReason, res.Usage, err}
	}()
	defer a.events.flushMessage()

	select {
	case r := <-resCh:
		if r.err == nil {
			// The turn's text first, then what it cost: the order it happened.
			a.events.flushMessage()
			a.events.promptUsage(r.usage)
		}
		return r.stop, "", r.err
	case <-a.interruptCh:
		// A steering note asked to interrupt: end this turn cleanly, and
		// the caller sends the note as the next one.
		a.out.addLine("idea-harness: interrupting the turn for a steering note; sent session/cancel")
		_ = conn.notify("session/cancel", map[string]string{"sessionId": sessionID})
		select {
		case r := <-resCh:
			if r.err == nil {
				a.events.flushMessage()
				a.events.promptUsage(r.usage)
			}
			return r.stop, "", r.err
		case <-time.After(e.cancelGrace):
			a.closeSteering()
			a.proc.kill()
			return "", "the agent did not stop for a steering interrupt", nil
		case <-ctx.Done():
		}
	case <-ctx.Done():
	}
	// Timed out or cancelled: ask the agent to stop, then give it a moment
	// to say it has.
	why = "cancelled"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		why = fmt.Sprintf("timed out after %s", e.opts.Timeout)
	}
	a.closeSteering()
	a.out.addLine("idea-harness: " + why + "; sent session/cancel")
	_ = conn.notify("session/cancel", map[string]string{"sessionId": sessionID})
	select {
	case <-resCh:
	case <-time.After(e.cancelGrace):
		a.proc.kill()
	}
	return "", why, nil
}

// handle answers one request from the agent.
func (e *ACPExecutor) handle(a *acpAttempt, workRoot, method string, params json.RawMessage) (any, *rpcError) {
	switch method {
	case "session/request_permission":
		var p acpPermissionRequest
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpcError{Code: rpcInvalidParams, Message: err.Error()}
		}
		rec, outcome := choosePermission(p)
		a.mu.Lock()
		a.permissions = append(a.permissions, rec)
		a.mu.Unlock()
		a.out.addLine(fmt.Sprintf("permission: %s %s", rec.Decision, firstNonEmpty(rec.Title, rec.ToolCallID)))
		a.events.permission(rec)
		return map[string]any{"outcome": outcome}, nil
	case "fs/read_text_file":
		var p struct {
			Path  string `json:"path"`
			Line  *int   `json:"line"`
			Limit *int   `json:"limit"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpcError{Code: rpcInvalidParams, Message: err.Error()}
		}
		host, err := acpHostPath(workRoot, p.Path)
		if err != nil {
			a.out.addLine("refused read outside the work root: " + p.Path)
			return nil, &rpcError{Code: rpcInvalidParams, Message: err.Error()}
		}
		content, err := readTextFile(host, p.Line, p.Limit)
		if err != nil {
			return nil, &rpcError{Code: rpcInternalError, Message: err.Error()}
		}
		return map[string]string{"content": content}, nil
	case "fs/write_text_file":
		var p struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &rpcError{Code: rpcInvalidParams, Message: err.Error()}
		}
		host, err := acpHostPath(workRoot, p.Path)
		if err != nil {
			a.out.addLine("refused write outside the work root: " + p.Path)
			return nil, &rpcError{Code: rpcInvalidParams, Message: err.Error()}
		}
		if err := os.MkdirAll(filepath.Dir(host), 0o755); err != nil {
			return nil, &rpcError{Code: rpcInternalError, Message: err.Error()}
		}
		// The previous content, for the live file list's +/- counts. Read
		// only when it is a file of source size; a missing file is new.
		var prev string
		if fi, err := os.Stat(host); err == nil && !fi.IsDir() && fi.Size() <= acpMaxReadFileBytes {
			if b, err := os.ReadFile(host); err == nil {
				prev = string(b)
			}
		}
		if err := os.WriteFile(host, []byte(p.Content), 0o644); err != nil {
			return nil, &rpcError{Code: rpcInternalError, Message: err.Error()}
		}
		// Written by the daemon on the agent's behalf; under a root daemon
		// the agent (ContainerUID) must still be able to edit it from inside
		// its container (containeruser.go).
		if err := handOver(workRoot, host); err != nil {
			return nil, &rpcError{Code: rpcInternalError, Message: err.Error()}
		}
		a.out.addLine("wrote " + p.Path)
		a.events.fileWrite(p.Path, prev, p.Content)
		return map[string]any{}, nil
	default:
		// terminal/* lands here: it is not advertised, so an agent that
		// calls it anyway gets the standard answer.
		return nil, &rpcError{Code: rpcMethodNotFound, Message: "method not supported by this client: " + method}
	}
}

// errACPPathEscape is the answer to an fs request outside the work root.
var errACPPathEscape = errors.New("path is outside the work root " + acpWorkDir)

// acpHostPath maps an absolute container path under /work to the host work
// root, refusing anything else: a relative path, a path that cleans to
// outside /work ("/work/../etc/passwd"), or one that a symlink inside the work
// root would carry outside it (security.ResolveWithin resolves the deepest
// existing ancestor before comparing).
func acpHostPath(workRoot, p string) (string, error) {
	if !path.IsAbs(p) {
		return "", fmt.Errorf("path %q must be absolute", p)
	}
	c := path.Clean(p)
	if c != acpWorkDir && !strings.HasPrefix(c, acpWorkDir+"/") {
		return "", errACPPathEscape
	}
	rel := strings.TrimPrefix(strings.TrimPrefix(c, acpWorkDir), "/")
	host, err := security.ResolveWithin(workRoot, filepath.FromSlash(rel))
	if err != nil {
		if errors.Is(err, security.ErrPathEscape) {
			return "", errACPPathEscape
		}
		return "", err
	}
	return host, nil
}

// readTextFile reads a file, optionally from a 1-based line for limit lines.
func readTextFile(p string, line, limit *int) (string, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if fi.IsDir() {
		return "", fmt.Errorf("%s is a directory", filepath.Base(p))
	}
	if fi.Size() > acpMaxReadFileBytes {
		return "", fmt.Errorf("file is %d bytes; this client reads at most %d", fi.Size(), acpMaxReadFileBytes)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return "", err
	}
	if line == nil && limit == nil {
		return string(b), nil
	}
	lines := strings.SplitAfter(string(b), "\n")
	start := 0
	if line != nil && *line > 1 {
		start = *line - 1
	}
	if start > len(lines) {
		start = len(lines)
	}
	end := len(lines)
	if limit != nil && *limit >= 0 && start+*limit < end {
		end = start + *limit
	}
	return strings.Join(lines[start:end], ""), nil
}

// choosePermission picks the answer to a permission request: allow_once,
// else allow_always, else (nothing to allow with) reject_once, else
// reject_always, else cancelled. It never asks a human: the container is the
// boundary. The choice is returned as a record for the run manifest.
func choosePermission(p acpPermissionRequest) (domain.ACPPermission, map[string]string) {
	rec := domain.ACPPermission{ToolCallID: p.ToolCall.ToolCallID, Title: p.ToolCall.Title, ToolKind: p.ToolCall.Kind, At: time.Now().UTC()}
	for _, kind := range []string{"allow_once", "allow_always", "reject_once", "reject_always"} {
		for _, o := range p.Options {
			if o.Kind == kind {
				rec.OptionID, rec.Decision = o.OptionID, kind
				return rec, map[string]string{"outcome": "selected", "optionId": o.OptionID}
			}
		}
	}
	rec.Decision = "cancelled"
	return rec, map[string]string{"outcome": "cancelled"}
}

// onNotification records session/update for the output tail. Only what a
// human watching the run wants: the agent's messages, tool calls, failures.
// Thought chunks are not kept (§50: never expose chain-of-thought).
func (a *acpAttempt) onNotification(method string, params json.RawMessage) {
	if method != "session/update" {
		return
	}
	a.events.update(params)
	var n struct {
		Update struct {
			SessionUpdate string          `json:"sessionUpdate"`
			Content       json.RawMessage `json:"content"`
			ToolCallID    string          `json:"toolCallId"`
			Title         string          `json:"title"`
			Kind          string          `json:"kind"`
			Status        string          `json:"status"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &n) != nil {
		return
	}
	u := n.Update
	switch u.SessionUpdate {
	case "agent_message_chunk":
		var c struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(u.Content, &c) == nil && c.Type == "text" {
			a.out.addText(c.Text)
		}
	case "tool_call":
		a.out.addLine("→ " + firstNonEmpty(u.Title, u.Kind, u.ToolCallID))
	case "tool_call_update":
		if u.Status == "failed" {
			a.out.addLine("✗ " + firstNonEmpty(u.Title, u.ToolCallID) + " failed")
		}
	}
}

// mcpServers renders executor.mcp_servers in ACP's McpServer shape. Values
// are resolved here, at Start, from the same lookup as the provider key; they
// travel to the agent in session/new over its stdin, never in argv.
func (e *ACPExecutor) mcpServers() ([]any, error) {
	out := make([]any, 0, len(e.opts.MCPServers))
	resolve := func(server string, nvs []config.MCPNameValue) ([]map[string]string, error) {
		r := make([]map[string]string, 0, len(nvs))
		for _, nv := range nvs {
			v, ok := config.ResolveMCPValue(nv, e.lookupKey)
			if !ok {
				return nil, fmt.Errorf("executor: acp: MCP server %q needs %s (for %s), which is set neither in the daemon's environment nor in the project's .env", server, nv.ValueEnv, nv.Name)
			}
			r = append(r, map[string]string{"name": nv.Name, "value": v})
		}
		return r, nil
	}
	for _, s := range e.opts.MCPServers {
		switch s.EffectiveType() {
		case "stdio":
			env, err := resolve(s.Name, s.Env)
			if err != nil {
				return nil, err
			}
			args := s.Args
			if args == nil {
				args = []string{}
			}
			out = append(out, map[string]any{"name": s.Name, "command": s.Command, "args": args, "env": env})
		default:
			headers, err := resolve(s.Name, s.Headers)
			if err != nil {
				return nil, err
			}
			out = append(out, map[string]any{"type": s.Type, "name": s.Name, "url": s.URL, "headers": headers})
		}
	}
	return out, nil
}

// checkMCPCapabilities refuses an http/sse server the agent did not say it
// supports, rather than handing it one it will ignore or reject.
func checkMCPCapabilities(servers []config.MCPServerConfig, caps acpMCPCapabilities) error {
	for _, s := range servers {
		switch s.EffectiveType() {
		case "http":
			if !caps.HTTP {
				return fmt.Errorf("MCP server %q is type http, and this agent does not support HTTP MCP servers (agentCapabilities.mcpCapabilities.http is false)", s.Name)
			}
		case "sse":
			if !caps.SSE {
				return fmt.Errorf("MCP server %q is type sse, and this agent does not support SSE MCP servers (agentCapabilities.mcpCapabilities.sse is false)", s.Name)
			}
		}
	}
	return nil
}

func (e *ACPExecutor) attempt(id string) (*acpAttempt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.attempts[id]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownAttempt, id)
	}
	return a, nil
}

// Status reports the prompt turn's progress.
func (e *ACPExecutor) Status(ctx context.Context, id string) (Status, error) {
	a, err := e.attempt(id)
	if err != nil {
		return Status{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return Status{Status: a.status, Phase: a.phase, Action: a.action}, nil
}

// Cancel ends the turn (session/cancel, then kill after a grace period) and
// removes the container and agent home.
func (e *ACPExecutor) Cancel(ctx context.Context, id string) error {
	a, err := e.attempt(id)
	if err != nil {
		return err
	}
	a.mu.Lock()
	if a.status == "running" {
		a.cancelled = true
	}
	a.mu.Unlock()
	a.cancel()
	return e.finish(id, a)
}

// Collect waits for the turn to end and cleans up. The engine computes the
// diff from the work root itself.
func (e *ACPExecutor) Collect(ctx context.Context, id string) (Result, error) {
	a, err := e.attempt(id)
	if err != nil {
		return Result{}, err
	}
	if err := e.finish(id, a); err != nil {
		return Result{}, err
	}
	return Result{}, nil
}

// finish waits for run to return, then kills whatever is left and forgets the
// attempt.
func (e *ACPExecutor) finish(id string, a *acpAttempt) error {
	select {
	case <-a.done:
	case <-time.After(e.cancelGrace + e.exitGrace + 5*time.Second):
		a.proc.kill()
		<-a.done
	}
	// Always: the docker CLI exiting does not prove the container is gone.
	a.proc.kill()
	e.mu.Lock()
	delete(e.attempts, id)
	e.mu.Unlock()
	// After the kill, so no container races the walk.
	_ = a.grant.Restore()
	if a.agentHome != "" {
		_ = removeAgentHome(a.agentHome)
	}
	return nil
}

// OutputTail implements OutputTailer: the agent's messages, tool calls,
// permission answers and stderr, sanitized and with its secrets redacted.
func (e *ACPExecutor) OutputTail(ctx context.Context, id string, lines int) string {
	a, err := e.attempt(id)
	if err != nil {
		return ""
	}
	if lines <= 0 {
		lines = agentOutputLines
	}
	s := strings.TrimSpace(sanitizeAgentLog(a.out.tail(lines)))
	a.mu.Lock()
	secrets := a.secrets
	a.mu.Unlock()
	for _, v := range secrets {
		if len(v) >= 8 {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}

// PermissionLog returns the permission requests the agent made and the
// answers given, in order. Read it before Collect or Cancel, which forget the
// attempt.
func (e *ACPExecutor) PermissionLog(id string) []domain.ACPPermission {
	a, err := e.attempt(id)
	if err != nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]domain.ACPPermission(nil), a.permissions...)
}

// PermissionLogger is implemented by executors whose agent asks for
// permissions the harness answers on the operator's behalf.
type PermissionLogger interface {
	PermissionLog(id string) []domain.ACPPermission
}

// ---------- transport ----------

// containerArgs builds the `docker run` argv for an ACP agent: the same
// hardening as SandboxedAgentExecutor.containerArgs, attached (-i) instead of
// detached, and --rm because nothing polls a finished container here.
//
// The secrets are passed as `-e NAME` with NO value, which makes docker read
// the value from its own environment: never in argv, and no env-file on disk
// that an attached `docker run` might not have read yet when it would be
// deleted.
func (e *ACPExecutor) containerArgs(l acpLaunch) []string {
	uid := containerUser()
	args := []string{
		"run", "-i", "--rm",
		"--name", agentContainerName(l.AttemptID),
		"--label", agentLabel + "=" + filepath.Dir(l.WorkRoot),
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--user", uid,
		"--memory", "2g",
		"--pids-limit", "256",
		"--tmpfs", "/tmp",
		"-v", l.WorkRoot + ":" + acpWorkDir,
		"-w", acpWorkDir,
	}
	if l.AgentHome != "" {
		args = append(args, "-v", l.AgentHome+":"+containerAgentHome, "-e", "HOME="+containerAgentHome)
	}
	names := make([]string, 0, len(l.Secrets))
	for k := range l.Secrets {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		args = append(args, "-e", k)
	}
	args = append(args, e.opts.Image)
	return append(args, l.Argv...)
}

// dockerLaunch is the production transport.
func (e *ACPExecutor) dockerLaunch(ctx context.Context, l acpLaunch) (*acpProc, error) {
	if e.opts.Image == "" {
		return nil, fmt.Errorf("executor.image is empty; there is no canonical ACP image")
	}
	cmd := exec.Command(e.dockerPath, e.containerArgs(l)...)
	cmd.Env = os.Environ()
	for k, v := range l.Secrets {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	name := agentContainerName(l.AttemptID)
	return startProc(cmd, l.Stderr, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		cctx, cancel := cleanupContext(ctx)
		defer cancel()
		_ = exec.CommandContext(cctx, e.dockerPath, "rm", "-f", name).Run()
	})
}

// startProc wires a command's stdio and starts it.
//
// stdout goes through an io.Pipe closed only once Wait has returned, rather
// than cmd.StdoutPipe: Wait closes a StdoutPipe as soon as the process exits,
// which can drop the agent's last messages, and Wait itself must run as soon
// as the process exits -- otherwise an agent that dies mid-session leaves the
// reader waiting on a pipe nobody closes, and the turn hangs until its
// timeout instead of failing at once.
func startProc(cmd *exec.Cmd, stderr io.Writer, kill func()) (*acpProc, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	if stderr != nil {
		cmd.Stderr = stderr
	}
	if err := cmd.Start(); err != nil {
		pw.Close()
		return nil, err
	}
	exited := make(chan struct{})
	var werr error
	go func() {
		werr = cmd.Wait()
		if lw, ok := stderr.(*lineWriter); ok {
			lw.Flush()
		}
		pw.Close()
		close(exited)
	}()
	return &acpProc{
		stdin:  stdin,
		stdout: pr,
		wait:   func() error { <-exited; return werr },
		kill:   kill,
	}, nil
}

// lineWriter feeds an output ring line by line.
type lineWriter struct {
	mu   sync.Mutex
	ring *outputRing
	buf  []byte
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := strings.IndexByte(string(w.buf), '\n')
		if i < 0 {
			break
		}
		w.ring.addLine(string(w.buf[:i]))
		w.buf = w.buf[i+1:]
	}
	// One unterminated line is not allowed to grow without bound.
	if len(w.buf) > 64<<10 {
		w.ring.addLine(string(w.buf))
		w.buf = nil
	}
	return len(p), nil
}

// Flush emits a trailing unterminated line.
func (w *lineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.ring.addLine(string(w.buf))
		w.buf = nil
	}
}

// makeAgentHome creates the attempt's writable home next to the work root,
// where SweepAgentLeftovers finds it if the daemon dies mid-run. ACP agents
// write their own state under $HOME (Claude Code's ~/.claude, Gemini's
// ~/.gemini), and the image's own home is not writable under --read-only with
// a host --user.
func makeAgentHome(workRoot, attemptID string) (string, error) {
	dir := filepath.Join(filepath.Dir(workRoot), agentHomePrefix+attemptID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("executor: create agent home: %w", err)
	}
	return dir, nil
}

// ---------- output ----------

// outputRing keeps the last max lines of agent output. Message text arrives
// in chunks that do not align with lines, so a partial line is buffered
// until its newline.
type outputRing struct {
	mu      sync.Mutex
	max     int
	lines   []string
	partial strings.Builder
}

func (r *outputRing) addLine(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushLocked()
	for _, l := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		r.appendLocked(l)
	}
}

func (r *outputRing) addText(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			r.partial.WriteString(s)
			return
		}
		r.partial.WriteString(s[:i])
		r.appendLocked(r.partial.String())
		r.partial.Reset()
		s = s[i+1:]
	}
}

func (r *outputRing) flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.flushLocked()
}

func (r *outputRing) flushLocked() {
	if r.partial.Len() > 0 {
		r.appendLocked(r.partial.String())
		r.partial.Reset()
	}
}

func (r *outputRing) appendLocked(l string) {
	max := r.max
	if max <= 0 {
		max = acpOutputLines
	}
	r.lines = append(r.lines, l)
	if len(r.lines) > max {
		r.lines = append([]string(nil), r.lines[len(r.lines)-max:]...)
	}
}

func (r *outputRing) tail(n int) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	lines := r.lines
	if p := r.partial.String(); p != "" {
		lines = append(append([]string(nil), lines...), p)
	}
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

// ---------- protocol shapes ----------

type acpInitializeRequest struct {
	ProtocolVersion    int                   `json:"protocolVersion"`
	ClientCapabilities acpClientCapabilities `json:"clientCapabilities"`
	ClientInfo         *acpImplementation    `json:"clientInfo,omitempty"`
}

type acpClientCapabilities struct {
	FS       acpFSCapabilities `json:"fs"`
	Terminal bool              `json:"terminal"`
}

type acpFSCapabilities struct {
	ReadTextFile  bool `json:"readTextFile"`
	WriteTextFile bool `json:"writeTextFile"`
}

type acpImplementation struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type acpInitializeResponse struct {
	ProtocolVersion   int `json:"protocolVersion"`
	AgentCapabilities struct {
		MCPCapabilities acpMCPCapabilities `json:"mcpCapabilities"`
	} `json:"agentCapabilities"`
	AgentInfo *acpImplementation `json:"agentInfo"`
}

type acpMCPCapabilities struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
}

type acpPermissionRequest struct {
	SessionID string `json:"sessionId"`
	ToolCall  struct {
		ToolCallID string `json:"toolCallId"`
		Title      string `json:"title"`
		Kind       string `json:"kind"`
	} `json:"toolCall"`
	Options []struct {
		OptionID string `json:"optionId"`
		Name     string `json:"name"`
		Kind     string `json:"kind"`
	} `json:"options"`
}

// ProbeACP checks that image runs an ACP agent: it starts command in the same
// hardened container with --network none (no key, no model call, nothing to
// reach), sends initialize, and requires a protocol-version-1 answer. It is
// `idea executors setup --kind acp`'s presence check -- the ACP counterpart
// of VerifyAgentCLI's `<cli> --version`, which an ACP adapter need not
// implement. Returns the agent's self-reported name and version.
func ProbeACP(ctx context.Context, image string, command []string) (string, error) {
	if len(command) == 0 {
		return "", fmt.Errorf("executor: acp: no command to probe")
	}
	args := []string{
		"run", "-i", "--rm",
		"--network", "none",
		"--read-only",
		"--cap-drop", "ALL",
		"--security-opt", "no-new-privileges",
		"--user", containerUser(),
		"--memory", "2g",
		"--pids-limit", "256",
		"--tmpfs", "/tmp",
		"-w", "/tmp",
		"-e", "HOME=/tmp",
		image,
	}
	cmd := exec.Command("docker", append(args, command...)...)
	var stderr outputRing
	proc, err := startProc(cmd, &lineWriter{ring: &stderr}, func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	if err != nil {
		return "", fmt.Errorf("executor: acp: start probe: %w", err)
	}
	name, err := probeACPProc(ctx, proc)
	if err != nil {
		if tail := strings.TrimSpace(sanitizeAgentLog(stderr.tail(20))); tail != "" {
			return "", fmt.Errorf("%w; agent output:\n%s", err, tail)
		}
	}
	return name, err
}

// probeACPProc runs the initialize half of the handshake on a started agent
// and stops it.
func probeACPProc(ctx context.Context, proc *acpProc) (string, error) {
	defer func() {
		_ = proc.stdin.Close()
		select {
		case <-waitChan(proc):
		case <-time.After(acpExitGrace):
			proc.kill()
		}
	}()
	conn := newACPConn(proc.stdin, nil, nil, nil)
	go conn.readLoop(proc.stdout)
	var res acpInitializeResponse
	if err := conn.call(ctx, "initialize", acpInitializeRequest{
		ProtocolVersion:    acpProtocolVersion,
		ClientCapabilities: acpClientCapabilities{FS: acpFSCapabilities{ReadTextFile: true, WriteTextFile: true}},
		ClientInfo:         &acpImplementation{Name: "idea-harness", Version: buildinfo.Get().String()},
	}, &res); err != nil {
		return "", fmt.Errorf("executor: acp: the image did not answer initialize: %w", err)
	}
	if res.ProtocolVersion != acpProtocolVersion {
		return "", fmt.Errorf("executor: acp: the agent speaks ACP protocol version %d; idea-harness speaks %d", res.ProtocolVersion, acpProtocolVersion)
	}
	name := "unnamed ACP agent"
	if res.AgentInfo != nil && res.AgentInfo.Name != "" {
		name = strings.TrimSpace(res.AgentInfo.Name + " " + res.AgentInfo.Version)
	}
	return name, nil
}

func waitChan(proc *acpProc) <-chan struct{} {
	ch := make(chan struct{})
	go func() { _ = proc.wait(); close(ch) }()
	return ch
}
