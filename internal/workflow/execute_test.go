package workflow

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/model"
	"github.com/devarispbrown/idea-harness/internal/security"
	"github.com/devarispbrown/idea-harness/internal/store"
)

func newExecuteFixture(t *testing.T) (*Engine, *store.DB, string) {
	t.Helper()
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{Network: config.NetworkDisabled, Review: config.ReviewConfig{MaxCycles: 3}})
	// Isolate both the executor work root and the canonical workspace per test.
	eng.SetWorkRoot(t.TempDir())
	eng.SetWorkspaceRoot(t.TempDir())
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	return eng, db, ini.ID
}

func TestExecuteCreatesReviewRequiredChangeSet(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}

	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.HumanID != "CHG-1" {
		t.Fatalf("expected CHG-1, got %q", cs.HumanID)
	}
	if cs.TaskID != task.ID {
		t.Fatalf("expected task id %q, got %q", task.ID, cs.TaskID)
	}
	if cs.PatchDigest == "" {
		t.Fatal("expected a patch digest")
	}
	if cs.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("expected review_required, got %q", cs.Status)
	}

	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("expected persisted review_required, got %q", got.Status)
	}
}

// TestChangeSetRecordsDemoModelWhenNoProviderIsConfigured pins the ChangeSet.Model
// contract the web-build worker codes against: the UI labels output
// "Shipped (demo)" / "Placeholder change" by checking model === "demo", so a
// change set produced with no real provider configured (newExecuteFixture's
// fixture: fake provider, no executor.model) must report that literal string,
// not the registry's internal key ("idea-harness-default").
func TestChangeSetRecordsDemoModelWhenNoProviderIsConfigured(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()
	task := &domain.Task{InitiativeID: iniID, Title: "t", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Model != "demo" {
		t.Fatalf("Model = %q, want %q", cs.Model, "demo")
	}
	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "demo" {
		t.Fatalf("persisted Model = %q, want %q", got.Model, "demo")
	}
}

// TestChangeSetRecordsTheConfiguredModel is the other half: a real
// executor.model must be reported as-is, not folded into "demo".
func TestChangeSetRecordsTheConfiguredModel(t *testing.T) {
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{
		Executor: config.ExecutorConfig{Model: "openai/gpt-4o-mini"},
	})
	eng.SetWorkRoot(t.TempDir())
	eng.SetWorkspaceRoot(t.TempDir())
	ctx := context.Background()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "t", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Model != "openai/gpt-4o-mini" {
		t.Fatalf("Model = %q, want %q", cs.Model, "openai/gpt-4o-mini")
	}
}

// TestStructuredDiffCarriesAUnifiedPatch is B6's backend half: the diff view
// renders the unified patch, not a full-file dump, for every change kind.
// Before this, StructuredFile carried only the post-image `content`, so an
// existing file rewritten wholesale looked identical to a brand-new file with
// no way to tell "this replaced 40 lines" from "this added 40 lines".
func TestStructuredDiffCarriesAUnifiedPatch(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	// Seed the workspace with a file execute will modify, so BaseDigest and
	// the pre-image both resolve from the live workspace at review time
	// (before any apply has run fsdiff.Prepare to persist a pre-image blob).
	if err := os.WriteFile(filepath.Join(eng.workspaceRoot, "server.js"), []byte("route /\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	task := &domain.Task{InitiativeID: iniID, Title: "t", Statement: "s", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	files := eng.StructuredDiff(&cs)
	if len(files) == 0 {
		t.Fatal("expected at least one changed file")
	}
	for _, f := range files {
		if f.Patch == "" {
			t.Errorf("file %q (%s) carries no patch", f.Path, f.ChangeType)
			continue
		}
		switch f.ChangeType {
		case "add":
			if strings.Contains(f.Patch, "\n- ") || strings.HasPrefix(f.Patch, "- ") {
				t.Errorf("added file %q patch has a removed line: %q", f.Path, f.Patch)
			}
		case "delete":
			if strings.Contains(f.Patch, "\n+ ") || strings.HasPrefix(f.Patch, "+ ") {
				t.Errorf("deleted file %q patch has an added line: %q", f.Path, f.Patch)
			}
		}
	}
}

// TestExecuteDoesNotRejectOnSecretMarkerAlone is the ICP-round fix (review
// D2/A1): a bare vocabulary word -- password/token/secret/auth and similar --
// anywhere in the produced patch no longer blocks the change set. It used to
// (see git history), with no override and no file or line, which is exactly
// what stopped the ICP journey's per-user-API-keys-with-rate-limiting feature
// from ever landing. The word still surfaces, but only as a non-blocking
// "sensitive area" finding for the reviewer.
func TestExecuteDoesNotRejectOnSecretMarkerAlone(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Store password in vault", Statement: "secret", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}

	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("a bare vocabulary marker must not block; got %q", cs.Status)
	}

	findings, err := db.ListFindingsByInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	var advisory, blocked bool
	for _, f := range findings {
		if f.TargetID != cs.ID {
			continue
		}
		if f.Category == "sensitive_area" {
			advisory = true
			if f.Severity.Blocking() {
				t.Errorf("the sensitive_area finding must not block: %+v", f)
			}
		}
		if f.Category == "secret_sweep" {
			blocked = true
		}
	}
	if !advisory {
		t.Fatal("expected a non-blocking sensitive_area finding naming the marker")
	}
	if blocked {
		t.Fatal("a bare vocabulary marker must not raise a secret_sweep finding")
	}
}

// TestSensitiveAreaFindingCreatedAtMostOnce is the low-priority review item:
// createSensitiveAreaFinding is called from both Execute's own sweep and the
// apply-time re-sweep (ApplyChangeSet), and a retried apply calls the latter
// again -- none of which should pile up a second, third, ... near-identical
// advisory finding on the same change set. Mutation-check: remove the
// "already recorded" early-return in createSensitiveAreaFinding, and this
// fails.
func TestSensitiveAreaFindingCreatedAtMostOnce(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Store password in vault", Statement: "secret", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}

	// Call createSensitiveAreaFinding again directly, simulating a second
	// pass over the same change set (e.g. a retried apply's re-sweep) with
	// signals still detected.
	signals := []domain.RiskSignal{{Name: "credential", Detected: true}}
	if err := eng.createSensitiveAreaFinding(ctx, iniID, cs.ID, signals, "second pass"); err != nil {
		t.Fatal(err)
	}

	findings, err := db.ListFindingsByTarget(ctx, "change_set", cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range findings {
		if f.Category == "sensitive_area" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("sensitive_area findings on %s = %d, want exactly 1", cs.ID, n)
	}
}

// TestExecuteRejectsOnRealCredential is the other half: a real credential
// VALUE (a known key format) still blocks, and the denial names where -- the
// path:line -- and the kind of secret, never the value itself (review #2).
func TestExecuteRejectsOnRealCredential(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Add key AKIAIOSFODNN7EXAMPLE", Statement: "x", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}

	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetRejected {
		t.Fatalf("expected rejected (real credential), got %q", cs.Status)
	}

	findings, err := db.ListFindingsByInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	var f *domain.Finding
	for i := range findings {
		if findings[i].TargetID == cs.ID && findings[i].Category == "secret_sweep" && findings[i].Severity == domain.SeverityCritical {
			f = &findings[i]
		}
	}
	if f == nil {
		t.Fatal("expected a critical secret_sweep finding explaining the rejection")
	}
	if !strings.Contains(f.Finding, ":") || !strings.Contains(f.Finding, "AWS access key ID") {
		t.Errorf("the denial must name path:line and the kind of secret: %q", f.Finding)
	}
	if strings.Contains(f.Finding, "AKIAIOSFODNN7EXAMPLE") || strings.Contains(f.Evidence, "AKIAIOSFODNN7EXAMPLE") {
		t.Errorf("the denial must never echo the credential value: %+v", f)
	}
	if f.SecretFingerprint == "" {
		t.Error("expected a secret fingerprint to be recorded for a possible override")
	}
	if f.SecretKind != "AWS access key ID" {
		t.Errorf("SecretKind = %q, want %q", f.SecretKind, "AWS access key ID")
	}
	// The deny code is what makes this a system-deny the review's
	// --accept-findings flag cannot sign off (denyReviewOpenBlocking keys on
	// Code != ""). Mutation-check for sweepDenyCode: make it always return "",
	// and this fails.
	if f.Code != domain.DenyCodeSecretSweep {
		t.Errorf("Code = %q, want %q", f.Code, domain.DenyCodeSecretSweep)
	}
}

// TestNotASecretDispositionUnblocksFutureExecutes is the review changes #1
// override exercised at the engine layer: disposition the raised finding
// not_a_secret, then confirm a re-execute of the identical content is no
// longer rejected. Mutation-check for filterAllowlisted (internal/workflow/
// execute.go): break it to keep every match regardless of the allowlist, and
// this fails.
func TestNotASecretDispositionUnblocksFutureExecutes(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Add key AKIAIOSFODNN7EXAMPLE", Statement: "x", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetRejected {
		t.Fatalf("setup: expected rejected, got %q", cs.Status)
	}
	findings, err := db.ListFindingsByInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	var findingID string
	for _, f := range findings {
		if f.Category == "secret_sweep" {
			findingID = f.ID
		}
	}
	if findingID == "" {
		t.Fatal("setup: no secret_sweep finding")
	}
	if err := db.SetFindingDisposition(ctx, findingID, domain.DispositionNotASecret, store.HumanActor); err != nil {
		t.Fatalf("disposition not_a_secret: %v", err)
	}

	// The task was reopened on rejection (SetTaskStatus -> proposed); the
	// re-run picks it up again and produces the identical content.
	cs2, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs2.Status == domain.ChangeSetRejected {
		t.Fatalf("the allowlisted fingerprint still rejected the re-run: %+v", cs2)
	}
}

// TestDedupeCredentialFindings is the low-priority review item: the same
// value at the same (path, line) can match more than one catalog pattern (or
// both a known value and a pattern), and must be reported once, not once per
// pattern that happened to match. Mutation-check: make dedupeCredentialFindings
// a no-op (`return matches`), and this fails.
func TestDedupeCredentialFindings(t *testing.T) {
	dup := security.CredentialFinding{Path: "a.go", Line: 5, Fingerprint: "fp-1", Kind: "AWS access key ID", Overridable: true}
	dupAgain := security.CredentialFinding{Path: "a.go", Line: 5, Fingerprint: "fp-1", Kind: "credential assignment", Overridable: true}
	sameLineDifferentValue := security.CredentialFinding{Path: "a.go", Line: 5, Fingerprint: "fp-2", Overridable: true}
	differentLine := security.CredentialFinding{Path: "a.go", Line: 6, Fingerprint: "fp-1", Overridable: true}
	differentPath := security.CredentialFinding{Path: "b.go", Line: 5, Fingerprint: "fp-1", Overridable: true}
	knownValueFirst := security.CredentialFinding{Path: "c.go", Line: 1, Fingerprint: "fp-known", Overridable: false, Kind: "configured secret value"}
	patternSameSpot := security.CredentialFinding{Path: "c.go", Line: 1, Fingerprint: "fp-known", Overridable: true, Kind: "API key"}

	out := dedupeCredentialFindings([]security.CredentialFinding{
		dup, dupAgain, sameLineDifferentValue, differentLine, differentPath, knownValueFirst, patternSameSpot,
	})
	if len(out) != 5 {
		t.Fatalf("dedupeCredentialFindings returned %d matches, want 5 (dup/dupAgain collapsed, and known/pattern at the same spot collapsed): %+v", len(out), out)
	}
	// The first occurrence at (a.go, 5, fp-1) must be the one kept.
	for _, m := range out {
		if m.Path == "a.go" && m.Line == 5 && m.Fingerprint == "fp-1" && m.Kind != dup.Kind {
			t.Errorf("expected the first occurrence's Kind %q to survive, got %q", dup.Kind, m.Kind)
		}
		// A known-value match must survive over a same-spot pattern match, not
		// the reverse (review H3's Overridable=false invariant, reinforced by
		// dedup order).
		if m.Path == "c.go" && !m.Overridable && m.Kind != "configured secret value" {
			t.Errorf("expected the known-value match to survive at c.go:1, got %+v", m)
		}
	}
}

// TestFilterAllowlistedScopedToPath is review H3, tested directly against
// the pure function: an allowlisted (fingerprint, path) pair must not
// suppress the SAME fingerprint at a DIFFERENT path.
func TestFilterAllowlistedScopedToPath(t *testing.T) {
	allowed := map[string]bool{store.AllowlistKey("fp-1", "tests/fixture.js"): true}
	matches := []security.CredentialFinding{
		{Path: "tests/fixture.js", Fingerprint: "fp-1", Overridable: true},
		{Path: "src/prod.js", Fingerprint: "fp-1", Overridable: true},
	}
	out := filterAllowlisted(matches, allowed)
	if len(out) != 1 || out[0].Path != "src/prod.js" {
		t.Fatalf("filterAllowlisted = %+v, want only src/prod.js to survive", out)
	}
}

// TestFilterAllowlistedNeverDropsAKnownValueMatch is review H3: even when
// the pattern-shaped match for the identical value+path was allowlisted (so
// its fingerprint+path IS in allowed), a known-value (Overridable=false)
// match with the same fingerprint+path must still survive -- a configured
// secret is never eligible for the override regardless of what a pattern
// match at the same fingerprint was allowlisted as. Mutation-check: drop
// `m.Overridable &&` from filterAllowlisted's condition, and this fails.
func TestFilterAllowlistedNeverDropsAKnownValueMatch(t *testing.T) {
	allowed := map[string]bool{store.AllowlistKey("fp-shared", "src/config.go"): true}
	matches := []security.CredentialFinding{
		{Path: "src/config.go", Fingerprint: "fp-shared", Overridable: false, Kind: "configured secret value"},
	}
	out := filterAllowlisted(matches, allowed)
	if len(out) != 1 {
		t.Fatalf("filterAllowlisted dropped a non-overridable match: %+v", out)
	}
}

// TestConfiguredSecretValueIsNeverAllowlistable is the Overridable=false
// invariant (review changes #1, inverted by review H3): a value drawn from
// the project's own configured secrets (the executor's resolved key, via the
// engine's redactor) is never eligible for the not_a_secret override at all
// -- unlike a pattern-shaped match, it is not a "might be a false positive"
// shape, it is a real credential by construction, and the store itself now
// refuses the disposition outright (persisted secret_overridable=false)
// rather than merely have the sweep re-deny it later. Mutation-check: remove
// the `!secretOverridable` refusal from SetFindingDisposition, or drop
// `m.Overridable &&` from filterAllowlisted's condition, and this fails.
func TestConfiguredSecretValueIsNeverAllowlistable(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	const known = "super-secret-configured-value-0001"
	eng.SetRedactor(security.NewRedactor(known))

	task := &domain.Task{InitiativeID: iniID, Title: "Add key " + known, Statement: "x", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetRejected {
		t.Fatalf("setup: expected rejected on a configured secret value, got %q", cs.Status)
	}
	findings, err := db.ListFindingsByInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	var findingID string
	for _, f := range findings {
		if f.Category != "secret_sweep" {
			continue
		}
		findingID = f.ID
		if f.SecretKind != "configured secret value" {
			t.Errorf("SecretKind = %q, want %q", f.SecretKind, "configured secret value")
		}
		if f.SecretOverridable {
			t.Errorf("SecretOverridable = true, want false for a configured secret value")
		}
	}
	if findingID == "" {
		t.Fatal("setup: no secret_sweep finding")
	}
	// The store now knows this finding is not eligible for the override at
	// all, and refuses the disposition outright.
	if err := db.SetFindingDisposition(ctx, findingID, domain.DispositionNotASecret, store.HumanActor); err == nil {
		t.Fatal("want SetFindingDisposition to refuse not_a_secret on a configured secret value, got nil error")
	}

	// The refused disposition must not have written an allowlist entry either
	// (belt and suspenders on top of the store-level refusal above).
	allowed, err := db.SecretAllowlistFingerprints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed) != 0 {
		t.Fatalf("SecretAllowlistFingerprints = %v, want empty: the refused disposition must not have allowlisted anything", allowed)
	}

	// The sweep must still deny the identical content again.
	cs2, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs2.Status != domain.ChangeSetRejected {
		t.Fatalf("a configured secret value must still be denied, got %q", cs2.Status)
	}
}

func TestExecuteRecordsDeterministicCheckFindings(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("expected review_required, got %q", cs.Status)
	}

	findings, err := db.ListFindingsByInitiative(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	// B5: patch-applies now runs for real (pass), while build/tests are
	// not_applicable because no build/test command is configured.
	got := map[string]domain.Finding{}
	for _, f := range findings {
		if f.TargetID == cs.ID {
			got[f.Category] = f
		}
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 deterministic-check findings, got %d: %+v", len(got), got)
	}
	if got["patch_applies"].Severity.Blocking() {
		t.Errorf("patch_applies should pass, got %+v", got["patch_applies"])
	}
	for _, c := range []string{"build", "tests"} {
		if got[c].Disposition != domain.DispositionNotApplicable {
			t.Errorf("%s should be not_applicable (no command), got %+v", c, got[c])
		}
	}
}

func TestApplyRequiresApproval(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}

	if err := eng.ApplyChangeSet(ctx, iniID, cs.ID); err == nil {
		t.Fatal("expected apply of a non-approved change set to fail")
	}
}

func TestReviewApproveThenApply(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}

	// The demo reviewer emits no findings, so approval is not blocked.
	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetApproved {
		t.Fatalf("expected approved, got %q", got.Status)
	}

	if err := eng.ApplyChangeSet(ctx, iniID, cs.ID); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetApplied {
		t.Fatalf("expected applied, got %q", got.Status)
	}

	journal, err := db.ListJournal(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	// H2: prepared → workspace_mutated → committed.
	if len(journal) != 3 || journal[2].State != domain.ApplyCommitted {
		t.Fatalf("expected prepared, workspace_mutated, committed journal rows, got %+v", journal)
	}

	// The real file must have been written into the canonical workspace.
	data, err := os.ReadFile(filepath.Join(eng.workspaceRoot, "hello.txt"))
	if err != nil {
		t.Fatalf("read applied hello.txt: %v", err)
	}
	if !strings.Contains(string(data), "task: Implement core") {
		t.Fatalf("hello.txt = %q, want it to contain %q", string(data), "task: Implement core")
	}
}

// TestApplyResweepDeniesAGrownCatalog is the apply-time defense-in-depth case
// (review D2/A1: "keep the apply-time re-sweep consistent with the execute-
// time sweep"): a value that passed the sweep at execute time can still be
// denied at apply if the project's configured secrets grew in between (a key
// rotated into .env, say). It must be denied the same way execute denies one
// -- a critical secret_sweep finding naming path:line and kind, the change set
// recovers to rejected, and the task reopens -- not applied to the workspace.
func TestApplyResweepDeniesAGrownCatalog(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	// No redactor yet: this passes the execute-time sweep cleanly.
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err != nil {
		t.Fatal(err)
	}

	// The catalog "grows" between execute and apply: a value the produced
	// content happens to contain is now a configured secret.
	eng.SetRedactor(security.NewRedactor("Implement core"))

	if err := eng.ApplyChangeSet(ctx, iniID, cs.ID); err == nil {
		t.Fatal("expected apply to be denied by the grown catalog")
	}

	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetRejected {
		t.Fatalf("expected the change set recovered to rejected, got %q", got.Status)
	}
	gotTask, err := db.GetTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotTask.Status != domain.TaskProposed {
		t.Fatalf("expected the task reopened to proposed, got %q", gotTask.Status)
	}
	if _, err := os.Stat(filepath.Join(eng.workspaceRoot, "hello.txt")); !os.IsNotExist(err) {
		t.Fatalf("the workspace must not have been mutated: stat hello.txt = %v", err)
	}

	findings, err := db.ListFindingsByTarget(ctx, "change_set", cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	var f *domain.Finding
	for i := range findings {
		if findings[i].Category == "secret_sweep" {
			f = &findings[i]
		}
	}
	if f == nil {
		t.Fatal("expected a secret_sweep finding from the apply-time re-sweep")
	}
	if !strings.Contains(f.Finding, "hello.txt:") {
		t.Errorf("the denial must name path:line, got %q", f.Finding)
	}
	if f.SecretKind != "configured secret value" {
		t.Errorf("SecretKind = %q, want %q", f.SecretKind, "configured secret value")
	}
	if f.Code != domain.DenyCodeSecretSweep {
		t.Errorf("Code = %q, want %q", f.Code, domain.DenyCodeSecretSweep)
	}
}

func TestRevertAppendsRolledBackJournal(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err != nil {
		t.Fatal(err)
	}
	if err := eng.ApplyChangeSet(ctx, iniID, cs.ID); err != nil {
		t.Fatal(err)
	}

	if err := eng.RevertChangeSet(ctx, iniID, cs.ID); err != nil {
		t.Fatal(err)
	}
	journal, err := db.ListJournal(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	// H2: prepared → workspace_mutated → committed → rolled_back.
	if len(journal) != 4 || journal[3].State != domain.ApplyRolledBack {
		t.Fatalf("expected prepared, workspace_mutated, committed, rolled_back journal rows, got %+v", journal)
	}

	got, err := db.GetChangeSet(ctx, cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ChangeSetReverted {
		t.Fatalf("expected reverted status after rollback, got %q", got.Status)
	}

	// The applied file must have been removed on revert (it did not exist before).
	if _, err := os.Stat(filepath.Join(eng.workspaceRoot, "hello.txt")); !os.IsNotExist(err) {
		t.Fatalf("hello.txt should be removed after revert, stat err = %v", err)
	}
}

func TestSelectExecutor(t *testing.T) {
	cases := []struct {
		kind config.ExecutorKind
		want string
	}{
		{config.ExecutorInternal, "internal"},
		{config.ExecutorContainer, "container"},
		{config.ExecutorKind(""), "internal"}, // empty/unknown fails closed to internal
		{config.ExecutorKind("bogus"), "internal"},
	}
	for _, tc := range cases {
		e := &Engine{cfg: config.HarnessConfig{Executor: config.ExecutorConfig{Kind: tc.kind}}}
		exec, err := e.selectExecutor()
		if err != nil {
			t.Fatalf("selectExecutor(%q): %v", tc.kind, err)
		}
		if exec == nil {
			t.Fatalf("selectExecutor(%q) returned nil", tc.kind)
		}
		if got := exec.Name(); got != tc.want {
			t.Errorf("selectExecutor(%q).Name() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

// A coding agent runs in a container with deliberate network egress and is
// handed a provider API key. That key used to be chosen by scanning the presets
// for whichever one happened to be set in the environment, so a typo in
// executor.model sent an unrelated provider's real key out of the machine.
func TestSelectExecutorFailsClosedWithoutAResolvableProvider(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-should-never-be-scavenged")

	for _, kind := range []config.ExecutorKind{config.ExecutorOMP, config.ExecutorHermes} {
		e := &Engine{cfg: config.HarnessConfig{Executor: config.ExecutorConfig{
			Kind:  kind,
			Model: "typo-provider/some-model",
		}}}
		exec, err := e.selectExecutor()
		if err == nil {
			t.Fatalf("selectExecutor(%q) with an unresolvable provider returned %v, want an error", kind, exec)
		}
		if !strings.Contains(err.Error(), "typo-provider/some-model") {
			t.Errorf("error should name the model that could not be resolved, got: %v", err)
		}
	}
}

// A provider the operator actually configured still resolves.
func TestSelectExecutorAcceptsAConfiguredProvider(t *testing.T) {
	// The key is set here, not inherited: a keyed provider without its key is
	// refused, so this passed only on machines whose shell happened to export
	// it -- and failed in CI, where none is set.
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-test")
	e := &Engine{cfg: config.HarnessConfig{
		Executor: config.ExecutorConfig{Kind: config.ExecutorHermes, Model: "anthropic/claude-sonnet-4-5-20250929"},
	}}
	exec, err := e.selectExecutor()
	if err != nil {
		t.Fatalf("selectExecutor with a preset provider: %v", err)
	}
	if got := exec.Name(); got != "hermes" {
		t.Errorf("Name() = %q, want hermes", got)
	}
}

func dockerAvailable() bool {
	if _, err := exec.LookPath("docker"); err != nil {
		return false
	}
	return exec.Command("docker", "info").Run() == nil
}

func TestExecuteRejectsOnFailingBuild(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available")
	}
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{
		Network:  config.NetworkDisabled,
		Review:   config.ReviewConfig{MaxCycles: 3},
		Check:    config.CheckConfig{Image: "alpine:3.20"},
		BuildCmd: "exit 1",
	})
	eng.SetWorkRoot(t.TempDir())
	eng.SetWorkspaceRoot(t.TempDir())
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetRejected {
		t.Fatalf("expected rejected (failing build command), got %q", cs.Status)
	}
}

func TestExecutePassesConfiguredBuild(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available")
	}
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{
		Network:  config.NetworkDisabled,
		Review:   config.ReviewConfig{MaxCycles: 3},
		Check:    config.CheckConfig{Image: "alpine:3.20"},
		BuildCmd: "echo ok",
		TestCmd:  "echo ok",
	})
	eng.SetWorkRoot(t.TempDir())
	eng.SetWorkspaceRoot(t.TempDir())
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("expected review_required (build passes), got %q", cs.Status)
	}
}

// TestExecuteRejectsCredentialInCheckOutput is review M2: a credential
// printed by the build/test command's own output must deny the change set
// via the same catalog + allowlist path file content gets -- a dedicated
// secret_sweep finding named "in the output of <check>", never the value.
// Before the fix, this used a bare detectCredential boolean with no
// allowlist filter and no dedicated finding at all.
func TestExecuteRejectsCredentialInCheckOutput(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("docker not available")
	}
	const fixtureKey = "AKIA1234567890ABCDEF"
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{
		Network:  config.NetworkDisabled,
		Review:   config.ReviewConfig{MaxCycles: 3},
		Check:    config.CheckConfig{Image: "alpine:3.20"},
		BuildCmd: "echo " + fixtureKey,
	})
	eng.SetWorkRoot(t.TempDir())
	eng.SetWorkspaceRoot(t.TempDir())
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetRejected {
		t.Fatalf("expected rejected (credential in check output), got %q", cs.Status)
	}

	findings, err := db.ListFindingsByInitiative(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	var findingID string
	for _, f := range findings {
		if f.Category != "secret_sweep" {
			continue
		}
		if !strings.Contains(f.Finding, "in the output of build") {
			continue
		}
		findingID = f.ID
		if strings.Contains(f.Finding, fixtureKey) || strings.Contains(f.Evidence, fixtureKey) {
			t.Errorf("the denial must never echo the credential value: %+v", f)
		}
		if f.SecretPath != "output of build" {
			t.Errorf("SecretPath = %q, want %q", f.SecretPath, "output of build")
		}
		if !f.SecretOverridable {
			t.Error("a pattern-shaped match in check output should be Overridable")
		}
	}
	if findingID == "" {
		t.Fatalf("no 'in the output of build' secret_sweep finding among: %+v", findings)
	}

	// review M2's other half: allowlisting that finding must let an identical
	// re-run pass, exactly like a file-content match.
	if err := db.SetFindingDisposition(ctx, findingID, domain.DispositionNotASecret, store.HumanActor); err != nil {
		t.Fatalf("disposition not_a_secret: %v", err)
	}
	cs2, err := eng.Execute(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cs2.Status == domain.ChangeSetRejected {
		t.Fatalf("an allowlisted fixture value printed by the build command must pass, got %q", cs2.Status)
	}
}

func TestChangesRequestedReopensTaskAndSupersedes(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}

	cs1, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs1.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("expected review_required, got %q", cs1.Status)
	}

	// Human requests changes: the task must re-open so a re-execute can supersede.
	if err := eng.ReviewChangeSet(ctx, iniID, cs1.ID, "changes_requested"); err != nil {
		t.Fatal(err)
	}
	tasks, err := db.ListTasks(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != domain.TaskProposed {
		t.Fatalf("expected task re-opened to proposed, got %+v", tasks)
	}

	cs2, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs2.ID == cs1.ID {
		t.Fatal("expected a new change set")
	}
	got1, err := db.GetChangeSet(ctx, cs1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got1.Status != domain.ChangeSetSuperseded {
		t.Fatalf("expected prior change set superseded, got %q", got1.Status)
	}
	if cs2.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("expected new change set review_required, got %q", cs2.Status)
	}
}

func TestExecuteRejectsBuildWhenNoSandboxImage(t *testing.T) {
	// Deterministic fail-closed path: BuildCmd set but no check.image → the check
	// refuses to run on the host and rejects the change set (no Docker needed).
	fp := model.NewFakeProvider()
	eng, db := newTestEngine(t, fp, config.HarnessConfig{
		Network:  config.NetworkDisabled,
		Review:   config.ReviewConfig{MaxCycles: 3},
		BuildCmd: "echo ok",
	})
	eng.SetWorkRoot(t.TempDir())
	eng.SetWorkspaceRoot(t.TempDir())
	ctx := context.Background()

	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	task := &domain.Task{InitiativeID: ini.ID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetRejected {
		t.Fatalf("expected rejected (no sandbox image), got %q", cs.Status)
	}
	// The task must be re-opened (proposed), not left done, so a re-execute can
	// produce a fresh change set and dependents are not permanently blocked.
	tasks, err := db.ListTasks(ctx, ini.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 || tasks[0].Status != domain.TaskProposed {
		t.Fatalf("expected re-opened task (proposed), got %+v", tasks)
	}
}

func TestTaskDependenciesApplied(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	a := &domain.Task{InitiativeID: iniID, Title: "A", Statement: "a", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, a, "", nil); err != nil {
		t.Fatal(err)
	}
	b := &domain.Task{InitiativeID: iniID, Title: "B", Statement: "b", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, b, "", nil); err != nil {
		t.Fatal(err)
	}
	if err := db.AddTaskDependencies(ctx, iniID, b.ID, []string{a.ID}); err != nil {
		t.Fatal(err)
	}

	// A has no deps -> ready.
	readyA, err := eng.taskDependenciesApplied(ctx, iniID, a.ID)
	if err != nil || !readyA {
		t.Fatalf("A should be ready, got ready=%v err=%v", readyA, err)
	}
	// B depends on A, which has no applied change set yet -> not ready.
	readyB, err := eng.taskDependenciesApplied(ctx, iniID, b.ID)
	if err != nil || readyB {
		t.Fatalf("B should be blocked (A not applied), got ready=%v err=%v", readyB, err)
	}

	// Apply A's change set (force status via SQL for the test).
	cs := &domain.ChangeSet{InitiativeID: iniID, TaskID: a.ID, PatchDigest: "patch"}
	if err := db.CreateChangeSet(ctx, cs); err != nil {
		t.Fatal(err)
	}
	if _, err := db.SQL().Exec(`UPDATE change_sets SET status = 'applied' WHERE id = ?`, cs.ID); err != nil {
		t.Fatal(err)
	}

	readyB, err = eng.taskDependenciesApplied(ctx, iniID, b.ID)
	if err != nil || !readyB {
		t.Fatalf("B should now be ready (A applied), got ready=%v err=%v", readyB, err)
	}
}

func TestApproveDeniedByOpenBlockingFinding(t *testing.T) {
	eng, db, iniID := newExecuteFixture(t)
	ctx := context.Background()

	task := &domain.Task{InitiativeID: iniID, Title: "Implement core", Statement: "build it", Priority: domain.PriorityMust}
	if err := db.CreateTask(ctx, task, "", nil); err != nil {
		t.Fatal(err)
	}
	cs, err := eng.Execute(ctx, iniID)
	if err != nil {
		t.Fatal(err)
	}
	if cs.Status != domain.ChangeSetReviewRequired {
		t.Fatalf("expected review_required, got %q", cs.Status)
	}

	// Plant an undisposed critical finding targeting the change set (as a prior
	// adversarial review would).
	if err := db.CreateFinding(ctx, &domain.Finding{
		InitiativeID: iniID,
		TargetType:   "change_set",
		TargetID:     cs.ID,
		Severity:     domain.SeverityCritical,
		Confidence:   domain.ConfidenceHigh,
		Category:     "review",
		Finding:      "prior review flagged a blocker",
	}); err != nil {
		t.Fatal(err)
	}

	// The human approve must be denied (system deny beats human approve).
	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err == nil {
		t.Fatal("expected approve to be denied by an open blocking finding")
	}

	// Dispositioning the blocking finding unblocks the approve.
	fs, err := db.ListFindingsByTarget(ctx, "change_set", cs.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range fs {
		if f.Severity.Blocking() {
			if err := db.SetFindingDisposition(ctx, f.ID, domain.DispositionFixed, store.HumanActor); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := eng.ReviewChangeSet(ctx, iniID, cs.ID, "approved"); err != nil {
		t.Fatalf("approve should succeed after dispositioning: %v", err)
	}
}

// TestCheckCommandRecommendsARealHarnessKey ties the advice to the schema that
// validates the file the advice is about.
//
// `idea reviews` printed `set tests_cmd in harness.yaml`, built by concatenating
// the finding category ("tests") with "_cmd". The field is `test_cmd`, so
// following the recommendation literally made `idea config validate` exit 1
// with `additional properties 'tests_cmd' not allowed`.
//
// The config keys are read from the PRODUCTION call sites by AST, not supplied
// by this test. A first version passed them in from a table here, so it
// validated its own strings: reintroducing the exact bug at execute.go's call
// site left the whole suite green.
func TestCheckCommandRecommendsARealHarnessKey(t *testing.T) {
	valid := map[string]bool{}
	for _, k := range config.HarnessTopLevelKeys() {
		valid[k] = true
	}
	if len(valid) == 0 {
		t.Fatal("no harness schema keys — the accessor is broken, so this check proves nothing")
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "execute.go", nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}

	// checkCommand(ctx, cs, workRoot, category, configKey, description, cmd)
	const categoryArg, configKeyArg = 3, 4
	found := 0
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) <= configKeyArg {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "checkCommand" {
			return true
		}
		key, kok := stringLit(call.Args[configKeyArg])
		cat, cok := stringLit(call.Args[categoryArg])
		if !kok || !cok {
			t.Errorf("%s: checkCommand's category/configKey are not string literals, so this "+
				"check cannot read them", fset.Position(call.Pos()))
			return true
		}
		found++
		if !valid[key] {
			t.Errorf("%s: recommends %q, which harness.yaml rejects — `idea config validate`\n"+
				"    exits 1 on it. Valid keys: %v", fset.Position(call.Pos()), key,
				config.HarnessTopLevelKeys())
		}
		if key == cat+"_cmd" && !valid[cat+"_cmd"] {
			t.Errorf("%s: the config key is being derived from the category again", fset.Position(call.Pos()))
		}
		return true
	})
	if found == 0 {
		t.Fatal("no checkCommand call sites found — the matcher is broken, so this proves nothing")
	}
	if found < 2 {
		t.Errorf("found only %d checkCommand call site(s); build and tests are both expected", found)
	}
}

// stringLit unquotes a basic string literal argument.
func stringLit(e ast.Expr) (string, bool) {
	lit, ok := e.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	v, err := strconv.Unquote(lit.Value)
	return v, err == nil
}

// A keyless provider is not an unresolvable one, and the executor used to
// refuse both with the same check.
//
// Ollama is a first-class preset with DefaultKeyEnv "" and NeedsKey false. The
// guard was `keyEnv == ""`, so it was indistinguishable from a typo in
// executor.model — and every keyless provider was locked out of the
// coding-agent path entirely.
func TestKeylessProviderIsNotTreatedAsUnresolvable(t *testing.T) {
	e := &Engine{cfg: config.HarnessConfig{
		Executor: config.ExecutorConfig{Kind: config.ExecutorHermes, Model: "ollama/llama3.1"},
	}}
	p, err := e.implementerProvider()
	if err != nil {
		t.Fatalf("ollama did not resolve, though it is a preset: %v", err)
	}
	if p.Name != "ollama" || p.KeyEnv != "" || p.BaseURL == "" {
		t.Errorf("ollama resolved to %+v", p)
	}
	if _, err := e.selectExecutor(); err != nil {
		t.Errorf("selectExecutor refused a keyless provider: %v", err)
	}
}

// An unresolvable provider must still fail closed: the agent runs with network
// egress, so it must never be launched with a credential nobody chose. With a
// default model set too, which is the case the fallback used to swallow.
func TestUnresolvableProviderStillFailsClosed(t *testing.T) {
	for _, dm := range []string{"", "openai/gpt-5.6-luna"} {
		e := &Engine{cfg: config.HarnessConfig{
			DefaultModel: dm,
			Executor:     config.ExecutorConfig{Kind: config.ExecutorHermes, Model: "typo-provider/some-model"},
		}}
		_, err := e.selectExecutor()
		if err == nil {
			t.Fatalf("default_model %q: selectExecutor accepted an unresolvable provider", dm)
		}
		if !strings.Contains(err.Error(), "typo-provider") {
			t.Errorf("the refusal does not name what could not be resolved: %v", err)
		}
	}
}

// A keyed provider whose key is set nowhere is refused before launch. Hermes
// exits 0 after a 401, so the run otherwise produced an empty change set and
// no reason.
func TestKeyedProviderWithoutAKeyIsRefused(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	e := &Engine{cfg: config.HarnessConfig{
		Executor: config.ExecutorConfig{Kind: config.ExecutorHermes, Model: "openai/gpt-5.6-luna"},
	}}
	_, err := e.selectExecutor()
	if err == nil {
		t.Fatal("a keyed provider launched with no key")
	}
	if !strings.Contains(err.Error(), "OPENAI_API_KEY") {
		t.Errorf("the refusal does not name the missing variable: %v", err)
	}
	// The project's .env counts, as it does for every other model call.
	e.SetDotEnv(map[string]string{"OPENAI_API_KEY": "sk-from-dotenv"})
	if _, err := e.selectExecutor(); err != nil {
		t.Errorf("a key in .env was not found: %v", err)
	}
}

// Every provider this project presets must reach the coding agent.
func TestEveryPresetProviderCanLaunchTheAgent(t *testing.T) {
	for _, p := range config.Presets {
		if p.DefaultKeyEnv != "" {
			t.Setenv(p.DefaultKeyEnv, "test-key")
		}
		e := &Engine{cfg: config.HarnessConfig{
			Executor: config.ExecutorConfig{Kind: config.ExecutorHermes, Model: p.Key + "/some-model"},
		}}
		if _, err := e.selectExecutor(); err != nil {
			t.Errorf("%s cannot launch the coding agent: %v", p.Key, err)
		}
	}
}
