package security

import (
	"strings"
	"testing"
)

// Luhn-valid numbers with real issuer prefixes that are NOT on any published
// test-card list -- the shape a real customer's card has.
const (
	piiVisa       = "4539148803436467"
	piiMastercard = "5237819462055713"
	piiMC2Series  = "2231045781362019"
	piiAmex       = "378282246300121"
	piiDiscover   = "6011543022178902"
	piiSSN        = "536-22-8417"
)

func TestFindPIIDetectsCardsAndSSNs(t *testing.T) {
	for _, tc := range []struct {
		name, text, kind string
	}{
		{"visa bare", "card: " + piiVisa + "\n", "Visa"},
		{"visa spaced", "card 4539 1488 0343 6467 on file", "Visa"},
		{"visa dashed", `"pan": "4539-1488-0343-6467",`, "Visa"},
		{"mastercard", "mc=" + piiMastercard, "Mastercard"},
		{"mastercard 2-series", piiMC2Series, "Mastercard"},
		{"amex grouped", "amex 3782 822463 00121.", "American Express"},
		{"amex bare", piiAmex, "American Express"},
		{"discover", "(" + piiDiscover + ")", "Discover"},
		{"end of sentence", "The customer's card is " + piiVisa + ".", "Visa"},
		{"ssn", "ssn: " + piiSSN + "\n", "Social Security"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := FindPIIInFile("src/customers.js", "// header\n"+tc.text)
			if len(got) != 1 {
				t.Fatalf("want exactly one finding, got %+v", got)
			}
			f := got[0]
			if !strings.Contains(f.Kind, tc.kind) {
				t.Errorf("kind = %q, want it to name %q", f.Kind, tc.kind)
			}
			if f.Line != 2 {
				t.Errorf("line = %d, want 2", f.Line)
			}
			if f.Advisory {
				t.Error("a src/ path must not be advisory")
			}
			// The preview never carries the value.
			for _, v := range []string{piiVisa, piiMastercard, piiAmex, piiDiscover, piiMC2Series, piiSSN, "4539", "536-22"} {
				if strings.Contains(f.Preview, v) {
					t.Errorf("preview %q leaks %q", f.Preview, v)
				}
			}
		})
	}
}

// TestFindPIIIgnoresNonPII is the false-positive side, which matters more:
// a detector that fires on ordinary code trains people to dismiss it.
func TestFindPIIIgnoresNonPII(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"stripe test card", "4242424242424242"},
		{"stripe test card spaced", "4242 4242 4242 4242"},
		{"classic test visa", "4111111111111111"},
		{"amex test card", "378282246310005"},
		{"luhn fails", "4539148803436468"},
		{"unknown issuer prefix", "9539148803436467"},
		{"visa prefix wrong length", "453914880343646"},
		{"nanosecond timestamp", "1760339091123456789"},
		{"phone-style grouping", "4539-148-803-436-467"},
		{"mixed separators", "4539 1488-0343 6467"},
		{"inside a longer number", "1" + "4539148803436467"},
		{"inside a hex id", "a4539148803436467f"},
		{"decimal", "0." + "4539148803436467"},
		{"version string", "v4539148803436467.2"},
		{"path segment", "/orders/4539148803436467/items"},
		{"all one digit", "4444444444444444"},
		{"ssn example (wallet)", "078-05-1120"},
		{"ssn placeholder", "123-45-6789"},
		{"ssn area 000", "000-12-3456"},
		{"ssn area 666", "666-12-3456"},
		{"ssn area 9xx", "912-34-5678"},
		{"ssn group 00", "536-00-8417"},
		{"ssn serial 0000", "536-22-0000"},
		{"ssn undashed", "536228417"},
		{"date-like run", "2024-536-22-8417"},
		{"email", "support@example.com"},
		{"phone", "+1 (415) 555-0132"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := FindPIIInFile("src/x.go", tc.text); len(got) != 0 {
				t.Fatalf("false positive on %q: %+v", tc.text, got)
			}
		})
	}
}

// TestFindPIIAdvisoryInTestPaths: fixture data is the one place realistic
// card numbers and SSNs are routine, so a match there is shown, never gating
// -- the same rule the credential sweep applies to its generic patterns.
func TestFindPIIAdvisoryInTestPaths(t *testing.T) {
	for _, p := range []string{"billing_test.go", "test/fixtures/customers.json", "src/__tests__/pay.spec.ts"} {
		got := FindPIIInFile(p, piiVisa)
		if len(got) != 1 || !got[0].Advisory {
			t.Errorf("%s: want one advisory finding, got %+v", p, got)
		}
	}
}

func TestLuhn(t *testing.T) {
	for _, d := range []string{piiVisa, piiMastercard, piiAmex, piiDiscover, "4242424242424242", "79927398713"} {
		if !luhnValid(d) {
			t.Errorf("%s should pass Luhn", d)
		}
	}
	for _, d := range []string{"4539148803436468", "79927398710", "1234567812345678"} {
		if luhnValid(d) {
			t.Errorf("%s should fail Luhn", d)
		}
	}
}
