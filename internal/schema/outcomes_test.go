package schema

import (
	"testing"

	"github.com/devarispbrown/idea-harness/internal/model"
)

// TestPRDObjectivesSchema pins the objectives/metrics shape the product
// manager returns: every requirement can name the objectives it serves, an
// objective carries measurable metrics, and an unknown baseline is null or
// absent rather than an invented number.
func TestPRDObjectivesSchema(t *testing.T) {
	valid := map[string]string{
		"with objectives": `{"summary":"s","requirements":[{"statement":"r","priority":"must","objectives":["O1"],
			"acceptance_criteria":[{"metric":"m","target":"1","condition":"c"}]}],
			"objectives":[{"key":"O1","statement":"Users succeed on day one","rationale":"retention",
				"metrics":[{"name":"activation","direction":"increase","baseline":null,"target":40,"guardrail":10,"cadence":"weekly",
					"hypothesis":"We believe REQ-1 will move activation up"}]}]}`,
		"maintain, no baseline field": `{"summary":"s","requirements":[],
			"objectives":[{"statement":"Keep errors low","metrics":[{"name":"error rate","direction":"maintain","target":1}]}]}`,
		// Back-compat: a PRD with no objectives at all is still a valid
		// response -- the deterministic review check reports the gap; the
		// schema does not reject the whole PRD over it.
		"no objectives": `{"summary":"s","requirements":[{"statement":"r","priority":"must","acceptance_criteria":[]}]}`,
	}
	for name, raw := range valid {
		if _, err := model.DecodeStructured([]byte(raw), []byte(PRD)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	invalid := map[string]string{
		"metric without target":     `{"summary":"s","requirements":[],"objectives":[{"statement":"o","metrics":[{"name":"m","direction":"increase"}]}]}`,
		"unknown direction":         `{"summary":"s","requirements":[],"objectives":[{"statement":"o","metrics":[{"name":"m","direction":"up","target":1}]}]}`,
		"target as text":            `{"summary":"s","requirements":[],"objectives":[{"statement":"o","metrics":[{"name":"m","direction":"increase","target":"lots"}]}]}`,
		"objective without metrics": `{"summary":"s","requirements":[],"objectives":[{"statement":"o"}]}`,
	}
	for name, raw := range invalid {
		if _, err := model.DecodeStructured([]byte(raw), []byte(PRD)); err == nil {
			t.Errorf("%s: should fail validation", name)
		}
	}
}
