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

// sameSubjectTextFloor is the minimum text-only Jaccard similarity (after
// normalizeJudgment) required before two decisions' OPTIONS may count toward
// a merge (review ICP re-run #2). Without this floor, two decisions that
// merely happen to offer the same generic options -- both a plain "yes"/"no"
// choice about entirely different subjects -- would merge on options alone.
const sameSubjectTextFloor = 0.5

// sameSubjectOptionsThreshold is the options-only Jaccard similarity, on top
// of sameSubjectTextFloor, at or above which two decisions of the same type
// are treated as the same subject even though their question wording falls
// short of nearDuplicateThreshold (review ICP re-run #2).
const sameSubjectOptionsThreshold = 0.7

// optionsSimilarity is the token-Jaccard similarity of two decisions'
// options lists (review ICP re-run #2): each list is flattened into one
// string and tokenized the same way similarJudgment tokenizes question text,
// so wording drift or reordering across revise cycles ("token bucket (per
// key)" vs "token bucket algorithm") does not by itself defeat the match,
// while still respecting tokenize's own exceptions (a short numeric token,
// e.g. a version or a quantity, is never dropped).
func optionsSimilarity(a, b []string) float64 {
	return jaccardSimilarity(strings.Join(a, " "), strings.Join(b, " "))
}

// similarDecision reports whether two decisions of the SAME TYPE are
// near-duplicates (review B4, adopted #8; review ICP re-run #2). A decision's
// wording tends to drift more across revise-cycle rounds than its options do
// -- an ICP re-run found the model re-asking effectively the same rate-limit
// algorithm choice with different phrasing each round, each time creating a
// new DEC instead of merging -- so this checks EITHER:
//
//  1. The pure-text rule (>= nearDuplicateThreshold), same as similarJudgment,
//     or
//  2. A same-subject signal: the question text is still recognizably about
//     the same thing (>= sameSubjectTextFloor) AND the two decisions offer
//     materially the same options (>= sameSubjectOptionsThreshold).
//
// The text floor in (2) is the false-merge protection: two decisions that
// merely share generic options (e.g. both offer "yes"/"no") but are about
// unrelated subjects have near-zero text similarity and so never merge on
// options alone. Callers must also check DecisionType equality themselves.
func similarDecision(aQuestion, bQuestion string, aOptions, bOptions []string) bool {
	if similarJudgment(aQuestion, bQuestion) {
		return true
	}
	textSim := jaccardSimilarity(normalizeJudgment(aQuestion), normalizeJudgment(bQuestion))
	if textSim < sameSubjectTextFloor {
		return false
	}
	return optionsSimilarity(aOptions, bOptions) >= sameSubjectOptionsThreshold
}
