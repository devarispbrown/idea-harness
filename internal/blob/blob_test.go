package blob

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestPutGetRoundTrip(t *testing.T) {
	s, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	content := []byte("hello idea-harness")
	d, err := s.Put(content)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(d)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("round-trip mismatch: got %q", got)
	}
}

func TestPutDeduplicates(t *testing.T) {
	s, _ := New(t.TempDir(), nil)
	content := []byte("dedupe me")
	d1, _ := s.Put(content)
	d2, _ := s.Put(content)
	if d1 != d2 {
		t.Fatalf("expected identical digests, got %s and %s", d1, d2)
	}
	// Only one file should exist for this content.
	entries, _ := os.ReadDir(s.root + "/sha256")
	if len(entries) != 1 {
		t.Fatalf("expected 1 blob, found %d", len(entries))
	}
}

func TestRedactionBeforeDigest(t *testing.T) {
	var redacted []byte
	s, _ := New(t.TempDir(), func(b []byte) ([]byte, error) {
		redacted = b
		return []byte("REDACTED"), nil
	})
	_, err := s.Put([]byte("secret-value"))
	if err != nil {
		t.Fatal(err)
	}
	// The raw content must have passed through the redactor before storage.
	if string(redacted) != "secret-value" {
		t.Fatalf("redactor not invoked with raw content: %q", redacted)
	}
	// Stored content is the redacted form, so Get returns "REDACTED".
	got, _ := s.Get(Digest([]byte("REDACTED")))
	if string(got) != "REDACTED" {
		t.Fatalf("expected redacted content to be stored, got %q", got)
	}
}

func TestGetVerifiesDigest(t *testing.T) {
	s, _ := New(t.TempDir(), nil)
	d, _ := s.Put([]byte("original"))
	// Corrupt the file on disk.
	path := s.root + "/sha256/" + d
	if err := os.WriteFile(path, []byte("tampered!"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(d); err != ErrDigestMismatch {
		t.Fatalf("expected ErrDigestMismatch, got %v", err)
	}
}

func TestExistsAndInvalidDigest(t *testing.T) {
	s, _ := New(t.TempDir(), nil)
	d, _ := s.Put([]byte("x"))
	ok, _ := s.Exists(d)
	if !ok {
		t.Fatal("expected blob to exist")
	}
	if _, err := s.Get("not-a-digest"); err == nil {
		t.Fatal("expected error for invalid digest")
	}
	if _, err := s.Get(strings.Repeat("a", 64)); err == nil {
		t.Fatal("expected error for missing digest")
	}
}
