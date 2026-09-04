package buildinfo

import "testing"

func TestInfoString(t *testing.T) {
	cases := []struct {
		name string
		info Info
		want string
	}{
		{"release", Info{Version: "v0.1.0", Commit: "16088ab1234567"}, "v0.1.0 (16088ab)"},
		{"local dirty tree", Info{Version: "dev", Commit: "abc1234", Modified: true}, "dev (abc1234, dirty)"},
		{"module proxy, no vcs data", Info{Version: "v0.1.0"}, "v0.1.0"},
		{"nothing known", Info{Version: "dev"}, "dev"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.info.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A release binary is built in CI from a clean checkout, but the release
// pipeline embeds the web UI over the committed placeholder before compiling,
// so the toolchain reports vcs.modified=true. Reporting that as "dirty" told
// every user of an official binary that it was built from uncommitted source.
// An explicit -ldflags stamp is the signal that the build was controlled.
func TestStampedBuildIgnoresTheToolchainsModifiedFlag(t *testing.T) {
	// Get() reads package-level vars set by the linker; set them the way
	// -ldflags -X would and confirm the rule Get() applies.
	t.Cleanup(func() { version, commit = "", "" })
	version, commit = "v0.1.0", "16088ab"

	got := Get()
	if got.Version != "v0.1.0" {
		t.Fatalf("Version = %q, want the stamped v0.1.0", got.Version)
	}
	if got.Modified {
		t.Error("a stamped release build must not report itself as dirty")
	}

	// Without a stamp, the toolchain's view is all there is, so it is honored:
	// a developer building from a modified tree should see it.
	version, commit = "", ""
	unstamped := Get()
	if unstamped.Version != "dev" {
		t.Errorf("unstamped Version = %q, want dev", unstamped.Version)
	}
}

// TestResolveVersion pins audit 5's P2-5.
//
// A local `go build ./cmd/idea` reported
// `v0.1.1-0.20260904033502-3eff16082659` — Go's module pseudo-version for the
// checkout. One patch above the only release, never existed, and it outranks
// the real version in any bug report, while the `dev` form the README documents
// had become unreachable.
//
// The decision is tested directly rather than through Get(), because Go does not
// VCS-stamp test binaries: a test that called Get() took the same branch with
// the fix reverted, and passed either way.
func TestResolveVersion(t *testing.T) {
	const pseudo = "v0.1.1-0.20260904033502-3eff16082659"

	for _, c := range []struct {
		name                   string
		stamped, moduleVersion string
		hasVCS                 bool
		want                   string
	}{
		{
			name:    "release build: the ldflags stamp wins",
			stamped: "v0.2.0", moduleVersion: pseudo, hasVCS: true, want: "v0.2.0",
		},
		{
			name:          "local build from a checkout must not adopt the pseudo-version",
			moduleVersion: pseudo, hasVCS: true, want: "",
		},
		{
			name:          "older toolchains reported (devel) for the same case",
			moduleVersion: "(devel)", hasVCS: true, want: "",
		},
		{
			name:          "proxy install: no VCS data, so the module version is the truth",
			moduleVersion: "v0.2.0", hasVCS: false, want: "v0.2.0",
		},
		{
			name:          "proxy install of a pseudo-version is still a real answer",
			moduleVersion: pseudo, hasVCS: false, want: pseudo,
		},
		{
			name:   "nothing known",
			hasVCS: true, want: "",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveVersion(c.stamped, c.moduleVersion, c.hasVCS); got != c.want {
				t.Errorf("resolveVersion(%q, %q, %v) = %q, want %q",
					c.stamped, c.moduleVersion, c.hasVCS, got, c.want)
			}
		})
	}
}

// TestLocalBuildDoesNotClaimAVersion pins audit 5's P2-5.
//
// A local `go build ./cmd/idea` reported
// `v0.1.1-0.20260904033502-3eff16082659` — Go's module pseudo-version for the
// checkout. That is one patch above the only release, has never existed, and
// outranks the real version in any bug report, while the `dev` form the README
// documents had become unreachable. The old guard tested for "(devel)", which
// Go no longer reports here.
//
// This asserts the discriminator rather than the output: the proxy path is the
// one with no VCS data, so a build that HAS VCS data must never take its
// version from Main.Version.
func TestLocalBuildDoesNotClaimAVersion(t *testing.T) {
	got := Get()
	if got.Version == "" {
		t.Fatal("Version is empty")
	}
	// This test binary is built from the checkout, so it has VCS data and no
	// -ldflags stamp: the only correct answer is "dev".
	if got.Version != "dev" {
		t.Errorf("a build from the working tree reports version %q; it must report \"dev\" "+
			"rather than a pseudo-version naming a release that does not exist", got.Version)
	}
	// No commit assertion: Go does not VCS-stamp test binaries, so it is empty
	// here and asserting on it would be a test that depends on how it was built
	// rather than on the fix.
}
