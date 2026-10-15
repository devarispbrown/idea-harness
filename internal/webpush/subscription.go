package webpush

import (
	"crypto/ecdh"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// Subscription is the browser's PushSubscription, as delivered by
// PushSubscription.toJSON(): the push-service endpoint plus the user agent's
// public key and auth secret.
type Subscription struct {
	Endpoint string
	// P256dh is the user agent's uncompressed P-256 public key (65 bytes),
	// base64url. Padded or standard-alphabet spellings are accepted too.
	P256dh string
	// Auth is the 16-byte authentication secret, base64url.
	Auth string
}

// Validate reports whether the subscription is usable: an absolute https
// endpoint with a host and no userinfo, and keys of the right shape with
// p256dh on the curve. It is meant to run when a subscription is first
// stored, so a bad one is rejected at the API boundary rather than failing
// every delivery later.
func (s Subscription) Validate() error { return s.validate(false) }

// validate is Validate with the scheme check relaxable for Sender's
// AllowInsecure (tests against httptest servers, which are plain http).
func (s Subscription) validate(allowHTTP bool) error {
	if _, err := endpointURL(s.Endpoint, allowHTTP); err != nil {
		return err
	}
	_, _, err := s.decodeKeys()
	return err
}

// endpointURL parses and checks an endpoint. Error strings deliberately do
// not echo the endpoint: its path is a bearer capability for pushing to the
// user, so it must not end up in logs.
func endpointURL(endpoint string, allowHTTP bool) (*url.URL, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, errors.New("webpush: endpoint is not a valid URL")
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && allowHTTP:
	default:
		return nil, errors.New("webpush: endpoint must be an https URL")
	}
	if u.Host == "" || u.Hostname() == "" {
		return nil, errors.New("webpush: endpoint has no host")
	}
	// Userinfo has no meaning for a push service and would be sent as Basic
	// auth by net/http; its only realistic use is smuggling or confusion.
	if u.User != nil {
		return nil, fmt.Errorf("webpush: endpoint for %s must not carry userinfo", u.Hostname())
	}
	if u.Opaque != "" {
		return nil, errors.New("webpush: endpoint is not a hierarchical URL")
	}
	return u, nil
}

// decodeKeys decodes and checks the subscription's key material. Error
// strings name the field, never its value: both are secrets of the user's
// subscription.
func (s Subscription) decodeKeys() (*ecdh.PublicKey, []byte, error) {
	raw, err := decodeB64(s.P256dh)
	if err != nil {
		return nil, nil, errors.New("webpush: p256dh is not valid base64")
	}
	if len(raw) != pointLen {
		return nil, nil, fmt.Errorf("webpush: p256dh must be %d bytes, got %d", pointLen, len(raw))
	}
	// NewPublicKey rejects compressed encodings, the point at infinity and
	// points not on P-256. The last matters: ECDH against an off-curve point
	// is the classic invalid-curve attack surface.
	pub, err := ecdh.P256().NewPublicKey(raw)
	if err != nil {
		return nil, nil, errors.New("webpush: p256dh is not a valid P-256 point")
	}
	auth, err := decodeB64(s.Auth)
	if err != nil {
		return nil, nil, errors.New("webpush: auth is not valid base64")
	}
	if len(auth) != authLen {
		return nil, nil, fmt.Errorf("webpush: auth must be %d bytes, got %d", authLen, len(auth))
	}
	return pub, auth, nil
}

// endpointHost is the only part of an endpoint safe to put in an error.
func endpointHost(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil || u.Hostname() == "" {
		return "push service"
	}
	return strings.ToLower(u.Hostname())
}
