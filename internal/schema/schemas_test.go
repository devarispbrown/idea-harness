package schema

import (
	"encoding/json"
	"testing"
)

// TestSchemasAreValidJSON guards every embedded schema constant against a
// syntax slip. These are raw Go string literals holding hand-edited JSON --
// nothing else in the build parses them -- so a stray backtick (which closes
// the Go raw string early and produces a confusing "syntax error: unexpected
// name" pointing at the JSON body, not the real cause) or a trailing comma
// only surfaces once the model call using the broken schema fails at
// runtime. This caught exactly that: an added `description` field for the
// decision "recommendation" property (review #2, accept-recommendations)
// originally quoted `options`/`recommendation` in backticks, which is
// invalid inside a backtick-delimited Go string.
func TestSchemasAreValidJSON(t *testing.T) {
	all := map[string]string{
		"IdeaBrief":           IdeaBrief,
		"ResearchBrief":       ResearchBrief,
		"PRD":                 PRD,
		"Review":              Review,
		"Discussion":          Discussion,
		"Architecture":        Architecture,
		"ProductDesign":       ProductDesign,
		"VerificationPlan":    VerificationPlan,
		"ExecutionPlan":       ExecutionPlan,
		"ImplementationBrief": ImplementationBrief,
		"Answers":             Answers,
		"Convergence":         Convergence,
	}
	for name, s := range all {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Errorf("%s: invalid JSON: %v", name, err)
		}
	}
}
