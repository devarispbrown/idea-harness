package daemon

import (
	"context"
	"sync"
	"time"

	"github.com/devarispbrown/idea-harness/internal/model"
)

// liveModelTTL is how long a provider's live model list is reused. The list
// changes when a provider ships a model, not between page polls.
const liveModelTTL = 5 * time.Minute

type liveModelKey struct {
	reg  *model.Registry // a reload builds a new registry, which is a new key
	name string
}

type liveModelEntry struct {
	models []string
	at     time.Time
}

var (
	liveModelMu    sync.Mutex
	liveModelCache = map[liveModelKey]liveModelEntry{}
)

// listModelsCached is reg.ListModels, remembered per registry and provider.
//
// Every GET /providers and GET /models asked the provider's own list endpoint
// with the user's key. A page that polls -- or, as happened, re-renders in a
// loop -- turned that into several calls a second against a third-party API.
// Only successful, non-empty lists are cached, so a failure is retried.
func listModelsCached(ctx context.Context, reg *model.Registry, name string) ([]string, error) {
	key := liveModelKey{reg, name}
	liveModelMu.Lock()
	if e, ok := liveModelCache[key]; ok && time.Since(e.at) < liveModelTTL {
		liveModelMu.Unlock()
		return e.models, nil
	}
	liveModelMu.Unlock()
	models, err := reg.ListModels(ctx, name)
	if err == nil && len(models) > 0 {
		liveModelMu.Lock()
		liveModelCache[key] = liveModelEntry{models, time.Now()}
		liveModelMu.Unlock()
	}
	return models, err
}
