package projection

import (
	"strings"
	"testing"
)

// TestNarrateNeverShowsARawEventType pins the decision page's What changed
// panel: an event type narrate had no phrase for used to come back verbatim,
// so the panel listed `stale.cleared` and `tasks.deleted` -- the event log's
// vocabulary, not the reader's. Every type now reads as words.
func TestNarrateNeverShowsARawEventType(t *testing.T) {
	for _, ev := range []string{
		"stale.cleared", "tasks.deleted", "change_set.status_changed", "change_set.review_set",
		"objectives.superseded", "requirement.objectives_replaced", "metric.created", "something_new.happened",
	} {
		for _, n := range []int{1, 3} {
			got := narrate(ev, n)
			if strings.ContainsAny(got, "._") {
				t.Errorf("narrate(%q, %d) = %q, still an identifier", ev, n, got)
			}
			if got == "" || got[:1] != strings.ToUpper(got[:1]) {
				t.Errorf("narrate(%q, %d) = %q, want a capitalised phrase", ev, n, got)
			}
		}
	}
	if got := narrate("objectives.superseded", 2); got != "Objectives superseded (2)" {
		t.Errorf("fallback with a count = %q", got)
	}
	if got := categoryLabel("change_set"); got != "Change sets" {
		t.Errorf("categoryLabel(change_set) = %q", got)
	}
	if got := categoryLabel("some_aggregate"); got != "Some aggregate" {
		t.Errorf("categoryLabel fallback = %q", got)
	}
}
