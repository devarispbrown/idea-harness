// Package security enforces security outside the LLM (§43): redaction, path
// guards, HTML sanitization, trust classification, and the local-API token.
package security

import (
	"crypto/hmac"
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
	// AWS secret access key (review M3): its own env-var name, since the
	// generic credential-assignment rule below requires the keyword
	// (api_key/secret/password/token) to sit immediately before the
	// separator, and "ACCESS_KEY" -- not one of those words -- comes between
	// "SECRET" and "=" in AWS_SECRET_ACCESS_KEY. AWS secret keys are 40
	// base64-ish characters; 30+ tolerates a truncated/rotated example.
	{regexp.MustCompile(`(?i)\bAWS_SECRET_ACCESS_KEY\s*[:=]\s*["']?([A-Za-z0-9/+=]{30,})["']?`), "AWS secret access key"},
	// Generic credential assignment in text/JSON/shell/URLs (review M3):
	// {"password": "..."}, DB_PASSWORD=..., client_secret: ..., and similar.
	//
	// [A-Za-z0-9_]* (not \b) before the keyword, so it matches inside a
	// snake_case or SCREAMING_CASE identifier -- DB_PASSWORD, client_secret,
	// AWS_SECRET_ACCESS_KEY's own "_SECRET_" segment elsewhere -- where "_" is
	// a word character and \b never fires between it and the keyword.
	//
	// ["']? right after the keyword (before \s*[:=]) consumes a JSON key's
	// closing quote -- "password": -- which \s*[:=] alone cannot skip over.
	//
	// The value itself is one of three shapes: double-quoted, single-quoted
	// (either may contain almost anything 8+ characters long -- a quote is
	// itself the evidence of a literal), or, with no quote at all, a run of
	// 8+ characters that excludes whitespace, quotes, parens, braces,
	// brackets, commas and '.': the punctuation an identifier, member access,
	// or function CALL uses and a bare secret literal does not. That is what
	// keeps `token = getenv(...)` and `const token = req.headers...` from
	// matching -- "getenv" stops at "(" (6 chars, under the 8-char floor) and
	// "req" stops at "." (3 chars) -- while still matching an unquoted
	// shell/env-style assignment like DB_PASSWORD=hunter2345.
	{regexp.MustCompile(`(?i)[A-Za-z0-9_]*(?:api[_-]?key|secret|passw(?:or)?d|token)["']?\s*[:=]\s*(?:"([^"\n]{8,})"|'([^'\n]{8,})'|([^\s"'(){}\[\],.]{8,}))`), "credential assignment"},
}

const redacted = "[REDACTED]"

// Fingerprint returns a secret value's fingerprint using an empty HMAC key
// (review H3: kept for callers with no per-project key -- tests, and any use
// outside a project's own Redactor). FingerprintWithKey is what every
// project-scoped call site should use instead, so the same secret does not
// fingerprint identically across every installation that ever saw it.
func Fingerprint(value string) string {
	return FingerprintWithKey(nil, value)
}

// FingerprintWithKey returns the stable HMAC-SHA256 hex digest of a secret
// value under key, used as the "not a secret" allowlist key (review changes
// #1, review H3: an HMAC keyed per-project replaces the original bare
// sha256, which was the same, precomputable value for the same secret on
// every installation). It is computed over the value's capture group, never
// the raw match text (which may include a vendor prefix or surrounding
// quotes), so the same secret fingerprints the same way regardless of
// quoting or whitespace. A nil/empty key is a valid (degraded, but never an
// error) case: the HMAC is still deterministic, just not keyed to a project.
func FingerprintWithKey(key []byte, value string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))
}

// maskValue returns a masked preview of a secret value for display for a
// human dispositioning a finding (review H3): the first 4 and last 4
// characters are kept, the rest collapsed to a single ellipsis (e.g.
// "sk-p…Xy9z"). A value of 8 characters or fewer is masked entirely, so a
// short secret's preview never reveals enough characters to reconstruct it.
func maskValue(v string) string {
	if len(v) <= 8 {
		return strings.Repeat("*", len(v))
	}
	return v[:4] + "…" + v[len(v)-4:]
}

// lineTextAt returns the full line of s containing byte offset pos (no
// trailing newline), for building a masked preview of the line a credential
// was found on.
func lineTextAt(s string, pos int) string {
	start := strings.LastIndexByte(s[:pos], '\n') + 1
	rest := s[pos:]
	if end := strings.IndexByte(rest, '\n'); end >= 0 {
		return s[start : pos+end]
	}
	return s[start:]
}

// CredentialFinding is one detected credential occurrence, located but never
// valued: the file and line it was found on, the kind of secret matched, and
// its stable fingerprint. Overridable reports whether a human may disposition
// this specific match "not a secret" (true for a pattern-shaped match; false
// for a value drawn from the project's own configured secrets, which are never
// eligible for that override). Preview and MaskedLine (review H3) show what a
// not_a_secret disposition would allowlist without ever carrying the raw
// value: Preview is the value alone, masked (see maskValue); MaskedLine is
// the full source line with the value replaced by that same mask.
type CredentialFinding struct {
	Path        string
	Line        int
	Kind        string
	Fingerprint string
	Overridable bool
	Preview     string
	MaskedLine  string
}

// lineAt returns the 1-based line number of byte offset pos within s.
func lineAt(s string, pos int) int {
	return 1 + strings.Count(s[:pos], "\n")
}

// capturedValue returns a regex match's secret value: the first capturing
// group that actually participated in the match (its indices are >= 0), or
// the whole match when the pattern has no capturing group. Most patterns in
// credentialCatalog have exactly one group, but the generic credential-
// assignment rule (review M3) has three alternative groups -- one per quoting
// style -- of which only one is ever populated per match, so "group 1" alone
// is not enough.
func capturedValue(content string, idx []int) string {
	for g := 1; g*2+1 < len(idx); g++ {
		if idx[g*2] >= 0 {
			return content[idx[g*2] : idx[g*2+1]]
		}
	}
	return content[idx[0]:idx[1]]
}

// FindCredentialsInFile scans one file's content for every known credential
// pattern, returning each match's line, kind and fingerprint -- never the
// matched value itself. It is the location-aware counterpart to FindCredential,
// used so a denial can name path:line and the kind of secret instead of just
// rejecting silently (§ D2 secret sweep, review changes #1/#7).
//
// Uses an empty HMAC key (see Fingerprint); (*Redactor)(nil).FindPatternsInFile
// is the per-project-keyed equivalent used inside a project's own pipeline.
func FindCredentialsInFile(path, content string) []CredentialFinding {
	return (*Redactor)(nil).FindPatternsInFile(path, content)
}

// Redactor redacts known secret values and credential patterns from content.
type Redactor struct {
	known []string // sorted by length desc for longest-match replacement
	// key is this project's HMAC key for Fingerprint (review H3), set via
	// NewRedactorWithKey. Nil is valid (falls back to Fingerprint's degraded,
	// unkeyed behavior) for a Redactor built with plain NewRedactor.
	key []byte
}

// NewRedactor builds a Redactor with the given known secret values and no
// per-project fingerprint key (fingerprints fall back to Fingerprint's
// unkeyed, degraded form). Prefer NewRedactorWithKey wherever a project's own
// store is available, so its findings' fingerprints are HMAC-keyed per
// project (review H3).
func NewRedactor(knownValues ...string) *Redactor {
	return NewRedactorWithKey(nil, knownValues...)
}

// NewRedactorWithKey is NewRedactor with an explicit per-project HMAC key
// (review H3) -- typically store.DB.SecretFingerprintKey's result -- so every
// fingerprint this Redactor computes (known-value and pattern matches alike)
// is scoped to the project rather than a bare, cross-installation-comparable
// sha256 of the value.
func NewRedactorWithKey(key []byte, knownValues ...string) *Redactor {
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
	return &Redactor{known: known, key: key}
}

// fingerprint computes value's fingerprint with this Redactor's key (nil-safe:
// a nil *Redactor falls back to an empty key, matching the package-level
// Fingerprint/FindCredentialsInFile's degraded behavior).
func (r *Redactor) fingerprint(value string) string {
	var key []byte
	if r != nil {
		key = r.key
	}
	return FingerprintWithKey(key, value)
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
			preview := maskValue(v)
			out = append(out, CredentialFinding{
				Path:        path,
				Line:        lineAt(content, pos),
				Kind:        "configured secret value",
				Fingerprint: r.fingerprint(v),
				Overridable: false,
				Preview:     preview,
				MaskedLine:  strings.ReplaceAll(lineTextAt(content, pos), v, preview),
			})
			start = pos + len(v)
		}
	}
	return out
}

// FindPatternsInFile is FindCredentialsInFile's per-Redactor, per-project-key
// equivalent (review H3): scans content for every known credential pattern,
// fingerprinting each match with this Redactor's key (nil-safe: a nil
// *Redactor falls back to an empty key, exactly matching the package-level
// FindCredentialsInFile).
func (r *Redactor) FindPatternsInFile(path, content string) []CredentialFinding {
	var out []CredentialFinding
	for _, p := range credentialCatalog {
		for _, idx := range p.re.FindAllStringSubmatchIndex(content, -1) {
			value := capturedValue(content, idx)
			preview := maskValue(value)
			out = append(out, CredentialFinding{
				Path:        path,
				Line:        lineAt(content, idx[0]),
				Kind:        p.kind,
				Fingerprint: r.fingerprint(value),
				Overridable: true,
				Preview:     preview,
				MaskedLine:  strings.ReplaceAll(lineTextAt(content, idx[0]), value, preview),
			})
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
