// Package buildinfo reports what build this binary is, for `idea version`, the
// daemon's /api/v1/version endpoint and `idea doctor`.
//
// Before this existed the only answer was a hardcoded const in the daemon, so
// every install said "0.1.0" and a bug report could not name a build.
//
// Three build paths produce three different amounts of information, and the
// stamp says which one it got rather than inventing the rest:
//
//   - `make build` / a release build: -ldflags -X sets Version and Commit.
//   - `go install ./cmd/idea` from a checkout: nothing is stamped, but the Go
//     toolchain records vcs.revision and vcs.modified, so the commit is known
//     and a dirty tree is reported as such.
//   - `go install <module>@<version>` from the proxy: the module version is
//     known (debug.BuildInfo.Main.Version); there is no VCS data at all,
//     because the proxy serves an extracted zip with no .git. That absence is
//     what distinguishes this path — Main.Version is also set for a local build,
//     to a pseudo-version naming a release that does not exist.
//
// An explicit -ldflags stamp wins over the toolchain's, and not only because it
// is more specific: building inside a git worktree, the toolchain reports the
// *main* checkout's HEAD rather than the worktree's, so `make build` (which
// takes the commit from `git rev-parse` in the working directory) is the one
// that names the commit you actually built.
package buildinfo

import (
	"runtime/debug"
	"strings"
)

// Stamped by the linker in release builds:
//
//	go build -ldflags "-X <module>/internal/buildinfo.version=v0.1.0 -X ...commit=abc1234"
var (
	version = ""
	commit  = ""
)

// Info describes the running build.
type Info struct {
	// Version is a release version ("v0.1.0"), a module version from the proxy,
	// or "dev" when neither is known.
	Version string `json:"version"`
	// Commit is the VCS revision when it is known, "" otherwise.
	Commit string `json:"commit,omitempty"`
	// Modified reports a build made from a dirty working tree.
	Modified bool `json:"modified,omitempty"`
}

// String renders the stamp for a human: "v0.1.0 (abc1234)", "dev (abc1234, dirty)".
func (i Info) String() string {
	s := i.Version
	var detail []string
	if i.Commit != "" {
		detail = append(detail, shortCommit(i.Commit))
	}
	if i.Modified {
		detail = append(detail, "dirty")
	}
	if len(detail) > 0 {
		s += " (" + strings.Join(detail, ", ") + ")"
	}
	return s
}

// Get resolves the build stamp, preferring what was set explicitly over what
// the toolchain inferred.
func Get() Info {
	info := Info{Version: version, Commit: commit}

	// An explicitly stamped build knows what it is. Release builds embed the web
	// UI over the committed placeholder before compiling, which flips the
	// toolchain's vcs.modified — so trusting it would make every release binary
	// report "dirty" from a clean CI checkout. The flag is only meaningful when
	// nothing was stamped, i.e. someone built from their own working tree.
	stamped := info.Version != ""

	if bi, ok := debug.ReadBuildInfo(); ok {
		var revision, modified string
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				revision = s.Value
			case "vcs.modified":
				modified = s.Value
			}
		}
		if info.Commit == "" {
			info.Commit = revision
		}
		info.Modified = !stamped && modified == "true"
		info.Version = resolveVersion(info.Version, bi.Main.Version, revision != "")
	}

	if info.Version == "" {
		info.Version = "dev"
	}
	return info
}

// resolveVersion decides what a build calls itself, given the -ldflags stamp,
// the module version the toolchain recorded, and whether the build had VCS data.
//
// It is a separate function because the interesting case cannot be reached from
// a test binary: Go does not VCS-stamp those, so a test exercising Get() takes
// the same branch whether or not the rule is right. A pre-merge check caught
// exactly that — the first test for this fix passed with the fix reverted.
//
// The rule: an explicit stamp wins. Otherwise a module version is trustworthy
// only when there is no VCS data, because the proxy — which serves an extracted
// zip with no .git — is the only path that produces one meaningfully. A build
// from a checkout also carries Main.Version, but it is a *pseudo-version*:
// `go build ./cmd/idea` reported v0.1.1-0.20260904033502-3eff16082659, one patch
// above the only release, a version that never existed and that outranks the
// real one in a bug report. The documented "dev" form had become unreachable.
// The older guard tested for "(devel)", which Go no longer reports here.
func resolveVersion(stampedVersion, moduleVersion string, hasVCS bool) string {
	if stampedVersion != "" {
		return stampedVersion
	}
	if !hasVCS && moduleVersion != "" && moduleVersion != "(devel)" {
		return moduleVersion
	}
	return ""
}

// SameBuild reports whether two stamps name the same binary.
//
// The commit decides whenever both know one, because the version strings do not
// survive the build paths above: `make build` off a tag leaves Version empty
// while `go install` reports `dev`, so identical source produces two different
// version strings. Commit is also the only thing that separates one `dev` from
// another, which is the case this exists for -- a daemon left running across an
// upgrade is executing a different commit under the same word.
//
// Both are normalised to seven characters first: `make build` stamps
// `git rev-parse --short` and the toolchain records the full 40-character
// revision, so a raw comparison would report a mismatch on every
// make-build-then-go-build pair.
//
// Version is the fallback for proxy installs, which carry no VCS data at all.
//
// Modified is deliberately ignored. Two builds from the same commit with
// different uncommitted edits are indistinguishable here, so "I edited it,
// rebuilt, and forgot to restart the daemon" is not caught -- catching it would
// mean warning on every dirty build, which is every build during development.
func SameBuild(a, b Info) bool {
	if a.Commit != "" && b.Commit != "" {
		return shortCommit(a.Commit) == shortCommit(b.Commit)
	}
	return a.Version == b.Version
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}
