package security

import "github.com/devarispbrown/idea-harness/internal/domain"

// TrustFor classifies context trust deterministically from its selector (§38).
// Trust is assigned by deterministic code here — never declared by model output.
func TrustFor(selector string) domain.TrustLevel {
	switch selector {
	case "system", "role_contract":
		return domain.TrustSystem
	case "immutable_policy", "project_policy":
		return domain.TrustPolicy
	case "human_approved", "task":
		// "task" is the user's own idea/direction — human-provided input.
		return domain.TrustHumanApproved
	case "reviewed_output":
		return domain.TrustReviewedAgentOutput
	case "unreviewed_output":
		return domain.TrustUnreviewedAgentOutput
	default:
		// Untrusted external content (web research, raw model output) is the
		// default: never auto-elevated.
		return domain.TrustExternalUntrusted
	}
}
