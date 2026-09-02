package workflow

import (
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/model"
)

func TestModelOverrideResolution(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, _ := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})

	const iniID = "some-initiative"
	eng.SetModelOverride(iniID, "openai/gpt-test")
	if got := eng.modelSpecFor(iniID, "product_manager"); got != "openai/gpt-test" {
		t.Fatalf("override not applied: %q", got)
	}

	// A different initiative is not affected by the override.
	if got := eng.modelSpecFor("other-initiative", "product_manager"); got == "openai/gpt-test" {
		t.Fatalf("override bled into another initiative: %q", got)
	}

	eng.clearModelOverride(iniID)
	if got := eng.modelSpecFor(iniID, "product_manager"); got == "openai/gpt-test" {
		t.Fatal("override not cleared")
	}
}
