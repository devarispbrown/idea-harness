// Package blob implements the content-addressed BlobStore (§31). Content is
// stored by SHA-256 digest under blobs/sha256/<digest>. An optional redactor is
// applied before digest computation so that secrets never reach the store (§47).
package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrDigestMismatch is returned when stored content does not match its digest.
var ErrDigestMismatch = errors.New("blob: content does not match digest")

// Redactor transforms content before it is persisted. It is the security
// choke point for redaction (§48).
type Redactor func(content []byte) ([]byte, error)

// Store is a content-addressed blob store rooted at a directory.
type Store struct {
	root    string
	redact  Redactor
}

// New creates a Store rooted at dir (which is created if absent).
func New(root string, redact Redactor) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "sha256"), 0o700); err != nil {
		return nil, fmt.Errorf("blob: mkdir: %w", err)
	}
	return &Store{root: root, redact: redact}, nil
}

// Digest returns the lowercase hex SHA-256 of content.
func Digest(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// Put stores content (post-redaction) and returns its digest. Writes are
// atomic: content is written to a temp file and renamed into place.
func (s *Store) Put(content []byte) (string, error) {
	if s.redact != nil {
		var err error
		content, err = s.redact(content)
		if err != nil {
			return "", fmt.Errorf("blob: redact: %w", err)
		}
	}

	digest := Digest(content)
	finalPath := filepath.Join(s.root, "sha256", digest)

	// Idempotent: if the blob already exists with the same content, we're done.
	if existing, err := os.ReadFile(finalPath); err == nil {
		if Digest(existing) == digest {
			return digest, nil
		}
		return "", ErrDigestMismatch
	}

	if err := os.MkdirAll(filepath.Join(s.root, "tmp"), 0o700); err != nil {
		return "", fmt.Errorf("blob: mkdir tmp: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Join(s.root, "tmp"), "blob-*")
	if err != nil {
		return "", fmt.Errorf("blob: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return "", fmt.Errorf("blob: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", fmt.Errorf("blob: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("blob: close: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return "", fmt.Errorf("blob: chmod: %w", err)
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		// Another writer may have won the race; verify their result.
		if existing, rerr := os.ReadFile(finalPath); rerr == nil && Digest(existing) == digest {
			return digest, nil
		}
		return "", fmt.Errorf("blob: rename: %w", err)
	}
	return digest, nil
}

// Get reads and verifies content for a digest.
func (s *Store) Get(digest string) ([]byte, error) {
	if !validDigest(digest) {
		return nil, fmt.Errorf("blob: invalid digest %q", digest)
	}
	content, err := os.ReadFile(filepath.Join(s.root, "sha256", digest))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, os.ErrNotExist
		}
		return nil, fmt.Errorf("blob: read: %w", err)
	}
	if Digest(content) != digest {
		return nil, ErrDigestMismatch
	}
	return content, nil
}

// Exists reports whether a blob with the given digest is present.
func (s *Store) Exists(digest string) (bool, error) {
	if !validDigest(digest) {
		return false, fmt.Errorf("blob: invalid digest %q", digest)
	}
	_, err := os.Stat(filepath.Join(s.root, "sha256", digest))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, fmt.Errorf("blob: stat: %w", err)
}

// WriteTo streams blob content to w after verification.
func (s *Store) WriteTo(digest string, w io.Writer) (int64, error) {
	content, err := s.Get(digest)
	if err != nil {
		return 0, err
	}
	n, err := w.Write(content)
	return int64(n), err
}

func validDigest(digest string) bool {
	if len(digest) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}
