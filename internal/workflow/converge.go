package workflow

import (
	"context"
	"fmt"
	"strings"

	ccontext "github.com/devarispbrown/idea-harness/internal/context"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/schema"
	"github.com/devarispbrown/idea-harness/internal/security"
)

// ConvergeDecisions runs the orchestrator synthesis across every open decision
// that has preserved dissents (§M2 tail). For each such decision the product
// orchestrator reconciles the dissenting positions into a single synthesized
// recommendation, which is stored back on the decision (and surfaced as its
// synthesis in the projection). Dissents themselves are never erased (§8).
func (e *Engine) ConvergeDecisions(ctx context.Context, initiativeID string) error {
	decisions, err := e.db.ListDecisions(ctx, initiativeID)
	if err != nil {
		return err
	}
	for _, d := range decisions {
		if d.Status != domain.DecisionOpen {
			continue
		}
		dissents, err := e.db.ListDissents(ctx, d.ID)
		if err != nil {
			return err
		}
		if len(dissents) == 0 {
			continue
		}
		runID, synthesis, err := e.synthesizeDecision(ctx, initiativeID, d, dissents)
		if err != nil {
			return err
		}
		if strings.TrimSpace(synthesis) == "" {
			continue
		}
		// Redact before persistence (§43) — the synthesis is model output.
		synthesis = security.Redact(synthesis)
		if err := e.db.SetDecisionRecommendation(ctx, d.ID, synthesis, runID, RoleOrchestrator); err != nil {
			return err
		}
	}
	return nil
}

// synthesizeDecision asks the product orchestrator to reconcile one decision's
// dissents into a single recommendation (schema.Convergence). It returns the
// producing run id so provenance (converged_by) is preserved.
func (e *Engine) synthesizeDecision(ctx context.Context, initiativeID string, d domain.Decision, dissents []domain.DecisionDissent) (string, string, error) {
	inputs := []ccontext.Input{
		{Selector: "immutable_policy", Content: "You are the product orchestrator. Read the decision, its options, the leading recommendation, and every preserved dissent. Synthesize a single recommendation that resolves the conflict fairly and surfaces the tradeoffs; never erase a dissent."},
		{Selector: "decision", Content: renderDecisionForDiscussion(&d)},
		{Selector: "dissents", Content: renderDissents(dissents)},
	}
	runID, obj, err := e.runModel(ctx, initiativeID, StageConvergence, RoleOrchestrator, "convergence", schema.Convergence, inputs, "Synthesize a recommendation for this decision.")
	if err != nil {
		return "", "", err
	}
	return runID, strings.TrimSpace(str(obj["summary"])), nil
}

// renderDissents renders a decision's preserved dissenting positions.
func renderDissents(dissents []domain.DecisionDissent) string {
	if len(dissents) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, x := range dissents {
		fmt.Fprintf(&b, "- %s: %s\n", x.ProponentRole, x.Position)
	}
	return b.String()
}
