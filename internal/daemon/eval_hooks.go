package daemon

import (
	"context"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
)

// The in-process hooks `idea eval` (internal/eval) needs to drive this
// daemon headlessly. Go-only on purpose: none of this is an HTTP route, so
// the API surface -- and docs/openapi.yaml -- is exactly what a user's daemon
// serves. The eval drives every workflow step through that same HTTP API; it
// only reaches past it to inject a scripted provider and to read the two
// things the API does not report (token usage, and per-cycle review
// convergence telemetry).

// RegisterProvider adds a model provider, under name, to the registry of
// every project this runtime opens or reloads from now on. A project selects
// it the ordinary way, with `default_model: <name>/<model>` in harness.yaml.
//
// It exists for the eval's deterministic scenarios: a reviewer that keeps
// re-raising rephrased findings, or one that holds a genuine blocker, is a
// scripted model.Provider, and the only way it can sit behind the real
// daemon's real registry is to be registered into it. A name a project also
// configures is overridden, so it cannot be mistaken for a real provider.
func (r *Runtime) RegisterProvider(name string, p model.Provider) {
	r.extraMu.Lock()
	defer r.extraMu.Unlock()
	if r.extraProviders == nil {
		r.extraProviders = map[string]model.Provider{}
	}
	r.extraProviders[name] = p
}

func (r *Runtime) addExtraProviders(reg *model.Registry) {
	r.extraMu.Lock()
	defer r.extraMu.Unlock()
	for name, p := range r.extraProviders {
		reg.Add(name, p)
	}
}

// RunStats is an initiative's model usage, summed over every run manifest.
type RunStats struct {
	Calls        int     `json:"calls"`
	InputTokens  int64   `json:"input_tokens"`
	OutputTokens int64   `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// InitiativeRunStats returns an initiative's summed model usage and its
// review convergence telemetry, oldest cycle first.
func (r *Runtime) InitiativeRunStats(ctx context.Context, projectID, initiativeID string) (RunStats, []domain.ReviewTelemetry, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return RunStats{}, nil, err
	}
	usage, calls, err := pr.db.SumRunUsage(ctx, initiativeID)
	if err != nil {
		return RunStats{}, nil, err
	}
	tel, err := pr.db.ListReviewTelemetry(ctx, initiativeID)
	if err != nil {
		return RunStats{}, nil, err
	}
	return RunStats{Calls: calls, InputTokens: usage.InputTokens, OutputTokens: usage.OutputTokens, CostUSD: usage.CostUSD}, tel, nil
}
