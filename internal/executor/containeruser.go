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
// A daemon running as root now runs each container as the owner of the
// project root it works for (the user whose files these are), so a check
// reads a 0600 file in the read-only /repo mount just as that user would,
// and nothing it leaves behind belongs to root or to a stray uid. Only when
// that owner is root itself, or cannot be read, or the container has no
// project (the setup probes) does it fall back to a fixed unprivileged
// ContainerUID:ContainerGID (65532, the "nonroot" user of distroless and
// Chainguard images). A root gid is never passed on: an owner whose group is
// 0 runs with ContainerGID.
//
// The price is ownership of the writable mounts: the work root and agent
// home are written by the root daemon before the run, so they are handed to
// the container user first (grantMounts) and handed back afterwards
// (mountGrant.Restore) -- every path to the owner it had, and anything the
// container created to the daemon -- before the engine reads, diffs or
// deletes them. A daemon killed mid-run cannot hand them back; the next one
// does at startup (RecoverWorkRoots). Non-root daemons are unchanged: their
// uid is already unprivileged, and the files are already theirs.
//
// Refusing to start as root was the alternative, and was rejected (deferred.md)
// because root-only CI and Docker-in-Docker setups would have no way forward.

const (
	ContainerUID = 65532
	ContainerGID = 65532
)

// Test seams: whether the daemon is root, who it is, the chown itself, and
// who owns a path.
var (
	daemonEUID = os.Geteuid
	daemonIDs  = func() (uid, gid int) { return os.Getuid(), os.Getgid() }
	lchown     = os.Lchown
	ownerOf    = statOwner
)

// identity is a container's uid:gid.
type identity struct{ uid, gid int }

func (i identity) String() string { return fmt.Sprintf("%d:%d", i.uid, i.gid) }

var unprivilegedIdentity = identity{ContainerUID, ContainerGID}

func statOwner(path string) (uid, gid int, err error) {
	fi, err := os.Stat(path)
	if err != nil {
		return 0, 0, err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, fmt.Errorf("executor: no owner for %s", path)
	}
	return int(st.Uid), int(st.Gid), nil
}

// containerIdentity is who a container working for the project at
// projectRoot runs as. A non-root daemon: itself. A root daemon: the owner of
// projectRoot, or 65532 when that is root, unreadable, or there is no
// project.
func containerIdentity(projectRoot string) identity {
	if daemonEUID() != 0 {
		return identity{os.Getuid(), os.Getgid()}
	}
	if projectRoot == "" {
		return unprivilegedIdentity
	}
	uid, gid, err := ownerOf(projectRoot)
	if err != nil || uid == 0 {
		return unprivilegedIdentity
	}
	if gid == 0 {
		gid = ContainerGID
	}
	return identity{uid, gid}
}

// containerUser is the --user value for a container working for the project
// at projectRoot ("" for none).
func containerUser(projectRoot string) string { return containerIdentity(projectRoot).String() }

// remapsContainerUser reports whether containers run as someone other than
// the daemon, i.e. whether mounts need handing over.
func remapsContainerUser() bool { return daemonEUID() == 0 }

// mountGrant records the ownership grantMounts changed, so Restore can put it
// back. The zero value (and nil) restores nothing.
type mountGrant struct {
	to     identity
	roots  []string
	owners map[string][2]int
}

// grantMounts hands every path under each dir to the container user to, when
// the daemon is root, recording what it found. A missing dir is skipped.
// Symlinks are chowned themselves, never followed (Lchown, WalkDir), so a
// link in a seeded work root cannot hand a host file to the container.
func grantMounts(to identity, dirs ...string) (*mountGrant, error) {
	g := &mountGrant{to: to, owners: map[string][2]int{}}
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
			return lchown(path, to.uid, to.gid)
		})
		if err != nil {
			// Hand back what was already handed over before failing.
			_ = g.Restore()
			return nil, fmt.Errorf("executor: hand %s to the container user %s: %w", dir, to, err)
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
	uid, gid := daemonIDs()
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
// to the grant's container user, with its new parent directories up to (not
// including) root: the ACP client writes files on the agent's behalf, and
// the agent must be able to edit them afterwards from inside its container.
// A nil grant, or a non-root daemon, hands over nothing.
func (g *mountGrant) handOver(root, path string) error {
	if g == nil || !remapsContainerUser() {
		return nil
	}
	root = filepath.Clean(root)
	for p := filepath.Clean(path); p != root && strings.HasPrefix(p, root+string(filepath.Separator)); p = filepath.Dir(p) {
		if err := lchown(p, g.to.uid, g.to.gid); err != nil {
			return err
		}
	}
	return nil
}

// RecoverWorkRoots hands back what a root daemon killed mid-run left in a
// container user's hands, and touches nothing outside stateRoot's own work
// directories:
//
//   - an initiative work root (projects/<id>/work/<initiative>) is kept -- it
//     is the next attempt's base until re-seeded, and a stopped run's partial
//     files are read from it -- so every path in it not owned by the daemon
//     is chowned back to the daemon, as Restore would have done;
//   - a quality-dimension copy (projects/<id>/quality-*) and a metric
//     command's scratch root (projects/<id>/metric-*) are disposable temp
//     roots, so they are removed. Metric commands wait for the startup sweep
//     before they start, so none of this daemon's can be caught here.
//
// Agent homes (work/.agent-home-*) are SweepAgentLeftovers' to remove. A
// symlink is never followed: one at the top of a root is skipped, one inside
// it is chowned as a link. Call it only once no container of an earlier
// daemon can still be running -- a live one could swap a directory for a
// symlink mid-walk -- i.e. after SweepAgentLeftovers reported every leftover
// container gone. A non-root daemon does nothing: it never handed anything
// over. Best effort; the first error is returned after every root was tried.
func RecoverWorkRoots(stateRoot string) (recovered int, err error) {
	if !remapsContainerUser() || stateRoot == "" {
		return 0, nil
	}
	uid, gid := daemonIDs()
	note := func(e error) {
		if e != nil && err == nil {
			err = e
		}
	}
	projects, _ := filepath.Glob(filepath.Join(stateRoot, "projects", "*"))
	for _, proj := range projects {
		if !isRealDir(proj) {
			continue
		}
		if work := filepath.Join(proj, "work"); isRealDir(work) {
			entries, e := os.ReadDir(work)
			note(e)
			for _, ent := range entries {
				root := filepath.Join(work, ent.Name())
				if strings.HasPrefix(ent.Name(), agentHomePrefix) || !isRealDir(root) {
					continue
				}
				changed := false
				note(filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
					if werr != nil {
						note(werr)
						if d != nil && d.IsDir() {
							return fs.SkipDir
						}
						return nil
					}
					info, ierr := d.Info()
					if ierr != nil {
						note(ierr)
						return nil
					}
					st, ok := info.Sys().(*syscall.Stat_t)
					if !ok || (int(st.Uid) == uid && int(st.Gid) == gid) {
						return nil
					}
					changed = true
					note(lchown(path, uid, gid))
					return nil
				}))
				if changed {
					recovered++
				}
			}
		}
		var temps []string
		for _, pattern := range []string{"quality-*", "metric-*"} {
			m, _ := filepath.Glob(filepath.Join(proj, pattern))
			temps = append(temps, m...)
		}
		for _, tmp := range temps {
			if !isRealDir(tmp) {
				continue
			}
			note(removeAgentHome(tmp))
			recovered++
		}
	}
	return recovered, err
}

// isRealDir reports whether path is a directory and not a symlink to one.
func isRealDir(path string) bool {
	fi, err := os.Lstat(path)
	return err == nil && fi.IsDir()
}
