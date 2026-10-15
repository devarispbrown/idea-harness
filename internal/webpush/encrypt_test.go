package webpush

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// RFC 8291 Appendix A.
const (
	rfcPlaintext = "When I grow up, I want to be a watermelon"
	rfcASPrivate = "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"
	rfcASPublic  = "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8"
	rfcUAPrivate = "q1dXpw3UpT5VOmu_cf_v6ih07Aems3njxI-JWgLcM94"
	rfcUAPublic  = "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"
	rfcAuth      = "BTBZMqHH6r4Tts7J_aSIgg"
	rfcSalt      = "DGv6ra1nlYgDCS1FRnbzlw"
	rfcECDH      = "kyrL1jIIOHEzg3sM2ZWRHDRB62YACZhhSlknJ672kSs"
	rfcIKM       = "S4lYMb_L0FxCeq0WhDx813KgSYqU26kOyzWUdsXYyrg"
	rfcCEK       = "oIhVW04MRdy2XN9CiKLxTg"
	rfcNonce     = "4h_95klXJ5E_qnoN"
	rfcBody      = "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
)

func mustB64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return b
}

func TestRFC8291Vector(t *testing.T) {
	asPriv, err := ecdh.P256().NewPrivateKey(mustB64(t, rfcASPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if got := b64(asPriv.PublicKey().Bytes()); got != rfcASPublic {
		t.Fatalf("as_public = %s, want %s", got, rfcASPublic)
	}
	uaPriv, err := ecdh.P256().NewPrivateKey(mustB64(t, rfcUAPrivate))
	if err != nil {
		t.Fatal(err)
	}
	if got := b64(uaPriv.PublicKey().Bytes()); got != rfcUAPublic {
		t.Fatalf("ua_public = %s, want %s", got, rfcUAPublic)
	}
	sub := Subscription{Endpoint: "https://push.example.net/push/JzLQ3raZJfFBR0aqvOMsLrt54w4rJUsV", P256dh: rfcUAPublic, Auth: rfcAuth}
	if err := sub.Validate(); err != nil {
		t.Fatal(err)
	}
	uaPub, auth, err := sub.decodeKeys()
	if err != nil {
		t.Fatal(err)
	}

	// Intermediates first, so a regression names the step that drifted.
	secret, err := asPriv.ECDH(uaPub)
	if err != nil {
		t.Fatal(err)
	}
	if got := b64(secret); got != rfcECDH {
		t.Errorf("ecdh_secret = %s, want %s", got, rfcECDH)
	}
	ikm, cek, nonce, err := deriveKeys(secret, auth, uaPub.Bytes(), asPriv.PublicKey().Bytes(), mustB64(t, rfcSalt))
	if err != nil {
		t.Fatal(err)
	}
	if got := b64(ikm); got != rfcIKM {
		t.Errorf("IKM = %s, want %s", got, rfcIKM)
	}
	if got := b64(cek); got != rfcCEK {
		t.Errorf("CEK = %s, want %s", got, rfcCEK)
	}
	if got := b64(nonce); got != rfcNonce {
		t.Errorf("NONCE = %s, want %s", got, rfcNonce)
	}

	body, err := encrypt([]byte(rfcPlaintext), uaPub, auth, asPriv, mustB64(t, rfcSalt))
	if err != nil {
		t.Fatal(err)
	}
	if got := b64(body); got != rfcBody {
		t.Fatalf("body mismatch\n got %s\nwant %s", got, rfcBody)
	}

	// And the independent decryptor agrees with the RFC's own body.
	pt, err := testDecrypt(mustB64(t, rfcBody), uaPriv, auth)
	if err != nil {
		t.Fatal(err)
	}
	if string(pt) != rfcPlaintext {
		t.Fatalf("decrypted %q", pt)
	}
}

// testDecrypt is a receiver-side (user agent) implementation of RFC 8291 /
// RFC 8188, written independently of the package code (its own HKDF on
// crypto/hmac, its own header parsing) so round-trip tests cannot pass by
// sharing a bug with the sender.
func testDecrypt(body []byte, uaPriv *ecdh.PrivateKey, auth []byte) ([]byte, error) {
	if len(body) < 21 {
		return nil, errors.New("short header")
	}
	salt := body[:16]
	rs := binary.BigEndian.Uint32(body[16:20])
	idlen := int(body[20])
	if len(body) < 21+idlen {
		return nil, errors.New("short keyid")
	}
	keyid := body[21 : 21+idlen]
	ct := body[21+idlen:]
	if rs < 18 || uint32(len(ct)) > rs {
		return nil, fmt.Errorf("record of %d bytes exceeds rs %d", len(ct), rs)
	}
	asPub, err := ecdh.P256().NewPublicKey(keyid)
	if err != nil {
		return nil, err
	}
	secret, err := uaPriv.ECDH(asPub)
	if err != nil {
		return nil, err
	}
	extract := func(salt, ikm []byte) []byte {
		m := hmac.New(sha256.New, salt)
		m.Write(ikm)
		return m.Sum(nil)
	}
	expand := func(prk []byte, info []byte, n int) []byte { // n <= 32: one block
		m := hmac.New(sha256.New, prk)
		m.Write(info)
		m.Write([]byte{1})
		return m.Sum(nil)[:n]
	}
	info := append([]byte("WebPush: info\x00"), uaPriv.PublicKey().Bytes()...)
	info = append(info, keyid...)
	ikm := expand(extract(auth, secret), info, 32)
	prk := extract(salt, ikm)
	cek := expand(prk, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := expand(prk, []byte("Content-Encoding: nonce\x00"), 12)

	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, err
	}
	// Strip padding: trailing zeros, then the delimiter, which must be 0x02
	// for the last (only) record.
	i := len(pt) - 1
	for i >= 0 && pt[i] == 0 {
		i--
	}
	if i < 0 || pt[i] != 0x02 {
		return nil, errors.New("bad padding delimiter")
	}
	return pt[:i], nil
}

// newTestUA creates a user-agent key pair and the matching Subscription.
func newTestUA(t *testing.T, endpoint string) (*ecdh.PrivateKey, []byte, Subscription) {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	return priv, auth, Subscription{Endpoint: endpoint, P256dh: b64(priv.PublicKey().Bytes()), Auth: b64(auth)}
}

func TestEncryptRoundTrip(t *testing.T) {
	uaPriv, auth, sub := newTestUA(t, "https://push.example.com/x")
	for _, n := range []int{0, 1, 100, MaxPayloadSize} {
		t.Run(fmt.Sprint(n), func(t *testing.T) {
			pt := make([]byte, n)
			rand.Read(pt)
			body, err := Encrypt(pt, sub)
			if err != nil {
				t.Fatal(err)
			}
			if len(body) > maxBodySize {
				t.Fatalf("body %d bytes exceeds %d", len(body), maxBodySize)
			}
			if n == MaxPayloadSize && len(body) != maxBodySize {
				t.Fatalf("max payload body is %d bytes, want exactly %d", len(body), maxBodySize)
			}
			got, err := testDecrypt(body, uaPriv, auth)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, pt) {
				t.Fatal("plaintext mismatch")
			}

			// Flipping any bit of the salt, keyid or ciphertext must fail.
			for _, idx := range []int{0, 15, 30, headerLen, len(body) - 1} {
				bad := bytes.Clone(body)
				bad[idx] ^= 0x01
				if _, err := testDecrypt(bad, uaPriv, auth); err == nil {
					t.Fatalf("tampered byte %d decrypted", idx)
				}
			}
			// Wrong auth secret must fail too.
			wrong := bytes.Clone(auth)
			wrong[0] ^= 0xff
			if _, err := testDecrypt(body, uaPriv, wrong); err == nil {
				t.Fatal("decrypted with wrong auth secret")
			}
		})
	}
}

func TestEncryptFreshKeyAndSaltPerMessage(t *testing.T) {
	_, _, sub := newTestUA(t, "https://push.example.com/x")
	a, err := Encrypt([]byte("same"), sub)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Encrypt([]byte("same"), sub)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a[:16], b[:16]) {
		t.Fatal("salt reused")
	}
	if bytes.Equal(a[21:headerLen], b[21:headerLen]) {
		t.Fatal("sender key reused")
	}
}

func TestEncryptPayloadTooLarge(t *testing.T) {
	_, _, sub := newTestUA(t, "https://push.example.com/x")
	if MaxPayloadSize != 3993 {
		t.Fatalf("MaxPayloadSize = %d, want 3993 (4096 - 86 header - 1 delimiter - 16 tag)", MaxPayloadSize)
	}
	for _, n := range []int{MaxPayloadSize + 1, 4096, 1 << 20} {
		if _, err := Encrypt(make([]byte, n), sub); !errors.Is(err, ErrPayloadTooLarge) {
			t.Fatalf("%d bytes: err = %v, want ErrPayloadTooLarge", n, err)
		}
	}
}

func TestEncryptRejectsInvalidSubscription(t *testing.T) {
	_, _, sub := newTestUA(t, "https://push.example.com/x")
	sub.Auth = "AAAA"
	if _, err := Encrypt([]byte("x"), sub); err == nil {
		t.Fatal("expected error for short auth")
	}
}

func TestDecodeB64Spellings(t *testing.T) {
	raw := mustB64(t, rfcUAPublic)
	std := base64.StdEncoding.EncodeToString(raw)
	for _, s := range []string{
		rfcUAPublic,
		base64.URLEncoding.EncodeToString(raw),
		std,
		strings.TrimRight(std, "="),
		" " + rfcUAPublic + "\n",
	} {
		got, err := decodeB64(s)
		if err != nil || !bytes.Equal(got, raw) {
			t.Errorf("decodeB64(%q) = %x, %v", s, got, err)
		}
	}
}
