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
	// valid, when non-nil, additionally filters this pattern's matches
	// (review H5): RE2 (Go's regexp engine) has no lookahead/lookbehind, so
	// checks like "not immediately followed by a call's `(`" or "not an
	// identifier/member-access shape" cannot live in the regexp itself and
	// are validated here against the full content and the match's index
	// slice instead. nil means every regex match is valid, unchanged from
	// before this field existed.
	valid func(content string, idx []int) bool
}

// validMatches returns p's matches within content that also satisfy p.valid
// (every regex match, when p.valid is nil).
func (p credentialPattern) validMatches(content string) [][]int {
	all := p.re.FindAllStringSubmatchIndex(content, -1)
	if p.valid == nil {
		return all
	}
	out := make([][]int, 0, len(all))
	for _, idx := range all {
		if p.valid(content, idx) {
			out = append(out, idx)
		}
	}
	return out
}

// replaceValid redacts p's valid matches in s with [REDACTED], leaving any
// regex match that failed p.valid untouched.
func (p credentialPattern) replaceValid(s string) string {
	matches := p.validMatches(s)
	if len(matches) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, idx := range matches {
		b.WriteString(s[last:idx[0]])
		b.WriteString(redacted)
		last = idx[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

// capturedGroupBounds returns the start/end byte offsets, within the string
// idx was computed against, of the capturing group that actually
// participated in the match -- mirroring capturedValue, but returning
// offsets rather than the substring, so a validator can inspect what comes
// immediately before or after it.
func capturedGroupBounds(idx []int) (start, end int) {
	for g := 1; g*2+1 < len(idx); g++ {
		if idx[g*2] >= 0 {
			return idx[g*2], idx[g*2+1]
		}
	}
	return idx[0], idx[1]
}

// identifierWordRe matches one identifier-shaped word: letters, digits and
// underscores, starting with a letter or underscore -- the shape of a single
// variable/property name or SCREAMING_SNAKE constant (`token`, `API_KEY`,
// `X`).
var identifierWordRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// hasIdentifierStructure reports whether a single (non-dotted) token has an
// internal structure that marks it as a NAME rather than arbitrary token
// data: camelCase (a lowercase-to-uppercase transition, `authToken`) or
// snake_case (an underscore, `token_from_somewhere`). A flat run of
// same-case letters with neither -- `abcdefghijklmnop` -- reads as
// plausible secret data, not a name, and is not rejected on shape alone.
func hasIdentifierStructure(value string) bool {
	if strings.Contains(value, "_") {
		return true
	}
	for i := 1; i < len(value); i++ {
		if value[i-1] >= 'a' && value[i-1] <= 'z' && value[i] >= 'A' && value[i] <= 'Z' {
			return true
		}
	}
	return false
}

// looksLikeIdentifierReference reports whether an unquoted assignment value
// reads as a variable/property reference rather than a literal secret
// (review H5).
//
// A value containing a digit is never treated as a reference: real
// identifiers occasionally contain one, but a value that's otherwise
// reference-shaped AND carries a digit reads as data, and this heuristic
// errs toward not rejecting a possible real secret.
//
// A single (non-dotted) token is a reference when it has camelCase or
// snake_case structure (hasIdentifierStructure): `authToken`,
// `hashedPassword`, `token_from_somewhere`, `API_KEY_FROM_ENV`.
//
// A dotted value is a reference when EVERY segment is itself
// identifier-shaped: `process.env.X`, `settings.API_KEY_FROM_ENV`,
// `req.headers.authorization`. This is deliberately broader than the
// single-token case -- it does not also require camelCase/snake_case
// structure per segment -- because a receiver.property chain's segments are
// typically plain lowercase words with no such structure of their own. The
// trade-off: a plain-word dotted literal with no digit (`xyz.abc.def.ghi`)
// is also read as a reference and not flagged. That is the safer direction
// for a shape-only heuristic that cannot otherwise tell a property chain
// from a dot-delimited secret apart, and it is what keeps a real
// member-access expression like `req.headers.authorization` from being
// rejected as a credential.
func looksLikeIdentifierReference(value string) bool {
	if strings.ContainsAny(value, "0123456789") {
		return false
	}
	if !strings.Contains(value, ".") {
		return identifierWordRe.MatchString(value) && hasIdentifierStructure(value)
	}
	for _, seg := range strings.Split(value, ".") {
		if seg == "" || !identifierWordRe.MatchString(seg) {
			return false
		}
	}
	return true
}

// credentialAssignmentGroups is the group index (within
// FindAllStringSubmatchIndex's idx slice) of the credential-assignment
// pattern's UNQUOTED alternative -- its third capturing group. idx[6] is
// that group's start offset ([0:2] is the whole match, [2:4] group 1,
// [4:6] group 2, [6:8] group 3).
const credentialAssignmentUnquotedGroup = 6

// validCredentialAssignmentValue is credentialCatalog's validator for the
// generic "credential assignment" pattern (review H5). A quoted value
// (groups 1/2) already carries its own evidence of being a literal -- a
// quote is not something an identifier or a function call produces -- and
// is rejected only for `$`-interpolation (`"${API_KEY}"`, a reference, not a
// literal). An unquoted value (group 3) additionally must not be a call
// (immediately followed by `(`) or read as an identifier/member-access
// reference (looksLikeIdentifierReference).
func validCredentialAssignmentValue(content string, idx []int) bool {
	start, end := capturedGroupBounds(idx)
	value := content[start:end]
	if value == "" || strings.HasPrefix(value, "$") {
		return false
	}
	if len(idx) <= credentialAssignmentUnquotedGroup || idx[credentialAssignmentUnquotedGroup] < 0 {
		return true // quoted (group 1 or 2): already checked for "$" above.
	}
	if end < len(content) && content[end] == '(' {
		return false // a call: generate_csrf_token(), getToken()
	}
	return !looksLikeIdentifierReference(value)
}

// credentialCatalog is the redaction and detection catalog (§48). Order
// matters only for readability; all matches are replaced with [REDACTED].
var credentialCatalog = []credentialPattern{
	// PEM private keys (multiline).
	{re: regexp.MustCompile(`(?s)(-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----)`), kind: "PEM private key"},
	// JWTs (three base64url segments).
	{re: regexp.MustCompile(`\b(eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})\b`), kind: "JWT"},
	// AWS access key id.
	{re: regexp.MustCompile(`\b(AKIA[0-9A-Z]{16})\b`), kind: "AWS access key ID"},
	// GitHub personal access token (classic + fine-grained) and OAuth tokens.
	{re: regexp.MustCompile(`\b(ghp_[A-Za-z0-9]{20,})\b`), kind: "GitHub personal access token"},
	{re: regexp.MustCompile(`\b(github_pat_[A-Za-z0-9_]{20,})\b`), kind: "GitHub fine-grained personal access token"},
	{re: regexp.MustCompile(`\b(gh[ousr]_[A-Za-z0-9]{20,})\b`), kind: "GitHub token"},
	// GitLab personal access token.
	{re: regexp.MustCompile(`\b(glpat-[A-Za-z0-9_-]{20,})\b`), kind: "GitLab personal access token"},
	// Google API key.
	{re: regexp.MustCompile(`\b(AIza[0-9A-Za-z_-]{30,})\b`), kind: "Google API key"},
	// npm access token.
	{re: regexp.MustCompile(`\b(npm_[A-Za-z0-9]{30,})\b`), kind: "npm access token"},
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
	{re: regexp.MustCompile(`\b(sk-(?:ant|proj|svcacct|admin)-[A-Za-z0-9_-]{16,})`), kind: "OpenAI/Anthropic API key"},
	// 2. The legacy flat OpenAI key: "sk-" and one unbroken alphanumeric run.
	{re: regexp.MustCompile(`\b(sk-[A-Za-z0-9]{20,})\b`), kind: "OpenAI API key"},
	// 3. Any other "sk-" token whose body carries an uppercase letter. That is
	//    what separates a key from the false positive the previous widening hit
	//    -- an ordinary URL slug like
	//    sk-decomposition-of-matrices-and-linear-algebra is lowercase and
	//    hyphenated, and never carries one.
	{re: regexp.MustCompile(`\b(sk-[a-z0-9_-]{0,64}[A-Z][A-Za-z0-9_-]{10,})`), kind: "API key"},
	// Stripe live secret key.
	{re: regexp.MustCompile(`\b(sk_live_[0-9A-Za-z]{16,})\b`), kind: "Stripe secret key"},
	// Slack tokens (bot, app, refresh, signing, user).
	{re: regexp.MustCompile(`\b(xox[baprs]-[0-9]{10,}-[0-9]{10,}-[0-9A-Za-z]{10,})\b`), kind: "Slack token"},
	// Generic Bearer token.
	{re: regexp.MustCompile(`(?i)\bBearer\s+([A-Za-z0-9._~+/-]{20,})\b`), kind: "bearer token"},
	// URL userinfo credentials (review H5): scheme://user:PASSWORD@host, e.g.
	// postgres://admin:hunter2345@db. The username is unconstrained (any
	// run with no ':'/'/'/'@'/whitespace); the password is 3+ characters
	// excluding '/'/'@'/whitespace, tolerating a short example password.
	{re: regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9+.-]*://[^\s/:@]+:([^\s/@]{3,})@`), kind: "URL userinfo credential"},
	// AWS secret access key (review M3): its own env-var name, since the
	// generic credential-assignment rule below requires the keyword
	// (api_key/secret/password/token) to sit immediately before the
	// separator, and "ACCESS_KEY" -- not one of those words -- comes between
	// "SECRET" and "=" in AWS_SECRET_ACCESS_KEY. AWS secret keys are 40
	// base64-ish characters; 30+ tolerates a truncated/rotated example.
	{re: regexp.MustCompile(`(?i)\bAWS_SECRET_ACCESS_KEY\s*[:=]\s*["']?([A-Za-z0-9/+=]{30,})["']?`), kind: "AWS secret access key"},
	// Generic credential assignment in text/JSON/shell/URLs (review M3, H5):
	// {"password": "..."}, DB_PASSWORD=..., client_secret: ..., pwd = "...",
	// SECRET_KEY = "...", and similar.
	//
	// [A-Za-z0-9_]* (not \b) before the keyword, so it matches inside a
	// snake_case or SCREAMING_CASE identifier -- DB_PASSWORD, client_secret,
	// AWS_SECRET_ACCESS_KEY's own "_SECRET_" segment elsewhere -- where "_" is
	// a word character and \b never fires between it and the keyword.
	// `secret` optionally takes a `_key`/`_value` (or `-key`/`-value`) suffix
	// (review H5), so SECRET_KEY = "..." and client_secret_value = "..." both
	// match; `pwd` joins `password`/`passwd` as a keyword.
	//
	// ["']? right after the keyword (before \s*[:=]) consumes a JSON key's
	// closing quote -- "password": -- which \s*[:=] alone cannot skip over.
	//
	// The value itself is one of three shapes: double-quoted, single-quoted
	// (either may contain almost anything 8+ characters long -- a quote is
	// itself the evidence of a literal), or, with no quote at all, a run of
	// 8+ characters excluding whitespace, quotes, parens, braces, brackets,
	// commas and ';' -- the punctuation a function CALL's argument list or a
	// statement terminator uses (without excluding ';', "authToken;" carried
	// the semicolon into the candidate value and defeated the
	// identifier-shape check below). Unlike the original version of this
	// pattern, '.' is NOT excluded here: a real secret can legitimately
	// contain one (a JWT-shaped value, a dot-delimited token). Excluding
	// calls, `$`-interpolation and identifier/member-access shapes
	// (`getenv(...)`, `${API_KEY}`, `req.headers.token`,
	// `settings.API_KEY_FROM_ENV`) is instead done by
	// validCredentialAssignmentValue, in Go, after the match: RE2 has no
	// lookahead to express "not followed by `(`" in the regexp itself, and no
	// regexp alone can tell a member-access chain from a dot-delimited
	// secret (see looksLikeIdentifierReference's doc comment for that
	// trade-off).
	{
		re:    regexp.MustCompile(`(?i)[A-Za-z0-9_]*(?:api[_-]?key|secret(?:[_-]?(?:key|value))?|passw(?:or)?d|pwd|token)["']?\s*[:=]\s*(?:"([^"\n]{8,})"|'([^'\n]{8,})'|([^\s"'(){}\[\],;]{8,}))`),
		kind:  "credential assignment",
		valid: validCredentialAssignmentValue,
	},
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
// human dispositioning a finding (review H1): at most min(4, len/4)
// characters are kept on each side, the rest collapsed to a single ellipsis
// (e.g. "sk-p…Xy9z"). Nothing is revealed at all for a value under 16
// characters -- the old floor of 8 revealed 4+4 of a 9-character value
// (`abcdefghi` -> `abcd…fghi`, 8 of 9 characters back in the clear), and
// short passwords are exactly the common case a human is dispositioning
// here.
func maskValue(v string) string {
	n := len(v)
	if n < 16 {
		return strings.Repeat("*", n)
	}
	reveal := n / 4
	if reveal > 4 {
		reveal = 4
	}
	return v[:reveal] + "…" + v[n-reveal:]
}

// maskAllOnLine returns line with EVERY credential this Redactor (and the
// pattern catalog) can find replaced by that match's own masked preview
// (review H1): a finding's MaskedLine used to mask only its own value, so
// `DB_PASSWORD=hunter2345 API_TOKEN=zzzzzzzzqqqq` came back as
// `DB_PASSWORD=hunter2345 API_TOKEN=zzzz…qqqq` -- the first secret in the
// clear. r may be nil (no known project values; pattern matches are still
// masked).
func maskAllOnLine(line string, r *Redactor) string {
	if r != nil {
		for _, v := range r.known {
			if v == "" {
				continue
			}
			line = strings.ReplaceAll(line, v, maskValue(v))
		}
	}
	for _, p := range credentialCatalog {
		line = p.re.ReplaceAllStringFunc(line, func(m string) string {
			idx := p.re.FindStringSubmatchIndex(m)
			if idx == nil {
				return m
			}
			value := capturedValue(m, idx)
			if value == "" {
				return m
			}
			return strings.Replace(m, value, maskValue(value), 1)
		})
	}
	return line
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
		s = p.replaceValid(s)
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
		if len(p.validMatches(s)) > 0 {
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
				MaskedLine:  maskAllOnLine(lineTextAt(content, pos), r),
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
		for _, idx := range p.validMatches(content) {
			value := capturedValue(content, idx)
			preview := maskValue(value)
			out = append(out, CredentialFinding{
				Path:        path,
				Line:        lineAt(content, idx[0]),
				Kind:        p.kind,
				Fingerprint: r.fingerprint(value),
				Overridable: true,
				Preview:     preview,
				MaskedLine:  maskAllOnLine(lineTextAt(content, idx[0]), r),
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
		if matches := p.validMatches(s); len(matches) > 0 {
			return s[matches[0][0]:matches[0][1]]
		}
	}
	return ""
}
