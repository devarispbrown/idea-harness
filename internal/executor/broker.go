// Package executor defines the worker abstraction (§48–§50). The deterministic
// `internal` executor is the first implementation; OMP is validated behind this
// same interface as `local_external`.
package executor

import (
	"fmt"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// actionCapabilities maps each executable action to the capability it requires.
// Capabilities are declared by the executor, deterministically; they are never
// granted by model output (§49, §50).
var actionCapabilities = map[string]string{
	"read_file":   "fs.read",
	"write_file":  "fs.write",
	"run_command": "exec.run",
}

// CapabilityBroker authorizes tool actions against an executor's declared
// capability set. It is a pure gate: it can only deny, never grant.
type CapabilityBroker struct{}

// Authorize returns nil if every declared capability required by action is
// present in caps, and an error otherwise. An unknown action is always denied.
func (CapabilityBroker) Authorize(caps []string, action string) error {
	required, ok := actionCapabilities[action]
	if !ok {
		return fmt.Errorf("broker: unknown action %q", action)
	}
	for _, c := range caps {
		if c == required {
			return nil
		}
	}
	return fmt.Errorf("broker: action %q requires capability %q", action, required)
}

// GrantSuggested returns an empty set: models cannot grant capabilities. The
// suggested list is deliberately ignored so a model can never widen its own
// executor's declared capability set (§50).
func (CapabilityBroker) GrantSuggested(suggested []string) []string {
	return nil
}

// trustActions maps an executor trust tier to the actions the deterministic
// engine permits for that tier (§49). Trust is assigned by the engine from how
// the executor is registered — never from the executor's own self-declaration —
// so an executor cannot widen its own authority.
var trustActions = map[domain.ExecutorTrust][]string{
	domain.ExecutorInternal:      {"read_file", "write_file", "run_command"},
	domain.ExecutorLocalExternal: {"read_file", "write_file"},
	// remote_external is an agent on another machine (openhands). It reads
	// the uploaded copy of the work root and writes into its own copy; what
	// it wrote reaches the local work root only through the executor's own
	// download, and then the same snapshot, diff, secret sweep and review as
	// any local agent's. Never run_command: the host is not its to run on.
	domain.ExecutorRemoteExternal: {"read_file", "write_file"},
	// No tier (TrustUnassigned, an unknown kind) is fail-closed: no actions.
}

// TrustAllows reports whether an action is permitted for an executor's
// engine-assigned trust tier. It is the policy gate used before any execution,
// independent of the executor's self-declared capability list.
func TrustAllows(trust domain.ExecutorTrust, action string) bool {
	for _, a := range trustActions[trust] {
		if a == action {
			return true
		}
	}
	return false
}

// TrustTierFor maps an executor's Name() (== the selected executor kind) to its
// engine-assigned trust tier. It is the single source of truth, never the
// executor's self-declared Capabilities().Trust, and it fails closed for any
// unknown kind (review: system-deny / engine-assigned trust).
//
// An unknown kind used to land in remote_external, which then had to allow
// nothing. Now that a real remote kind exists (openhands) and remote_external
// grants it read/write, an unknown kind gets no tier at all, which allows
// nothing -- the same fail-closed backstop, no longer borrowing a real
// tier's name.
func TrustTierFor(name string) domain.ExecutorTrust {
	switch name {
	case "internal":
		return domain.ExecutorInternal
	case "container", "omp", "hermes", "acp":
		return domain.ExecutorLocalExternal
	case "openhands":
		return domain.ExecutorRemoteExternal
	default:
		return TrustUnassigned // fail-closed: no actions
	}
}

// TrustUnassigned is the tier an unknown executor kind gets: none of the
// domain's tiers, so no action is ever granted to it.
const TrustUnassigned domain.ExecutorTrust = ""
