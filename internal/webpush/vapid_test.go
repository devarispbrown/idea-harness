package webpush

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type vapidClaims struct {
	Aud string `json:"aud"`
	Exp int64  `json:"exp"`
	Sub string `json:"sub"`
}

// verifyVAPID parses an Authorization header the way a push service does:
// split t= and k=, check the JWT header, and verify the raw r||s ES256
// signature against k with crypto/ecdsa.
func verifyVAPID(header string) (vapidClaims, string, error) {
	var c vapidClaims
	rest, ok := strings.CutPrefix(header, "vapid ")
	if !ok {
		return c, "", fmt.Errorf("scheme is not vapid: %q", header)
	}
	var tok, k string
	for _, part := range strings.Split(rest, ",") {
		part = strings.TrimSpace(part)
		switch {
		case strings.HasPrefix(part, "t="):
			tok = part[2:]
		case strings.HasPrefix(part, "k="):
			k = part[2:]
		}
	}
	if tok == "" || k == "" {
		return c, "", fmt.Errorf("missing t or k")
	}
	pubRaw, err := decodeB64(k)
	if err != nil {
		return c, "", err
	}
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pubRaw)
	if err != nil {
		return c, "", err
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return c, "", fmt.Errorf("jwt has %d parts", len(parts))
	}
	hdr, err := decodeB64(parts[0])
	if err != nil {
		return c, "", err
	}
	var h map[string]string
	if err := json.Unmarshal(hdr, &h); err != nil {
		return c, "", err
	}
	if h["alg"] != "ES256" || h["typ"] != "JWT" {
		return c, "", fmt.Errorf("jwt header %v", h)
	}
	sig, err := decodeB64(parts[2])
	if err != nil {
		return c, "", err
	}
	if len(sig) != 64 {
		return c, "", fmt.Errorf("signature is %d bytes, want raw 64", len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(pub, digest[:], r, s) {
		return c, "", fmt.Errorf("bad signature")
	}
	payload, err := decodeB64(parts[1])
	if err != nil {
		return c, "", err
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return c, "", err
	}
	return c, k, nil
}

func TestVAPIDAuthorization(t *testing.T) {
	keys, err := GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := decodeB64(keys.PublicKey()); len(raw) != 65 || raw[0] != 0x04 {
		t.Fatalf("public key is not an uncompressed point: %d bytes", len(raw))
	}
	if strings.Contains(keys.PublicKey(), "=") {
		t.Fatal("public key is padded")
	}
	now := time.Unix(1_700_000_000, 0)

	cases := []struct {
		name     string
		endpoint string
		expiry   time.Duration
		wantAud  string
		wantExp  time.Duration
	}{
		{"default expiry", "https://fcm.googleapis.com/fcm/send/abc?x=1", 0, "https://fcm.googleapis.com", 12 * time.Hour},
		{"explicit", "https://Updates.Push.Services.Mozilla.com/wpush/v2/xyz", time.Hour, "https://updates.push.services.mozilla.com", time.Hour},
		{"capped", "https://web.push.apple.com/abc", 72 * time.Hour, "https://web.push.apple.com", 24 * time.Hour},
		{"port kept", "https://push.example.com:8443/p", 0, "https://push.example.com:8443", 12 * time.Hour},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, err := keys.VAPIDAuthorization(tc.endpoint, "mailto:ops@example.com", now, tc.expiry)
			if err != nil {
				t.Fatal(err)
			}
			c, k, err := verifyVAPID(h)
			if err != nil {
				t.Fatal(err)
			}
			if k != keys.PublicKey() {
				t.Errorf("k = %s, want %s", k, keys.PublicKey())
			}
			if c.Aud != tc.wantAud {
				t.Errorf("aud = %q, want %q", c.Aud, tc.wantAud)
			}
			if c.Exp != now.Add(tc.wantExp).Unix() {
				t.Errorf("exp = %d, want now+%s", c.Exp, tc.wantExp)
			}
			if c.Sub != "mailto:ops@example.com" {
				t.Errorf("sub = %q", c.Sub)
			}
			if strings.Contains(h, "abc") || strings.Contains(h, "xyz") {
				t.Error("endpoint path leaked into the token")
			}
		})
	}

	// A signature from different keys must not verify against k.
	other, _ := GenerateKeys()
	h, _ := other.VAPIDAuthorization("https://push.example.com/p", "https://example.com", now, 0)
	tok := strings.SplitN(h, ", k=", 2)[0]
	if _, _, err := verifyVAPID(tok + ", k=" + keys.PublicKey()); err == nil {
		t.Fatal("foreign signature verified")
	}
}

func TestVAPIDRejects(t *testing.T) {
	keys, _ := GenerateKeys()
	now := time.Now()
	for _, sub := range []string{"", "ops@example.com", "http://example.com", "MAILTO:x", "javascript:alert(1)"} {
		if _, err := keys.VAPIDAuthorization("https://push.example.com/p", sub, now, 0); err == nil {
			t.Errorf("subject %q accepted", sub)
		}
	}
	if _, err := keys.VAPIDAuthorization("https://push.example.com/p", "mailto:a@b.c", now, -time.Second); err == nil {
		t.Error("negative expiry accepted")
	}
	if _, err := keys.VAPIDAuthorization("not a url", "mailto:a@b.c", now, 0); err == nil {
		t.Error("bad endpoint accepted")
	}
}

func TestLoadOrCreateKeys(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "keys")
	k1, err := LoadOrCreateKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "vapid.json")
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("file mode %o, want 600", fi.Mode().Perm())
		}
		di, _ := os.Stat(dir)
		if di.Mode().Perm() != 0o700 {
			t.Errorf("dir mode %o, want 700", di.Mode().Perm())
		}
	}
	var kj keysJSON
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &kj); err != nil {
		t.Fatal(err)
	}
	if kj.PublicKey != k1.PublicKey() {
		t.Error("stored public key differs")
	}
	if d, _ := decodeB64(kj.PrivateKey); len(d) != 32 {
		t.Errorf("private key is %d bytes", len(d))
	}
	if _, err := time.Parse(time.RFC3339, kj.CreatedAt); err != nil {
		t.Errorf("created_at: %v", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, ".vapid-*")); len(matches) != 0 {
		t.Errorf("temp files left behind: %v", matches)
	}

	k2, err := LoadOrCreateKeys(dir)
	if err != nil {
		t.Fatal(err)
	}
	if k2.PublicKey() != k1.PublicKey() {
		t.Fatal("reload produced a different key")
	}
	// The reloaded private key signs tokens the original public key verifies.
	h, err := k2.VAPIDAuthorization("https://push.example.com/p", "mailto:a@b.c", time.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, k, err := verifyVAPID(h); err != nil || k != k1.PublicKey() {
		t.Fatalf("reloaded key signature: k=%s err=%v", k, err)
	}

	if runtime.GOOS != "windows" {
		// Loose permissions are tightened on load.
		os.Chmod(path, 0o644)
		if _, err := LoadOrCreateKeys(dir); err != nil {
			t.Fatal(err)
		}
		fi, _ := os.Stat(path)
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("mode after reload %o, want 600", fi.Mode().Perm())
		}
	}
}

func TestLoadOrCreateKeysConcurrent(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	pubs := make([]string, 8)
	errs := make([]error, 8)
	for i := range pubs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k, err := LoadOrCreateKeys(dir)
			errs[i] = err
			if err == nil {
				pubs[i] = k.PublicKey()
			}
		}()
	}
	wg.Wait()
	for i := range pubs {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if pubs[i] != pubs[0] {
			t.Fatal("concurrent creators disagree on the key pair")
		}
	}
}

func TestLoadOrCreateKeysRejectsBadFiles(t *testing.T) {
	good, _ := GenerateKeys()
	other, _ := GenerateKeys()
	d, _ := good.priv.Bytes()
	cases := map[string]string{
		"not json":       "{nope",
		"empty":          "",
		"short private":  `{"public_key":"` + good.PublicKey() + `","private_key":"AAAA"}`,
		"bad base64":     `{"public_key":"` + good.PublicKey() + `","private_key":"!!!"}`,
		"zero scalar":    `{"public_key":"` + good.PublicKey() + `","private_key":"` + b64(make([]byte, 32)) + `"}`,
		"mismatched pub": `{"public_key":"` + other.PublicKey() + `","private_key":"` + b64(d) + `"}`,
		"missing pub":    `{"private_key":"` + b64(d) + `"}`,
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "vapid.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadOrCreateKeys(dir)
			if err == nil {
				t.Fatal("bad key file accepted")
			}
			if strings.Contains(err.Error(), b64(d)) {
				t.Fatal("error leaks the private key")
			}
			// Crucially, the file was not replaced by a fresh pair.
			after, _ := os.ReadFile(path)
			if string(after) != content {
				t.Fatal("bad key file was overwritten")
			}
		})
	}
}
