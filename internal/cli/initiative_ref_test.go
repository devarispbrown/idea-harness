package cli

import (
	"strings"
	"testing"
)

// TestUnknownInitiativeReferenceIsRefused pins audit 7's P1-6 across the
// commands docs/cli.md's reference-taking set names.
//
// resolveInitiative returned args[0] unchecked, so every one of these accepted a
// garbage id and produced whatever downstream 404 that endpoint happened to
// give — while docs/cli.md:110 promises an unknown argument is an error. The
// table is the point: a per-command test would leave the seventeenth command to
// be rediscovered as new work.
//
// `idea prd` is deliberately absent: its first positional is an artifact KIND,
// not a reference (prd.go slices it off before resolveInitiative sees the rest),
// so refusing an unknown value there would be a different check.
func TestUnknownInitiativeReferenceIsRefused(t *testing.T) {
	for _, cmd := range [][]string{
		{"status", "ZZZZ"},
		{"changed", "ZZZZ"},
		{"explain", "ZZZZ"},
		{"advance", "ZZZZ"},
		{"plan", "ZZZZ"},
		{"tasks", "ZZZZ"},
		{"decisions", "ZZZZ"},
		{"reviews", "ZZZZ"},
	} {
		t.Run(strings.Join(cmd, " "), func(t *testing.T) {
			f := newFakeDaemon(t)
			f.seedInitiative()

			_, _, err := run(t, cmd...)
			if err == nil {
				t.Fatalf("`idea %s` accepted an initiative reference that matches nothing", strings.Join(cmd, " "))
			}
			if !strings.Contains(err.Error(), "ZZZZ") {
				t.Errorf("the error does not name the reference that was refused: %v", err)
			}
		})
	}
}

// The other half: a reference that DOES match must still work, or the guard is
// a refusal of everything.
func TestKnownInitiativeReferenceIsAccepted(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()

	stdout, _, err := run(t, "status", "ini-1")
	if err != nil {
		t.Fatalf("a valid reference was refused: %v", err)
	}
	if strings.TrimSpace(stdout) == "" {
		t.Error("scoped status printed nothing")
	}
}

// `idea status` unscoped stays the overview -- every initiative -- because that
// is what its help line says and what the docs describe. Scoped, it prints one.
func TestStatusScopesToItsReference(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedTwoInitiatives()

	all, _, err := run(t, "status", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(all, `"id"`); n != 2 {
		t.Fatalf("bare `idea status --json` returned %d initiatives, want 2 (it is the overview)", n)
	}
	one, _, err := run(t, "status", "ini-2", "--json")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(one, `"id"`); n != 1 {
		t.Fatalf("scoped `idea status --json` returned %d initiatives, want 1", n)
	}
	if !strings.Contains(one, "ini-2") {
		t.Errorf("scoped status returned the wrong initiative: %s", one)
	}
	// Still an ARRAY. docs/cli.md documents `[{"id","title","status"}]` and
	// ledger row G4 pins that sentence; an object here would be an undeclared
	// response-shape change and would break `jq '.[0]'`.
	if !strings.HasPrefix(strings.TrimSpace(one), "[") {
		t.Errorf("scoped `--json` is no longer an array: %s", one)
	}
}

// `idea changed` used inis[0] -- the OLDEST, since ListInitiatives is
// ORDER BY created_at ASC -- which is the direct opposite of the default
// docs/cli.md promises.
func TestChangedActsOnTheNewestInitiative(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedTwoInitiatives()

	if _, _, err := run(t, "changed"); err != nil {
		t.Fatalf("idea changed: %v", err)
	}
	for _, r := range f.requests {
		if strings.Contains(r, "/initiatives/ini-1/changed") {
			t.Fatalf("idea changed acted on the oldest initiative: %v", f.requests)
		}
	}
	if !anyRequest(f, "/initiatives/ini-2/changed") {
		t.Fatalf("idea changed did not act on the newest initiative: %v", f.requests)
	}
}

func anyRequest(f *fakeDaemon, substr string) bool {
	for _, r := range f.requests {
		if strings.Contains(r, substr) {
			return true
		}
	}
	return false
}
