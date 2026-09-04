package workflow

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/model"
)

// allStages is every stage key that reaches a model call, plus the two
// non-constant keys used directly in the code.
var allStages = []string{
	StageFraming, StageResearch, StageProductDefinition, StageReview,
	StageArchitecture, StageProductDesign, StageVerificationDesign,
	StageConvergence, StageExecutionPlan, StageExecutionPlanReview,
	StageImplementationBrief,
	"suggest_answers",
	"an_unknown_stage_falls_to_the_default",
}

// realProviders is the set a user can actually configure, with the ceilings
// each one declares.
func realProviders() map[string]model.Provider {
	return map[string]model.Provider{
		"openai":    model.NewOpenAI("k", "https://example.invalid"),
		"anthropic": model.NewAnthropic("k", "https://example.invalid"),
		"gemini":    model.NewGemini("k", "https://example.invalid"),
	}
}

// TestEveryStageBudgetFitsEveryProviderCeiling is audit 5's B1, as a guard.
//
// `maxOutputForStage` returned 32000 for the PRD directly beneath a comment
// asserting that its values "stay within the providers' declared MaxOutput
// ceilings (16k for Anthropic/OpenAI)". Nothing compared the two, because
// Capabilities.MaxOutput was declared per provider and never read. Every real
// OpenAI run therefore failed at product_definition with a hard 400 -- the
// README's own worked example -- and it shipped that way in v0.1.0.
//
// Four audits missed it because none of them made a real provider call. This
// test is the cheap mechanical version of that call: it needs no key, no
// network, and it fails on the exact arithmetic the provider would have
// rejected.
func TestEveryStageBudgetFitsEveryProviderCeiling(t *testing.T) {
	for name, prov := range realProviders() {
		ceiling := prov.Capabilities().MaxOutput
		if ceiling <= 0 {
			t.Errorf("provider %s declares no MaxOutput; the cap cannot protect it", name)
			continue
		}
		for _, stage := range allStages {
			got := effectiveMaxOutput(stage, prov)
			if got > ceiling {
				t.Errorf("stage %q sends MaxOutput %d to %s, which declares %d — "+
					"the provider rejects the request outright", stage, got, name, ceiling)
			}
			if got <= 0 {
				t.Errorf("stage %q sends MaxOutput %d to %s", stage, got, name)
			}
		}
	}
}

// TestTheCapIsOnlyACap: the fix must not quietly shrink a stage that already
// fits, or the budgets in maxOutputForStage stop meaning anything.
func TestTheCapIsOnlyACap(t *testing.T) {
	anthropic := model.NewAnthropic("k", "https://example.invalid")
	ceiling := anthropic.Capabilities().MaxOutput

	for _, stage := range allStages {
		want := maxOutputForStage(stage)
		got := effectiveMaxOutput(stage, anthropic)
		switch {
		case want <= ceiling && got != want:
			t.Errorf("stage %q wants %d, which fits under %d, but %d was sent",
				stage, want, ceiling, got)
		case want > ceiling && got != ceiling:
			t.Errorf("stage %q wants %d over the %d ceiling; expected it capped to the ceiling, got %d",
				stage, want, ceiling, got)
		}
	}
}

// uncappedProvider declares no ceiling, like the OpenAI-compatible endpoints a
// user can point at (Ollama, Groq, LM Studio, LiteLLM).
type uncappedProvider struct{}

func (uncappedProvider) Name() string                     { return "uncapped" }
func (uncappedProvider) Capabilities() model.Capabilities { return model.Capabilities{} }
func (uncappedProvider) Complete(context.Context, model.Request) (model.Response, error) {
	return model.Response{}, nil
}

// TestAProviderWithNoDeclaredCeilingIsNotCapped: `MaxOutput: 0` means "not
// stated", not "zero tokens". Treating it as a ceiling would send MaxOutput 0
// to every custom endpoint and truncate every reply to nothing — turning a fix
// for one provider into an outage for a whole class of them.
func TestAProviderWithNoDeclaredCeilingIsNotCapped(t *testing.T) {
	if got := minOutput(32000, nil); got != 32000 {
		t.Errorf("minOutput with no provider = %d, want 32000", got)
	}
	if got := minOutput(32000, uncappedProvider{}); got != 32000 {
		t.Errorf("minOutput against a provider declaring no ceiling = %d, want 32000", got)
	}
	if got := effectiveMaxOutput(StageProductDefinition, uncappedProvider{}); got != maxOutputForStage(StageProductDefinition) {
		t.Errorf("effectiveMaxOutput against an uncapped provider = %d, want the stage budget %d",
			got, maxOutputForStage(StageProductDefinition))
	}
}
