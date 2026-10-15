package executor

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/fsdiff"
)

// Workspace handoff for the openhands executor.
//
// The engine's contract with every executor is: the agent writes into an
// isolated local work root, and the engine snapshots it before and after and
// computes the diff itself. A remote agent cannot write into a local
// directory, so the executor carries the work root there and back:
//
//   - Up: the work root is packed as ONE tar.gz of exactly the files the
//     engine's own snapshot sees (fsdiff.Snapshot: regular files only, no
//     symlinks, no .git/.idea-harness, no dependency/cache directories),
//     uploaded with POST /api/file/upload, and unpacked on the server with a
//     single `tar -xzf` through POST /api/bash/execute_bash_command.
//   - Down: GET /api/file/archive?format=tar.gz returns the remote directory
//     as one tarball, which is validated completely in a staging directory
//     before anything in the work root is touched, then mirrored in.
//
// Why a tarball and not the alternatives the API offers:
//
//   - Per-file upload/download is one request per file; a work root is
//     thousands of files, and a crash halfway leaves a half-copied tree.
//   - The archive route's git-delta format needs a git repository on the
//     server and a patch applier here. The work root has no .git (the seed
//     leaves it out deliberately), and the engine computes its own diff
//     anyway -- it needs the files, not someone else's patch of them.
//   - Reading files back through the bash route is out: it returns only the
//     last 1 MiB chunk of a command's output.
//
// A tarball is one request each way, binary-safe, carries deletions by
// absence, and needs nothing on the server but tar -- which the Agent
// Server image has (ASSUMED from its Debian base; the unpack command fails
// loudly, naming the command, if it does not).

const (
	// ohMaxUploadBytes matches the seed's own cap on the work root
	// (workflow.seedMaxTotalBytes): what is uploaded is never more than
	// what was seeded.
	ohMaxUploadBytes = 200 << 20
	// ohMaxUnpackedBytes / ohMaxUnpackedFiles bound what a server can make
	// this machine write: a tarball is untrusted input.
	ohMaxUnpackedBytes = 1 << 30
	ohMaxUnpackedFiles = 100_000
	// ohMaxSingleFile bounds one file fetched on its own.
	ohMaxSingleFile = 64 << 20
)

// packWorkRoot builds the upload: a tar.gz of the work root's snapshot. It
// returns the manifest too -- its digest is the run's base digest, and its
// paths are what "deleted on the server" is judged against on the way back.
func packWorkRoot(root string) ([]byte, fsdiff.Manifest, error) {
	m, err := fsdiff.Snapshot(root)
	if err != nil {
		return nil, nil, fmt.Errorf("openhands: snapshot work root: %w", err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	var total int64
	for _, e := range m {
		p := filepath.Join(root, filepath.FromSlash(e.Path))
		fi, err := os.Lstat(p)
		if err != nil {
			return nil, nil, fmt.Errorf("openhands: pack %s: %w", e.Path, err)
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		total += fi.Size()
		if total > ohMaxUploadBytes {
			return nil, nil, fmt.Errorf("openhands: the work root is larger than %d MiB; refusing to upload it", ohMaxUploadBytes>>20)
		}
		mode := int64(0o644)
		if fi.Mode()&0o111 != 0 {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{Name: e.Path, Mode: mode, Size: fi.Size(), Typeflag: tar.TypeReg, ModTime: fi.ModTime()}); err != nil {
			return nil, nil, err
		}
		f, err := os.Open(p)
		if err != nil {
			return nil, nil, err
		}
		_, err = io.CopyN(tw, f, fi.Size())
		f.Close()
		if err != nil {
			return nil, nil, fmt.Errorf("openhands: pack %s: %w", e.Path, err)
		}
	}
	if err := tw.Close(); err != nil {
		return nil, nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, nil, err
	}
	return buf.Bytes(), m, nil
}

// shellQuote single-quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// unpackCommand is the one command run on the server: replace the run's
// directory with the upload. rm -rf first makes a re-dispatch after a crash
// idempotent: a half-unpacked directory from the first try is discarded.
func unpackCommand(archivePath, dir string) string {
	return fmt.Sprintf("set -e; rm -rf %s; mkdir -p %s; tar -xzf %s -C %s; rm -f %s",
		shellQuote(dir), shellQuote(dir), shellQuote(archivePath), shellQuote(dir), shellQuote(archivePath))
}

// mirrorResult reports what a download did to the work root.
type mirrorResult struct {
	Written int
	Deleted int
	// Skipped are archive entries not written, with why: symlinks, links,
	// devices, control-plane paths. The engine's snapshot would ignore them
	// anyway; they are reported so nothing is dropped silently.
	Skipped []string
}

// archiveManifestName is the metadata file the archive route adds at the
// root of every tarball it serves (CONFIRMED against the v1.54.0 image: JSON
// of the excludes, file count and size). It is not part of the directory, so
// it is never mirrored from the tarball; mirrorInto fetches a file of that
// name on its own, which finds one only if the agent really wrote it.
const archiveManifestName = "archive_manifest.json"

// stripArchivePrefix normalises one entry name from the server's archive.
//
// CONFIRMED against the v1.54.0 image: entries are rooted under the
// directory's base name ("<base>/a/b.go"). A leading "./" (a server rooting
// them at the directory itself) is accepted too.
func stripArchivePrefix(name, base string) string {
	name = strings.TrimPrefix(name, "./")
	if base != "" && (name == base || strings.HasPrefix(name, base+"/")) {
		name = strings.TrimPrefix(strings.TrimPrefix(name, base), "/")
	}
	return name
}

// safeArchivePath cleans an entry name and refuses anything that could land
// outside the work root, or in the harness's control plane. ok=false with a
// reason means "skip it".
func safeArchivePath(name string) (string, string, bool) {
	if name == "" || name == "." {
		return "", "", false
	}
	if strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		return "", "absolute or backslashed path", false
	}
	clean := path.Clean(name)
	if clean == ".." || strings.HasPrefix(clean, "../") || clean != strings.TrimSuffix(name, "/") {
		return "", "path escapes the work root or is not clean", false
	}
	first := strings.SplitN(clean, "/", 2)[0]
	if first == ".git" || first == ".idea-harness" {
		return "", "control-plane path", false
	}
	segs := strings.Split(clean, "/")
	for _, seg := range segs[:len(segs)-1] {
		if fsdiff.DefaultIgnoredDirNames[seg] {
			// A dependency/cache directory: the snapshot ignores it, so it
			// would only cost disk.
			return "", "ignored directory", false
		}
	}
	return clean, "", true
}

// unpackArchive extracts a tar.gz into staging, validating every entry and
// enforcing the size caps. Nothing outside staging is written.
func unpackArchive(data []byte, staging, base string) (files map[string]bool, skipped []string, err error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, nil, fmt.Errorf("openhands: the downloaded archive is not gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	files = map[string]bool{}
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("openhands: read archive: %w", err)
		}
		name := stripArchivePrefix(h.Name, base)
		rel, why, ok := safeArchivePath(name)
		if !ok {
			if why != "" {
				skipped = append(skipped, h.Name+" ("+why+")")
			}
			continue
		}
		if rel == archiveManifestName {
			// The route's own metadata; mirrorInto asks the server whether
			// a real file of that name exists.
			continue
		}
		switch h.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg:
		default:
			skipped = append(skipped, rel+" (not a regular file)")
			continue
		}
		if len(files) >= ohMaxUnpackedFiles {
			return nil, nil, fmt.Errorf("openhands: the downloaded archive has more than %d files; refusing it", ohMaxUnpackedFiles)
		}
		total += h.Size
		if h.Size < 0 || total > ohMaxUnpackedBytes {
			return nil, nil, fmt.Errorf("openhands: the downloaded archive unpacks to more than %d MiB; refusing it", ohMaxUnpackedBytes>>20)
		}
		dst := filepath.Join(staging, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, nil, err
		}
		mode := os.FileMode(0o644)
		if h.Mode&0o111 != 0 {
			mode = 0o755
		}
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_EXCL, mode)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return nil, nil, fmt.Errorf("openhands: the downloaded archive names %s twice", rel)
			}
			return nil, nil, err
		}
		n, err := io.Copy(f, io.LimitReader(tr, h.Size))
		cerr := f.Close()
		if err != nil || cerr != nil || n != h.Size {
			return nil, nil, fmt.Errorf("openhands: unpack %s: short or failed write", rel)
		}
		files[rel] = true
	}
	return files, skipped, nil
}

// mirrorInto makes the work root match the server's directory: every file in
// the archive is written, and every file that was UPLOADED and is not in the
// archive is removed -- unless fetching it on its own finds it (the archive
// route leaves out what its default excludes cover; that is not a deletion).
//
// Only files that were uploaded can be deleted. A file the engine never sent
// (an ignored directory, the agent home) is never touched, so the worst a
// lying server can do here is what an agent can do locally: edit and delete
// files the work root holds, which the diff then shows for review.
func mirrorInto(ctx context.Context, c *ohClient, data []byte, workRoot, remoteDir string, uploaded fsdiff.Manifest) (mirrorResult, error) {
	var res mirrorResult
	staging, err := os.MkdirTemp(filepath.Dir(workRoot), ".openhands-download-*")
	if err != nil {
		return res, fmt.Errorf("openhands: staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	files, skipped, err := unpackArchive(data, staging, path.Base(remoteDir))
	if err != nil {
		return res, err
	}
	res.Skipped = skipped

	// Uploaded files the archive does not carry: deleted, or merely
	// excluded by the archive route? Ask for each. archive_manifest.json is
	// always asked about, uploaded or not, since the tarball's copy of it is
	// the route's metadata, never the file.
	var missing []string
	askedManifest := false
	for _, e := range uploaded {
		if !files[e.Path] {
			missing = append(missing, e.Path)
			askedManifest = askedManifest || e.Path == archiveManifestName
		}
	}
	var manifestBody []byte
	if !askedManifest {
		body, found, err := c.download(ctx, remoteDir+"/"+archiveManifestName, ohMaxSingleFile)
		if err != nil {
			return res, fmt.Errorf("openhands: check %s on the server: %w", archiveManifestName, err)
		}
		if found {
			manifestBody = body
		}
	}
	if manifestBody != nil {
		if err := os.WriteFile(filepath.Join(staging, archiveManifestName), manifestBody, 0o644); err != nil {
			return res, err
		}
		files[archiveManifestName] = true
	}
	sort.Strings(missing)
	var deleted []string
	for _, rel := range missing {
		body, found, err := c.download(ctx, remoteDir+"/"+rel, ohMaxSingleFile)
		if err != nil {
			return res, fmt.Errorf("openhands: check %s on the server: %w", rel, err)
		}
		if !found {
			deleted = append(deleted, rel)
			continue
		}
		dst := filepath.Join(staging, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return res, err
		}
		if err := os.WriteFile(dst, body, 0o644); err != nil {
			return res, err
		}
		files[rel] = true
	}

	// Everything is validated and staged; only now is the work root changed.
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		if err := replaceFile(filepath.Join(staging, filepath.FromSlash(rel)), workRoot, rel); err != nil {
			return res, err
		}
		res.Written++
	}
	for _, rel := range deleted {
		p := filepath.Join(workRoot, filepath.FromSlash(rel))
		if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
			if err := os.Remove(p); err != nil {
				return res, fmt.Errorf("openhands: remove %s: %w", rel, err)
			}
			res.Deleted++
		}
	}
	return res, nil
}

// replaceFile moves a staged file to rel under workRoot, refusing to write
// through a symlink anywhere on the way (a symlink in the work root is the
// one way a write could leave it).
func replaceFile(src, workRoot, rel string) error {
	parts := strings.Split(rel, "/")
	dir := workRoot
	for _, seg := range parts[:len(parts)-1] {
		dir = filepath.Join(dir, seg)
		fi, err := os.Lstat(dir)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(dir, 0o755); err != nil {
				return err
			}
		case err != nil:
			return err
		case fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir():
			return fmt.Errorf("openhands: refusing to write %s: %s is not a plain directory in the work root", rel, dir)
		}
	}
	dst := filepath.Join(workRoot, filepath.FromSlash(rel))
	if fi, err := os.Lstat(dst); err == nil {
		if !fi.Mode().IsRegular() {
			if fi.IsDir() {
				return fmt.Errorf("openhands: refusing to write %s: a directory is in the way", rel)
			}
			if err := os.Remove(dst); err != nil {
				return err
			}
		}
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	fi, err := os.Stat(src)
	if err != nil {
		return err
	}
	// Write-then-rename, so a reader of the work root never sees half a file.
	tmp := dst + ".openhands-tmp"
	if err := os.WriteFile(tmp, data, fi.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}
