package store

import (
	"database/sql"
	"fmt"
	"strings"
	"unicode"
)

// maxRefLen bounds a reference echoed back in an error. A reference reaches the
// user's terminal through the CLI (cmd/idea/main.go writes the daemon's message
// to stderr), so it is bounded and stripped of control characters before it is
// quoted back.
const maxRefLen = 64

// NotFoundError is the store's answer for a lookup that matched nothing. It
// carries the kind and the reference the caller asked for, so every layer above
// can render "no change set matching \"CHG-99\"" instead of the driver's
// "sql: no rows in result set", and the daemon can map it to 404.
//
// It unwraps to sql.ErrNoRows so existing errors.Is checks keep working.
type NotFoundError struct {
	Kind string // "change set", "initiative", "requirement", …
	Ref  string // what the caller asked for
}

func (e *NotFoundError) Error() string {
	if e.Ref == "" {
		return fmt.Sprintf("no %s found", e.Kind)
	}
	return fmt.Sprintf("no %s matching %q", e.Kind, sanitizeRef(e.Ref))
}

func (e *NotFoundError) Unwrap() error { return sql.ErrNoRows }

// notFound builds a NotFoundError for kind/ref.
func notFound(kind, ref string) error { return &NotFoundError{Kind: kind, Ref: ref} }

// notFoundIfNoRows maps sql.ErrNoRows to a NotFoundError and passes anything
// else through unchanged, so a real database failure is never disguised as a
// missing row.
func notFoundIfNoRows(err error, kind, ref string) error {
	if err == sql.ErrNoRows {
		return notFound(kind, ref)
	}
	return err
}

// sanitizeRef makes a user-supplied reference safe to quote back into a
// terminal: control characters (ANSI escapes included) are dropped and the
// result is truncated.
func sanitizeRef(ref string) string {
	var b strings.Builder
	for _, r := range ref {
		if unicode.IsControl(r) {
			continue
		}
		b.WriteRune(r)
		if b.Len() >= maxRefLen {
			return b.String() + "…"
		}
	}
	return b.String()
}
