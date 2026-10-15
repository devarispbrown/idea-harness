package schema

import (
	"encoding/json"
	"testing"
)

type itemProps struct {
	Properties map[string]struct {
		Items struct {
			Properties map[string]any `json:"properties"`
		} `json:"items"`
	} `json:"properties"`
}

// TestPRDCitesAssertions pins the based_on contract: a requirement and a
// decision can each name the assertion codes they rest on, and a research
// finding can corroborate or contradict a framing assumption. The workflow
// turns these into derived_from / supports / contradicts edges; dropping one
// from the schema would quietly empty the graph those gaps read.
func TestPRDCitesAssertions(t *testing.T) {
	var prd itemProps
	if err := json.Unmarshal([]byte(PRD), &prd); err != nil {
		t.Fatal(err)
	}
	for _, list := range []string{"requirements", "decisions"} {
		if _, ok := prd.Properties[list].Items.Properties["based_on"]; !ok {
			t.Errorf("PRD %s items have no based_on", list)
		}
	}
	var rb itemProps
	if err := json.Unmarshal([]byte(ResearchBrief), &rb); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"supports", "contradicts"} {
		if _, ok := rb.Properties["findings"].Items.Properties[f]; !ok {
			t.Errorf("research findings have no %s", f)
		}
	}
}
