// Package textdiff computes a minimal unified line diff between two texts.
package textdiff

import "strings"

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
