package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestNotFoundErrorMessage(t *testing.T) {
	err := notFound("change set", "CHG-99")
	want := `no change set matching "CHG-99"`
	if err.Error() != want {
		t.Fatalf("Error() = %q, want %q", err.Error(), want)
	}
	// Callers that predate the typed error still match on sql.ErrNoRows.
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("NotFoundError should unwrap to sql.ErrNoRows")
	}
	// The message is what the user sees; it must never be the driver's.
	if strings.Contains(err.Error(), "sql:") {
		t.Fatalf("user-facing message leaks the driver error: %q", err.Error())
	}
}

func TestNotFoundIfNoRowsPassesOtherErrorsThrough(t *testing.T) {
	real := errors.New("disk on fire")
	if got := notFoundIfNoRows(real, "initiative", "ini-1"); got != real {
		t.Fatalf("a real failure must not be disguised as not-found, got %v", got)
	}
	var nf *NotFoundError
	if !errors.As(notFoundIfNoRows(sql.ErrNoRows, "initiative", "ini-1"), &nf) {
		t.Fatal("sql.ErrNoRows should map to NotFoundError")
	}
}

// A reference is echoed back into the user's terminal, so control characters
// (ANSI escapes) are stripped and the length is bounded.
func TestNotFoundErrorSanitizesTheReference(t *testing.T) {
	err := notFound("change set", "CHG-\x1b[2J\x1b[H99")
	if strings.ContainsRune(err.Error(), '\x1b') {
		t.Fatalf("escape sequence survived into the message: %q", err.Error())
	}
	long := notFound("change set", strings.Repeat("A", 500))
	if len(long.Error()) > maxRefLen+64 {
		t.Fatalf("message not truncated: %d bytes", len(long.Error()))
	}
}
