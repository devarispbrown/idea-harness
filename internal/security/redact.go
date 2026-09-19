// Package security enforces security outside the LLM (§43): redaction, path
// guards, HTML sanitization, trust classification, and the local-API token.
package security

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
)

// credentialPattern is one entry in the redaction/detection catalog: a regexp
// and the human-readable kind of secret it matches (§ D2 secret sweep). Each
// regexp carries exactly one capturing group around the secret VALUE itself
// (not any vendor prefix), so a match's fingerprint (see Fingerprint) is
// computed over the value alone and stays stable across quoting and
// surrounding whitespace (review changes #1). A pattern with no meaningful
// prefix (a PEM block, a JWT) wraps the whole match in that group.
type credentialPattern struct {
	re   *regexp.Regexp
	kind string
}

// credentialCatalog is the redaction and detection catalog (§48). Order
// matters only for readability; all matches are replaced with [REDACTED].
var credentialCatalog = []credentialPattern{
	// PEM private keys (multiline).
	{regexp.MustCompile(`(?s)(-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----)`), "PEM private key"},
	// JWTs (three base64url segments).
	{regexp.MustCompile(`\b(eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})\b`), "JWT"},
	// AWS access key id.
	{regexp.MustCompile(`\b(AKIA[0-9A-Z]{16})\b`), "AWS access key ID"},
	// GitHub personal access token (classic + fine-grained) and OAuth tokens.
	{regexp.MustCompile(`\b(ghp_[A-Za-z0-9]{20,})\b`), "GitHub personal access token"},
	{regexp.MustCompile(`\b(github_pat_[A-Za-z0-9_]{20,})\b`), "GitHub fine-grained personal access token"},
	{regexp.MustCompile(`\b(gh[ousr]_[A-Za-z0-9]{20,})\b`), "GitHub token"},
	// GitLab personal access token.
	{regexp.MustCompile(`\b(glpat-[A-Za-z0-9_-]{20,})\b`), "GitLab personal access token"},
	// Google API key.
	{regexp.MustCompile(`\b(AIza[0-9A-Za-z_-]{30,})\b`), "Google API key"},
	// npm access token.
	{regexp.MustCompile(`\b(npm_[A-Za-z0-9]{30,})\b`), "npm access token"},
	// Anthropic, OpenAI and other `sk-` keys.
	//
	// Shaped as "a few short vendor segments, then a long alphanumeric run",
	// because every current key format puts hyphens inside the first characters
	// -- sk-ant-api03-…, sk-proj-…, sk-svcacct-… -- while the secret itself is
	// one unbroken run.
	//
	// The original pattern required 20+ contiguous alphanumerics immediately
	// after "sk-", so it matched only the legacy flat OpenAI shape and silently
	// passed every modern key, Anthropic's included -- the credential forwarded
	// into the sandboxed agent container.
	//
	// Simply widening the class to admit "-" went too far the other way: it
	// matched any 20+ character hyphenated string starting "sk-", so an ordinary
	// URL slug like sk-decomposition-of-matrices-and-linear-algebra was redacted,
	// and ContainsCredential rejected the whole document upload containing it.
	// Narrowing it to a hyphen-group prefix and an alphanumeric body went too
	// far the other way, and less visibly: provider key bodies are base64url,
	// so a "_" or "-" anywhere in the first 16 characters of the body broke the
	// match and the key passed in the clear. Measured over random base64url
	// bodies, that missed 27-30% of every modern shape -- a coin-flip redactor
	// on the credential forwarded into the agent container.
	//
	// Three patterns instead of one clever one, because the false-positive risk
	// and the true-positive shape differ per case:
	//
	// 1. A known provider prefix. The prefix is the evidence, so the body is
	//    plain base64url with no further constraint.
	{regexp.MustCompile(`\b(sk-(?:ant|proj|svcacct|admin)-[A-Za-z0-9_-]{16,})`), "OpenAI/Anthropic API key"},
	// 2. The legacy flat OpenAI key: "sk-" and one unbroken alphanumeric run.
	{regexp.MustCompile(`\b(sk-[A-Za-z0-9]{20,})\b`), "OpenAI API key"},
	// 3. Any other "sk-" token whose body carries an uppercase letter. That is
	//    what separates a key from the false positive the previous widening hit
	//    -- an ordinary URL slug like
	//    sk-decomposition-of-matrices-and-linear-algebra is lowercase and
	//    hyphenated, and never carries one.
	{regexp.MustCompile(`\b(sk-[a-z0-9_-]{0,64}[A-Z][A-Za-z0-9_-]{10,})`), "API key"},
	// Stripe live secret key.
	{regexp.MustCompile(`\b(sk_live_[0-9A-Za-z]{16,})\b`), "Stripe secret key"},
	// Slack tokens (bot, app, refresh, signing, user).
	{regexp.MustCompile(`\b(xox[baprs]-[0-9]{10,}-[0-9]{10,}-[0-9A-Za-z]{10,})\b`), "Slack token"},
	// Generic Bearer token.
	{regexp.MustCompile(`(?i)\bBearer\s+([A-Za-z0-9._~+/-]{20,})\b`), "bearer token"},
	// Generic key=value credentials in text/URLs.
	{regexp.MustCompile(`(?i)\b(?:api[_-]?key|secret|password|token|bearer)\s*[:=]\s*["']?([A-Za-z0-9._~+/=-]{8,})["']?`), "credential assignment"},
}

const redacted = "[REDACTED]"

// Fingerprint returns the stable sha256 hex digest of a secret value, used as
// the "not a secret" allowlist key (review changes #1). It is computed over
// the value's capture group, never the raw match text (which may include a
// vendor prefix or surrounding quotes), so the same secret fingerprints the
// same way regardless of quoting or whitespace.
func Fingerprint(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

// CredentialFinding is one detected credential occurrence, located but never
// valued: the file and line it was found on, the kind of secret matched, and
// its stable fingerprint. Overridable reports whether a human may disposition
// this specific match "not a secret" (true for a pattern-shaped match; false
// for a value drawn from the project's own configured secrets, which are never
// eligible for that override).
type CredentialFinding struct {
	Path        string
	Line        int
	Kind        string
	Fingerprint string
	Overridable bool
}

// lineAt returns the 1-based line number of byte offset pos within s.
func lineAt(s string, pos int) int {
	return 1 + strings.Count(s[:pos], "\n")
}

// FindCredentialsInFile scans one file's content for every known credential
// pattern, returning each match's line, kind and fingerprint -- never the
// matched value itself. It is the location-aware counterpart to FindCredential,
// used so a denial can name path:line and the kind of secret instead of just
// rejecting silently (§ D2 secret sweep, review changes #1/#7).
func FindCredentialsInFile(path, content string) []CredentialFinding {
	var out []CredentialFinding
	for _, p := range credentialCatalog {
		for _, idx := range p.re.FindAllStringSubmatchIndex(content, -1) {
			value := content[idx[0]:idx[1]]
			if len(idx) >= 4 && idx[2] >= 0 {
				value = content[idx[2]:idx[3]]
			}
			out = append(out, CredentialFinding{
				Path:        path,
				Line:        lineAt(content, idx[0]),
				Kind:        p.kind,
				Fingerprint: Fingerprint(value),
				Overridable: true,
			})
		}
	}
	return out
}

// Redactor redacts known secret values and credential patterns from content.
type Redactor struct {
	known []string // sorted by length desc for longest-match replacement
}

// NewRedactor builds a Redactor with the given known secret values.
func NewRedactor(knownValues ...string) *Redactor {
	known := make([]string, 0, len(knownValues))
	seen := map[string]bool{}
	for _, v := range knownValues {
		v = strings.TrimSpace(v)
		if v != "" && !seen[v] {
			seen[v] = true
			known = append(known, v)
		}
	}
	sort.Slice(known, func(i, j int) bool { return len(known[i]) > len(known[j]) })
	return &Redactor{known: known}
}

// Redact implements the blob.Redactor contract ([]byte in, []byte out).
func (r *Redactor) Redact(content []byte) ([]byte, error) {
	return []byte(r.RedactString(string(content))), nil
}

// RedactString redacts a string. It is safe to call concurrently.
func (r *Redactor) RedactString(s string) string {
	for _, v := range r.known {
		s = strings.ReplaceAll(s, v, redacted)
	}
	for _, p := range credentialCatalog {
		s = p.re.ReplaceAllString(s, redacted)
	}
	return s
}

// ContainsCredential reports whether s contains any known secret value or
// credential pattern. It is the detection counterpart to RedactString, used as a
// hard-reject signal (nil-safe: a nil Redactor falls back to patterns only).
func (r *Redactor) ContainsCredential(s string) bool {
	if r != nil {
		for _, v := range r.known {
			if strings.Contains(s, v) {
				return true
			}
		}
	}
	for _, p := range credentialCatalog {
		if p.re.MatchString(s) {
			return true
		}
	}
	return false
}

// FindKnownInFile locates every configured secret value (r.known) within one
// file's content, by line. Unlike FindCredentialsInFile's pattern matches,
// these are never Overridable: a value drawn from the project's own resolved
// secrets (a provider key, a .env value) is a real credential by construction,
// not a shape that might be a false positive.
func (r *Redactor) FindKnownInFile(path, content string) []CredentialFinding {
	if r == nil {
		return nil
	}
	var out []CredentialFinding
	for _, v := range r.known {
		if v == "" {
			continue
		}
		start := 0
		for {
			i := strings.Index(content[start:], v)
			if i < 0 {
				break
			}
			pos := start + i
			out = append(out, CredentialFinding{
				Path:        path,
				Line:        lineAt(content, pos),
				Kind:        "configured secret value",
				Fingerprint: Fingerprint(v),
				Overridable: false,
			})
			start = pos + len(v)
		}
	}
	return out
}

// Redact applies pattern-based redaction only (no known values). Used as a
// convenience for callers without resolved secrets.
func Redact(s string) string {
	return (&Redactor{}).RedactString(s)
}

// FindCredential returns the first credential-like pattern found in s, or "" if
// none. It is the detection counterpart to Redact: the same catalog, used as a
// hard rejection signal (§45) rather than a redaction.
func FindCredential(s string) string {
	for _, p := range credentialCatalog {
		if m := p.re.FindString(s); m != "" {
			return m
		}
	}
	return ""
}
