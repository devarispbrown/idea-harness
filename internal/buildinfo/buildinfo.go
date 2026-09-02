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
//     because the proxy serves an extracted zip with no .git.
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

	bi, ok := debug.ReadBuildInfo()
	if ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if info.Commit == "" {
					info.Commit = s.Value
				}
			case "vcs.modified":
				info.Modified = s.Value == "true"
			}
		}
		// The proxy path stamps a module version and nothing else. "(devel)" is
		// what the toolchain reports for a local build, which is not a version.
		if info.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			info.Version = bi.Main.Version
		}
	}

	if info.Version == "" {
		info.Version = "dev"
	}
	return info
}

func shortCommit(c string) string {
	if len(c) > 7 {
		return c[:7]
	}
	return c
}
