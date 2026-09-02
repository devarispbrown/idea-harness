package cli

import (
	"strings"
	"testing"
)

// The gate this asserts (UNEXPLAINED_ERROR): a mistyped id used to answer with
// the database driver's own string.
//
//	$ idea apply CHG-99
//	idea: sql: no rows in result set        (exit 1)
//
// The daemon now maps a store miss to 404 with a message naming what was not
// found; the CLI surfaces that message unchanged.
func TestUnknownReferenceGetsAFriendlyError(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"apply", []string{"apply", "CHG-99"}},
		{"review", []string{"review", "CHG-99", "approve"}},
		{"change", []string{"change", "CHG-99"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeDaemon(t)
			f.seedInitiative()
			f.fail("POST /api/v1/projects/{projectId}/change-sets/{id}/apply", 404, `no change set matching "CHG-99"`)
			f.fail("POST /api/v1/projects/{projectId}/change-sets/{id}/review", 404, `no change set matching "CHG-99"`)
			f.fail("GET /api/v1/projects/{projectId}/change-sets/{id}", 404, `no change set matching "CHG-99"`)

			_, _, err := run(t, tc.args...)
			if err == nil {
				t.Fatalf("idea %s: want an error", strings.Join(tc.args, " "))
			}
			assertNoRawStoreError(t, err.Error())
			if !strings.Contains(err.Error(), "CHG-99") {
				t.Fatalf("error should name the reference the user typed, got: %v", err)
			}
		})
	}
}

// A 500 carries a correlation id and no internal detail: the daemon logs the
// real error server-side under that id.
func TestServerFailureIsGenericWithACorrelationID(t *testing.T) {
	f := newFakeDaemon(t)
	f.seedInitiative()
	f.fail("POST /api/v1/projects/{projectId}/change-sets/{id}/apply", 500,
		"internal error (correlation id: a1b2c3d4e5f6)")

	_, _, err := run(t, "apply", "CHG-1")
	if err == nil {
		t.Fatal("want an error")
	}
	assertNoRawStoreError(t, err.Error())
	if !strings.Contains(err.Error(), "correlation id") {
		t.Fatalf("a 500 should carry a correlation id, got: %v", err)
	}
}

// assertNoRawStoreError fails if a user-facing message contains internal
// detail. Asserted as a shape rather than a blocklist of one token: an absolute
// state path leaks just as much as "sql:" does.
func assertNoRawStoreError(t *testing.T, msg string) {
	t.Helper()
	for _, leak := range []string{"sql:", "SELECT ", "INSERT ", ".db", "/Users/", "goroutine"} {
		if strings.Contains(msg, leak) {
			t.Fatalf("user-facing error leaks internal detail (%q): %s", leak, msg)
		}
	}
}
