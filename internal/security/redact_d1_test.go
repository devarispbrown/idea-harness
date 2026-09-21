package security

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestConstDeclarationRedactionStaysValidGo reproduces ICP re-run D1: the
// adversarial change-set reviewer read the harness's own redaction as broken
// code and raised a blocking, high-confidence false-positive critical
// ("authfixture_test.go contains an invalid Go const declaration that will
// not compile, breaking the entire main package") even though `go build
// ./...` and `go test ./...` both passed on the same change set and `gofmt
// -e` parsed the file cleanly.
//
// Root cause: the generic "credential assignment" catalog pattern's match
// starts at an optional `[A-Za-z0-9_]*` prefix before its keyword, so an
// identifier that merely CONTAINS a keyword as a suffix --
// "AuthFixtureSecret" contains "Secret" -- pulls the identifier, the `=` and
// the surrounding quotes into the match, not just the value. redact() used
// to replace the WHOLE match with "[REDACTED]", so
// `const AuthFixtureSecret = "<value>"` became the bare, unparseable
// `const [REDACTED]` -- no identifier, no `=`, nothing that reads as a
// value at all.
//
// Fix: redact() now replaces only the capturing group's own span (the value
// itself), which every catalog pattern's doc comment already promises is
// what its one capturing group wraps. The identifier, `=` and quotes survive
// untouched, and the masked span still reads as a string literal value, not
// an absent one.
//
// Mutation-check: reverting redact() to replace idx[0]:idx[1] (the whole
// match) instead of capturedGroupBounds(idx) reproduces this exactly -- the
// identifier disappears and the snippet no longer parses as Go.
func TestConstDeclarationRedactionStaysValidGo(t *testing.T) {
	const secret = "aB3dE9fGhJ2kL5mN8pQrS1tUvWxYz0123456"
	src := "package fixture\n\nconst AuthFixtureSecret = \"" + secret + "\"\n"

	got := Redact(src)

	if strings.Contains(got, secret) {
		t.Fatalf("secret value survived redaction:\n%s", got)
	}
	if !strings.Contains(got, "AuthFixtureSecret") {
		t.Fatalf("redaction swallowed the const's own identifier, not just its value -- this is what made the reviewer see an invalid declaration:\n%s", got)
	}
	if !strings.Contains(got, `= "[REDACTED]"`) {
		t.Fatalf("redaction did not leave a legible, quoted placeholder in the value's own position:\n%s", got)
	}

	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "fixture.go", got, parser.AllErrors); err != nil {
		t.Fatalf("masked source is not valid Go -- exactly the false positive the adversarial reviewer raised: %v\n%s", err, got)
	}
}

// TestGenericAssignmentRedactionPreservesSurroundingSyntax broadens the D1
// repro to sibling forms of the same class (an identifier prefix, JSON/YAML
// key, or env-var name that survives around a masked value), confirming the
// key/operator/quoting around a masked value is never swallowed.
func TestGenericAssignmentRedactionPreservesSurroundingSyntax(t *testing.T) {
	cases := []struct {
		name, in, wantContains string
	}{
		{"go_const_password", `const DBPassword = "hunter2345678901"`, `DBPassword = "[REDACTED]"`},
		{"env_form", `DB_PASSWORD=latestProdPw9xK2`, `DB_PASSWORD=[REDACTED]`},
		{"yaml_form", `password: "contested-harbor-77"`, `password: "[REDACTED]"`},
		{"json_form", `{"api_key": "k3jQ9zPmT7xR2wLnB6xyz"}`, `"api_key": "[REDACTED]"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Redact(c.in)
			if !strings.Contains(got, c.wantContains) {
				t.Fatalf("Redact(%q) = %q, want it to contain %q (key/operator/quotes intact around the masked value)", c.in, got, c.wantContains)
			}
		})
	}
}
