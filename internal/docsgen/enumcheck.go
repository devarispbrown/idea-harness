package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// enumsGo is the single file that declares the product's controlled
// vocabularies. Every `type X string` there with constants is a vocabulary a
// client may have to switch on.
const enumsGo = "internal/domain/enums.go"

// schemaNameRe matches a named schema under components/schemas.
var schemaNameRe = regexp.MustCompile(`^    ([A-Za-z0-9_]+):\s*$`)

// inlineEnumRe matches `enum: [a, b, c]`; blockEnumItemRe matches the `- a`
// form. openapi.yaml uses both, and a check that read only one would silently
// skip the vocabularies written in the other.
var (
	inlineEnumRe    = regexp.MustCompile(`^\s*enum:\s*\[([^\]]*)\]\s*$`)
	blockEnumStart  = regexp.MustCompile(`^\s*enum:\s*$`)
	blockEnumItemRe = regexp.MustCompile(`^\s*-\s*(\S+)\s*$`)
)

// runEnumCheck compares each named string enum in docs/openapi.yaml against the
// domain constants of the same name.
//
// This is the axis that produced two contract bugs nothing else could see.
// `RequirementStatus` listed four of the six values the domain emits — `failed`
// and `waived` missing since human evidence landed — so a generated client
// rejects a valid response. `verdict` listed `approve` and `reject` where the
// engine's switch takes `approved` and `rejected`, so a generated client could
// not approve or reject a change set at all.
//
// Route-level guards were green through both: api-check compares paths,
// bodycheck compares request-body field *names*. Nothing compared a value
// vocabulary to the constants that produce it.
func runEnumCheck() error {
	root, err := repoRoot()
	if err != nil {
		return err
	}
	goEnums, err := domainEnums(filepath.Join(root, enumsGo))
	if err != nil {
		return err
	}
	specEnums, err := specStringEnums(filepath.Join(root, specPath))
	if err != nil {
		return err
	}

	var problems []string
	checked := 0
	for name, want := range goEnums {
		got, ok := specEnums[name]
		if !ok {
			continue // not every domain vocabulary is exposed on the wire
		}
		checked++
		missing := difference(want, got)
		extra := difference(got, want)
		if len(missing) == 0 && len(extra) == 0 {
			continue
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%s disagrees with domain.%s:", name, name)
		if len(missing) > 0 {
			fmt.Fprintf(&b, "\n    the domain emits these and the spec omits them: %s"+
				"\n      a client generated from the spec rejects a valid response",
				strings.Join(missing, ", "))
		}
		if len(extra) > 0 {
			fmt.Fprintf(&b, "\n    the spec declares these and nothing produces them: %s"+
				"\n      a client written against the spec handles a case that cannot occur, "+
				"or sends one that is refused", strings.Join(extra, ", "))
		}
		problems = append(problems, b.String())
	}

	sort.Strings(problems)
	if len(problems) > 0 {
		return fmt.Errorf("%d enum(s) drifted from internal/domain/enums.go:\n  %s",
			len(problems), strings.Join(problems, "\n  "))
	}
	if checked == 0 {
		return fmt.Errorf("no named enum matched a domain type; this check is not reading either file")
	}
	fmt.Printf("enum-check: %d spec enum(s) match their domain constants exactly\n", checked)
	return nil
}

func difference(a, b []string) []string {
	in := map[string]bool{}
	for _, v := range b {
		in[v] = true
	}
	var out []string
	for _, v := range a {
		if !in[v] {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// domainEnums returns every `type X string` in enums.go with its constant
// values, in declaration order.
func domainEnums(path string) (map[string][]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	stringTypes := map[string]bool{}
	out := map[string][]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			if ts, ok := spec.(*ast.TypeSpec); ok {
				if id, ok := ts.Type.(*ast.Ident); ok && id.Name == "string" {
					stringTypes[ts.Name.Name] = true
				}
				continue
			}
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || vs.Type == nil || len(vs.Values) != 1 {
				continue
			}
			typeName, ok := vs.Type.(*ast.Ident)
			if !ok || !stringTypes[typeName.Name] {
				continue
			}
			lit, ok := vs.Values[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				continue
			}
			out[typeName.Name] = append(out[typeName.Name], v)
		}
	}
	return out, nil
}

// specStringEnums returns each named `type: string` schema's enum values,
// reading both the inline and the block list forms.
func specStringEnums(path string) (map[string][]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(raw), "\n")
	out := map[string][]string{}

	for i, line := range lines {
		m := schemaNameRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		var isString bool
		var values []string
		// Walk the schema body: it ends at the next line indented 4 or less
		// that is not blank.
		for j := i + 1; j < len(lines); j++ {
			l := lines[j]
			if strings.TrimSpace(l) == "" {
				continue
			}
			if !strings.HasPrefix(l, "     ") { // less than 5 spaces: next schema
				break
			}
			if strings.TrimSpace(l) == "type: string" {
				isString = true
			}
			if em := inlineEnumRe.FindStringSubmatch(l); em != nil {
				for _, v := range strings.Split(em[1], ",") {
					if v = strings.TrimSpace(v); v != "" {
						values = append(values, v)
					}
				}
				continue
			}
			if blockEnumStart.MatchString(l) {
				for k := j + 1; k < len(lines); k++ {
					im := blockEnumItemRe.FindStringSubmatch(lines[k])
					if im == nil {
						break
					}
					values = append(values, im[1])
				}
			}
		}
		if isString && len(values) > 0 {
			out[name] = values
		}
	}
	return out, nil
}
