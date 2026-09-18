package workflow

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// TestRunSurfacesTheMissingKeyMessageInsteadOfARawProviderError is F1's
// workflow-level acceptance test.
//
// The daemon registers a model.MissingKeyProvider (internal/model/missingkey.go)
// in place of the real HTTP client whenever a keyed provider's key is unset
// (internal/daemon/runtime.go's providerFor). This confirms that failure
// actually reaches the caller of Engine.Run -- the model-call path every
// stage of the workflow goes through -- as the friendly pre-flight message,
// not a provider-shaped error a user cannot act on.
func TestRunSurfacesTheMissingKeyMessageInsteadOfARawProviderError(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	blobs, err := blob.New(filepath.Join(t.TempDir(), "blobs"), nil)
	if err != nil {
		t.Fatal(err)
	}

	reg := model.NewRegistry("openai")
	reg.Add("openai", model.NewMissingKeyProvider("openai", "OPENAI_API_KEY"))

	eng := New(db, blobs, reg, config.HarnessConfig{DefaultModel: "openai/gpt-4o-mini"}, nil)
	eng.SetRawBlobs(blobs)

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(context.Background(), ini); err != nil {
		t.Fatal(err)
	}

	_, err = eng.Run(context.Background(), ini.ID, "an idea that needs a real model")
	if err == nil {
		t.Fatal("Run succeeded with a keyless provider registered")
	}
	if !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Errorf("error does not name the missing variable: %v", err)
	}
	if !strings.Contains(err.Error(), "idea daemon stop") {
		t.Errorf("error does not name the fix: %v", err)
	}
}
