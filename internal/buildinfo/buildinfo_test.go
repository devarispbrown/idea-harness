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
