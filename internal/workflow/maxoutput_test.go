package workflow

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
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

// TestEveryModelRequestGoesThroughTheCap is audit 5's B1, as a guard that can
// actually fail.
//
// The first version of this test asserted
// `effectiveMaxOutput(stage, prov) <= ceiling` — which is unreachable by
// construction, because effectiveMaxOutput IS the cap. Raising a stage budget
// to 200000 would have left it green. A pre-merge review caught it: a test that
// passes for a reason unrelated to the fix, which is the exact pattern four
// previous rounds of guards kept producing.
//
// The real invariant is not arithmetic, it is reachability: every model.Request
// built anywhere in this package must take its MaxOutput from the cap. A new
// call site that writes `MaxOutput: maxOutputForStage(...)` or a bare literal is
// how the defect comes back, and only an enumeration can see that.
func TestEveryModelRequestGoesThroughTheCap(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	found := 0
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				sel, ok := lit.Type.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Request" {
					return true
				}
				if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "model" {
					return true
				}
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok || key.Name != "MaxOutput" {
						continue
					}
					found++
					call, ok := kv.Value.(*ast.CallExpr)
					if !ok {
						t.Errorf("%s: model.Request sets MaxOutput to a value that is not a call; "+
							"it must go through effectiveMaxOutput or minOutput, or the provider "+
							"rejects the request outright", fset.Position(kv.Pos()))
						continue
					}
					fn, ok := call.Fun.(*ast.Ident)
					if !ok || (fn.Name != "effectiveMaxOutput" && fn.Name != "minOutput") {
						t.Errorf("%s: model.Request sets MaxOutput from %s(...); it must go through "+
							"effectiveMaxOutput or minOutput", fset.Position(kv.Pos()), exprName(call.Fun))
					}
				}
				return true
			})
			_ = name
		}
	}
	if found == 0 {
		t.Fatal("found no model.Request literal setting MaxOutput; this test is not reading the package")
	}
	t.Logf("checked %d model.Request MaxOutput site(s)", found)
}

func exprName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprName(v.X) + "." + v.Sel.Name
	}
	return "?"
}

// unverifiedModel is a model name deliberately absent from model.MaxOutputFor
// (see pricing.go's modelMaxOutput), so reviewDefaultMaxOutput falls back to
// its floor (16384) -- the same number every stage's want resolved to before
// the D2 fix. Tests that are not specifically about that fix use it so their
// arithmetic stays exactly what it was.
const unverifiedModel = "an-unverified-test-model"

// TestStageBudgetsThatExceedACeilingAreCapped documents which stages actually
// need the cap today, so that lowering a raw budget is a visible change rather
// than a silent one.
func TestStageBudgetsThatExceedACeilingAreCapped(t *testing.T) {
	for name, prov := range realProviders() {
		ceiling := prov.Capabilities().MaxOutput
		for _, stage := range allStages {
			raw := maxOutputForStage(stage, unverifiedModel)
			got := effectiveMaxOutput(stage, prov, unverifiedModel, 0)
			if raw > ceiling && got != ceiling {
				t.Errorf("stage %q wants %d, %s declares %d, but %d was sent", stage, raw, name, ceiling, got)
			}
			if raw <= ceiling && got != raw {
				t.Errorf("stage %q wants %d, which fits %s's %d, but %d was sent", stage, raw, name, ceiling, got)
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
		want := maxOutputForStage(stage, unverifiedModel)
		got := effectiveMaxOutput(stage, anthropic, unverifiedModel, 0)
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

// uncappedProvider declares no ceiling.
//
// No provider in the tree does today — a review corrected an earlier caption
// here that named Ollama, Groq and LM Studio, all of which reach
// NewOpenAICompat and inherit its 16384. The case is kept because MaxOutput is
// an ordinary int field with no validation: any new provider constructor that
// omits it declares 0, and `0` must mean "not stated", not "zero tokens".
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
	if got := effectiveMaxOutput(StageProductDefinition, uncappedProvider{}, unverifiedModel, 0); got != maxOutputForStage(StageProductDefinition, unverifiedModel) {
		t.Errorf("effectiveMaxOutput against an uncapped provider = %d, want the stage budget %d",
			got, maxOutputForStage(StageProductDefinition, unverifiedModel))
	}
}

// TestReviewMaxTokensRaisesTheWant is the fix for the second layer of the
// change-set review truncation defect (the defect's own remediation, "raise
// the stage budget", pointed at review.max_tokens, and it did nothing:
// runModel only ever checked it AFTER a response came back, to fail a stage
// that exceeded it -- never to raise what was actually requested). An
// explicit review.max_tokens above the stage's built-in want must now change
// what is actually sent, not just enforce a ceiling after the fact.
func TestReviewMaxTokensRaisesTheWant(t *testing.T) {
	anthropic := model.NewAnthropic("k", "https://example.invalid")
	ceiling := anthropic.Capabilities().MaxOutput
	builtIn := maxOutputForStage(StageReview, unverifiedModel)

	raised := int64(builtIn + 20000)
	if raised > int64(ceiling) {
		t.Fatalf("test fixture: raised (%d) must stay under this provider's ceiling (%d)", raised, ceiling)
	}
	if got := effectiveMaxOutput(StageReview, anthropic, unverifiedModel, raised); got != int(raised) {
		t.Errorf("effectiveMaxOutput(StageReview, review.max_tokens=%d) = %d, want %d (the built-in want, %d, was never raised)",
			raised, got, raised, builtIn)
	}
}

// TestReviewMaxTokensStillCappedByTheProviderCeiling: raising review.max_tokens
// past what the model can actually produce must still be capped by the
// model's real ceiling -- it is a way to raise the ask, not to bypass
// Capabilities.MaxOutput and send a request the provider will reject or
// truncate anyway.
func TestReviewMaxTokensStillCappedByTheProviderCeiling(t *testing.T) {
	anthropic := model.NewAnthropic("k", "https://example.invalid")
	ceiling := anthropic.Capabilities().MaxOutput

	got := effectiveMaxOutput(StageReview, anthropic, unverifiedModel, int64(ceiling)*10)
	if got != ceiling {
		t.Errorf("effectiveMaxOutput with review.max_tokens far over the ceiling = %d, want capped to %d", got, ceiling)
	}
}

// TestReviewMaxTokensAppliesBeyondTheReviewStage: review.max_tokens is
// enforced (post-hoc) against every stage's response, not only the
// adversarial review's (see runModel's own hard-stop check) -- docs/
// configuration.md documents it as checked "against one response's
// output-token count" with no stage scoping. Raising it must be symmetric
// with that: it overrides any stage's want, not only StageReview's.
func TestReviewMaxTokensAppliesBeyondTheReviewStage(t *testing.T) {
	anthropic := model.NewAnthropic("k", "https://example.invalid")
	small := int64(1234)
	if got := effectiveMaxOutput(StageProductDefinition, anthropic, unverifiedModel, small); got != int(small) {
		t.Errorf("effectiveMaxOutput(StageProductDefinition, review.max_tokens=%d) = %d, want %d", small, got, small)
	}
}

// TestReviewMaxTokensZeroLeavesEveryStageAtItsBuiltInWant: 0 (the config
// default, "no ceiling" per docs/configuration.md) must not change any
// stage's existing behavior.
func TestReviewMaxTokensZeroLeavesEveryStageAtItsBuiltInWant(t *testing.T) {
	for name, prov := range realProviders() {
		for _, stage := range allStages {
			if got, want := effectiveMaxOutput(stage, prov, unverifiedModel, 0), minOutput(maxOutputForStage(stage, unverifiedModel), prov); got != want {
				t.Errorf("%s/%s: effectiveMaxOutput(..., 0) = %d, want %d (unchanged from before review.max_tokens was wired through)",
					name, stage, got, want)
			}
		}
	}
}

// TestReviewDefaultMaxOutputExceedsTheOldFlatConstant is D2 of the 2026-09-21
// truncation probe: claude-sonnet-5's real output ceiling is 128000
// (model.MaxOutputFor), and the review stage's default ask must now be sized
// off that, not stuck at the old flat 16384 -- the exact number the probe's
// real-world review was truncated at, with review.max_tokens still unset.
func TestReviewDefaultMaxOutputExceedsTheOldFlatConstant(t *testing.T) {
	const oldFlatConstant = 16384

	got := reviewDefaultMaxOutput("claude-sonnet-5")
	if got <= oldFlatConstant {
		t.Fatalf("reviewDefaultMaxOutput(claude-sonnet-5) = %d, want > %d (the old flat constant)", got, oldFlatConstant)
	}

	anthropic := model.NewAnthropic("k", "https://example.invalid")
	if got := effectiveMaxOutput(StageReview, anthropic, "claude-sonnet-5", 0); got <= oldFlatConstant {
		t.Errorf("effectiveMaxOutput(StageReview, claude-sonnet-5, review.max_tokens=0) = %d, want > %d", got, oldFlatConstant)
	}
	if got := effectiveMaxOutput(StageExecutionPlanReview, anthropic, "claude-sonnet-5", 0); got <= oldFlatConstant {
		t.Errorf("effectiveMaxOutput(StageExecutionPlanReview, claude-sonnet-5, review.max_tokens=0) = %d, want > %d", got, oldFlatConstant)
	}
}

// TestReviewDefaultMaxOutputFallsBackForAnUnverifiedModel: a model absent from
// model.MaxOutputFor (a brand-new one, or a typo) must not silently ask for
// an arbitrary number -- it keeps the old, safe flat constant.
func TestReviewDefaultMaxOutputFallsBackForAnUnverifiedModel(t *testing.T) {
	if got := reviewDefaultMaxOutput(unverifiedModel); got != reviewDefaultFloor {
		t.Errorf("reviewDefaultMaxOutput(%s) = %d, want the floor %d", unverifiedModel, got, reviewDefaultFloor)
	}
}

// TestReviewDefaultMaxOutputIsCapped: the default must not simply ask for a
// model's full output ceiling (needlessly slow/expensive for the common
// case) -- it is bounded at reviewDefaultCeiling, matching the PRD stage's
// own want.
func TestReviewDefaultMaxOutputIsCapped(t *testing.T) {
	// claude-sonnet-5's real ceiling (128000) is well over reviewDefaultCeiling.
	if got := reviewDefaultMaxOutput("claude-sonnet-5"); got != reviewDefaultCeiling {
		t.Errorf("reviewDefaultMaxOutput(claude-sonnet-5) = %d, want capped at %d", got, reviewDefaultCeiling)
	}
}

// TestReviewMaxTokensStillOverridesTheRaisedDefault: review.max_tokens must
// remain the final word even after the default itself was raised -- both an
// explicit lower and an explicit higher value must win over
// reviewDefaultMaxOutput's answer for a verified model.
func TestReviewMaxTokensStillOverridesTheRaisedDefault(t *testing.T) {
	anthropic := model.NewAnthropic("k", "https://example.invalid")

	lower := int64(9000)
	if got := effectiveMaxOutput(StageReview, anthropic, "claude-sonnet-5", lower); got != int(lower) {
		t.Errorf("effectiveMaxOutput(StageReview, claude-sonnet-5, review.max_tokens=%d) = %d, want %d", lower, got, lower)
	}

	higher := int64(50000)
	if got := effectiveMaxOutput(StageReview, anthropic, "claude-sonnet-5", higher); got != int(higher) {
		t.Errorf("effectiveMaxOutput(StageReview, claude-sonnet-5, review.max_tokens=%d) = %d, want %d", higher, got, higher)
	}
}
