package workflow

import (
	"context"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/security"
)

// Runs on a remote execution plane (executor kind openhands) outlive the
// Execute that started them. The engine owns one RemoteSupervisor per
// project for them: Execute's executor registers its run with it, the daemon
// asks it to re-attach to whatever a previous daemon left running
// (ReconcileRemoteRuns), and to let go without stopping anything when it
// shuts down (DetachRemoteRuns).
//
// None of this takes e.mu. Execute holds e.mu while it waits on its own run,
// as it does for every executor kind -- one Execute per engine is the lock's
// whole design -- but reconciliation and the watchers write only their own
// remote_runs rows, so a run the previous daemon left going is followed to
// the end without blocking, or being blocked by, anything else.

// RemoteSupervisor returns the engine's supervisor, creating it on first use.
func (e *Engine) RemoteSupervisor() *executor.RemoteSupervisor {
	e.remoteMu.Lock()
	defer e.remoteMu.Unlock()
	if e.remote == nil {
		e.remote = executor.NewRemoteSupervisor(e.log)
	}
	return e.remote
}

// ReconcileRemoteRuns re-attaches to every remote run not yet over: each is
// caught up from its event cursor and followed until it finishes (its task's
// next execute collects the result), fails, or turns out to have vanished
// from the server. It returns once the watchers are started.
func (e *Engine) ReconcileRemoteRuns(ctx context.Context) (int, error) {
	snap := e.snapshotConfig()
	oh := snap.cfg.Executor.OpenHands
	opts := executor.OpenHandsOptionsFrom(snap.cfg.Executor, "")
	return e.RemoteSupervisor().Reconcile(ctx, e.db, executor.OpenHandsReconcile{
		// The session key is read through the configured variable, from the
		// daemon's environment and then .env, as for a fresh run. A run
		// dispatched to a server the config no longer names still gets that
		// variable: there is no other place its key could be.
		SessionKey: func(string) string { return e.lookupKey(oh.EffectiveAPIKeyEnv()) },
		Redact:     e.redactRemote,
		Timeout:    opts.Timeout,
	})
}

// DetachRemoteRuns stops following remote runs without stopping them: the
// daemon is shutting down, and the next one re-attaches.
func (e *Engine) DetachRemoteRuns() {
	e.remoteMu.Lock()
	sup := e.remote
	e.remoteMu.Unlock()
	if sup != nil {
		sup.Detach()
	}
}

// openHandsExecutor builds the openhands executor for this engine's
// configuration.
func (e *Engine) openHandsExecutor(model string, provider executor.AgentProvider) (*executor.OpenHandsExecutor, error) {
	snap := e.snapshotConfig()
	ec := snap.cfg.Executor
	if ec.OpenHands.URL == "" {
		return nil, errOpenHandsURL
	}
	if err := config.ValidateOpenHandsURL(ec.OpenHands.URL); err != nil {
		return nil, err
	}
	ex := executor.NewOpenHandsExecutor(executor.OpenHandsOptionsFrom(ec, model), provider, e.db, e.RemoteSupervisor())
	ex.SetKeyLookup(e.lookupKey)
	ex.SetRedactor(e.redactRemote)
	return ex, nil
}

// redactRemote redacts what is stored from a remote server's events: the
// project's redactor when there is one, the credential patterns regardless.
func (e *Engine) redactRemote(s string) string {
	if r := e.snapshotConfig().redactor; r != nil {
		return r.RedactString(s)
	}
	return security.Redact(s)
}
