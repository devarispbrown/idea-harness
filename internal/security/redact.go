// Package security enforces security outside the LLM (§43): redaction, path
// guards, HTML sanitization, trust classification, and the local-API token.
package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"math"
	"path/filepath"
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
	if looksLikeTemplatePlaceholder(value) {
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

// placeholderWords are case-insensitive substrings that mark an assigned
// value as a placeholder rather than a real secret (ICP re-run defect #1):
// "example", "test", "dummy", "fake", "placeholder", "changeme", "xxx". Scoped
// to the GENERIC "credential assignment" pattern only (validGenericAssignment)
// -- never a provider-specific format -- because a real, documented example
// key can legitimately carry one of these words (AWS's own
// "AKIAIOSFODNN7EXAMPLE"/"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY" docs
// literals, which this catalog's AWS-specific patterns must keep matching).
var placeholderWords = []string{"example", "test", "dummy", "fake", "placeholder", "changeme", "xxx"}

// containsPlaceholderWord reports whether value contains one of
// placeholderWords, case-insensitively.
func containsPlaceholderWord(value string) bool {
	lower := strings.ToLower(value)
	for _, w := range placeholderWords {
		if strings.Contains(lower, w) {
			return true
		}
	}
	return false
}

// placeholderRunLen is the minimum length of a run of one repeated character
// that marks a value a placeholder body -- "sk_live_...0000", "...1111",
// "...2222" (the ICP re-run's own reported Stripe-shaped example keys), or an
// all-"x" stand-in.
const placeholderRunLen = 4

// hasLongRun reports whether value contains a run of n or more identical
// consecutive bytes.
func hasLongRun(value string, n int) bool {
	if len(value) < n {
		return false
	}
	run := 1
	for i := 1; i < len(value); i++ {
		if value[i] == value[i-1] {
			run++
			if run >= n {
				return true
			}
		} else {
			run = 1
		}
	}
	return false
}

// isSequentialFill reports whether value is essentially ONE long ascending or
// descending run of consecutive byte values (>= 6 characters, and covering at
// least 70% of value's own length) -- "abcdefghijklmnop", "0123456789876543".
// Deliberately majority-of-value, not "contains any 4+ run": a great many
// existing, legitimately-realistic test/example secrets embed a short
// sequential fragment (AKIA1234567890ABCDEF, the alphabet tail of a
// hand-written base64-shaped fixture) without the value AS A WHOLE being a
// sequential filler, and a substring-only check flagged those as placeholders
// too. Scoped to the generic assignment pattern only, the same as
// containsPlaceholderWord, for the same reason.
func isSequentialFill(value string) bool {
	const minRun = 6
	if len(value) < minRun {
		return false
	}
	best := 1
	asc, desc := 1, 1
	for i := 1; i < len(value); i++ {
		switch {
		case value[i] == value[i-1]+1:
			asc++
			desc = 1
		case value[i] == value[i-1]-1:
			desc++
			asc = 1
		default:
			asc, desc = 1, 1
		}
		if asc > best {
			best = asc
		}
		if desc > best {
			best = desc
		}
	}
	return best >= minRun && float64(best)/float64(len(value)) >= 0.7
}

// placeholderEntropyThreshold is the minimum Shannon entropy per character a
// value must have to read as plausibly-random secret data. A real generated
// token (base64/hex/alphanumeric) comfortably clears this; a low-variety
// filler like "aaaabbbbcccc" or "passwordpassword" does not.
const placeholderEntropyThreshold = 2.5

// shannonEntropyPerChar returns value's Shannon entropy in bits per
// character.
func shannonEntropyPerChar(value string) float64 {
	if len(value) == 0 {
		return 0
	}
	freq := make(map[rune]int)
	for _, r := range value {
		freq[r]++
	}
	n := float64(len(value))
	var h float64
	for _, c := range freq {
		p := float64(c) / n
		h -= p * math.Log2(p)
	}
	return h
}

// maxPlaceholderShapeLen bounds isPlaceholderShape to short, flat token
// values. A PEM block or JWT's capture group is the whole (long, multi-part)
// match, where a repeated-character run or a low aggregate entropy score is
// not a meaningful signal either way; those patterns simply never apply this
// check (see credentialCatalog).
const maxPlaceholderShapeLen = 200

// isPlaceholderShape reports whether value has the SHAPE of a placeholder --
// a long run of one repeated character, or overall entropy too low to
// plausibly be generated secret data -- independent of vocabulary. Applied to
// every flat-token pattern in credentialCatalog (validFlatTokenValue), not
// just the generic assignment rule, so a provider-specific format with an
// obviously-fake body (the Stripe-shaped "sk_live_...0000" example) is also
// excluded.
func isPlaceholderShape(value string) bool {
	if value == "" || len(value) > maxPlaceholderShapeLen {
		return false
	}
	if hasLongRun(value, placeholderRunLen) {
		return true
	}
	return shannonEntropyPerChar(value) < placeholderEntropyThreshold
}

// looksLikeTemplatePlaceholder reports whether value is a comment/template
// placeholder rather than a literal -- "<secret>", "<id>:<secret>",
// "${API_KEY}", "{{ secret }}" -- which must never match as a credential
// (ICP re-run defect #1).
func looksLikeTemplatePlaceholder(value string) bool {
	if strings.Contains(value, "${") || strings.Contains(value, "{{") {
		return true
	}
	if strings.HasPrefix(value, "<") {
		return true
	}
	return strings.Contains(value, "<") && strings.Contains(value, ">")
}

// validGenericAssignment is credentialCatalog's validator for the generic
// "credential assignment" pattern: validCredentialAssignmentValue's existing
// call-vs-literal-vs-reference checks, plus the placeholder/low-entropy and
// test-fixture-path handling from the ICP re-run's defect #1. A match that
// fails this never becomes a finding at all (not even advisory) -- it reads
// as a placeholder, not a possible false positive worth showing a reviewer.
func validGenericAssignment(content string, idx []int) bool {
	if !validCredentialAssignmentValue(content, idx) {
		return false
	}
	start, end := capturedGroupBounds(idx)
	value := content[start:end]
	if containsPlaceholderWord(value) {
		return false
	}
	if isSequentialFill(value) {
		return false
	}
	return !isPlaceholderShape(value)
}

// validFlatTokenValue is credentialCatalog's validator for every
// provider-specific, single-flat-token pattern (AWS/GitHub/GitLab/Google/npm/
// OpenAI-Anthropic/Stripe/Slack): only the placeholder-SHAPE check applies
// (isPlaceholderShape) -- never the vocabulary word list or the sequential-fill
// check, both of which are scoped to the generic assignment rule alone (see
// their doc comments for why: a real documented example key, or a
// hand-written realistic-looking test fixture, can otherwise trip either
// one).
func validFlatTokenValue(content string, idx []int) bool {
	start, end := capturedGroupBounds(idx)
	return !isPlaceholderShape(content[start:end])
}

// isTestFixturePath reports whether path looks like test or fixture code
// (ICP re-run defect #1): a Go test file, a `.spec.`/`.test.` file, or a path
// under a test/tests/testdata/fixtures/__tests__ directory. Used to downgrade
// a generic "credential assignment" match to advisory (non-blocking, still
// visible) in test/fixture code, where a `"secret": "..."` field in a fixture
// or a `X-API-Key: <id>:<secret>` doc comment is common and is not a real
// credential -- while a provider-specific format or a configured secret value
// still blocks there exactly as anywhere else.
func isTestFixturePath(path string) bool {
	p := filepath.ToSlash(path)
	base := filepath.Base(p)
	if strings.HasSuffix(base, "_test.go") {
		return true
	}
	if strings.Contains(base, ".spec.") || strings.Contains(base, ".test.") {
		return true
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "test", "tests", "testdata", "fixtures", "__tests__":
			return true
		}
	}
	return false
}

// genericAssignmentKind is credentialCatalog's kind string for the generic
// "credential assignment" pattern -- the ONLY category a test/fixture path
// downgrades to advisory (see isTestFixturePath and FindPatternsInFile).
const genericAssignmentKind = "credential assignment"

// credentialCatalog is the redaction and detection catalog (§48). Order
// matters only for readability; all matches are replaced with [REDACTED].
var credentialCatalog = []credentialPattern{
	// PEM private keys (multiline).
	{re: regexp.MustCompile(`(?s)(-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----)`), kind: "PEM private key"},
	// JWTs (three base64url segments).
	{re: regexp.MustCompile(`\b(eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})\b`), kind: "JWT"},
	// AWS access key id.
	{re: regexp.MustCompile(`\b(AKIA[0-9A-Z]{16})\b`), kind: "AWS access key ID", valid: validFlatTokenValue},
	// GitHub personal access token (classic + fine-grained) and OAuth tokens.
	{re: regexp.MustCompile(`\b(ghp_[A-Za-z0-9]{20,})\b`), kind: "GitHub personal access token", valid: validFlatTokenValue},
	{re: regexp.MustCompile(`\b(github_pat_[A-Za-z0-9_]{20,})\b`), kind: "GitHub fine-grained personal access token", valid: validFlatTokenValue},
	{re: regexp.MustCompile(`\b(gh[ousr]_[A-Za-z0-9]{20,})\b`), kind: "GitHub token", valid: validFlatTokenValue},
	// GitLab personal access token.
	{re: regexp.MustCompile(`\b(glpat-[A-Za-z0-9_-]{20,})\b`), kind: "GitLab personal access token", valid: validFlatTokenValue},
	// Google API key.
	{re: regexp.MustCompile(`\b(AIza[0-9A-Za-z_-]{30,})\b`), kind: "Google API key", valid: validFlatTokenValue},
	// npm access token.
	{re: regexp.MustCompile(`\b(npm_[A-Za-z0-9]{30,})\b`), kind: "npm access token", valid: validFlatTokenValue},
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
	{re: regexp.MustCompile(`\b(sk-(?:ant|proj|svcacct|admin)-[A-Za-z0-9_-]{16,})`), kind: "OpenAI/Anthropic API key", valid: validFlatTokenValue},
	// 2. The legacy flat OpenAI key: "sk-" and one unbroken alphanumeric run.
	{re: regexp.MustCompile(`\b(sk-[A-Za-z0-9]{20,})\b`), kind: "OpenAI API key", valid: validFlatTokenValue},
	// 3. Any other "sk-" token whose body carries an uppercase letter. That is
	//    what separates a key from the false positive the previous widening hit
	//    -- an ordinary URL slug like
	//    sk-decomposition-of-matrices-and-linear-algebra is lowercase and
	//    hyphenated, and never carries one.
	{re: regexp.MustCompile(`\b(sk-[a-z0-9_-]{0,64}[A-Z][A-Za-z0-9_-]{10,})`), kind: "API key", valid: validFlatTokenValue},
	// Stripe live secret key.
	{re: regexp.MustCompile(`\b(sk_live_[0-9A-Za-z]{16,})\b`), kind: "Stripe secret key", valid: validFlatTokenValue},
	// Slack tokens (bot, app, refresh, signing, user).
	{re: regexp.MustCompile(`\b(xox[baprs]-[0-9]{10,}-[0-9]{10,}-[0-9A-Za-z]{10,})\b`), kind: "Slack token", valid: validFlatTokenValue},
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
	//
	// ICP re-run defect #4: the ["']? right after the keyword (mirroring the
	// generic assignment pattern below) was missing here, so a JSON key --
	// "aws_secret_access_key": "<40-char value>" -- never matched: \s*[:=]
	// requires whitespace only immediately after the keyword, and JSON's
	// closing quote on the KEY sits right there instead. A real founder-
	// journey run had exactly this shape in config/deploy.json: the
	// aws_access_key_id line on the line above fired (its own pattern
	// matches the VALUE's AKIA... shape, not a key name, so JSON quoting
	// never mattered to it), and the aws_secret_access_key line next to it
	// produced no finding at all. Shell/env (`AWS_SECRET_ACCESS_KEY=...`) and
	// YAML (`aws_secret_access_key: ...`) syntax have no quote there and are
	// unaffected; this only widens what already matched.
	{re: regexp.MustCompile(`(?i)\bAWS_SECRET_ACCESS_KEY["']?\s*[:=]\s*["']?([A-Za-z0-9/+=]{30,})["']?`), kind: "AWS secret access key", valid: validFlatTokenValue},
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
		kind:  genericAssignmentKind,
		valid: validGenericAssignment,
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
	// Advisory marks a match downgraded to non-blocking (ICP re-run defect
	// #1): a generic "credential assignment" match found in a test/fixture
	// path (isTestFixturePath) -- never a provider-specific format or a
	// configured secret value, and never outside a test/fixture path. The
	// caller (workflow.Engine) is responsible for actually treating this as
	// non-blocking; this field only carries the classification.
	Advisory bool
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
			return content[idx[g*2]:idx[g*2+1]]
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
				// Only the generic assignment rule ever downgrades to
				// advisory, and only in a test/fixture path (ICP re-run
				// defect #1): a provider-specific format or a configured
				// secret value blocks everywhere, tests included.
				Advisory: p.kind == genericAssignmentKind && isTestFixturePath(path),
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
