package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Bounded cuts on a rune boundary (a multibyte character straddling the
// limit is dropped whole, never split into invalid UTF-8), keeps only the
// title's first line, and marks the cut.
func TestRunEventBoundedCutsOnRuneBoundary(t *testing.T) {
	e := RunEvent{Kind: RunEventMessage, Title: "first\nsecond", Detail: strings.Repeat("é", RunEventDetailMax)}
	b := e.Bounded()
	if b.Title != "first" {
		t.Fatalf("title = %q, want its first line", b.Title)
	}
	if !utf8.ValidString(b.Detail) || !b.Truncated || len(b.Detail) > RunEventDetailMax+len("…") {
		t.Fatalf("detail: valid=%v truncated=%v len=%d", utf8.ValidString(b.Detail), b.Truncated, len(b.Detail))
	}
	small := RunEvent{Kind: RunEventAction, Title: "ok", Detail: "short"}.Bounded()
	if small.Truncated || small.Detail != "short" {
		t.Fatalf("a short event was changed: %+v", small)
	}
	if (RunEvent{Kind: "nonsense"}).Bounded().Kind != RunEventAction {
		t.Fatal("an unknown kind was kept")
	}
}

// Redacted passes every free-text field through the redactor and leaves the
// structure alone.
func TestRunEventRedactedCoversEveryTextField(t *testing.T) {
	const secret = "sk-live-abcdefghijklmnop"
	e := RunEvent{Seq: 4, Kind: RunEventToolCall, Title: "run " + secret, Detail: "env " + secret, Path: "x/" + secret, ToolKind: secret, Added: 3}
	r := e.Redacted(func(s string) string { return strings.ReplaceAll(s, secret, "[redacted]") })
	for name, v := range map[string]string{"title": r.Title, "detail": r.Detail, "path": r.Path, "tool_kind": r.ToolKind} {
		if strings.Contains(v, secret) {
			t.Errorf("%s kept the secret: %q", name, v)
		}
	}
	if r.Seq != 4 || r.Added != 3 || r.Kind != RunEventToolCall {
		t.Fatalf("structure changed: %+v", r)
	}
	if got := e.Redacted(nil); got.Title != e.Title {
		t.Fatal("a nil redactor changed the event")
	}
}
