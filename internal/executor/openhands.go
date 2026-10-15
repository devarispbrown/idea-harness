package executor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/fsdiff"
	"github.com/devarispbrown/idea-harness/internal/id"
	"github.com/devarispbrown/idea-harness/internal/security"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// OpenHandsExecutor delegates the agent run to an OpenHands Agent Server: the
// server is the execution plane, idea-harness stays the control plane. What
// the server is trusted with, and what it is not:
//
//   - It receives the work root (the seeded copy of the workspace, already
//     stripped of .env files, credential-looking files and the control
//     plane), the implementation brief, and -- unless withheld -- the coding
//     model's provider key. That is what leaves the machine.
//   - It decides nothing. Approval, the workflow state, the change set and
//     its review all stay here. What the agent wrote comes back as files
//     into the local work root, and the engine snapshots, diffs, secret-
//     sweeps and reviews them exactly as it does a local agent's.
//
// Runs are durable (store.remote_runs): the conversation keeps going on the
// server when the daemon stops, and is re-attached to rather than started
// twice. See RemoteSupervisor for the reconciliation side.
type OpenHandsExecutor struct {
	opts      OpenHandsOptions
	provider  AgentProvider
	runs      *store.DB
	sup       *RemoteSupervisor
	lookupKey func(string) string
	redact    func(string) string
	// newClient is the transport; tests point it at a fake server.
	newClient func(url, key string) (*ohClient, error)

	mu       sync.Mutex
	attempts map[string]*ohAttempt
}

// OpenHandsOptions is the openhands kind's configuration.
type OpenHandsOptions struct {
	URL                 string
	APIKeyEnv           string
	Agent               string // openhands | acp
	ACPCommand          []string
	WorkspaceDir        string
	Model               string
	WithholdProviderKey bool
	MaxIterations       int
	Timeout             time.Duration
}

// OpenHandsOptionsFrom builds OpenHandsOptions from harness.yaml.
func OpenHandsOptionsFrom(ec config.ExecutorConfig, model string) OpenHandsOptions {
	o := ec.OpenHands
	opts := OpenHandsOptions{
		URL:                 o.URL,
		APIKeyEnv:           o.EffectiveAPIKeyEnv(),
		Agent:               o.EffectiveAgent(),
		ACPCommand:          o.ACPCommand,
		WorkspaceDir:        o.EffectiveWorkspaceDir(),
		Model:               model,
		WithholdProviderKey: o.WithholdProviderKey,
		MaxIterations:       o.MaxIterations,
	}
	if o.TimeoutSeconds > 0 {
		opts.Timeout = time.Duration(o.TimeoutSeconds) * time.Second
	}
	return opts
}

const (
	openHandsName = "openhands"
	// defaultOHTimeout matches acp's: under the engine's 15-minute ceiling,
	// so the run is interrupted cleanly on the server rather than abandoned.
	defaultOHTimeout       = 840 * time.Second
	defaultOHMaxIterations = 100
	// ohCancelSyncTimeout bounds bringing a stopped run's files back.
	ohCancelSyncTimeout = 60 * time.Second
)

// ohAttempt is the in-process state of one Start: what Collect and Cancel
// need that the durable record does not hold.
type ohAttempt struct {
	runID     string
	workRoot  string
	uploaded  fsdiff.Manifest
	remoteDir string
	convID    string
	client    *ohClient
	secrets   []string
	synced    bool
}

// NewOpenHandsExecutor returns the executor. runs is the project's state
// database (the remote_runs table); sup is the engine's supervisor, shared by
// every Start so one watcher follows each remote run.
func NewOpenHandsExecutor(opts OpenHandsOptions, provider AgentProvider, runs *store.DB, sup *RemoteSupervisor) *OpenHandsExecutor {
	if opts.Timeout <= 0 {
		opts.Timeout = defaultOHTimeout
	}
	if opts.MaxIterations <= 0 {
		opts.MaxIterations = defaultOHMaxIterations
	}
	if opts.APIKeyEnv == "" {
		opts.APIKeyEnv = config.DefaultOpenHandsAPIKeyEnv
	}
	if opts.Agent == "" {
		opts.Agent = "openhands"
	}
	if opts.WorkspaceDir == "" {
		opts.WorkspaceDir = config.DefaultOpenHandsWorkspaceDir
	}
	if sup == nil {
		sup = NewRemoteSupervisor(nil)
	}
	return &OpenHandsExecutor{
		opts:      opts,
		provider:  provider,
		runs:      runs,
		sup:       sup,
		lookupKey: os.Getenv,
		redact:    security.Redact,
		newClient: newOHClient,
		attempts:  map[string]*ohAttempt{},
	}
}

// SetKeyLookup sets where the session key and the provider key are read
// from (the daemon's environment, then the project's .env).
func (e *OpenHandsExecutor) SetKeyLookup(lookup func(string) string) {
	if lookup != nil {
		e.lookupKey = lookup
	}
}

// SetRedactor sets the redaction applied to everything stored from the
// server's events (the project's redactor: its known secrets and the
// credential patterns). The executor's own secrets are removed first either
// way.
func (e *OpenHandsExecutor) SetRedactor(redact func(string) string) {
	if redact != nil {
		e.redact = redact
	}
}

// Name reports the executor's registered name.
func (e *OpenHandsExecutor) Name() string { return openHandsName }

// Capabilities: the agent runs on another machine, in whatever sandbox that
// server provides -- which the harness neither sees nor enforces, so it is
// reported as remote, not container.
func (e *OpenHandsExecutor) Capabilities(ctx context.Context) Capabilities {
	return Capabilities{
		Trust:        domain.ExecutorRemoteExternal,
		SandboxMode:  domain.SandboxRemote,
		Network:      true,
		Capabilities: []string{"fs.write"},
	}
}

// IdempotencyKey is the remote-run key for one (task, attempt). Deterministic,
// so a retry after a crash computes the same key and finds the same row.
func IdempotencyKey(taskID string, attempt int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("idea-harness/openhands/v1\x00%s\x00%d", taskID, attempt)))
	return hex.EncodeToString(sum[:16])
}

// conversationIDFor derives the remote conversation id from the key, as a
// UUID (version 8, "custom"): the server accepts a client-chosen id and
// treats a create with an existing one as "return it", so a crash between
// "create sent" and "id recorded" cannot start a second conversation.
func conversationIDFor(key string) string {
	sum := sha256.Sum256([]byte("conversation\x00" + key))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x80
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// sessionKey reads the server's session key ("" when unset: a server
// started without one needs none, and one that does answers 401, which is
// reported as such).
func (e *OpenHandsExecutor) sessionKey() string { return e.lookupKey(e.opts.APIKeyEnv) }

// providerKey is the coding model's key as sent to the server, or "" when
// withheld or keyless. A keyed provider whose key is set nowhere is refused
// before anything leaves the machine.
func (e *OpenHandsExecutor) providerKey() (string, error) {
	if e.opts.WithholdProviderKey || e.provider.KeyEnv == "" {
		return "", nil
	}
	v := e.lookupKey(e.provider.KeyEnv)
	if v == "" {
		return "", fmt.Errorf("executor: openhands: provider %q needs %s, which is set neither in the daemon's environment nor in the project's .env "+
			"(or set executor.openhands.withhold_provider_key: true if the Agent Server supplies its own)", e.provider.Name, e.provider.KeyEnv)
	}
	return v, nil
}

// Start dispatches the run, or re-attaches to the one already dispatched for
// this (task, attempt).
func (e *OpenHandsExecutor) Start(ctx context.Context, req Request) (Attempt, error) {
	if e.runs == nil {
		return Attempt{}, fmt.Errorf("executor: openhands: no run store (remote runs must be durable)")
	}
	if err := config.ValidateOpenHandsURL(e.opts.URL); err != nil {
		return Attempt{}, fmt.Errorf("executor: openhands: refusing to dispatch: %w", err)
	}
	if e.opts.Agent == "acp" && len(e.opts.ACPCommand) == 0 {
		return Attempt{}, fmt.Errorf("executor: openhands: agent acp needs executor.openhands.acp_command, the ACP agent's argv on the server")
	}
	pkey, err := e.providerKey()
	if err != nil {
		return Attempt{}, err
	}
	skey := e.sessionKey()
	client, err := e.newClient(e.opts.URL, skey)
	if err != nil {
		return Attempt{}, err
	}
	if err := os.MkdirAll(req.WorkRoot, 0o755); err != nil {
		return Attempt{}, fmt.Errorf("executor: openhands: mkdir work root: %w", err)
	}
	upload, manifest, err := packWorkRoot(req.WorkRoot)
	if err != nil {
		return Attempt{}, err
	}
	digest := manifest.Digest()
	secrets := nonEmpty(skey, pkey)

	// At most a few rounds: each one retires a stale or vanished run and
	// moves to the next attempt number, so this cannot loop.
	for round := 0; round < 4; round++ {
		rec, err := e.claimRun(ctx, req, digest)
		if err != nil {
			return Attempt{}, err
		}
		if rec.ServerURL != e.opts.URL || rec.BaseDigest != digest {
			reason := "the workspace changed since this run was dispatched, so its result no longer applies"
			if rec.ServerURL != e.opts.URL {
				reason = fmt.Sprintf("executor.openhands.url changed from %s since this run was dispatched", rec.ServerURL)
			}
			e.retire(ctx, rec, client, reason)
			continue
		}
		if req.RunID != "" && rec.RunID != req.RunID {
			// The run whose event stream this remote run now feeds: this
			// Execute's, whether it dispatched the run or re-attached to it.
			if err := e.runs.SetRemoteRunAgentRun(ctx, rec.ID, req.RunID); err != nil {
				return Attempt{}, err
			}
		}
		unlock := e.sup.lockRun(rec.ID)
		rec, err = e.runs.GetRemoteRun(ctx, rec.ID)
		if err != nil {
			unlock()
			return Attempt{}, err
		}
		switch rec.Status {
		case domain.RemoteRunCollected, domain.RemoteRunFailed, domain.RemoteRunCancelled:
			// Retired between claim and lock (a reconciler found it gone).
			unlock()
			continue
		case domain.RemoteRunDispatching:
			err = e.dispatch(ctx, client, rec, req, upload, pkey)
		case domain.RemoteRunRunning:
			// Re-attach: it must still exist on the server.
			if _, gerr := client.getConversation(ctx, rec.ConversationID); errors.Is(gerr, ErrOpenHandsNotFound) {
				e.markFailed(ctx, rec.ID, vanishedReason(rec))
				unlock()
				continue
			} else if gerr != nil && !isTransient(gerr) {
				err = gerr
			}
		case domain.RemoteRunFinished:
			// Finished while nobody was attached: collect it.
		}
		unlock()
		if err != nil {
			return Attempt{}, fmt.Errorf("executor: openhands: %w", err)
		}
		e.mu.Lock()
		e.attempts[rec.ID] = &ohAttempt{runID: rec.ID, workRoot: req.WorkRoot, uploaded: manifest, remoteDir: rec.RemoteDir,
			convID: rec.ConversationID, client: client, secrets: secrets}
		e.mu.Unlock()
		if rec.Status != domain.RemoteRunFinished {
			e.sup.watch(rec.ID, e.watchSpec(client, secrets))
		}
		return Attempt{ID: rec.ID, Executor: openHandsName, Status: "running", Phase: "Editing"}, nil
	}
	return Attempt{}, fmt.Errorf("executor: openhands: could not claim a remote run for task %s", req.TaskID)
}

// claimRun finds or creates the record for this task's current attempt.
func (e *OpenHandsExecutor) claimRun(ctx context.Context, req Request, digest string) (*domain.RemoteRun, error) {
	taskKey := req.TaskID
	if taskKey == "" {
		// No task (a smoke test): nothing to be idempotent about.
		taskKey = "adhoc-" + id.New()
	}
	n, err := e.runs.CountTerminalRemoteRuns(ctx, openHandsName, taskKey)
	if err != nil {
		return nil, err
	}
	key := IdempotencyKey(taskKey, n+1)
	conv := conversationIDFor(key)
	rec, _, err := e.runs.CreateRemoteRun(ctx, domain.RemoteRun{
		Executor: openHandsName, IdempotencyKey: key, InitiativeID: req.InitiativeID, TaskID: taskKey, Attempt: n + 1,
		ServerURL: e.opts.URL, ConversationID: conv, RemoteDir: e.opts.WorkspaceDir + "/" + conv, BaseDigest: digest,
		Status: domain.RemoteRunDispatching,
	})
	return rec, err
}

// dispatch uploads the work root and starts the conversation. Every step is
// idempotent, so a dispatch interrupted anywhere is simply run again: the
// conversation id is fixed by the key, the unpack starts with rm -rf, and a
// create for an existing id returns it.
func (e *OpenHandsExecutor) dispatch(ctx context.Context, c *ohClient, rec *domain.RemoteRun, req Request, upload []byte, pkey string) error {
	if _, err := c.getConversation(ctx, rec.ConversationID); err == nil {
		// Created before a crash; only the status write was lost.
		return e.setStatus(ctx, rec.ID, domain.RemoteRunRunning, ohRunning)
	} else if !errors.Is(err, ErrOpenHandsNotFound) {
		return err
	}
	archive := "/tmp/idea-harness-" + rec.ConversationID + ".tar.gz"
	if err := c.upload(ctx, archive, upload); err != nil {
		return err
	}
	out, err := c.execute(ctx, unpackCommand(archive, rec.RemoteDir), "/", 300)
	if err != nil {
		return err
	}
	if out.ExitCode == nil || *out.ExitCode != 0 {
		code := "no exit code"
		if out.ExitCode != nil {
			code = fmt.Sprintf("exit %d", *out.ExitCode)
		}
		return fmt.Errorf("unpacking the work root on the server failed (%s): %s", code,
			e.scrub(strings.TrimSpace(out.Stderr+" "+out.Stdout), nonEmpty(pkey, e.sessionKey())))
	}
	body, err := e.conversationBody(rec, req, pkey)
	if err != nil {
		return err
	}
	if _, err := c.startConversation(ctx, body); err != nil {
		return err
	}
	return e.setStatus(ctx, rec.ID, domain.RemoteRunRunning, ohRunning)
}

// conversationBody is the StartConversationRequest (openhands-sdk
// conversation/request.py). confirmation_policy is left out on purpose: the
// server's default is NeverConfirm, the same auto-approve every local agent
// kind runs with, and leaving it out avoids depending on its JSON shape.
func (e *OpenHandsExecutor) conversationBody(rec *domain.RemoteRun, req Request, pkey string) (map[string]any, error) {
	stmt := req.Brief
	if stmt == "" {
		stmt = req.TaskStatement
	}
	if stmt == "" {
		stmt = req.TaskTitle
	}
	prompt := fmt.Sprintf("Your working directory is %s, a copy of the project. Make the change below by editing files there. "+
		"Do not commit, and do not install toolchains to verify your work -- the harness builds and tests it afterwards.\n\n%s", rec.RemoteDir, stmt)
	body := map[string]any{
		"conversation_id": rec.ConversationID,
		"workspace":       map[string]any{"working_dir": rec.RemoteDir},
		"initial_message": map[string]any{"role": "user", "content": []map[string]string{{"type": "text", "text": prompt}}, "run": true},
		"max_iterations":  e.opts.MaxIterations,
	}
	if e.opts.Agent == "acp" {
		body["agent"] = map[string]any{"kind": "ACPAgent", "acp_command": e.opts.ACPCommand}
		if s := acpSecrets(e.provider.KeyEnv, pkey); s != nil {
			body["secrets"] = s
		}
		return body, nil
	}
	model, base := liteLLMModel(e.opts.Model, e.provider)
	if model == "" {
		return nil, fmt.Errorf("no coding model is configured for the OpenHands agent; set executor.model or models.implementer")
	}
	llm := map[string]any{"model": model, "usage_id": "agent"}
	if pkey != "" {
		llm["api_key"] = pkey
	}
	if base != "" {
		llm["base_url"] = base
	}
	body["agent"] = map[string]any{
		"kind": "Agent",
		"llm":  llm,
		// Registered tool names (openhands-tools preset/default.py): the
		// snake_case name, not the class name.
		"tools": []map[string]string{{"name": "terminal"}, {"name": "file_editor"}, {"name": "task_tracker"}},
	}
	return body, nil
}

// acpSecrets hands an ACP agent its provider key as a conversation secret,
// which the server exports into the agent's environment under that name.
//
// ASSUMED: the JSON form of a secret value, {"kind": "StaticSecret",
// "value": ...} -- the class name is confirmed (sdk secret sources are
// discriminated by kind), the exact serialisation is inferred.
func acpSecrets(keyEnv, key string) map[string]any {
	if keyEnv == "" || key == "" {
		return nil
	}
	return map[string]any{keyEnv: map[string]string{"kind": "StaticSecret", "value": key}}
}

// liteLLMModel maps executor.model to the model name and base URL the
// OpenHands agent's LLM layer (LiteLLM) expects. The six built-in providers
// already use LiteLLM's own prefixes; a custom OpenAI-compatible provider
// goes through LiteLLM's openai/ route with its base URL.
func liteLLMModel(spec string, p AgentProvider) (model, baseURL string) {
	_, bare := config.ParseModelSpec(spec)
	if bare == "" {
		return "", ""
	}
	kind := p.Name
	if config.PresetFor(kind) == nil {
		if preset := config.PresetForBaseURL(p.BaseURL); preset != nil {
			kind = preset.Key
		}
	}
	switch kind {
	case "openai", "anthropic", "gemini", "deepseek", "openrouter":
		return kind + "/" + bare, ""
	case "ollama":
		// The server's localhost is not this machine's: an Ollama base URL
		// of localhost only works when the server runs here.
		return "ollama/" + bare, strings.TrimSuffix(strings.TrimRight(p.BaseURL, "/"), "/v1")
	}
	switch p.Type {
	case config.ProviderAnthropic:
		return "anthropic/" + bare, p.BaseURL
	case config.ProviderGemini:
		return "gemini/" + bare, p.BaseURL
	}
	return "openai/" + bare, p.BaseURL
}

func vanishedReason(rec *domain.RemoteRun) string {
	return fmt.Sprintf("the remote conversation %s no longer exists on %s (deleted, or the server restarted without persistence); "+
		"run the task again to start a new one", rec.ConversationID, rec.ServerURL)
}

// retire fails a run that can no longer be used and best-effort stops it on
// the server, so a stale agent does not keep spending.
func (e *OpenHandsExecutor) retire(ctx context.Context, rec *domain.RemoteRun, c *ohClient, reason string) {
	e.markFailed(ctx, rec.ID, reason)
	e.sup.stop(rec.ID)
	if rec.ConversationID != "" && rec.ServerURL == e.opts.URL {
		ictx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		_ = c.interrupt(ictx, rec.ConversationID)
		cancel()
	}
}

func (e *OpenHandsExecutor) setStatus(ctx context.Context, runID string, st domain.RemoteRunStatus, remote string) error {
	return e.runs.UpdateRemoteRun(context.WithoutCancel(ctx), runID, store.RemoteRunUpdate{Status: &st, RemoteStatus: &remote})
}

func (e *OpenHandsExecutor) markFailed(ctx context.Context, runID, reason string) {
	st := domain.RemoteRunFailed
	_ = e.runs.UpdateRemoteRun(context.WithoutCancel(ctx), runID, store.RemoteRunUpdate{Status: &st, Error: &reason})
}

func (e *OpenHandsExecutor) watchSpec(c *ohClient, secrets []string) watchSpec {
	return watchSpec{runs: e.runs, client: c, timeout: e.opts.Timeout, redact: func(s string) string { return e.scrub(s, secrets) }}
}

// scrub removes the executor's own secrets, then applies the redactor.
func (e *OpenHandsExecutor) scrub(s string, secrets []string) string {
	for _, v := range secrets {
		if len(v) >= 8 {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	if e.redact != nil {
		s = e.redact(s)
	}
	return s
}

func (e *OpenHandsExecutor) attempt(runID string) (*ohAttempt, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.attempts[runID]
	if !ok {
		return nil, fmt.Errorf("%w %q", ErrUnknownAttempt, runID)
	}
	return a, nil
}

// Status reports the run as the harness last recorded it; the supervisor's
// watcher keeps the record current.
func (e *OpenHandsExecutor) Status(ctx context.Context, runID string) (Status, error) {
	if _, err := e.attempt(runID); err != nil {
		return Status{}, err
	}
	rec, err := e.runs.GetRemoteRun(ctx, runID)
	if err != nil {
		return Status{}, err
	}
	switch rec.Status {
	case domain.RemoteRunFinished, domain.RemoteRunCollected:
		return Status{Status: "succeeded", Phase: "Finished", Action: "the agent finished on the server"}, nil
	case domain.RemoteRunFailed:
		return Status{Status: "failed", Phase: "Finished", Action: rec.Error}, nil
	case domain.RemoteRunCancelled:
		return Status{Status: "cancelled", Phase: "Finished", Action: "stopped"}, nil
	}
	return Status{Status: "running", Phase: "Editing", Action: lastLine(rec.Progress)}, nil
}

// Collect brings the server's files back into the work root -- the moment
// the remote result enters the local pipeline -- and marks the run
// collected. The engine then snapshots and diffs the work root as for any
// agent, secret sweep and review included.
func (e *OpenHandsExecutor) Collect(ctx context.Context, runID string) (Result, error) {
	a, err := e.attempt(runID)
	if err != nil {
		return Result{}, err
	}
	rec, err := e.runs.GetRemoteRun(ctx, runID)
	if err != nil {
		return Result{}, err
	}
	if rec.Status == domain.RemoteRunCollected {
		e.forget(runID)
		return Result{}, nil
	}
	if rec.Status != domain.RemoteRunFinished {
		return Result{}, fmt.Errorf("executor: openhands: run %s is %s, not finished", runID, rec.Status)
	}
	if err := e.syncBack(ctx, a); err != nil {
		if errors.Is(err, ErrOpenHandsNotFound) {
			e.markFailed(ctx, runID, vanishedReason(rec))
		}
		return Result{}, fmt.Errorf("executor: openhands: bring the agent's files back: %w", err)
	}
	st := domain.RemoteRunCollected
	if err := e.runs.UpdateRemoteRun(context.WithoutCancel(ctx), runID, store.RemoteRunUpdate{Status: &st}); err != nil {
		return Result{}, err
	}
	e.forget(runID)
	return Result{}, nil
}

func (e *OpenHandsExecutor) syncBack(ctx context.Context, a *ohAttempt) error {
	data, err := a.client.archive(ctx, a.remoteDir)
	if err != nil {
		return err
	}
	res, err := mirrorInto(ctx, a.client, data, a.workRoot, a.remoteDir, a.uploaded)
	if err != nil {
		return err
	}
	a.synced = true
	note := fmt.Sprintf("files brought back from the server: %d written, %d deleted", res.Written, res.Deleted)
	if len(res.Skipped) > 0 {
		note += fmt.Sprintf("; %d skipped (%s)", len(res.Skipped), strings.Join(firstN(res.Skipped, 5), ", "))
	}
	e.appendNote(ctx, a.runID, note)
	return nil
}

// appendNote adds one line to the run's progress log outside event
// ingestion (the cursor does not move).
func (e *OpenHandsExecutor) appendNote(ctx context.Context, runID, line string) {
	rec, err := e.runs.GetRemoteRun(ctx, runID)
	if err != nil {
		return
	}
	_, _ = e.runs.AppendRemoteRunProgress(context.WithoutCancel(ctx), runID, rec.LastEventID, rec.LastEventID, 0, []string{e.redact(line)})
}

func (e *OpenHandsExecutor) forget(runID string) {
	e.mu.Lock()
	delete(e.attempts, runID)
	e.mu.Unlock()
}

// Cancel stops the run: marked cancelled here first (a terminal status the
// watcher and any later poll respect), then interrupted on the server, with
// pause as the fallback. Whatever the agent had written is brought back into
// the work root, so Stop keeps it like it does for a local agent.
//
// While the daemon is shutting down it does none of that: the run keeps
// going on the server and is re-attached to on the next start, which is the
// point of a remote execution plane.
func (e *OpenHandsExecutor) Cancel(ctx context.Context, runID string) error {
	a, err := e.attempt(runID)
	if err != nil {
		return err
	}
	if e.sup.Detached() {
		e.forget(runID)
		return nil
	}
	rec, err := e.runs.GetRemoteRun(ctx, runID)
	if err != nil {
		return err
	}
	if rec.Status == domain.RemoteRunCollected {
		e.forget(runID)
		return nil
	}
	if !rec.Status.Terminal() {
		st := domain.RemoteRunCancelled
		msg := "stopped by the operator"
		if err := e.runs.UpdateRemoteRun(context.WithoutCancel(ctx), runID, store.RemoteRunUpdate{Status: &st, Error: &msg}); err != nil {
			return err
		}
	}
	e.sup.stop(runID)
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), ohCancelSyncTimeout)
	defer cancel()
	if rec.Status == domain.RemoteRunRunning || rec.Status == domain.RemoteRunDispatching {
		if err := a.client.interrupt(bg, a.convID); err != nil && !errors.Is(err, ErrOpenHandsNotFound) {
			if perr := a.client.pause(bg, a.convID); perr != nil && !errors.Is(perr, ErrOpenHandsNotFound) {
				e.appendNote(ctx, runID, "could not stop the run on the server: "+e.scrub(perr.Error(), a.secrets))
			}
		}
	}
	if !a.synced && rec.Status != domain.RemoteRunDispatching {
		if err := e.syncBack(bg, a); err != nil {
			e.appendNote(ctx, runID, "could not bring the stopped run's files back: "+e.scrub(err.Error(), a.secrets))
		}
	}
	e.forget(runID)
	return nil
}

// OutputTail implements OutputTailer from the run's stored progress log,
// which was redacted as it was ingested.
func (e *OpenHandsExecutor) OutputTail(ctx context.Context, runID string, lines int) string {
	a, err := e.attempt(runID)
	if err != nil {
		return ""
	}
	rec, err := e.runs.GetRemoteRun(ctx, runID)
	if err != nil {
		return ""
	}
	if lines <= 0 {
		lines = agentOutputLines
	}
	out := tailLines(rec.Progress, lines)
	if rec.Error != "" && (rec.Status == domain.RemoteRunFailed || rec.Status == domain.RemoteRunCancelled) {
		out = strings.TrimSpace(out + "\n" + rec.Error)
	}
	return e.scrub(out, a.secrets)
}

// ---------- supervision ----------

// RemoteSupervisor follows remote runs for one project: one watcher per run,
// whoever asked for it -- an Execute that dispatched or re-attached, or the
// daemon's reconciliation at start. It is never under the engine lock: a
// watcher only writes its own remote_runs row.
type RemoteSupervisor struct {
	log *slog.Logger

	mu       sync.Mutex
	ctx      context.Context
	cancel   context.CancelFunc
	watchers map[string]context.CancelFunc
	locks    map[string]*sync.Mutex
	detached bool
	wg       sync.WaitGroup

	// pollInterval and wsRetry pace a watcher; fields for tests.
	pollInterval time.Duration
	wsRetry      time.Duration
}

// NewRemoteSupervisor returns a supervisor. log may be nil.
func NewRemoteSupervisor(log *slog.Logger) *RemoteSupervisor {
	ctx, cancel := context.WithCancel(context.Background())
	return &RemoteSupervisor{log: log, ctx: ctx, cancel: cancel, watchers: map[string]context.CancelFunc{},
		locks: map[string]*sync.Mutex{}, pollInterval: 2 * time.Second, wsRetry: 5 * time.Second}
}

// SetPollIntervalForTest makes watchers poll faster.
func (s *RemoteSupervisor) SetPollIntervalForTest(d time.Duration) {
	s.mu.Lock()
	s.pollInterval, s.wsRetry = d, d
	s.mu.Unlock()
}

// Detach stops every watcher WITHOUT stopping the runs: the daemon is
// shutting down, and the runs are re-attached to on the next start. Cancel
// on an executor of a detached supervisor leaves the server alone.
func (s *RemoteSupervisor) Detach() {
	s.mu.Lock()
	s.detached = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
}

// Detached reports whether Detach was called.
func (s *RemoteSupervisor) Detached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detached
}

// Wait blocks until every watcher has returned (tests).
func (s *RemoteSupervisor) Wait() { s.wg.Wait() }

// lockRun serialises dispatch and reconciliation of one run in this
// process, so a reconciler cannot fail a run as "never started" while an
// Execute is in the middle of starting it.
func (s *RemoteSupervisor) lockRun(runID string) func() {
	s.mu.Lock()
	l, ok := s.locks[runID]
	if !ok {
		l = &sync.Mutex{}
		s.locks[runID] = l
	}
	s.mu.Unlock()
	l.Lock()
	return l.Unlock
}

type watchSpec struct {
	runs    *store.DB
	client  *ohClient
	timeout time.Duration
	redact  func(string) string
}

// watch starts following a run unless something already is.
func (s *RemoteSupervisor) watch(runID string, spec watchSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.detached || s.watchers[runID] != nil {
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.watchers[runID] = cancel
	s.wg.Add(1)
	poll, wsRetry := s.pollInterval, s.wsRetry
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.watchers, runID)
			s.mu.Unlock()
			cancel()
		}()
		w := &watcher{runID: runID, spec: spec, poll: poll, wsRetry: wsRetry, log: s.log}
		w.run(ctx)
	}()
}

// stop ends a run's watcher (the run itself is not touched).
func (s *RemoteSupervisor) stop(runID string) {
	s.mu.Lock()
	cancel := s.watchers[runID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// OpenHandsReconcile is what reconciliation needs from the current
// configuration: how to read the session key, and how to redact.
type OpenHandsReconcile struct {
	SessionKey func(serverURL string) string
	Redact     func(string) string
	Timeout    time.Duration
}

// Reconcile re-attaches to every non-terminal remote run in runs: on daemon
// start, a run the previous daemon left going is caught up from its event
// cursor and followed to the end -- finished (collected by the next execute
// of its task), failed, or found vanished. It returns at once; the watchers
// run in the background on the supervisor's own context.
func (s *RemoteSupervisor) Reconcile(ctx context.Context, runs *store.DB, rc OpenHandsReconcile) (int, error) {
	active, err := runs.ListRemoteRuns(ctx, true, 0)
	if err != nil {
		return 0, err
	}
	n := 0
	for i := range active {
		rec := active[i]
		if rec.Executor != openHandsName {
			continue
		}
		if rec.Status == domain.RemoteRunFinished {
			// Done on the server; waiting for its task's next execute.
			continue
		}
		fail := func(reason string) {
			st := domain.RemoteRunFailed
			_ = runs.UpdateRemoteRun(context.WithoutCancel(ctx), rec.ID, store.RemoteRunUpdate{Status: &st, Error: &reason})
		}
		if err := config.ValidateOpenHandsURL(rec.ServerURL); err != nil {
			fail("cannot re-attach: " + err.Error())
			continue
		}
		key := ""
		if rc.SessionKey != nil {
			key = rc.SessionKey(rec.ServerURL)
		}
		client, err := newOHClient(rec.ServerURL, key)
		if err != nil {
			fail("cannot re-attach: " + err.Error())
			continue
		}
		redact := rc.Redact
		if redact == nil {
			redact = security.Redact
		}
		scrub := func(s string) string {
			if len(key) >= 8 {
				s = strings.ReplaceAll(s, key, "[redacted]")
			}
			return redact(s)
		}
		timeout := rc.Timeout
		if timeout <= 0 {
			timeout = defaultOHTimeout
		}
		if rec.Status == domain.RemoteRunDispatching {
			// Under the run lock: an Execute in this process may be
			// dispatching it right now.
			unlock := s.lockRun(rec.ID)
			cur, err := runs.GetRemoteRun(ctx, rec.ID)
			if err == nil && cur.Status == domain.RemoteRunDispatching {
				_, gerr := client.getConversation(ctx, rec.ConversationID)
				switch {
				case errors.Is(gerr, ErrOpenHandsNotFound):
					fail("the daemon stopped before this run started on the server, so nothing ran remotely; run the task again")
					unlock()
					continue
				case gerr == nil:
					st, remote := domain.RemoteRunRunning, ohRunning
					_ = runs.UpdateRemoteRun(context.WithoutCancel(ctx), rec.ID, store.RemoteRunUpdate{Status: &st, RemoteStatus: &remote})
				}
			}
			unlock()
		}
		s.watch(rec.ID, watchSpec{runs: runs, client: client, timeout: timeout, redact: scrub})
		n++
	}
	return n, nil
}

// ---------- the watcher ----------

type watcher struct {
	runID   string
	spec    watchSpec
	poll    time.Duration
	wsRetry time.Duration
	log     *slog.Logger
	// lastError is the newest error event's text, for the failure reason.
	lastError string
	// costLoaded/totIn/totOut/totCost are the token and cost totals already
	// stored as cost events for this run, so a "stats" update's running
	// totals become deltas -- loaded once from the store, so a watcher
	// re-attaching after a restart does not count the run's spend twice.
	costLoaded    bool
	totIn, totOut int64
	totCost       float64
}

// run follows one run until it is terminal (or finished), the context ends,
// or the server answers in a way that settles it. Event ingestion is always
// GET events/search from the stored cursor -- the same path for live
// following and for catching up after a restart, so there is one way events
// get in and the cursor guard makes it exactly-once. The websocket only says
// "something happened, look now"; without it the watcher polls.
func (w *watcher) run(ctx context.Context) {
	db, c := w.spec.runs, w.spec.client
	notify := make(chan struct{}, 1)
	rec, err := db.GetRemoteRun(ctx, w.runID)
	if err != nil {
		return
	}
	wsCtx, stopWS := context.WithCancel(ctx)
	defer stopWS()
	go w.socket(wsCtx, rec.ConversationID, notify)

	backoff := w.poll
	consecutive := 0
	for {
		rec, err = db.GetRemoteRun(ctx, w.runID)
		if err != nil || rec.Status.Terminal() || rec.Status == domain.RemoteRunFinished {
			return
		}
		err := w.step(ctx, rec)
		switch {
		case err == nil:
			consecutive, backoff = 0, w.poll
		case ctx.Err() != nil:
			return
		case errors.Is(err, ErrOpenHandsAuth):
			w.fail(ctx, "the Agent Server refused the session API key while following the run; check executor.openhands.api_key_env")
			return
		case errors.Is(err, ErrOpenHandsNotFound):
			w.fail(ctx, vanishedReason(rec))
			return
		default:
			consecutive++
			if backoff < 30*time.Second {
				backoff *= 2
			}
			if w.log != nil && consecutive == 1 {
				w.log.Warn("openhands: following a remote run failed; retrying", "run", w.runID, "err", w.spec.redact(err.Error()))
			}
		}
		if time.Since(rec.CreatedAt) > w.spec.timeout {
			ictx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			_ = c.interrupt(ictx, rec.ConversationID)
			cancel()
			reason := fmt.Sprintf("the run did not finish within %s and was interrupted on the server", w.spec.timeout)
			if consecutive > 0 {
				reason = fmt.Sprintf("the Agent Server could not be reached for long enough that the run passed its %s limit (last error: %s)",
					w.spec.timeout, w.spec.redact(err.Error()))
			}
			w.fail(ctx, reason)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-notify:
		case <-time.After(backoff):
		}
	}
}

// step catches up on events, then settles the run if the server says it is
// over.
func (w *watcher) step(ctx context.Context, rec *domain.RemoteRun) error {
	if err := w.ingest(ctx); err != nil {
		return err
	}
	conv, err := w.spec.client.getConversation(ctx, rec.ConversationID)
	if err != nil {
		return err
	}
	db := w.spec.runs
	remote := conv.ExecutionStatus
	switch remote {
	case ohFinished:
		// One last catch-up, so the log ends with the agent's last words.
		_ = w.ingest(ctx)
		st := domain.RemoteRunFinished
		return db.UpdateRemoteRun(context.WithoutCancel(ctx), w.runID, store.RemoteRunUpdate{Status: &st, RemoteStatus: &remote})
	case ohError, ohStuck:
		_ = w.ingest(ctx)
		reason := "the agent stopped with an error on the server"
		if remote == ohStuck {
			reason = "the agent got stuck (the server's stuck detection stopped it)"
		}
		if w.lastError != "" {
			reason += ": " + w.lastError
		}
		w.fail(ctx, reason)
		return nil
	case ohPaused:
		w.fail(ctx, "the run was paused on the server, not by idea-harness; run the task again to start over")
		return nil
	case ohWaitingForConfirmation:
		// Runs are created with the server's default policy, NeverConfirm;
		// something changed it, and nobody here will answer.
		w.fail(ctx, "the agent is waiting for a confirmation on the server, which idea-harness does not give; run the task again")
		return nil
	case ohDeleting:
		w.fail(ctx, "the conversation is being deleted on the server")
		return nil
	}
	return db.UpdateRemoteRun(context.WithoutCancel(ctx), w.runID, store.RemoteRunUpdate{RemoteStatus: &remote})
}

func (w *watcher) fail(ctx context.Context, reason string) {
	st := domain.RemoteRunFailed
	reason = w.spec.redact(reason)
	_ = w.spec.runs.UpdateRemoteRun(context.WithoutCancel(ctx), w.runID, store.RemoteRunUpdate{Status: &st, Error: &reason})
}

// ingest pages through the events after the stored cursor and folds them
// into the progress log. A page that loses the cursor race (another ingester
// got there first) is simply re-read from the new cursor.
func (w *watcher) ingest(ctx context.Context) error {
	db, c := w.spec.runs, w.spec.client
	for page := 0; page < 50; page++ {
		rec, err := db.GetRemoteRun(ctx, w.runID)
		if err != nil {
			return err
		}
		events, headers, next, err := c.searchEventsAfter(ctx, rec.ConversationID, rec.LastEventID, 100)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		lines := make([]string, 0, len(events))
		var evs []domain.RunEvent
		for i, raw := range events {
			line, errText := eventLine(headers[i].Kind, headers[i].Source, raw)
			if errText != "" {
				w.lastError = w.spec.redact(errText)
			}
			if line != "" {
				lines = append(lines, w.spec.redact(line))
			}
			if rec.RunID == "" {
				continue
			}
			at := ohTimestamp(headers[i].Timestamp)
			mapped := ohRunEvents(headers[i].Kind, headers[i].Source, at, raw)
			if c, ok := w.costDelta(ctx, rec.RunID, headers[i].Kind, raw); ok {
				c.At = at
				mapped = append(mapped, c)
			}
			for _, ev := range mapped {
				ev.Path = ohRemoteRel(rec.RemoteDir, ev.Path)
				ev.Title = strings.ReplaceAll(ev.Title, rec.RemoteDir+"/", "")
				ev.InitiativeID, ev.TaskID, ev.Executor = rec.InitiativeID, rec.TaskID, openHandsName
				evs = append(evs, ev.Redacted(w.spec.redact))
			}
		}
		if _, err := db.AppendRemoteRunProgressEvents(context.WithoutCancel(ctx), w.runID, rec.LastEventID, headers[len(headers)-1].ID, len(events), lines, evs); err != nil {
			return err
		}
		if !next {
			return nil
		}
	}
	return nil
}

// costDelta turns a "stats" state update's running totals into a cost event
// carrying the change since the last one (ohStatsTotals).
func (w *watcher) costDelta(ctx context.Context, agentRunID, kind string, raw json.RawMessage) (domain.RunEvent, bool) {
	if kind != "ConversationStateUpdateEvent" {
		return domain.RunEvent{}, false
	}
	var s struct {
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}
	if json.Unmarshal(raw, &s) != nil || s.Key != "stats" {
		return domain.RunEvent{}, false
	}
	in, out, cost, ok := ohStatsTotals(s.Value)
	if !ok {
		return domain.RunEvent{}, false
	}
	if !w.costLoaded {
		i, o, c, err := w.spec.runs.RunEventCostTotals(ctx, agentRunID)
		if err != nil {
			return domain.RunEvent{}, false
		}
		w.totIn, w.totOut, w.totCost, w.costLoaded = i, o, c, true
	}
	dIn, dOut, dCost := in-w.totIn, out-w.totOut, cost-w.totCost
	if dIn <= 0 && dOut <= 0 && dCost <= 1e-9 {
		return domain.RunEvent{}, false
	}
	w.totIn, w.totOut, w.totCost = max(in, w.totIn), max(out, w.totOut), max(cost, w.totCost)
	return domain.RunEvent{Kind: domain.RunEventCost, Source: domain.RunEventSourceAgent,
		Title:       fmt.Sprintf("%d in / %d out tokens, $%.4f", max(dIn, 0), max(dOut, 0), max(dCost, 0)),
		InputTokens: max(dIn, 0), OutputTokens: max(dOut, 0), CostUSD: max(dCost, 0)}, true
}

// socket keeps the event websocket open while the run is followed, turning
// every frame into a wake-up. A socket that cannot be opened is retried
// slowly; polling covers the gap.
func (w *watcher) socket(ctx context.Context, convID string, notify chan<- struct{}) {
	for ctx.Err() == nil {
		err := w.spec.client.subscribe(ctx, convID, func([]byte) {
			select {
			case notify <- struct{}{}:
			default:
			}
		})
		if ctx.Err() != nil || errors.Is(err, ErrOpenHandsAuth) || errors.Is(err, ErrOpenHandsNotFound) {
			// The poller reports these; the socket just stops.
			return
		}
		if err := sleepCtx(ctx, w.wsRetry); err != nil {
			return
		}
	}
}

// eventLine turns one server event into a compact progress line (or none),
// and returns the error text of an error event. Thoughts and reasoning are
// never kept -- the same rule as every other agent's output (§50).
func eventLine(kind, source string, raw json.RawMessage) (line, errText string) {
	switch kind {
	case "MessageEvent":
		if source != "agent" {
			// The user message is the brief the harness sent.
			return "", ""
		}
		var ev struct {
			LLMMessage struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"llm_message"`
		}
		_ = json.Unmarshal(raw, &ev)
		var parts []string
		for _, c := range ev.LLMMessage.Content {
			if c.Text != "" {
				parts = append(parts, c.Text)
			}
		}
		if len(parts) == 0 {
			return "", ""
		}
		return "agent: " + compact(strings.Join(parts, " "), 400), ""
	case "ActionEvent":
		var ev struct {
			ToolName string          `json:"tool_name"`
			Summary  string          `json:"summary"`
			Action   json.RawMessage `json:"action"`
		}
		_ = json.Unmarshal(raw, &ev)
		detail := ev.Summary
		if detail == "" {
			var act struct {
				Command string `json:"command"`
				Path    string `json:"path"`
			}
			_ = json.Unmarshal(ev.Action, &act)
			detail = strings.TrimSpace(act.Command + " " + act.Path)
		}
		if ev.ToolName == "" {
			ev.ToolName = "action"
		}
		return compact("→ "+ev.ToolName+" "+detail, 240), ""
	case "ObservationEvent":
		var ev struct {
			ToolName    string `json:"tool_name"`
			Observation struct {
				ExitCode *int `json:"exit_code"`
				Error    bool `json:"is_error"`
			} `json:"observation"`
		}
		_ = json.Unmarshal(raw, &ev)
		if ev.Observation.ExitCode != nil && *ev.Observation.ExitCode != 0 {
			return fmt.Sprintf("← %s exited %d", ev.ToolName, *ev.Observation.ExitCode), ""
		}
		if ev.Observation.Error {
			return "← " + ev.ToolName + " failed", ""
		}
		return "", ""
	case "AgentErrorEvent":
		var ev struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(raw, &ev)
		msg := compact(ev.Error, 400)
		return "error: " + msg, msg
	case "ConversationErrorEvent":
		var ev struct {
			Code   string `json:"code"`
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(raw, &ev)
		msg := compact(strings.TrimSpace(ev.Code+": "+ev.Detail), 400)
		return "error: " + msg, msg
	case "ConversationStateUpdateEvent":
		var ev struct {
			Key   string          `json:"key"`
			Value json.RawMessage `json:"value"`
		}
		_ = json.Unmarshal(raw, &ev)
		if ev.Key == "execution_status" {
			var v string
			if json.Unmarshal(ev.Value, &v) == nil && v != "" {
				return "status: " + v, ""
			}
		}
		return "", ""
	case "PauseEvent":
		return "paused", ""
	case "InterruptEvent":
		return "interrupted", ""
	}
	return "", ""
}

// ---------- probes and smoke ----------

// ProbeOpenHands checks a server before it is configured or relied on: the
// URL is safe to use, GET /server_info answers, and the session key is
// accepted. It returns the server's version.
func ProbeOpenHands(ctx context.Context, serverURL, sessionKey string) (string, error) {
	if err := config.ValidateOpenHandsURL(serverURL); err != nil {
		return "", err
	}
	c, err := newOHClient(serverURL, sessionKey)
	if err != nil {
		return "", err
	}
	c.retry = ohRetry{attempts: 2, base: 500 * time.Millisecond, max: time.Second}
	info, err := c.serverInfo(ctx)
	if err != nil {
		return "", fmt.Errorf("the Agent Server at %s did not answer GET /server_info: %w", serverURL, err)
	}
	if err := c.checkAuth(ctx); err != nil {
		if errors.Is(err, ErrOpenHandsAuth) {
			return info.Version, fmt.Errorf("the Agent Server at %s refused the session API key: set the variable executor.openhands.api_key_env names", serverURL)
		}
		return info.Version, err
	}
	return info.Version, nil
}

// SmokeOpenHands runs the smoke task through an Agent Server: the trivial
// task every agent kind's smoke runs, the same assertion that a file came
// back. Its run record lives in a throwaway database under workDir.
func SmokeOpenHands(ctx context.Context, opts OpenHandsOptions, provider AgentProvider, lookupKey func(string) string, workDir string) error {
	if workDir != "" {
		if err := os.MkdirAll(workDir, 0o755); err != nil {
			return err
		}
	}
	dir, err := os.MkdirTemp(workDir, "idea-openhands-smoke-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	db, err := store.Open(dir + "/state.db")
	if err != nil {
		return err
	}
	defer db.Close()
	sup := NewRemoteSupervisor(nil)
	defer sup.Detach()
	return runSmoke(ctx, openHandsName, NewOpenHandsExecutor(opts, provider, db, sup), provider, lookupKey, workDir)
}

// ---------- small helpers ----------

func nonEmpty(vs ...string) []string {
	var out []string
	for _, v := range vs {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

func compact(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return append(append([]string{}, s[:n]...), "…")
	}
	return s
}
