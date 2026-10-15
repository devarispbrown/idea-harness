package executor

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Who a container runs as (docs/security-model.md "Execution gateway").
//
// Every `docker run` here passes --user, and it used to be the daemon's own
// uid:gid -- chosen so that what an agent writes into the bind-mounted work
// root is owned by, and editable by, the daemon afterwards. A root daemon
// therefore ran root containers: capability-dropped, read-only and
// no-new-privileges, but uid 0, which is root on the host's files for any
// mount and for any container escape (deferred.md, audit 3 / S6).
//
// Now a daemon running as root runs its containers as a fixed unprivileged
// user, ContainerUID:ContainerGID (65532, the "nonroot" user of distroless
// and Chainguard images; nothing on a normal host owns it). The price is
// ownership of the writable mounts: the work root and agent home are
// written by the root daemon before the run, so they are handed to 65532
// first (grantMounts) and handed back afterwards (mountGrant.Restore) --
// every path to the owner it had, and anything the container created to the
// daemon -- before the engine reads, diffs or deletes them. Non-root daemons
// are unchanged: their uid is already unprivileged, and the files are already
// theirs.
//
// Refusing to start as root was the alternative, and was rejected (deferred.md)
// because root-only CI and Docker-in-Docker setups would have no way forward.
//
// What it does not cover: a read-only /repo mount (deterministic checks,
// metric commands) is the user's real workspace and is never chowned, so as
// 65532 a check reads it only where its "other" permission bits allow. A
// 0600 file in the repo is unreadable to the check; a 0644 one is fine.

const (
	ContainerUID = 65532
	ContainerGID = 65532
)

// Test seams: whether the daemon is root, and the chown itself.
var (
	daemonEUID = os.Geteuid
	lchown     = os.Lchown
)

// containerUser is the --user value for every container this package starts.
func containerUser() string {
	if daemonEUID() == 0 {
		return fmt.Sprintf("%d:%d", ContainerUID, ContainerGID)
	}
	return fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
}

// remapsContainerUser reports whether containers run as someone other than
// the daemon, i.e. whether mounts need handing over.
func remapsContainerUser() bool { return daemonEUID() == 0 }

// mountGrant records the ownership grantMounts changed, so Restore can put it
// back. The zero value (and nil) restores nothing.
type mountGrant struct {
	roots  []string
	owners map[string][2]int
}

// grantMounts hands every path under each dir to the container user, when
// the daemon is root, recording what it found. A missing dir is skipped.
// Symlinks are chowned themselves, never followed (Lchown, WalkDir), so a
// link in a seeded work root cannot hand a host file to the container.
func grantMounts(dirs ...string) (*mountGrant, error) {
	g := &mountGrant{owners: map[string][2]int{}}
	if !remapsContainerUser() {
		return g, nil
	}
	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		g.roots = append(g.roots, dir)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if st, ok := info.Sys().(*syscall.Stat_t); ok {
				g.owners[path] = [2]int{int(st.Uid), int(st.Gid)}
			}
			return lchown(path, ContainerUID, ContainerGID)
		})
		if err != nil {
			// Hand back what was already handed over before failing.
			_ = g.Restore()
			return nil, fmt.Errorf("executor: hand %s to the container user %d:%d: %w", dir, ContainerUID, ContainerGID, err)
		}
	}
	return g, nil
}

// Restore hands every path under the granted dirs back: to the owner it had
// before the run, or -- for anything the container created -- to the daemon.
// Call it only once the container is gone; the walk does not follow symlinks
// and chowns them as links, so nothing the container left behind can point
// it at a file outside the mount. Best effort over the whole tree: the first
// error is returned after every path was tried.
func (g *mountGrant) Restore() error {
	if g == nil || len(g.roots) == 0 {
		return nil
	}
	uid, gid := os.Getuid(), os.Getgid()
	var first error
	for _, root := range g.roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if first == nil {
					first = err
				}
				if d != nil && d.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			o, ok := g.owners[path]
			if !ok {
				o = [2]int{uid, gid}
			}
			if err := lchown(path, o[0], o[1]); err != nil && first == nil {
				first = err
			}
			return nil
		})
		if err != nil && first == nil {
			first = err
		}
	}
	g.roots = nil
	return first
}

// handOver gives one path the daemon just created inside a granted work root
// to the container user, with its new parent directories up to (not
// including) root: the ACP client writes files on the agent's behalf, and
// the agent must be able to edit them afterwards from inside its container.
func handOver(root, path string) error {
	if !remapsContainerUser() {
		return nil
	}
	root = filepath.Clean(root)
	for p := filepath.Clean(path); p != root && strings.HasPrefix(p, root+string(filepath.Separator)); p = filepath.Dir(p) {
		if err := lchown(p, ContainerUID, ContainerGID); err != nil {
			return err
		}
	}
	return nil
}
