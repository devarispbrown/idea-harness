package workflow

import (
	"strings"
	"unicode"
)

// normalizeJudgment returns a canonical form for comparing decision questions
// and open questions. Leading "[Source: ...]" attribution prefixes (which the
// model adds when re-listing prior decisions across revise cycles) are stripped,
// case and whitespace are normalized, and trailing punctuation is dropped so
// that near-identical judgments collapse onto one row.
func normalizeJudgment(text string) string {
	s := strings.TrimSpace(text)
	for strings.HasPrefix(s, "[") {
		end := strings.Index(s, "]")
		if end < 0 {
			break
		}
		s = strings.TrimSpace(s[end+1:])
	}
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	return strings.TrimRight(s, "?!.;: ")
}

var stopWords = map[string]bool{
	"a": true, "an": true, "the": true, "is": true, "are": true, "be": true,
	"what": true, "how": true, "do": true, "does": true, "should": true,
	"would": true, "will": true, "we": true, "to": true, "of": true, "in": true,
	"for": true, "on": true, "with": true, "and": true, "or": true, "by": true,
	"vs": true, "it": true, "this": true, "that": true, "there": true,
}

// tokenize splits a judgment into significant lowercase tokens (stop words and
// sub-3-character tokens dropped).
func tokenize(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 3 || stopWords[f] {
			continue
		}
		out = append(out, f)
	}
	return out
}

// similarJudgment reports whether two judgments are near-duplicates — high
// significant-token overlap relative to the shorter one. This suppresses the
// semantic duplicates that exact-text matching misses (e.g. "How many mailboxes
// are included by default?" vs "How many mailboxes per domain purchase?").
func similarJudgment(a, b string) bool {
	ta := tokenize(a)
	tb := tokenize(b)
	if len(ta) == 0 || len(tb) == 0 {
		return false
	}
	overlap := 0
	for _, t := range ta {
		for _, u := range tb {
			if t == u {
				overlap++
				break
			}
		}
	}
	den := len(ta)
	if len(tb) < den {
		den = len(tb)
	}
	return float64(overlap)/float64(den) >= 0.6
}
