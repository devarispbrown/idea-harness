package webpush

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// defaultVAPIDExpiry is the token lifetime when the caller passes 0.
	// Long enough that clock skew between us and the push service never
	// matters, comfortably under the 24h ceiling.
	defaultVAPIDExpiry = 12 * time.Hour
	// maxVAPIDExpiry is RFC 8292 section 2's ceiling; push services reject
	// tokens whose exp is further out than 24h.
	maxVAPIDExpiry = 24 * time.Hour

	keysFile = "vapid.json"
)

// Keys is a VAPID application-server key pair (P-256). The public half is
// what the browser receives as applicationServerKey when subscribing; every
// push must then be signed by the private half or the push service refuses
// it, which is what ties a subscription to this server.
type Keys struct {
	priv *ecdsa.PrivateKey
}

// GenerateKeys creates a fresh key pair.
func GenerateKeys() (*Keys, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("webpush: generate vapid key: %w", err)
	}
	return &Keys{priv: priv}, nil
}

// PublicKey is the uncompressed 65-byte public point, base64url without
// padding: exactly the applicationServerKey format pushManager.subscribe
// expects.
func (k *Keys) PublicKey() string { return b64(k.publicBytes()) }

func (k *Keys) publicBytes() []byte {
	b, err := k.priv.PublicKey.Bytes()
	if err != nil {
		// Unreachable for a P-256 key built by this package.
		panic("webpush: encode public key: " + err.Error())
	}
	return b
}

// keysJSON is the on-disk form of Keys. The public key is stored redundantly
// so a load can prove the file is internally consistent (see LoadOrCreateKeys)
// and so an operator can read it without tooling.
type keysJSON struct {
	PublicKey  string `json:"public_key"`
	PrivateKey string `json:"private_key"`
	CreatedAt  string `json:"created_at"`
}

// LoadOrCreateKeys returns the key pair persisted at <dir>/vapid.json,
// generating and writing one on first use.
//
// Stability matters more than anything else here: every browser
// subscription is bound to the public key it was created with, so silently
// minting a new pair (because the file was unreadable or corrupt) would
// orphan every existing subscription. Any problem with an existing file is
// therefore an error, never a regeneration.
func LoadOrCreateKeys(dir string) (*Keys, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("webpush: create key dir: %w", err)
	}
	path := filepath.Join(dir, keysFile)
	k, err := loadKeys(path)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return k, err
	}

	k, err = GenerateKeys()
	if err != nil {
		return nil, err
	}
	d, err := k.priv.Bytes()
	if err != nil {
		return nil, fmt.Errorf("webpush: encode private key: %w", err)
	}
	data, err := json.MarshalIndent(keysJSON{
		PublicKey:  k.PublicKey(),
		PrivateKey: b64(d),
		CreatedAt:  time.Now().UTC().Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')

	created, err := writeNoClobber(dir, path, data)
	if err != nil {
		return nil, err
	}
	if !created {
		// Another process won the race to create the file; its keys are the
		// ones subscriptions will be bound to, so use them, not ours.
		return loadKeys(path)
	}
	return k, nil
}

// writeNoClobber writes data to path atomically (temp file + link/rename,
// so a crash never leaves a half-written key file) and reports whether it
// created the file. Linking rather than renaming makes creation exclusive:
// two daemons starting at once must not each believe their own freshly
// generated pair is the persistent one.
func writeNoClobber(dir, path string, data []byte) (bool, error) {
	tmp, err := os.CreateTemp(dir, ".vapid-*.tmp")
	if err != nil {
		return false, fmt.Errorf("webpush: write keys: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	// CreateTemp already uses 0600; the explicit chmod documents the intent
	// and survives an unusual umask-free platform default.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return false, fmt.Errorf("webpush: write keys: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return false, fmt.Errorf("webpush: write keys: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return false, fmt.Errorf("webpush: write keys: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("webpush: write keys: %w", err)
	}
	err = os.Link(tmpName, path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrExist) {
		return false, nil
	}
	// Some filesystems (certain FUSE/network mounts) lack hard links. Fall
	// back to rename: still atomic, merely not exclusive.
	if err := os.Rename(tmpName, path); err != nil {
		return false, fmt.Errorf("webpush: write keys: %w", err)
	}
	return true, nil
}

// loadKeys reads and validates an existing key file. Errors name the path
// but never the file's contents.
func loadKeys(path string) (*Keys, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("webpush: read %s: %w", path, err)
	}
	// The private key must not be readable by other users. Tightening the
	// mode (rather than refusing to start) fixes a file restored from a
	// backup or copied with a loose umask without stranding subscriptions.
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(path, 0o600); err != nil {
			return nil, fmt.Errorf("webpush: tighten permissions on %s: %w", path, err)
		}
	}

	var kj keysJSON
	if err := json.Unmarshal(data, &kj); err != nil {
		return nil, fmt.Errorf("webpush: %s is not valid JSON", path)
	}
	d, err := decodeB64(kj.PrivateKey)
	if err != nil || len(d) != 32 {
		return nil, fmt.Errorf("webpush: %s: private_key is not a base64url 32-byte scalar", path)
	}
	priv, err := ecdsa.ParseRawPrivateKey(elliptic.P256(), d)
	if err != nil {
		return nil, fmt.Errorf("webpush: %s: private_key is not a valid P-256 key", path)
	}
	k := &Keys{priv: priv}
	// The stored public key is what browsers were given. If it does not
	// derive from the private key, every push would be signed by a key the
	// subscriptions do not trust, so fail loudly instead of sending pushes
	// the push service will reject one by one.
	stored, err := decodeB64(kj.PublicKey)
	if err != nil || !bytes.Equal(stored, k.publicBytes()) {
		return nil, fmt.Errorf("webpush: %s: public_key does not match private_key", path)
	}
	return k, nil
}

// VAPIDAuthorization returns the Authorization header value
// "vapid t=<jwt>, k=<public key>" (RFC 8292 section 3) for endpoint.
//
// expiry 0 means 12h; anything above 24h is capped at 24h because push
// services reject longer-lived tokens. subject is the contact the push
// service may use to reach the operator and must be a mailto: or https: URI.
func (k *Keys) VAPIDAuthorization(endpoint, subject string, now time.Time, expiry time.Duration) (string, error) {
	u, err := endpointURL(endpoint, true)
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(subject, "mailto:") && !strings.HasPrefix(subject, "https://") {
		return "", errors.New(`webpush: vapid subject must start with "mailto:" or "https://"`)
	}
	switch {
	case expiry < 0:
		return "", errors.New("webpush: vapid expiry must not be negative")
	case expiry == 0:
		expiry = defaultVAPIDExpiry
	case expiry > maxVAPIDExpiry:
		expiry = maxVAPIDExpiry
	}

	// The audience is the push service's origin, not the full endpoint: the
	// token is then reusable across subscriptions on the same service, and
	// the per-subscription path never appears inside a token a push service
	// may log.
	claims, err := json.Marshal(struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}{
		Aud: u.Scheme + "://" + strings.ToLower(u.Host),
		Exp: now.Add(expiry).Unix(),
		Sub: subject,
	})
	if err != nil {
		return "", err
	}
	signingInput := b64([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + b64(claims)

	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, k.priv, digest[:])
	if err != nil {
		return "", fmt.Errorf("webpush: sign vapid token: %w", err)
	}
	// JWS ES256 signatures are the fixed-width concatenation r||s (RFC 7518
	// section 3.4), not the ASN.1 DER that ecdsa.SignASN1 produces; push
	// services reject DER.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])

	return "vapid t=" + signingInput + "." + b64(sig) + ", k=" + k.PublicKey(), nil
}
