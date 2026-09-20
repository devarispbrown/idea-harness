package workflow

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/blob"
	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/security"
)

// TestReloadDoesNotRaceConfigReaders is review H4's engine-level regression
// test.
//
// Reload's own doc comment claimed it was "guarded by e.mu, which ALL entry
// points now hold" -- false: Run, Advance, Retry, RevisePlan, Resume, Revise,
// ReopenPhase and their many helpers (runModel, storeBlob, resolveModelSpec,
// redact, researchEvidence, contextDocs, ...) read cfg/registry/redactor/
// blobs/dotEnv without holding mu (mu is scoped to Execute/ApplyChangeSet/
// RevertChangeSet only -- see the Engine struct's own doc comment). The
// review's race detector run caught exactly this: a write to e.blobs
// (Reload) racing a read of it (storeBlob, reached from runModel during a
// Run/Advance/Retry/RevisePlan/Resume/Revise pipeline).
//
// This hammers Reload against storeBlob and resolveModelSpec concurrently --
// both real, unexported engine internals reached via this same package's
// tests, standing in for the many pipeline call sites that reach them. Run
// under `go test -race`, it is deterministic for the pre-cfgMu code: any
// interleaving where the write and an unsynchronized read touch the same
// field is flagged, not just the rare ones that also produce a visibly wrong
// value.
func TestReloadDoesNotRaceConfigReaders(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, _ := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled})

	dir := t.TempDir()
	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(3)

	go func() {
		defer wg.Done()
		for i := 0; !stop.Load(); i++ {
			blobs, err := blob.New(dir, nil)
			if err != nil {
				continue
			}
			reg := model.NewRegistry("idea-harness-default")
			reg.Add("idea-harness-default", fp)
			eng.Reload(config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}},
				reg, security.NewRedactor(), blobs, map[string]string{"K": "v"})
		}
	}()

	go func() {
		defer wg.Done()
		for !stop.Load() {
			eng.storeBlob("race probe content")
		}
	}()

	go func() {
		defer wg.Done()
		for !stop.Load() {
			_ = eng.resolveModelSpec(RoleEngineeringLead)
			_ = eng.implementerModel()
			_ = eng.redact("some text with no secret in it")
		}
	}()

	time.Sleep(150 * time.Millisecond)
	stop.Store(true)
	wg.Wait()
}
