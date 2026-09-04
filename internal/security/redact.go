// Package security enforces security outside the LLM (§43): redaction, path
// guards, HTML sanitization, trust classification, and the local-API token.
package security

import (
	"regexp"
	"sort"
	"strings"
)

// credentialPatterns is the redaction catalog (§48). Order matters only for
// readability; all matches are replaced with [REDACTED].
var credentialPatterns = []*regexp.Regexp{
	// PEM private keys (multiline).
	regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
	// JWTs (three base64url segments).
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\b`),
	// AWS access key id.
	regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
	// GitHub personal access token (classic + fine-grained) and OAuth tokens.
	regexp.MustCompile(`\bghp_[A-Za-z0-9]{20,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
	regexp.MustCompile(`\bgh[ousr]_[A-Za-z0-9]{20,}\b`),
	// GitLab personal access token.
	regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`),
	// Google API key.
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}\b`),
	// npm access token.
	regexp.MustCompile(`\bnpm_[A-Za-z0-9]{30,}\b`),
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
	regexp.MustCompile(`\bsk-(?:ant|proj|svcacct|admin)-[A-Za-z0-9_-]{16,}`),
	// 2. The legacy flat OpenAI key: "sk-" and one unbroken alphanumeric run.
	regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}\b`),
	// 3. Any other "sk-" token whose body carries an uppercase letter. That is
	//    what separates a key from the false positive the previous widening hit
	//    -- an ordinary URL slug like
	//    sk-decomposition-of-matrices-and-linear-algebra is lowercase and
	//    hyphenated, and never carries one.
	regexp.MustCompile(`\bsk-[a-z0-9_-]{0,64}[A-Z][A-Za-z0-9_-]{10,}`),
	// Stripe live secret key.
	regexp.MustCompile(`\bsk_live_[0-9A-Za-z]{16,}\b`),
	// Slack tokens (bot, app, refresh, signing, user).
	regexp.MustCompile(`\bxox[baprs]-[0-9]{10,}-[0-9]{10,}-[0-9A-Za-z]{10,}\b`),
	// Generic Bearer token.
	regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/-]{20,}\b`),
	// Generic key=value credentials in text/URLs.
	regexp.MustCompile(`(?i)\b(api[_-]?key|secret|password|token|bearer)\s*[:=]\s*["']?[A-Za-z0-9._~+/=-]{8,}["']?`),
}

const redacted = "[REDACTED]"

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
	for _, re := range credentialPatterns {
		s = re.ReplaceAllString(s, redacted)
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
	for _, re := range credentialPatterns {
		if re.MatchString(s) {
			return true
		}
	}
	return false
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
	for _, re := range credentialPatterns {
		if m := re.FindString(s); m != "" {
			return m
		}
	}
	return ""
}
