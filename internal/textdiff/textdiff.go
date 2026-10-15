// Package textdiff computes a minimal unified line diff between two texts.
package textdiff

import (
	"fmt"
	"strings"
)

// splitLines splits text into lines, normalizing CRLF and dropping the phantom
// trailing empty element that strings.Split would otherwise produce for text
// ending in a newline.
func splitLines(s string) []string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// Unified returns a unified diff of old and new, with each line prefixed by
// "-" (removed), "+" (added), or "  " (context). It is a simple LCS-based diff
// sufficient for artifact-version comparison (documents are small); it is not a
// full Myers diff and may report a larger-than-minimal edit script.
func Unified(old, new string) string {
	o := splitLines(old)
	n := splitLines(new)
	m, k := len(o), len(n)

	// dp[i][j] is the LCS length of o[i:] and n[j:].
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, k+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := k - 1; j >= 0; j-- {
			if o[i] == n[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}

	var b strings.Builder
	i, j := 0, 0
	for i < m && j < k {
		switch {
		case o[i] == n[j]:
			b.WriteString("  " + o[i] + "\n")
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			b.WriteString("- " + o[i] + "\n")
			i++
		default:
			b.WriteString("+ " + n[j] + "\n")
			j++
		}
	}
	for ; i < m; i++ {
		b.WriteString("- " + o[i] + "\n")
	}
	for ; j < k; j++ {
		b.WriteString("+ " + n[j] + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// hunkContext is the number of unchanged lines shown around each change.
const hunkContext = 3

// MaxDiffCells bounds the LCS table Hunks will build, after the common prefix
// and suffix are trimmed: the table is (m+1)*(k+1) ints. Past this the diff is
// refused rather than computed -- a changed 15,000-line lockfile would
// otherwise allocate about 1.8 GB inside the daemon to render one file.
const MaxDiffCells = 4_000_000

type op struct {
	kind byte // ' ', '-', '+'
	text string
}

// Hunks returns a git-style unified diff of old and new, without file headers:
// hunks headed "@@ -a,b +c,d @@", lines prefixed ' ', '-' or '+', with three
// lines of context. ok is false when the changed region is too large to diff
// within MaxDiffCells; the caller should show the content instead.
//
// Unified above renders the whole file, which suits short documents; a change
// set diff is read to find what changed, so only the changes and their
// context are shown.
func Hunks(old, new string) (string, bool) {
	o, n := splitLines(old), splitLines(new)

	// Trim the common prefix and suffix first. Most edits touch a few lines of
	// a long file, and this keeps the LCS table to the changed region.
	pre := 0
	for pre < len(o) && pre < len(n) && o[pre] == n[pre] {
		pre++
	}
	suf := 0
	for suf < len(o)-pre && suf < len(n)-pre && o[len(o)-1-suf] == n[len(n)-1-suf] {
		suf++
	}
	om, nm := o[pre:len(o)-suf], n[pre:len(n)-suf]
	if (len(om)+1)*(len(nm)+1) > MaxDiffCells {
		return "", false
	}

	ops := make([]op, 0, len(o)+len(nm))
	for _, l := range o[:pre] {
		ops = append(ops, op{' ', l})
	}
	ops = append(ops, lcsOps(om, nm)...)
	for _, l := range o[len(o)-suf:] {
		ops = append(ops, op{' ', l})
	}
	return renderHunks(ops), true
}

func lcsOps(o, n []string) []op {
	m, k := len(o), len(n)
	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, k+1)
	}
	for i := m - 1; i >= 0; i-- {
		for j := k - 1; j >= 0; j-- {
			switch {
			case o[i] == n[j]:
				dp[i][j] = dp[i+1][j+1] + 1
			case dp[i+1][j] >= dp[i][j+1]:
				dp[i][j] = dp[i+1][j]
			default:
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var ops []op
	i, j := 0, 0
	for i < m && j < k {
		switch {
		case o[i] == n[j]:
			ops = append(ops, op{' ', o[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, op{'-', o[i]})
			i++
		default:
			ops = append(ops, op{'+', n[j]})
			j++
		}
	}
	for ; i < m; i++ {
		ops = append(ops, op{'-', o[i]})
	}
	for ; j < k; j++ {
		ops = append(ops, op{'+', n[j]})
	}
	return ops
}

// renderHunks groups an edit script into hunks with hunkContext lines of
// context, merging changes whose context would overlap.
func renderHunks(ops []op) string {
	// Positions of changed ops.
	var changed []int
	for i, o := range ops {
		if o.kind != ' ' {
			changed = append(changed, i)
		}
	}
	if len(changed) == 0 {
		return ""
	}
	// Line numbers (1-based) of ops[i] in old and new, before it is applied.
	oldLine := make([]int, len(ops)+1)
	newLine := make([]int, len(ops)+1)
	oldLine[0], newLine[0] = 1, 1
	for i, o := range ops {
		oldLine[i+1], newLine[i+1] = oldLine[i], newLine[i]
		if o.kind != '+' {
			oldLine[i+1]++
		}
		if o.kind != '-' {
			newLine[i+1]++
		}
	}

	var b strings.Builder
	for c := 0; c < len(changed); {
		start := max(changed[c]-hunkContext, 0)
		end := changed[c]
		for c < len(changed) && changed[c] <= end+2*hunkContext+1 {
			end = changed[c]
			c++
		}
		end = min(end+hunkContext, len(ops)-1)
		var oc, nc int
		for _, o := range ops[start : end+1] {
			if o.kind != '+' {
				oc++
			}
			if o.kind != '-' {
				nc++
			}
		}
		os, ns := oldLine[start], newLine[start]
		if oc == 0 {
			os--
		}
		if nc == 0 {
			ns--
		}
		fmt.Fprintf(&b, "@@ -%d,%d +%d,%d @@\n", os, oc, ns, nc)
		for _, o := range ops[start : end+1] {
			b.WriteByte(o.kind)
			b.WriteString(o.text)
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// Counts returns how many lines a change from old to new adds and removes,
// the "+12 -3" a file list shows. It uses the same prefix/suffix trim and LCS
// as Hunks; past MaxDiffCells it falls back to counting lines by multiset
// (a line present more times in new than in old is added, and vice versa),
// which never undercounts a pure addition or deletion and is cheap at any
// size -- a live run view must not stall on a rewritten lockfile.
func Counts(old, new string) (added, removed int) {
	o, n := splitLines(old), splitLines(new)
	pre := 0
	for pre < len(o) && pre < len(n) && o[pre] == n[pre] {
		pre++
	}
	suf := 0
	for suf < len(o)-pre && suf < len(n)-pre && o[len(o)-1-suf] == n[len(n)-1-suf] {
		suf++
	}
	om, nm := o[pre:len(o)-suf], n[pre:len(n)-suf]
	if (len(om)+1)*(len(nm)+1) > MaxDiffCells {
		seen := make(map[string]int, len(om))
		for _, l := range om {
			seen[l]++
		}
		for _, l := range nm {
			if seen[l] > 0 {
				seen[l]--
				continue
			}
			added++
		}
		for _, c := range seen {
			removed += c
		}
		return added, removed
	}
	for _, x := range lcsOps(om, nm) {
		switch x.kind {
		case '+':
			added++
		case '-':
			removed++
		}
	}
	return added, removed
}
