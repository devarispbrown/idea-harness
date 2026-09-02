package textdiff

import "testing"

func TestUnifiedIdentical(t *testing.T) {
	if got := Unified("a\nb\n", "a\nb\n"); got != "  a\n  b" {
		t.Fatalf("identical diff = %q", got)
	}
}

func TestUnifiedAddAndRemove(t *testing.T) {
	got := Unified("a\nb\nc\n", "a\nc\nd\n")
	want := "  a\n- b\n  c\n+ d"
	if got != want {
		t.Fatalf("diff = %q, want %q", got, want)
	}
}

func TestUnifiedEmpty(t *testing.T) {
	if got := Unified("", "x\n"); got != "+ x" {
		t.Fatalf("empty->x diff = %q", got)
	}
	if got := Unified("x\n", ""); got != "- x" {
		t.Fatalf("x->empty diff = %q", got)
	}
}
