package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

// TestCancelledOperationLogsInfoNotError closes the gap a real founder-journey
// run found: every user Stop (`idea cancel`, or the web UI's Stop button)
// cancels the operation's context, which unwinds the in-flight call as a
// plain `context canceled` error -- and failOperation logged that at ERROR
// alongside a genuine failure, so `level=ERROR msg="execute failed"
// err="context canceled"` appeared in the daemon log for completely ordinary,
// user-requested cancellations. A cancellation the user asked for is expected
// behaviour, not something worth paging over; a real failure still is.
func TestCancelledOperationLogsInfoNotError(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	rt.log = slog.New(slog.NewTextHandler(&buf, nil))

	opID, err := pr.db.CreateOperation(t.Context(), projectID, iniID, "execute_initiative")
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	rt.failOperation(opID, iniID, "execute failed", cancelled, pr, context.Canceled, false)

	out := buf.String()
	if strings.Contains(out, "level=ERROR") {
		t.Errorf("a user cancel logged at ERROR:\n%s", out)
	}
	if !strings.Contains(out, "level=INFO") || !strings.Contains(out, "stopped by you") {
		t.Errorf("a user cancel should log \"stopped by you\" at INFO:\n%s", out)
	}
}

// TestFailedOperationStillLogsError is TestCancelledOperationLogsInfoNotError's
// negative: a genuine failure (the context was NOT cancelled) must keep
// logging at ERROR with the original message -- the fix must not also quiet a
// real failure.
func TestFailedOperationStillLogsError(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	rt.log = slog.New(slog.NewTextHandler(&buf, nil))

	opID, err := pr.db.CreateOperation(t.Context(), projectID, iniID, "execute_initiative")
	if err != nil {
		t.Fatal(err)
	}
	rt.failOperation(opID, iniID, "execute failed", t.Context(), pr, errors.New("boom"), false)

	out := buf.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "execute failed") {
		t.Errorf("a real failure should still log \"execute failed\" at ERROR:\n%s", out)
	}
	if strings.Contains(out, "stopped by you") {
		t.Errorf("a real failure must not be reported as a user cancel:\n%s", out)
	}
}
