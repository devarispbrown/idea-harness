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
		"token=k3jQ9zPmT7xR2wLn",
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

func TestContainsCredentialKnownValueAndPattern(t *testing.T) {
	const knownOnly = "Xq7kP3mN9vR2tW8sY5uL" // matches no pattern
	r := NewRedactor(knownOnly)
	if !r.ContainsCredential("here is " + knownOnly + " in text") {
		t.Fatal("known value not detected")
	}
	if !r.ContainsCredential("sk_live_4eC39HqLyjWDarjtT1zdp7dc") {
		t.Fatal("sk_live_ pattern not detected")
	}
	if !r.ContainsCredential("AIzaSySuperSecretGoogleKey0123456789") {
		t.Fatal("AIza pattern not detected")
	}
	if r.ContainsCredential("nothing sensitive here") {
		t.Fatal("false positive")
	}
	// Nil redactor falls back to patterns only.
	var nilR *Redactor
	if !nilR.ContainsCredential("AKIA1234567890ABCDEF") {
		t.Fatal("nil redactor should still detect patterns")
	}
	if nilR.ContainsCredential(knownOnly) {
		t.Fatal("nil redactor cannot detect a value that only exists as a known value")
	}
}

// TestFingerprintWithKeyDiffersPerProject is review H3: the same secret value
// must fingerprint differently under different per-project keys, so a
// fingerprint is not the same, precomputable value across every installation
// that ever saw the same credential (the bare sha256 this replaces was).
// Mutation-check: make FingerprintWithKey ignore its key argument (e.g.
// `return Fingerprint(value)`), and this fails.
func TestFingerprintWithKeyDiffersPerProject(t *testing.T) {
	const value = "sk-ant-api03-some-secret-value-0123456789"
	keyA := []byte("project-a-key-0123456789abcdef0")
	keyB := []byte("project-b-key-0123456789abcdef0")

	fpA := FingerprintWithKey(keyA, value)
	fpB := FingerprintWithKey(keyB, value)
	fpNone := FingerprintWithKey(nil, value)

	if fpA == fpB {
		t.Fatalf("FingerprintWithKey(keyA, ...) == FingerprintWithKey(keyB, ...) = %q, want different fingerprints per key", fpA)
	}
	if fpA == fpNone || fpB == fpNone {
		t.Fatal("a keyed fingerprint must differ from the unkeyed (nil-key) fingerprint")
	}
	// Deterministic: the same key+value always fingerprints the same way, so
	// the allowlist can recognize a repeat.
	if got := FingerprintWithKey(keyA, value); got != fpA {
		t.Fatalf("FingerprintWithKey(keyA, ...) = %q, want the same value again: %q", got, fpA)
	}

	r := NewRedactorWithKey(keyA)
	if got := r.fingerprint(value); got != fpA {
		t.Fatalf("Redactor.fingerprint with keyA = %q, want %q (FingerprintWithKey(keyA, ...))", got, fpA)
	}
}

// TestFindPatternsInFileHonorsRedactorKey checks the actual sweep path (a
// Redactor built with NewRedactorWithKey) produces per-project fingerprints
// for pattern matches too, not just FindKnownInFile's known-value matches.
func TestFindPatternsInFileHonorsRedactorKey(t *testing.T) {
	content := "const key = \"AKIA1234567890ABCDEF\"\n"
	r1 := NewRedactorWithKey([]byte("key-one-0123456789abcdef01234567"))
	r2 := NewRedactorWithKey([]byte("key-two-0123456789abcdef01234567"))

	m1 := r1.FindPatternsInFile("a.go", content)
	m2 := r2.FindPatternsInFile("a.go", content)
	if len(m1) == 0 || len(m2) == 0 {
		t.Fatal("expected at least one match from both redactors")
	}
	if m1[0].Fingerprint == m2[0].Fingerprint {
		t.Fatalf("pattern match fingerprint did not vary by Redactor key: %q", m1[0].Fingerprint)
	}
}

// TestMaskedPreviewNeverCarriesTheRawValue is review H3: a finding's preview
// must show only the first/last 4 characters of a 24+ character value (or be
// fully masked for a short value -- see TestMaskValueScalesRevealForShortSecrets
// for maskValue's 16-23 character tier), and the masked line must not
// contain the raw value anywhere. Uses a 30-character value (GitHub PAT
// shape, no length cap unlike AKIA's fixed 20) so it lands in maskValue's
// min(4, len/4) tier rather than the shorter LOW-fix tier below it.
func TestMaskedPreviewNeverCarriesTheRawValue(t *testing.T) {
	const value = "ghp_1234567890ABCDEFGHIJKLMNOP"
	content := "x = \"" + value + "\" # do not commit\n"
	matches := FindCredentialsInFile("a.env", content)
	if len(matches) == 0 {
		t.Fatal("expected a match")
	}
	m := matches[0]
	if strings.Contains(m.Preview, value) {
		t.Fatalf("Preview leaks the raw value: %q", m.Preview)
	}
	if strings.Contains(m.MaskedLine, value) {
		t.Fatalf("MaskedLine leaks the raw value: %q", m.MaskedLine)
	}
	if !strings.HasPrefix(m.Preview, value[:4]) || !strings.HasSuffix(m.Preview, value[len(value)-4:]) {
		t.Fatalf("Preview = %q, want it to start/end with the value's first/last 4 characters", m.Preview)
	}
	if !strings.Contains(m.MaskedLine, m.Preview) {
		t.Fatalf("MaskedLine = %q, want it to contain the masked Preview %q", m.MaskedLine, m.Preview)
	}
}

// TestMaskValueMasksShortValuesEntirely is review H1: a value under 16
// characters reveals nothing at all -- the old floor of 8 revealed 4+4 of a
// 9-character value (`abcdefghi` -> `abcd…fghi`, 8 of 9 characters back in
// the clear, and short passwords are exactly the common case). This covers
// the whole boundary up to and including 15 characters, not just up to the
// old (broken) floor of 8.
func TestMaskValueMasksShortValuesEntirely(t *testing.T) {
	for _, v := range []string{
		"a",
		"abcdefgh",        // 8: the old floor
		"hunter2345",      // 10: review's own example (`hunt…2345` used to leak)
		"abcdefghi",       // 9: the boundary where the old leak started
		"abcdefghijklmno", // 15: one under the new floor
	} {
		got := maskValue(v)
		if got != strings.Repeat("*", len(v)) {
			t.Errorf("maskValue(%q) = %q, want fully masked (len %d < 16)", v, got, len(v))
		}
	}
}

// TestMaskValueRevealsAtMostQuarterPerSide is review H1's fix, as scaled down
// by review LOW: for a value of maskValueLongThreshold (24)+ characters, at
// most min(4, len/4) characters are revealed on each side -- never more,
// however long the value.
func TestMaskValueRevealsAtMostQuarterPerSide(t *testing.T) {
	cases := []struct {
		value      string
		wantReveal int
	}{
		{strings.Repeat("a", 24), 4}, // 24/4 == 6, capped at 4
		{strings.Repeat("a", 100), 4},
	}
	for _, tc := range cases {
		got := maskValue(tc.value)
		wantPrefix := tc.value[:tc.wantReveal]
		wantSuffix := tc.value[len(tc.value)-tc.wantReveal:]
		if !strings.HasPrefix(got, wantPrefix) || !strings.HasSuffix(got, wantSuffix) {
			t.Fatalf("maskValue(%d chars) = %q, want exactly %d revealed per side", len(tc.value), got, tc.wantReveal)
		}
		// Exactly wantReveal on each side plus the ellipsis: no more of the
		// value's own characters are exposed than that (e.g. never the whole
		// value, however long).
		middle := strings.TrimPrefix(strings.TrimSuffix(got, wantSuffix), wantPrefix)
		if strings.ContainsAny(middle, tc.value) {
			t.Fatalf("maskValue(%d chars) = %q, revealed more than %d characters per side", len(tc.value), got, tc.wantReveal)
		}
	}
}

// TestMaskValueScalesRevealForShortSecrets is review LOW: maskValue used to
// reveal min(4, len/4) characters per side starting at 16 characters, which
// gave a 16-character secret 50% of itself back in the clear
// (`Hq7zR2mVx9LpQwEr` -> `Hq7z…QwEr`, persisted in SecretPreview on every
// finding and rendered in the UI). Between 16 and maskValueLongThreshold-1
// (24) characters, at most maskValueShortRevealLen (1) character is now
// revealed per side; the fuller min(4, len/4) reveal only starts at
// maskValueLongThreshold.
func TestMaskValueScalesRevealForShortSecrets(t *testing.T) {
	cases := []struct {
		value      string
		wantReveal int
	}{
		{"Hq7zR2mVx9LpQwEr", 1},      // 16 chars: the review's own example
		{strings.Repeat("a", 23), 1}, // just under the long threshold
		{strings.Repeat("a", 24), 4}, // at the long threshold: fuller reveal begins
	}
	for _, tc := range cases {
		got := maskValue(tc.value)
		wantPrefix := tc.value[:tc.wantReveal]
		wantSuffix := tc.value[len(tc.value)-tc.wantReveal:]
		if !strings.HasPrefix(got, wantPrefix) || !strings.HasSuffix(got, wantSuffix) {
			t.Fatalf("maskValue(%d chars) = %q, want exactly %d revealed per side", len(tc.value), got, tc.wantReveal)
		}
	}
	// The review's own example: at most 2 characters total back in the
	// clear, never 8 (50%).
	got := maskValue("Hq7zR2mVx9LpQwEr")
	revealed := len(got) - len("…") // maskValue always inserts exactly one "…"
	if revealed > 2 {
		t.Fatalf("maskValue(%q) = %q, revealed %d characters, want at most 2", "Hq7zR2mVx9LpQwEr", got, revealed)
	}
}

// TestMaskedLineMasksEveryMatchOnTheLine is review H1: MaskedLine used to
// mask only the finding's own value, leaving a second secret on the same
// line in the clear. Probing with
// `DB_PASSWORD=hunter2345 API_TOKEN=kX9mPqT4wZ2vLf8N` used to give
// `DB_PASSWORD=hunter2345 API_TOKEN=zzzz…qqqq` -- the first secret intact.
func TestMaskedLineMasksEveryMatchOnTheLine(t *testing.T) {
	content := "DB_PASSWORD=hunter2345 API_TOKEN=kX9mPqT4wZ2vLf8N\n"
	matches := FindCredentialsInFile("a.env", content)
	if len(matches) < 2 {
		t.Fatalf("expected at least 2 matches on the line, got %d: %+v", len(matches), matches)
	}
	for _, m := range matches {
		if strings.Contains(m.MaskedLine, "hunter2345") {
			t.Fatalf("MaskedLine still leaks the first secret: %q", m.MaskedLine)
		}
		if strings.Contains(m.MaskedLine, "kX9mPqT4wZ2vLf8N") {
			t.Fatalf("MaskedLine still leaks the second secret: %q", m.MaskedLine)
		}
	}
}
