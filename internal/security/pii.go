package security

import (
	"fmt"
	"regexp"
	"strings"
)

// PIIFinding is one detected occurrence of personal data in a file, located
// but never carrying the value: Preview is masked (a card number keeps only
// its last four digits, the way a receipt does; an SSN keeps nothing).
type PIIFinding struct {
	Path    string
	Line    int
	Kind    string
	Preview string
	// Advisory is true in a test/fixture path (isTestFixturePath): shown to
	// the reviewer, never blocking. Fixture data is the one place a
	// realistic-looking card number or SSN is routine.
	Advisory bool
}

// The PII detector is deliberately narrow (the `pii` system-deny code). It
// looks for exactly two kinds of data, each with a checksum or range rule that
// makes a random digit string unlikely to pass:
//
//   - Payment card numbers: a known issuer prefix (Visa, Mastercard, American
//     Express, Discover) at that issuer's length, written either unseparated
//     or in the issuer's own printed grouping (4-4-4-4, or 4-6-5 for Amex)
//     with one consistent separator, and passing the Luhn check. The issuers'
//     published test numbers (4242 4242 4242 4242 and friends) are excluded:
//     they are the most common card numbers in source code and none is real.
//   - US Social Security numbers, dashed form only (AAA-GG-SSSS), within the
//     ranges the SSA has ever issued (area not 000/666/9xx, group not 00,
//     serial not 0000), excluding the well-known example and "wallet" numbers.
//
// What it does NOT look for, and why:
//
//   - Email addresses and phone numbers. Both are everywhere in ordinary code
//     -- package.json authors, copyright headers, support@ addresses, seed
//     data, i18n strings -- and a detector that fires on most change sets
//     trains a reviewer to dismiss it, which is worse than not having it.
//   - An undashed nine-digit SSN. Indistinguishable from any other nine-digit
//     number (a zip+4, an order id) without context this scan does not have.
//
// The result is a scan with few false positives that will also miss PII it
// was never designed to see. It is a backstop for an agent pasting real
// customer data into a fixture or a log line, not a data-loss-prevention
// system.
var (
	// The printed groupings first: Go's regexp is leftmost-first, so a
	// grouped card is matched as a whole before the bare-digit alternative
	// can claim its first group. One separator character throughout -- RE2
	// has no backreferences, so each separator is spelled out.
	cardCandidateRe = regexp.MustCompile(
		`\d{4} \d{4} \d{4} \d{4}(?: \d{3})?` +
			`|\d{4}-\d{4}-\d{4}-\d{4}(?:-\d{3})?` +
			`|\d{4} \d{6} \d{5}|\d{4}-\d{6}-\d{5}` +
			`|\d{13,19}`)
	ssnCandidateRe = regexp.MustCompile(`\d{3}-\d{2}-\d{4}`)
)

// testCardNumbers are the issuers' and processors' documented test numbers.
// They pass Luhn and carry real issuer prefixes, and they appear in a great
// deal of legitimate payment-integration code.
var testCardNumbers = map[string]bool{
	"4242424242424242": true, "4000056655665556": true, "4111111111111111": true,
	"4012888888881881": true, "4222222222222": true, "4000000000000002": true,
	"4000000000009995": true, "4000002500003155": true, "4000000000000077": true,
	"5555555555554444": true, "5200828282828210": true, "5105105105105100": true,
	"2223003122003222": true, "5454545454545454": true, "2222400010000008": true,
	"378282246310005": true, "371449635398431": true, "378734493671000": true,
	"6011111111111117": true, "6011000990139424": true, "6011000400000000": true,
	"6011981111111113": true,
}

// exampleSSNs are numbers published as examples (078-05-1120 was printed on a
// wallet insert in 1938; 219-09-9999 in an SSA pamphlet) or used as the
// canonical placeholder.
var exampleSSNs = map[string]bool{
	"078-05-1120": true, "219-09-9999": true, "123-45-6789": true,
}

// FindPIIInFile scans one file's content for payment card numbers and US
// Social Security numbers (see the catalog comment above for exactly what is
// and is not detected).
func FindPIIInFile(path, content string) []PIIFinding {
	advisory := isTestFixturePath(path)
	var out []PIIFinding
	for _, idx := range cardCandidateRe.FindAllStringIndex(content, -1) {
		if !standalone(content, idx[0], idx[1]) {
			continue
		}
		digits := cardDigits(content[idx[0]:idx[1]])
		brand := cardBrand(digits)
		if brand == "" || testCardNumbers[digits] || !luhnValid(digits) || allSameDigit(digits) {
			continue
		}
		out = append(out, PIIFinding{
			Path:     path,
			Line:     lineAt(content, idx[0]),
			Kind:     "payment card number (" + brand + ")",
			Preview:  fmt.Sprintf("%s%s", strings.Repeat("*", len(digits)-4), digits[len(digits)-4:]),
			Advisory: advisory,
		})
	}
	for _, idx := range ssnCandidateRe.FindAllStringIndex(content, -1) {
		if !standalone(content, idx[0], idx[1]) {
			continue
		}
		ssn := content[idx[0]:idx[1]]
		if exampleSSNs[ssn] || !validSSN(ssn) {
			continue
		}
		out = append(out, PIIFinding{
			Path:     path,
			Line:     lineAt(content, idx[0]),
			Kind:     "US Social Security number",
			Preview:  "***-**-****",
			Advisory: advisory,
		})
	}
	return out
}

// standalone reports whether content[start:end] is not part of a longer
// token. A letter, digit or underscore directly on either side joins it to
// something else (a longer number, a hex id). A '.', '-' or '/' joins it only
// when another letter or digit follows on the far side -- "1.4111…" is a
// decimal, "v4111….1" a version, "/4111…/" a path segment -- so the full stop
// ending a sentence, or a dash used as punctuation, does not hide a match.
func standalone(content string, start, end int) bool {
	word := func(b byte) bool {
		return b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b == '_'
	}
	joiner := func(b byte) bool { return b == '.' || b == '-' || b == '/' }
	if start > 0 {
		b := content[start-1]
		if word(b) || joiner(b) && start > 1 && word(content[start-2]) {
			return false
		}
	}
	if end < len(content) {
		b := content[end]
		if word(b) || joiner(b) && end+1 < len(content) && word(content[end+1]) {
			return false
		}
	}
	return true
}

// cardDigits strips the separators from a card candidate. The regexp has
// already restricted where they may be: none at all, or one separator
// character between the issuer's printed groups (4-4-4-4, 4-4-4-4-3, or 4-6-5
// for Amex). A digit run broken anywhere else -- a phone number, a part
// number -- never reaches here.
func cardDigits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

// cardBrand names the issuer for a number with a known prefix at that
// issuer's length, or "" -- most 13-19 digit strings have neither.
func cardBrand(d string) string {
	n := len(d)
	p2 := atoiPrefix(d, 2)
	p3 := atoiPrefix(d, 3)
	p4 := atoiPrefix(d, 4)
	switch {
	case d[0] == '4' && (n == 13 || n == 16 || n == 19):
		return "Visa"
	case n == 16 && (p2 >= 51 && p2 <= 55 || p4 >= 2221 && p4 <= 2720):
		return "Mastercard"
	case n == 15 && (p2 == 34 || p2 == 37):
		return "American Express"
	case n >= 16 && n <= 19 && (p4 == 6011 || p2 == 65 || p3 >= 644 && p3 <= 649):
		return "Discover"
	}
	return ""
}

func atoiPrefix(d string, n int) int {
	v := 0
	for i := 0; i < n && i < len(d); i++ {
		v = v*10 + int(d[i]-'0')
	}
	return v
}

// luhnValid is the mod-10 checksum every card number carries. A random digit
// string passes it one time in ten, which is why it is one filter among
// several here and not the whole test.
func luhnValid(d string) bool {
	sum := 0
	double := false
	for i := len(d) - 1; i >= 0; i-- {
		v := int(d[i] - '0')
		if double {
			v *= 2
			if v > 9 {
				v -= 9
			}
		}
		sum += v
		double = !double
	}
	return sum%10 == 0
}

func allSameDigit(d string) bool {
	return strings.Count(d, d[:1]) == len(d)
}

// validSSN applies the issuance rules: area 001-665 or 667-899, group 01-99,
// serial 0001-9999.
func validSSN(s string) bool {
	area := atoiPrefix(s, 3)
	group := atoiPrefix(s[4:], 2)
	serial := atoiPrefix(s[7:], 4)
	if area == 0 || area == 666 || area >= 900 {
		return false
	}
	return group != 0 && serial != 0
}
