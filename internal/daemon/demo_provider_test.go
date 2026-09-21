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
		// max_cycles == 0 here happens to be <= cycles's own effective cap of
		// 3, so this row alone never distinguished "clamped to the engine's
		// effective 3" from the old, wrong "unclamped" behavior -- see the
		// next case for that.
		{"no max_cycles configured, below the effective default", 2, 0, 2},
		// review MEDIUM #7, proven: max_cycles == 0 does NOT mean unclamped.
		// config.applyDefaults only defaults max_cycles to 3 when EVERY
		// review.* field is unset, so a harness.yaml setting some other
		// review.* field (or simply block_review_cycles alone, as here)
		// leaves max_cycles at its raw zero even though the engine (New/
		// Reload) coerces it to 3 before ever running a review loop. The old
		// `max > 0 &&` guard read that raw zero as "no clamp" and handed the
		// surplus 2 entries to the plan/change-set review queues instead.
		{"no max_cycles configured, above the effective default", 5, 0, 3},
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
