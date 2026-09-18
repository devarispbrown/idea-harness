package textdiff

import (
	"strings"
	"testing"
)

func TestHunksFormat(t *testing.T) {
	for _, c := range []struct{ name, old, new, want string }{
		{"one changed line", "a\nb\nc\n", "a\nB\nc\n", "@@ -1,3 +1,3 @@\n a\n-b\n+B\n c"},
		{"added file", "", "x\ny\n", "@@ -0,0 +1,2 @@\n+x\n+y"},
		{"deleted file", "x\ny\n", "", "@@ -1,2 +0,0 @@\n-x\n-y"},
		{"no change", "a\n", "a\n", ""},
	} {
		got, ok := Hunks(c.old, c.new)
		if !ok || got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

// A one-line edit to a long file shows that line and its context, not the file.
func TestHunksShowOnlyTheChange(t *testing.T) {
	var lines []string
	for i := range 1000 {
		lines = append(lines, "line "+strings.Repeat("x", i%7))
	}
	old := strings.Join(lines, "\n")
	lines[500] = "changed"
	got, ok := Hunks(old, strings.Join(lines, "\n"))
	if !ok {
		t.Fatal("refused a small edit")
	}
	if n := strings.Count(got, "\n") + 1; n != 1+3+1+1+3 {
		t.Errorf("hunk has %d lines, want 9:\n%s", n, got)
	}
	if !strings.HasPrefix(got, "@@ -498,7 +498,7 @@") {
		t.Errorf("wrong header: %s", strings.SplitN(got, "\n", 2)[0])
	}
}

// Two changes far apart are two hunks; close together, one.
func TestHunksMergeAndSplit(t *testing.T) {
	var lines []string
	for i := range 40 {
		lines = append(lines, strings.Repeat("l", i+1))
	}
	old := strings.Join(lines, "\n")
	far := append([]string(nil), lines...)
	far[2], far[30] = "A", "B"
	got, _ := Hunks(old, strings.Join(far, "\n"))
	if strings.Count(got, "@@ -") != 2 {
		t.Errorf("far-apart changes should be two hunks:\n%s", got)
	}
	near := append([]string(nil), lines...)
	near[2], near[6] = "A", "B"
	got, _ = Hunks(old, strings.Join(near, "\n"))
	if strings.Count(got, "@@ -") != 1 {
		t.Errorf("nearby changes should be one hunk:\n%s", got)
	}
}

// The daemon must not build a huge table to render one file.
func TestHunksRefusesAHugeChangedRegion(t *testing.T) {
	var a, b []string
	for i := range 3000 {
		a = append(a, "a"+strings.Repeat("x", i%11))
		b = append(b, "b"+strings.Repeat("y", i%13))
	}
	if _, ok := Hunks(strings.Join(a, "\n"), strings.Join(b, "\n")); ok {
		t.Error("a 3000x3000 changed region was diffed; it must be refused")
	}
}
