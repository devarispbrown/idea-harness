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

// tokenize splits a judgment into significant lowercase tokens: stop words
// and sub-3-character NON-numeric tokens are dropped, but a purely numeric
// token is always kept, whatever its length (review M2).
//
// Dropping short tokens indiscriminately merged "Support Python 3.11?" with
// "Support Python 3.12?" (each number splits on '.' into "3" and "11"/"12",
// both under the 3-character floor and so dropped entirely -- the two
// questions then tokenize to the identical {support, python}, Jaccard 1.0)
// and "Max 10 or 50 items per page?" with "Max 20 or 25 items per page?"
// (same reasoning: every number dropped, both left as {max, items, page}).
// A version number or a quantity is exactly the kind of short token whose
// value distinguishes two otherwise-similar judgments -- the digits are
// significant regardless of how few of them there are.
func tokenize(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r)
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if isNumericToken(f) {
			out = append(out, f)
			continue
		}
		if len(f) < 3 || stopWords[f] {
			continue
		}
		out = append(out, f)
	}
	return out
}

// isNumericToken reports whether s consists entirely of digits (a version
// component, a quantity) -- tokenize's exception to the 3-character floor
// (review M2).
func isNumericToken(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// jaccardSimilarity returns the Jaccard similarity of a and b's significant
// token sets (tokenize's output, deduplicated): |intersection| / |union|. 0
// when either side tokenizes to nothing.
func jaccardSimilarity(a, b string) float64 {
	ta := tokenize(a)
	tb := tokenize(b)
	if len(ta) == 0 || len(tb) == 0 {
		return 0
	}
	setA := make(map[string]bool, len(ta))
	for _, t := range ta {
		setA[t] = true
	}
	setB := make(map[string]bool, len(tb))
	for _, t := range tb {
		setB[t] = true
	}
	inter := 0
	for t := range setA {
		if setB[t] {
			inter++
		}
	}
	union := len(setA) + len(setB) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

// nearDuplicateThreshold is the token-Jaccard similarity, after
// normalizeJudgment strips revise-cycle prefixes, at or above which two
// decisions (of the same DecisionType) or two questions are merged at
// creation (review B4, adopted #8).
//
// 0.85, not the looser overlap-over-shorter-side measure this replaced: "Which
// database should we use?" vs "Which cache should we use?" shares "which" and
// "use" against each side's own {which,database,use}/{which,cache,use} --
// Jaccard 2/4 = 0.5, correctly kept apart, where the old 0.6-of-shorter-side
// rule (2/3 = 0.67) wrongly merged them.
const nearDuplicateThreshold = 0.85

// similarJudgment reports whether two judgments are near-duplicates by token
// Jaccard similarity (review B4, adopted #8) after normalizeJudgment. Callers
// comparing decisions must also check DecisionType equality themselves --
// this function only ever compares text.
func similarJudgment(a, b string) bool {
	return jaccardSimilarity(normalizeJudgment(a), normalizeJudgment(b)) >= nearDuplicateThreshold
}
