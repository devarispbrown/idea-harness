package textdiff

import (
	"strconv"
	"strings"
	"testing"
)

func TestCounts(t *testing.T) {
	cases := []struct {
		old, new       string
		added, removed int
	}{
		{"", "a\nb\n", 2, 0},
		{"a\nb\n", "", 0, 2},
		{"a\nb\nc\n", "a\nB\nc\n", 1, 1},
		{"a\nb\nc\n", "a\nb\nc\n", 0, 0},
		{"a\n", "a\nb\nc\n", 2, 0},
	}
	for _, c := range cases {
		a, r := Counts(c.old, c.new)
		if a != c.added || r != c.removed {
			t.Errorf("Counts(%q, %q) = +%d -%d, want +%d -%d", c.old, c.new, a, r, c.added, c.removed)
		}
	}
}

func TestCountsFallsBackPastTheCellBound(t *testing.T) {
	var o, n strings.Builder
	for i := 0; i < 3000; i++ {
		o.WriteString("old " + strconv.Itoa(i) + "\n")
		n.WriteString("new " + strconv.Itoa(i) + "\n")
	}
	a, r := Counts(o.String(), n.String())
	if a != 3000 || r != 3000 {
		t.Fatalf("Counts on a full rewrite = +%d -%d, want +3000 -3000", a, r)
	}
}
