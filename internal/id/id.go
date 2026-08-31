// Package id generates opaque identifiers and scoped human-readable codes.
// Identifiers are cryptographically random; codes are deterministic and
// project-scoped (monotonic) per the plan (§6).
package id

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// New returns a 128-bit random identifier encoded as 32 lowercase hex chars.
func New() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is unrecoverable for identifier generation.
		panic(fmt.Sprintf("id: crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b[:])
}

// Code returns a human-readable scoped code, e.g. Code("REQ", 17) => "REQ-17".
func Code(prefix string, seq int) string {
	return fmt.Sprintf("%s-%d", prefix, seq)
}
