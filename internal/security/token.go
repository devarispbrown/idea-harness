package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
)

// LoadOrCreateToken returns the per-install API token, creating it (0600) if
// absent. The token authenticates every local-API request (§15).
func LoadOrCreateToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil {
		t := string(b)
		if len(t) >= 32 {
			return t, nil
		}
		// Corrupt/too-short token: regenerate.
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("security: token dir: %w", err)
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return "", fmt.Errorf("security: write token: %w", err)
	}
	return token, nil
}

func newToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("security: rand: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// TokenEqual compares two tokens in constant time.
func TokenEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
