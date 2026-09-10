package daemon

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// decodeStrict decodes a request body and refuses a field the target does not
// declare, returning a message that names it.
//
// The two config-writing endpoints used the permissive decoder, and
// `encoding/json` drops an unknown field in silence. So
// `PUT …/providers {"providers":{"openai":{"api_key":"sk-live-…"}}}` answered
// `200 {"status":"updated"}` having discarded the field the caller sent to
// carry the credential -- and written `openai: {}`, which ResolveProvider fills
// from the preset, so the write really did configure OpenAI and the demo banner
// correctly went away. DX audit 7 observed billed gpt-4o calls follow. The spec
// says "Inline key values are rejected"; they were ignored, which is the
// failure mode that costs money.
//
// The asymmetry was never client-vs-server: config.Load already validates the
// YAML document strictly, rejecting unknown fields and inline secrets by path.
// It was YAML-strict, JSON-lenient.
//
// The unknown-field error is returned verbatim because it names a key the
// caller sent, not a value -- `json: unknown field "api_key"`. A syntax error
// is not: its text can quote the body, and a body that carries a credential is
// exactly the case this exists for.
func decodeStrict(r io.Reader, into any) error {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		if strings.HasPrefix(err.Error(), "json: unknown field ") {
			return err
		}
		// Everything else -- a syntax error, a type mismatch, an empty body --
		// is "invalid body". Deliberately not the decoder's text: it can quote
		// the body, and a body carrying a credential is exactly the case this
		// exists for.
		return errors.New("invalid body")
	}
	// One JSON value, and nothing after it. Decode stops at the end of the first
	// value, so `{"providers":{...}} {"api_key":"sk"}` was accepted and the
	// trailing object silently discarded -- the same class as the unknown field,
	// on the endpoint the fix exists for.
	if dec.More() {
		return errors.New("invalid body")
	}
	return nil
}
