package model

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"unicode"
)

// maxErrBody bounds how much of a provider's error response is read into the
// message. Provider errors are short JSON objects; the cap exists so a provider
// answering an error with an HTML page or a stream cannot put a megabyte into a
// log line, a Finding, or a stored artifact.
const maxErrBody = 2048

// httpError builds the error for a non-200 provider response, including the
// body the provider sent.
//
// The body is the whole point. `openai: status 400` was the message for every
// OpenAI failure, and the response it discarded said:
//
//	max_tokens is too large: 32000. This model supports at most 16384
//	completion tokens, whereas you provided 32000.
//
// That defect was in the tree from the initial commit and shipped in v0.1.0.
// It took four DX audits to find because no audit had made a real provider
// call, and once found it was undiagnosable from inside the product -- `idea
// logs`, `idea doctor` and `idea status` all showed only `status 400`. Gemini
// already included its body, which is why the equivalent Gemini failure was
// diagnosed in minutes.
//
// A provider that echoes the request back can echo the Authorization header or
// an inline key, so the body is scrubbed for anything that looks like a
// credential before it goes anywhere. This is deliberately a local, dependency
// -free scrub rather than internal/security's catalog: internal/model must not
// import it (security imports config, config imports model), and the failure
// mode to avoid here is a key reaching a log, not a full redaction contract.
//
// model is the id that was actually sent (req.Model), so a 404 -- which is
// always the model id, every provider's own docs agree on that -- can name it
// and say how to change it. An ICP walkthrough hit exactly this: a stale
// catalog entry (gemini-2.5-flash, retired for new accounts) 404'd on the
// first real call, and the message was indistinguishable from any other
// non-200 status. Diagnosing "which config value do I even change" cost the
// walkthrough real time that `idea model set <provider>/<model>` or Settings
// -> Providers would have saved in one line.
func httpError(provider, model string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrBody))
	msg := scrubForLog(string(body))
	if resp.StatusCode == http.StatusNotFound {
		detail := ""
		if msg != "" {
			detail = ": " + msg
		}
		return fmt.Errorf("%s: model %q not found (status 404)%s -- "+
			"this model id is unknown, retired, or unavailable on your account; "+
			"change it with `idea model set %s/<model>` (or Settings → Providers in the web UI)",
			provider, model, detail, provider)
	}
	if msg == "" {
		return fmt.Errorf("%s: status %d (no response body)", provider, resp.StatusCode)
	}
	return fmt.Errorf("%s: status %d: %s", provider, resp.StatusCode, msg)
}

// scrubForLog collapses whitespace and removes anything shaped like a bearer
// token or an API key, so an echoed credential does not travel with the error.
func scrubForLog(s string) string {
	s = strings.Join(strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\t'
	}), " ")
	s = strings.TrimSpace(s)

	var b strings.Builder
	for _, field := range strings.Fields(s) {
		if looksLikeSecret(field) {
			b.WriteString("[REDACTED]")
		} else {
			b.WriteString(field)
		}
		b.WriteByte(' ')
	}
	return strings.TrimSpace(b.String())
}

// looksLikeSecret is deliberately blunt: a long unbroken run of credential-ish
// characters carrying both letters and digits, or any known key prefix. A false
// positive costs one word of an error message; a false negative writes a live
// key into a log.
func looksLikeSecret(field string) bool {
	trimmed := strings.Trim(field, `"',.:;{}[]()`)
	for _, prefix := range []string{"sk-", "sk_", "Bearer", "bearer", "AKIA", "ghp_", "xox"} {
		if strings.HasPrefix(trimmed, prefix) && len(trimmed) > len(prefix)+8 {
			return true
		}
	}
	if len(trimmed) < 24 {
		return false
	}
	var upper, lower, digits, other int
	for _, r := range trimmed {
		switch {
		case unicode.IsUpper(r):
			upper++
		case unicode.IsLower(r):
			lower++
		case unicode.IsDigit(r):
			digits++
		case r == '-' || r == '_':
		default:
			other++
		}
	}
	// Mixed case is what separates a key from the thing most worth keeping in a
	// provider error: the model id. `claude-3-7-sonnet-latest` is 24 characters
	// of letters, digits and hyphens and was redacted by an earlier version of
	// this rule -- eating the one token the reader needs. A lowercase slug is
	// never a key; a base64url secret essentially always carries both cases.
	return other == 0 && upper > 0 && lower > 0 && digits > 0
}

// ErrTruncated reports that the provider stopped because it hit the output
// budget rather than because the model finished.
//
// This is the safety net under the stage-budget cap. Capping a request to the
// provider's ceiling turns a guaranteed 400 into a request that succeeds -- but
// a structured reply cut mid-object is NOT reliably caught downstream:
// repairJSON closes unterminated strings and containers, and the review schema
// has no minItems, so a findings list truncated from 15 entries to 1 validates
// clean. The result would be a silently shorter adversarial review.
//
// So truncation is detected where it is actually visible -- the provider's own
// finish reason -- and raised loudly. The engine's retry loop resends the same
// request, so a truncation surfaces as a real failure with a message naming the
// cause instead of a quietly diminished artifact.
var ErrTruncated = errors.New("model: output truncated at the token budget")

// truncatedError names the model and the budget that was actually sent (not
// the stage's raw "want" -- capOutput may already have reduced it), because
// that is the number that was actually hit.
//
// It deliberately does NOT suggest a fix: this package has no idea whether
// the caller can raise the ceiling, split the request, or is even running
// inside the workflow engine at all, and a suggestion this layer cannot back
// is exactly the defect being closed here (model.truncatedError used to say
// "raise the stage budget", and harness.yaml's review.max_tokens -- the one
// config field a human would try -- did nothing: internal/workflow/engine.go
// computed the request's MaxOutput without ever reading it). The caller that
// DOES know what is reachable (internal/workflow.wrapTruncationGuidance)
// adds that guidance when it surfaces this to a human.
func truncatedError(provider, model string, sentMaxOutput int) error {
	return fmt.Errorf("%s: %w: model %q hit its %d-token output budget and was cut off mid-structure, "+
		"so the reply is not safe to use", provider, ErrTruncated, model, sentMaxOutput)
}

// capOutput bounds a requested budget by ceiling -- the model's real output
// ceiling, already resolved by the caller (MaxOutputFor(req.Model, ...)) so
// this function stays a pure, providerless clamp. A ceiling of 0 means "not
// stated" and is left alone.
//
// This is deliberately duplicated with workflow.effectiveMaxOutput rather than
// replacing it. The workflow cap is what lets a stage ask for more than one
// provider allows and still work across all of them; this one makes the
// invariant true at the boundary that actually owns the number, so no caller --
// including a future one that is not the workflow engine -- can emit a request
// the provider will reject. The defect this closes shipped in v0.1.0 and took
// four audits and a real API key to find, so both halves stay.
//
// Capping here is safe only because truncation is now detected from the
// provider's finish reason: a reply cut short raises ErrTruncated instead of
// being quietly accepted.
func capOutput(want, ceiling int) int {
	if ceiling > 0 && want > ceiling {
		return ceiling
	}
	return want
}
