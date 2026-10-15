package workflow

import (
	"context"
	"fmt"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/model"
)

// ExecutorResolution is which executor a run uses, and why.
//
// An unset executor.kind is "auto": hermes on its pinned image when Docker is
// reachable and the coding model's provider key is set, internal otherwise.
// Internal writes a placeholder file, not code, so falling back to it is never
// silent: Warning is set, and every surface that starts or inspects a run --
// `idea execute`, the run manifest, `idea doctor`, the Build page's guidance --
// shows it. Before auto existed an unset kind simply meant internal, and a
// user who never found docs/agents.md got placeholder change sets with nothing
// saying a real agent had never been tried.
type ExecutorResolution struct {
	// Requested is harness.yaml's executor.kind ("auto" when unset).
	Requested string `json:"requested"`
	// Resolved is the kind that runs.
	Resolved string `json:"resolved"`
	// Image is the agent image for an agent kind.
	Image string `json:"image,omitempty"`
	// Server is the Agent Server URL for kind openhands: where the work root
	// goes.
	Server string `json:"server,omitempty"`
	// Warning is the loud one: auto fell back to the placeholder executor.
	// It says why, and how to fix it.
	Warning string `json:"warning,omitempty"`
	// Note is the quiet one: executor.kind is explicitly internal, which is a
	// legitimate demo setup, so it is mentioned rather than shouted.
	Note string `json:"note,omitempty"`
}

// Placeholder reports whether this run writes a placeholder, not code.
func (r ExecutorResolution) Placeholder() bool {
	return r.Resolved == string(config.ExecutorInternal)
}

// autoDockerProbe answers "is Docker reachable" for auto resolution.
//
// Inside a `go test` binary it answers false unless a test swaps it
// (SetAutoDockerProbeForTest): a developer machine often has both Docker and
// an OPENAI_API_KEY, and without this every workflow and daemon test that
// leaves executor.kind unset would launch a real Hermes container -- a
// multi-gigabyte pull and a paid model call -- from `go test ./...`.
var autoDockerProbe = func(ctx context.Context) bool {
	if testing.Testing() {
		return false
	}
	return executor.DockerReachable(ctx)
}

// SetAutoDockerProbeForTest replaces the Docker probe auto resolution uses,
// returning a restore func. It panics outside a test binary, for the reason
// executor.TestOnlySetKnownIncompatible does.
func SetAutoDockerProbeForTest(probe func(context.Context) bool) (restore func()) {
	if !testing.Testing() {
		panic("workflow: SetAutoDockerProbeForTest must only be called from a test binary")
	}
	saved := autoDockerProbe
	autoDockerProbe = probe
	return func() { autoDockerProbe = saved }
}

// ResolveExecutor decides which executor a run with this configuration uses.
// lookupKey reads a provider key the way the run will (the daemon's
// environment, then the project's .env).
func ResolveExecutor(ctx context.Context, h config.HarnessConfig, registry *model.Registry, lookupKey func(string) string) ExecutorResolution {
	kind := h.Executor.Kind
	if kind == "" {
		kind = config.ExecutorAuto
	}
	res := ExecutorResolution{Requested: string(kind), Resolved: string(kind)}
	switch kind {
	case config.ExecutorAuto:
		// resolved below
	case config.ExecutorInternal:
		res.Note = "executor.kind is internal: runs write a placeholder file, not real code. " +
			"Remove the line (auto) or set executor.kind: hermes|omp|acp to use a real coding agent (docs/agents.md)."
		return res
	case config.ExecutorHermes, config.ExecutorOMP, config.ExecutorACP:
		res.Image = h.Executor.Image
		if res.Image == "" {
			res.Image = executor.DefaultAgentImage(string(kind))
		}
		return res
	case config.ExecutorOpenHands:
		// Only ever explicit. Auto (below) never resolves to openhands: the
		// work root, and the provider key unless withheld, leave the
		// machine, which is a choice to make, not a default to fall into.
		res.Server = h.Executor.OpenHands.URL
		return res
	default:
		return res
	}

	fallback := func(reason, fix string) ExecutorResolution {
		res.Resolved = string(config.ExecutorInternal)
		res.Warning = fmt.Sprintf("NO REAL CODE WILL BE WRITTEN: executor.kind is unset (auto) and %s, so this run uses the internal "+
			"placeholder executor. Fix: %s. To keep the placeholder on purpose, set executor.kind: internal in .idea-harness/harness.yaml.",
			reason, fix)
		return res
	}
	spec := ImplementerModelSpec(h, registry)
	if spec == "" || spec == demoModelSpec {
		return fallback("no coding model is configured (demo mode)",
			"run `idea model set <provider>/<model>` and set that provider's API key")
	}
	provider, err := ImplementerProvider(h, registry)
	if err != nil {
		return fallback(fmt.Sprintf("the coding model %s does not resolve to a provider", spec),
			"run `idea model set <provider>/<model>` with a built-in provider or one under providers: in harness.yaml")
	}
	if provider.KeyEnv == "" {
		// A keyless provider (Ollama) is not picked automatically: auto would
		// otherwise pull a multi-gigabyte image to call a local server that may
		// not be running. Choosing it is one line.
		return fallback(fmt.Sprintf("the coding model's provider %q needs no key, which auto does not choose a real agent for", provider.Name),
			"set executor.kind: hermes in .idea-harness/harness.yaml (or run `idea executors setup --model "+spec+" --yes`)")
	}
	if lookupKey == nil || lookupKey(provider.KeyEnv) == "" {
		return fallback(fmt.Sprintf("%s (the key for %s) is set neither in the daemon's environment nor in the project's .env", provider.KeyEnv, spec),
			fmt.Sprintf("add %s to .env, or export it and restart the daemon with `idea daemon stop`", provider.KeyEnv))
	}
	if !autoDockerProbe(ctx) {
		return fallback("Docker is not running or not installed (the coding agent runs in a container)",
			"start Docker, then run again")
	}
	res.Resolved = string(config.ExecutorHermes)
	res.Image = h.Executor.Image
	if res.Image == "" {
		res.Image = executor.DefaultAgentImage("hermes")
	}
	return res
}

// ResolveExecutor is the package function applied to this engine's current
// configuration and key lookup.
func (e *Engine) ResolveExecutor(ctx context.Context) ExecutorResolution {
	snap := e.snapshotConfig()
	return ResolveExecutor(ctx, snap.cfg, snap.registry, e.lookupKey)
}
