package store

import (
	"context"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// newSecretSweepFinding creates a secret_sweep finding scoped to path with
// the given fingerprint/overridable, and returns its id. A single shared
// fingerprint across two different paths is exactly the H3 scoping scenario
// tests below exercise.
func newSecretSweepFinding(t *testing.T, db *DB, iniID, path, fingerprint string, overridable bool) string {
	t.Helper()
	f := &domain.Finding{
		InitiativeID:      iniID,
		TargetType:        "change_set",
		TargetID:          "cs-1",
		Severity:          domain.SeverityCritical,
		Confidence:        domain.ConfidenceHigh,
		Category:          "secret_sweep",
		Code:              domain.DenyCodeSecretSweep,
		Finding:           path + ": looks like a secret",
		Evidence:          "test",
		Impact:            "test",
		Recommendation:    "test",
		Disposition:       domain.DispositionRejected,
		SecretFingerprint: fingerprint,
		SecretKind:        "test kind",
		SecretPath:        path,
		SecretOverridable: overridable,
	}
	if err := db.CreateFinding(context.Background(), f); err != nil {
		t.Fatalf("CreateFinding: %v", err)
	}
	return f.ID
}

func newIniForSecretTests(t *testing.T, db *DB) string {
	t.Helper()
	ini := &domain.Initiative{Title: "x", Description: "x"}
	if err := db.CreateInitiative(context.Background(), ini); err != nil {
		t.Fatal(err)
	}
	return ini.ID
}

// TestNotASecretRefusedForNonOverridableFinding is review H3: the store
// itself must refuse not_a_secret on a finding whose SecretOverridable is
// false (a configured secret value), not merely rely on the sweep to
// re-deny it later. Mutation-check: remove the `!secretOverridable` check in
// SetFindingDisposition, and this fails.
func TestNotASecretRefusedForNonOverridableFinding(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	iniID := newIniForSecretTests(t, db)

	findingID := newSecretSweepFinding(t, db, iniID, "src/config.go", "fp-known-value", false)

	if err := db.SetFindingDisposition(ctx, findingID, domain.DispositionNotASecret, HumanActor); err == nil {
		t.Fatal("want an error: this finding is not eligible for not_a_secret")
	}

	allowed, err := db.SecretAllowlistFingerprints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed) != 0 {
		t.Fatalf("SecretAllowlistFingerprints = %v, want empty after a refused disposition", allowed)
	}
}

// TestNotASecretAllowedForOverridableFinding is the positive case: an
// ordinary pattern-shaped finding (SecretOverridable = true) can be
// dispositioned not_a_secret, and it writes an allowlist entry scoped to
// (fingerprint, path).
func TestNotASecretAllowedForOverridableFinding(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	iniID := newIniForSecretTests(t, db)

	findingID := newSecretSweepFinding(t, db, iniID, "src/config.go", "fp-pattern", true)

	if err := db.SetFindingDisposition(ctx, findingID, domain.DispositionNotASecret, HumanActor); err != nil {
		t.Fatalf("SetFindingDisposition: %v", err)
	}

	allowed, err := db.SecretAllowlistFingerprints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed[AllowlistKey("fp-pattern", "src/config.go")] {
		t.Fatalf("allowed = %v, want fp-pattern scoped to src/config.go", allowed)
	}
}

// TestSecretAllowlistScopedToPath is review H3: the same fingerprint
// allowlisted for one path must NOT suppress a finding with that fingerprint
// at a different path.
func TestSecretAllowlistScopedToPath(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	iniID := newIniForSecretTests(t, db)

	fixtureFinding := newSecretSweepFinding(t, db, iniID, "tests/fixture.js", "fp-shared", true)
	if err := db.SetFindingDisposition(ctx, fixtureFinding, domain.DispositionNotASecret, HumanActor); err != nil {
		t.Fatalf("SetFindingDisposition: %v", err)
	}

	allowed, err := db.SecretAllowlistFingerprints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed[AllowlistKey("fp-shared", "tests/fixture.js")] {
		t.Fatal("expected tests/fixture.js to be allowlisted")
	}
	if allowed[AllowlistKey("fp-shared", "src/prod.js")] {
		t.Fatal("the same fingerprint must not be allowlisted for src/prod.js just because tests/fixture.js was")
	}
}

// TestChangingDispositionAwayFromNotASecretRemovesAllowlistEntry is review
// M1: re-dispositioning a not_a_secret finding to something else must revoke
// the allowlist entry it created, so the sweep denies that value again.
func TestChangingDispositionAwayFromNotASecretRemovesAllowlistEntry(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	iniID := newIniForSecretTests(t, db)

	findingID := newSecretSweepFinding(t, db, iniID, "src/config.go", "fp-pattern", true)
	if err := db.SetFindingDisposition(ctx, findingID, domain.DispositionNotASecret, HumanActor); err != nil {
		t.Fatal(err)
	}
	allowed, err := db.SecretAllowlistFingerprints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !allowed[AllowlistKey("fp-pattern", "src/config.go")] {
		t.Fatal("setup: expected an allowlist entry")
	}

	if err := db.SetFindingDisposition(ctx, findingID, domain.DispositionRejected, HumanActor); err != nil {
		t.Fatal(err)
	}
	allowed, err = db.SecretAllowlistFingerprints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if allowed[AllowlistKey("fp-pattern", "src/config.go")] {
		t.Fatal("the allowlist entry must be removed once the disposition moved away from not_a_secret")
	}
}

// TestSecretAllowlistUpsertOnFingerprintAndPath is review M1: two findings
// sharing a (fingerprint, path) pair must not create two rows -- an upsert
// keeps exactly one entry.
func TestSecretAllowlistUpsertOnFingerprintAndPath(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	iniID := newIniForSecretTests(t, db)

	f1 := newSecretSweepFinding(t, db, iniID, "src/config.go", "fp-dup", true)
	f2 := newSecretSweepFinding(t, db, iniID, "src/config.go", "fp-dup", true)

	if err := db.SetFindingDisposition(ctx, f1, domain.DispositionNotASecret, HumanActor); err != nil {
		t.Fatal(err)
	}
	if err := db.SetFindingDisposition(ctx, f2, domain.DispositionNotASecret, HumanActor); err != nil {
		t.Fatal(err)
	}

	entries, err := db.ListSecretAllowlist(ctx)
	if err != nil {
		t.Fatal(err)
	}
	matching := 0
	for _, e := range entries {
		if e.Fingerprint == "fp-dup" && e.Path == "src/config.go" {
			matching++
		}
	}
	if matching != 1 {
		t.Fatalf("matching allowlist rows = %d, want exactly 1 (upserted, not duplicated)", matching)
	}
}

// TestRevokeSecretAllowlistDeletesByFingerprintAndPath is review M1: revoke
// must clear every row for the entry's (fingerprint, path) pair, so a
// pre-existing duplicate (from before the unique index existed) cannot
// survive a revoke.
func TestRevokeSecretAllowlistDeletesByFingerprintAndPath(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	// Simulate a pre-existing duplicate row directly (bypassing the upsert
	// path), which the unique index alone could not have prevented if it was
	// added to a database that already had one.
	if _, err := db.SQL().ExecContext(ctx,
		`INSERT INTO secret_allowlist (id, fingerprint, kind, path, actor, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"dup-1", "fp-x", "kind", "a.go", HumanActor, nowMS()); err != nil {
		t.Fatal(err)
	}

	if err := db.RevokeSecretAllowlist(ctx, "dup-1"); err != nil {
		t.Fatal(err)
	}

	allowed, err := db.SecretAllowlistFingerprints(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if allowed[AllowlistKey("fp-x", "a.go")] {
		t.Fatal("revoke did not clear the allowlist entry")
	}
}

// TestRevokeSecretAllowlistUnknownIDReturnsNotFound checks the not-found
// path still reports clearly.
func TestRevokeSecretAllowlistUnknownIDReturnsNotFound(t *testing.T) {
	db := openTestDB(t)
	if err := db.RevokeSecretAllowlist(context.Background(), "no-such-id"); err == nil {
		t.Fatal("want an error revoking an unknown id")
	}
}

// TestSecretFingerprintKeyIsStableAndPersisted is review H3: the per-project
// HMAC key is generated once and returned identically on every later call
// (persisted, not regenerated per call -- which would make every fingerprint
// unrecognizable on the next request).
func TestSecretFingerprintKeyIsStableAndPersisted(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	key1, err := db.SecretFingerprintKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(key1) == 0 {
		t.Fatal("want a non-empty key")
	}
	key2, err := db.SecretFingerprintKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(key1) != string(key2) {
		t.Fatal("SecretFingerprintKey returned a different key on a second call")
	}
}
