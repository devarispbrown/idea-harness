package daemon

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/model"
)

type countingLister struct {
	*model.FakeProvider
	calls *atomic.Int32
}

func (c countingLister) ListModels(context.Context) ([]string, error) {
	c.calls.Add(1)
	return []string{"m1", "m2"}, nil
}

// A page polling /providers (or, as happened, re-rendering in a loop) called
// the provider's model-list API with the user's key on every request.
func TestLiveModelListsAreReusedNotRefetched(t *testing.T) {
	var calls atomic.Int32
	reg := model.NewRegistry("p")
	reg.Add("p", countingLister{model.NewFakeProvider(), &calls})
	for range 20 {
		if _, err := listModelsCached(t.Context(), reg, "p"); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("the provider was asked %d times for 20 requests, want 1", n)
	}
	// A reload builds a new registry, and that is asked afresh.
	reg2 := model.NewRegistry("p")
	reg2.Add("p", countingLister{model.NewFakeProvider(), &calls})
	if _, err := listModelsCached(t.Context(), reg2, "p"); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 2 {
		t.Errorf("a new registry reused the old one's list (calls=%d)", n)
	}
}
