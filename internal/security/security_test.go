package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRedactKnownValues(t *testing.T) {
	r := NewRedactor("sk-super-secret-value", "user-password")
	out := r.RedactString("key=sk-super-secret-value and password=user-password")
	if strings.Contains(out, "sk-super-secret-value") || strings.Contains(out, "user-password") {
		t.Fatalf("known values leaked: %q", out)
	}
	if !strings.Contains(out, "[REDACTED]") {
		t.Fatalf("expected redaction marker, got %q", out)
	}
}

func TestRedactPatterns(t *testing.T) {
	cases := []string{
		"AKIA1234567890ABCDEF",
		"ghp_abcdefghijklmnopqrstuvwxyz",
		"sk-abcdefghijklmnopqrstuvwxyz123456",
		"-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.abcdefghijklmnopqrstuvwxyz",
		"token=abcdefghijklmnop",
		"sk_live_4eC39HqLyjWDarjtT1zdp7dc",
		"xoxb-123456789012-123456789012-abcdefghijklmnop",
	}
	for _, c := range cases {
		out := Redact(c)
		if out == c {
			t.Errorf("pattern not redacted: %q", c)
		}
		if FindCredential(c) == "" {
			t.Errorf("credential not detected: %q", c)
		}
	}
}

func TestResolveWithinRejectsEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := ResolveWithin(root, "../etc/passwd"); err != ErrPathEscape {
		t.Fatalf("expected ErrPathEscape, got %v", err)
	}
	ok, err := ResolveWithin(root, "blobs/sha256/abc")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(ok, filepath.Join("sha256", "abc")) {
		t.Fatalf("unexpected path %q", ok)
	}
}

func TestResolveWithinRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if _, err := ResolveWithin(root, "link/secret.txt"); err != ErrPathEscape {
		t.Fatalf("expected ErrPathEscape for symlink escape, got %v", err)
	}
}

func TestSanitizeMarkdown(t *testing.T) {
	in := `<script>alert(1)</script> [x](javascript:alert(1)) <img src="data:text/html,x" onerror="x">`
	out := SanitizeMarkdown(in)
	if strings.Contains(out, "<script") || strings.Contains(out, "javascript:") || strings.Contains(out, "data:") {
		t.Fatalf("unsanitized content leaked: %q", out)
	}
}

func TestTokenLoadCreateAndCompare(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	t1, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	t2, err := LoadOrCreateToken(path)
	if err != nil {
		t.Fatal(err)
	}
	if t1 != t2 {
		t.Fatal("token should be stable across loads")
	}
	if !TokenEqual(t1, t2) {
		t.Fatal("TokenEqual should return true for identical tokens")
	}
	if TokenEqual(t1, t1+"x") {
		t.Fatal("TokenEqual should return false for different tokens")
	}
	// File must be 0600.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("token file perms = %o, want 600", perm)
	}
}
