// Package webpush delivers Web Push messages to browser push services
// without a third-party dependency: payload encryption per RFC 8291 (the
// aes128gcm content coding of RFC 8188) and application-server
// identification per RFC 8292 (VAPID), built only on the standard library.
//
// The package is deliberately small and strict. Everything that arrives from
// a browser (the PushSubscription) is untrusted input that names a URL this
// process will POST to, so Subscription.Validate and the Sender's default
// HTTP client together refuse anything that is not a public https endpoint.
// Errors returned to callers name the endpoint's host at most: the endpoint
// path and query are bearer capabilities (anyone holding them can push to the
// user), and the subscription keys and VAPID private key are secrets, so none
// of them may leak into logs through an error string.
package webpush

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

const (
	// recordSize is the RFC 8188 "rs" header field. Every message is a single
	// record, so rs only has to be at least as large as that record; 4096 is
	// what RFC 8291's own example uses and what every push service accepts.
	recordSize = 4096

	// maxBodySize is the largest encrypted body push services are required
	// to accept (RFC 8030 section 7.2 lets them reject anything larger with
	// 413). Enforcing it here turns a guaranteed-to-fail delivery into an
	// immediate, typed error at the call site.
	maxBodySize = 4096

	saltLen    = 16
	authLen    = 16
	pointLen   = 65 // uncompressed P-256 point: 0x04 || X(32) || Y(32)
	tagLen     = 16
	headerLen  = saltLen + 4 + 1 + pointLen
	recordOver = 1 + tagLen // padding delimiter + GCM tag

	// MaxPayloadSize is the largest plaintext Encrypt accepts: the 4096-byte
	// body budget minus the RFC 8188 header (with the 65-byte sender key as
	// keyid), the one-byte padding delimiter, and the GCM tag.
	MaxPayloadSize = maxBodySize - headerLen - recordOver
)

// ErrPayloadTooLarge is returned when the plaintext would encrypt to a body
// larger than push services accept. Callers should shrink the payload (send
// an id and let the client fetch details) rather than retry.
var ErrPayloadTooLarge = fmt.Errorf("webpush: payload exceeds %d bytes", MaxPayloadSize)

// Encrypt produces the aes128gcm request body for plaintext addressed to
// sub: the RFC 8188 header (salt, rs, idlen, keyid = a fresh sender public
// key) followed by a single encrypted record.
//
// A new ephemeral sender key and salt are drawn for every message. RFC 8291
// requires this: the content key is derived from the ECDH secret and the
// salt, so reusing either across messages would reuse an AES-GCM key/nonce
// pair, which is catastrophic for GCM.
func Encrypt(plaintext []byte, sub Subscription) ([]byte, error) {
	uaPub, auth, err := sub.decodeKeys()
	if err != nil {
		return nil, err
	}
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("webpush: generate sender key: %w", err)
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("webpush: generate salt: %w", err)
	}
	return encrypt(plaintext, uaPub, auth, asPriv, salt)
}

// encrypt is the deterministic core of Encrypt. Taking the sender key and
// salt as parameters is what lets the tests reproduce the RFC 8291 Appendix A
// vector byte for byte; production code must only reach it through Encrypt.
func encrypt(plaintext []byte, uaPub *ecdh.PublicKey, auth []byte, asPriv *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(plaintext) > MaxPayloadSize {
		return nil, ErrPayloadTooLarge
	}
	if len(salt) != saltLen {
		return nil, errors.New("webpush: salt must be 16 bytes")
	}
	if len(auth) != authLen {
		return nil, errors.New("webpush: auth secret must be 16 bytes")
	}
	secret, err := asPriv.ECDH(uaPub)
	if err != nil {
		return nil, fmt.Errorf("webpush: ecdh: %w", err)
	}
	asPub := asPriv.PublicKey().Bytes()
	_, cek, nonce, err := deriveKeys(secret, auth, uaPub.Bytes(), asPub, salt)
	if err != nil {
		return nil, err
	}

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// Single (and therefore last) record: plaintext followed by the 0x02
	// delimiter and no further padding. Padding would hide the payload length
	// from the push service, but it spends the 4 KiB budget and the push
	// service already sees the request size; keeping the record minimal is
	// what RFC 8291's example does too.
	record := make([]byte, 0, len(plaintext)+1)
	record = append(record, plaintext...)
	record = append(record, 0x02)

	body := make([]byte, headerLen, headerLen+len(record)+tagLen)
	copy(body, salt)
	binary.BigEndian.PutUint32(body[saltLen:], recordSize)
	body[saltLen+4] = pointLen
	copy(body[saltLen+5:], asPub)
	// RFC 8188 nonces are NONCE XOR record sequence number; with one record
	// the sequence number is 0, so the derived nonce is used unchanged.
	body = gcm.Seal(body, nonce, record, nil)
	return body, nil
}

// deriveKeys runs the RFC 8291 section 3.4 key schedule. It returns IKM as
// well as the content-encryption key and nonce purely so tests can check the
// intermediate values the RFC publishes; a wrong intermediate pinpoints which
// step drifted, where a mismatched ciphertext alone would not.
func deriveKeys(ecdhSecret, auth, uaPub, asPub, salt []byte) (ikm, cek, nonce []byte, err error) {
	// key_info binds both public keys into the IKM so the derived key is
	// specific to this (user agent, sender) pair, not just the shared secret.
	keyInfo := make([]byte, 0, 14+2*pointLen)
	keyInfo = append(keyInfo, "WebPush: info\x00"...)
	keyInfo = append(keyInfo, uaPub...)
	keyInfo = append(keyInfo, asPub...)

	ikm, err = hkdf.Key(sha256.New, ecdhSecret, auth, string(keyInfo), 32)
	if err != nil {
		return nil, nil, nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, nil, nil, err
	}
	cek, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, nil, nil, err
	}
	nonce, err = hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, nil, nil, err
	}
	return ikm, cek, nonce, nil
}

// decodeB64 accepts any of the base64 spellings browsers and client
// libraries actually produce for subscription keys. The Push API specifies
// unpadded base64url, but toJSON() output gets round-tripped through code
// that re-pads it or converts it to the standard alphabet, and rejecting
// those would silently drop real subscriptions over a cosmetic difference.
func decodeB64(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, "=")
	s = strings.NewReplacer("+", "-", "/", "_").Replace(s)
	return base64.RawURLEncoding.DecodeString(s)
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
