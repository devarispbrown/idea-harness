package daemon

import (
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/model"
)

// "It is a count, not a flag" is the load-bearing claim behind this design —
// a standing response would also block the plan and change-set reviews, and a
// blocking finding on a change set could not be cleared by anything the CLI
// could do (until `--accept-findings`, which clears the reviewer's own findings
// and not a deny code).
// It was asserted in a CHANGELOG entry, two doc comments and the configuration
// reference, and tested nowhere.
func TestDemoProviderQueuesExactlyWhatIsAsked(t *testing.T) {
	drain := func(fp *model.FakeProvider) int {
		n := 0
		req := model.Request{JSONSchemaName: "review", Messages: []model.Message{{Role: "user", Content: "x"}}}
		for i := 0; i < 10; i++ {
			resp, err := fp.Complete(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			if resp.RawJSON != model.DemoBlockingReviewJSON {
				break
			}
			n++
		}
		return n
	}

	for _, tc := range []struct {
		name       string
		cycles     int
		maxCycles  int
		wantQueued int
	}{
		{"off by default", 0, 3, 0},
		{"one", 1, 3, 1},
		{"exactly max_cycles", 3, 3, 3},
		// Clamped: a surplus outlives M1's review calls. The plan review has
		// its own queue key now, so what a surplus on `review` reaches is the
		// change-set review — where a blocking finding cannot be cleared by
		// anything the CLI can do, which is the state this design avoids.
		{"clamped to max_cycles", 5, 3, 3},
		// max_cycles unset means the engine's own default of 3 applies later;
		// nothing to clamp against here, so the value stands.
		{"no max_cycles configured", 2, 0, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fp := demoProvider(config.HarnessConfig{
				Demo:   config.DemoConfig{BlockReviewCycles: tc.cycles},
				Review: config.ReviewConfig{MaxCycles: tc.maxCycles},
			})
			if got := drain(fp); got != tc.wantQueued {
				t.Errorf("queued %d blocking reviews, want %d", got, tc.wantQueued)
			}
		})
	}
}
