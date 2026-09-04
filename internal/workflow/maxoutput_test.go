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

// TestStageBudgetsThatExceedACeilingAreCapped documents which stages actually
// need the cap today, so that lowering a raw budget is a visible change rather
// than a silent one.
func TestStageBudgetsThatExceedACeilingAreCapped(t *testing.T) {
	for name, prov := range realProviders() {
		ceiling := prov.Capabilities().MaxOutput
		for _, stage := range allStages {
			raw := maxOutputForStage(stage)
			got := effectiveMaxOutput(stage, prov)
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
	if got := effectiveMaxOutput(StageProductDefinition, uncappedProvider{}); got != maxOutputForStage(StageProductDefinition) {
		t.Errorf("effectiveMaxOutput against an uncapped provider = %d, want the stage budget %d",
			got, maxOutputForStage(StageProductDefinition))
	}
}
