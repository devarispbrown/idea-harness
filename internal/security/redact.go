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
	// GitHub personal access token.
	regexp.MustCompile(`\bghp_[A-Za-z0-9]{20,}\b`),
	// OpenAI / generic sk- keys.
	regexp.MustCompile(`\bsk-[A-Za-z0-9]{20,}\b`),
	// Stripe live secret key.
	regexp.MustCompile(`\bsk_live_[0-9A-Za-z]{16,}\b`),
	// Slack bot token (xoxb-).
	regexp.MustCompile(`\bxoxb-[0-9]{10,}-[0-9]{10,}-[0-9A-Za-z]{10,}\b`),
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
