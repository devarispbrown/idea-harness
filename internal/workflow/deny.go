package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/executor"
	"github.com/devarispbrown/idea-harness/internal/fsdiff"
	"github.com/devarispbrown/idea-harness/internal/security"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// SystemDenyError is a fail-closed refusal that carries its machine-readable
// DenyCode, for the denies that stop an action before there is a change set
// (or a finding) to attach the code to. Callers that want the code use
// errors.As; the message names it as "system deny <code>" like every other
// deny error in this package.
type SystemDenyError struct {
	Code   domain.DenyCode
	Detail string
}

func (e *SystemDenyError) Error() string {
	return fmt.Sprintf("workflow: system deny %s: %s", e.Code, e.Detail)
}

// authorizeExecutorWrite is the trust_deny gate: the engine-assigned trust
// tier of the selected executor (executor.TrustTierFor, from its kind --
// never its self-declared capabilities) must allow write_file before an agent
// may produce a change set at all.
//
// No executor kind selectExecutor can build today maps to a tier that lacks
// write_file, so this never fires in a shipped configuration. It is the
// fail-closed backstop for the day one does -- a new kind added to
// selectExecutor without a TrustTierFor case lands in remote_external, which
// allows nothing -- and that refusal now carries its code instead of being a
// bare error.
func authorizeExecutorWrite(kind string) error {
	trust := executor.TrustTierFor(kind)
	if executor.TrustAllows(trust, "write_file") {
		return nil
	}
	return &SystemDenyError{
		Code:   domain.DenyCodeTrustDeny,
		Detail: fmt.Sprintf("executor %q has trust tier %q, which may not write_file", kind, trust),
	}
}

// recordProducedContentFindings raises the findings about WHAT a change set
// touches and contains that are not credential matches: a protected
// (auto-executing) path, and personal data. Neither rejects the change set;
// both block its approval until a human has looked (docs/security-model.md).
// read returns a produced file's content, false when it is unavailable.
func (e *Engine) recordProducedContentFindings(ctx context.Context, cs *domain.ChangeSet, files []domain.FileChange, read func(domain.FileChange) (string, bool)) error {
	if err := e.recordProtectedPathFinding(ctx, cs, files); err != nil {
		return err
	}
	return e.recordPIIFindings(ctx, cs, files, read)
}

// recordProtectedPathFinding raises one blocking `protected_path` finding when
// the change set adds, edits or deletes a file that runs automatically (a CI
// workflow, a git hook, .envrc; fsdiff.AutoExecutingPath has the list and the
// reasoning).
//
// The false-positive policy: an initiative may legitimately be about CI, so
// this is not a deny and carries no DenyCode. It is a high-severity finding,
// so a plain `approve` is refused and the change set cannot be applied until a
// human signs it off -- with `idea review <CHG-n> approve --accept-findings`,
// which records accepted_risk against this finding under the human actor, or
// by dispositioning it individually. What it rules out is the silent path: a
// workflow edit riding along in a change set about something else, approved
// by someone who never opened that file.
func (e *Engine) recordProtectedPathFinding(ctx context.Context, cs *domain.ChangeSet, files []domain.FileChange) error {
	var hits []string
	for _, fc := range files {
		if what, ok := fsdiff.AutoExecutingPath(fc.Path); ok {
			hits = append(hits, fmt.Sprintf("%s (%s, %s)", fc.Path, what, changeVerb(fc)))
		}
	}
	if len(hits) == 0 {
		return nil
	}
	return e.db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: cs.InitiativeID,
		TargetType:   "change_set",
		TargetID:     cs.ID,
		Severity:     domain.SeverityHigh,
		Confidence:   domain.ConfidenceHigh,
		Category:     "protected_path",
		Finding:      fmt.Sprintf("This change set changes %d file(s) that run automatically: %s", len(hits), strings.Join(hits, "; ")),
		Evidence:     strings.Join(hits, "\n"),
		Impact: "Once merged, these run without anyone invoking them -- on CI runners (usually with " +
			"repository secrets in scope), in a git hook on the next commit, or when the folder is opened.",
		Recommendation: fmt.Sprintf("Read these files in the diff. If the task is meant to change them, approve with "+
			"`idea review %s approve --accept-findings` to sign this off explicitly; if not, reject and run the task "+
			"again with a note telling the agent to leave them alone.", cs.HumanID),
	})
}

func changeVerb(fc domain.FileChange) string {
	switch {
	case fc.IsDelete():
		return "deleted"
	case fc.ChangeType == domain.FileAdded:
		return "added"
	default:
		return "modified"
	}
}

// recordPIIFindings raises one `pii` finding per detected payment card number
// or US Social Security number in produced content (security.FindPIIInFile:
// what it detects, what it deliberately does not, and why).
//
// Outside a test/fixture path the finding carries DenyCode pii and blocks
// approval. Unlike secret_sweep it does not reject the change set: the
// detector is checksum- and range-based, not proof, and rejecting would make a
// legitimate synthetic fixture outside a test directory impossible to land.
// --accept-findings never signs a coded finding off; a human has to
// disposition this one individually (accepted_risk: "this is synthetic data")
// after reading it. In a test/fixture path it is advisory: shown, never gating.
//
// Never the value: Finding and Evidence carry path:line and a masked preview.
func (e *Engine) recordPIIFindings(ctx context.Context, cs *domain.ChangeSet, files []domain.FileChange, read func(domain.FileChange) (string, bool)) error {
	for _, fc := range files {
		if fc.IsDelete() {
			continue
		}
		content, ok := read(fc)
		if !ok {
			continue
		}
		for _, m := range security.FindPIIInFile(fc.Path, content) {
			f := &domain.Finding{
				InitiativeID: cs.InitiativeID,
				TargetType:   "change_set",
				TargetID:     cs.ID,
				Severity:     domain.SeverityHigh,
				Confidence:   domain.ConfidenceMedium,
				Category:     "pii",
				Finding:      fmt.Sprintf("%s:%d: looks like a %s", m.Path, m.Line, m.Kind),
				Evidence:     fmt.Sprintf("%s:%d: %s", m.Path, m.Line, m.Preview),
				Impact:       "Real personal data committed to the repository is copied to every clone, CI log and backup.",
				Recommendation: "If it is real, remove it and run the task again. If it is synthetic test data, " +
					"disposition this finding accepted_risk (it is not covered by --accept-findings), or move it under a test/ or fixtures/ path.",
			}
			if m.Advisory {
				f.Advisory = true
				f.Finding += " (advisory: test/fixture code)"
				f.Impact = "shown for review; does not block a test/fixture-only match"
			} else {
				f.Code = domain.DenyCodePII
			}
			if err := e.db.CreateFinding(ctx, f); err != nil {
				return err
			}
		}
	}
	return nil
}

// denyPathEscape is the apply-time path_escape system deny (fsdiff.Escapes):
// every change must resolve inside the workspace and outside the control
// plane against the workspace as it is NOW, before anything is written. On a
// refusal it records one finding per offending path, rejects an approved
// change set and reopens its task -- the same recovery the apply-time secret
// sweep uses, since retrying the same patch would be refused the same way.
func (e *Engine) denyPathEscape(ctx context.Context, cs *domain.ChangeSet, fromStatus domain.ChangeSetStatus, workspaceRoot string, files []domain.FileChange) error {
	escapes := fsdiff.Escapes(workspaceRoot, files)
	if len(escapes) == 0 {
		return nil
	}
	for _, line := range escapes {
		if err := e.db.CreateFinding(ctx, &domain.Finding{
			InitiativeID:   cs.InitiativeID,
			TargetType:     "change_set",
			TargetID:       cs.ID,
			Severity:       domain.SeverityCritical,
			Confidence:     domain.ConfidenceHigh,
			Category:       "path_escape",
			Code:           domain.DenyCodePathEscape,
			Finding:        e.redact(line),
			Evidence:       e.redact(line),
			Impact:         "apply denied before workspace mutation",
			Recommendation: "Check the workspace for a symlink on this path that points outside the project or into .git/ or .idea-harness/; remove it and run the task again.",
			Disposition:    domain.DispositionRejected,
		}); err != nil {
			return err
		}
	}
	if fromStatus == domain.ChangeSetApproved {
		_ = e.db.TransitionChangeSet(context.Background(), cs.ID, domain.ChangeSetApproved, domain.ChangeSetRejected, store.ActorOrchestrator)
		if cs.TaskID != "" {
			_ = e.db.SetTaskStatus(context.Background(), cs.TaskID, domain.TaskProposed)
		}
	}
	return &SystemDenyError{
		Code:   domain.DenyCodePathEscape,
		Detail: fmt.Sprintf("apply denied: %s", strings.Join(escapes, "; ")),
	}
}
