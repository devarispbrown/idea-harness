package main

// api-check: the drift guard between the route table and the OpenAPI spec.
//
//	go run ./internal/docsgen -api-check   # or: make api-check
//
// It reads every `mux.HandleFunc("<METHOD> <path>", …)` out of the AST of
// package internal/daemon — internal/daemon/routes.go is where they all live —
// and compares that set against the operations declared under `paths:` in
// docs/openapi.yaml. It fails when a route has no operation in the spec, and
// when the spec declares an operation no route serves.
//
// What it deliberately does not do is check schemas. Request bodies are
// anonymous inline structs, so there is nothing to compare them against, and
// comparing response types to hand-written schemas is a much bigger tool. The
// drift that actually happens is "someone added an endpoint and forgot the
// spec", and that is exactly what this catches.
//
// Scope: only paths under /api/. The documentation endpoints themselves
// (/openapi.yaml, /openapi.json, /api-reference) are served outside /api/
// because the token middleware guards /api/ only; they are plumbing for the
// spec rather than part of the API surface it describes, so they are excluded
// on both sides and the spec may document them or not.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// apiPrefix is the surface the spec is expected to describe.
const apiPrefix = "/api/"

// specPath and routesPkg are relative to the repository root.
var (
	specPath  = filepath.Join("docs", "openapi.yaml")
	routesPkg = filepath.Join("internal", "daemon")
)

// httpMethods are the keys inside an OpenAPI path item that denote an
// operation. Everything else there (`parameters`, `summary`, `servers`,
// `$ref`, `x-…`) describes the path, not a method, and is skipped.
var httpMethods = map[string]bool{
	"get": true, "put": true, "post": true, "delete": true,
	"options": true, "head": true, "patch": true, "trace": true,
}

// operation is one method+path pair, from either side of the comparison.
type operation struct {
	method string // upper case; "" for a ServeMux pattern with no method
	path   string
}

func (o operation) String() string {
	if o.method == "" {
		return "(any) " + o.path
	}
	return o.method + " " + o.path
}

// runAPICheck reports drift between the route table and the spec.
func runAPICheck() error {
	root, err := repoRoot()
	if err != nil {
		return err
	}

	routes, err := parseRoutes(filepath.Join(root, routesPkg))
	if err != nil {
		return err
	}
	if len(routes) == 0 {
		return fmt.Errorf("no routes found in %s: the parser found no HandleFunc patterns, which is a bug in the checker, not an empty API", routesPkg)
	}

	spec, specNote, err := parseSpecOperations(filepath.Join(root, specPath))
	if err != nil {
		return err
	}

	// Structural validity, which the three axes below do not touch: they compare
	// paths, body fields and 409s, and none of them reads a schema as a schema.
	rawSpec, err := os.ReadFile(filepath.Join(root, specPath))
	if err != nil {
		return err
	}
	var specDoc any
	if err := yaml.Unmarshal(rawSpec, &specDoc); err != nil {
		return err
	}
	if err := schemaShapeError(checkSchemaShapes(specDoc)); err != nil {
		return err
	}
	if err := declaredStatusError(checkDeclaredStatuses(specDoc)); err != nil {
		return err
	}
	if err := declaredStatusError(checkRefsResolve(specDoc)); err != nil {
		return err
	}
	if err := declaredStatusError(checkOperationCountClaim(string(rawSpec), specDoc)); err != nil {
		return err
	}

	var undocumented, unimplemented []string
	for _, op := range sortedOps(routes) {
		if !strings.HasPrefix(op.path, apiPrefix) {
			continue
		}
		if _, ok := spec[op]; !ok {
			undocumented = append(undocumented, fmt.Sprintf("  %-64s  %s", op, routes[op]))
		}
	}
	for _, op := range sortedOps(spec) {
		if !strings.HasPrefix(op.path, apiPrefix) {
			continue
		}
		if _, ok := routes[op]; !ok {
			unimplemented = append(unimplemented, fmt.Sprintf("  %-64s  %s", op, spec[op]))
		}
	}

	if len(undocumented) == 0 && len(unimplemented) == 0 {
		return nil
	}

	var b strings.Builder
	if specNote != "" {
		fmt.Fprintf(&b, "%s\n\n", specNote)
	}
	if len(undocumented) > 0 {
		fmt.Fprintf(&b, "%d route(s) served but missing from %s:\n%s\n\n",
			len(undocumented), specPath, strings.Join(undocumented, "\n"))
	}
	if len(unimplemented) > 0 {
		fmt.Fprintf(&b, "%d operation(s) in %s that no route serves:\n%s\n\n",
			len(unimplemented), specPath, strings.Join(unimplemented, "\n"))
	}
	b.WriteString("Add the missing operations to " + specPath + " (or remove the stale ones) and re-run `make api-check`.")
	return fmt.Errorf("%s", b.String())
}

// parseRoutes extracts every ServeMux pattern registered in the Go package at
// dir, mapped to the file:line that registered it.
//
// It parses rather than greps so a commented-out or string-concatenated
// registration cannot fool it, and it treats a pattern it cannot read as a hard
// error: a route the checker silently skipped is a route that can drift.
func parseRoutes(dir string) (map[operation]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	out := map[operation]string{}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		full := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, full, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", full, err)
		}
		var inspectErr error
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
				return true
			}
			if len(call.Args) == 0 {
				return true
			}
			pos := fset.Position(call.Lparen)
			where := fmt.Sprintf("%s:%d", filepath.ToSlash(relOrAbs(pos.Filename)), pos.Line)

			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				if sel.Sel.Name != "HandleFunc" {
					// `Handle` is a common enough method name on other types
					// that a non-string first argument means "not a route".
					return true
				}
				// A HandleFunc pattern assembled at runtime is invisible to this
				// checker, which is the one way a route could drift out of the
				// spec unnoticed. Refuse rather than skip.
				inspectErr = fmt.Errorf(
					"%s: HandleFunc pattern is not a string literal, so `make api-check` cannot see this route; "+
						"write the pattern inline", where)
				return false
			}
			pattern, err := strconv.Unquote(lit.Value)
			if err != nil {
				inspectErr = fmt.Errorf("%s: cannot read pattern %s: %w", where, lit.Value, err)
				return false
			}
			op, ok := patternOperation(pattern)
			if !ok {
				return true // not a path pattern (e.g. "/" for the UI file server)
			}
			out[op] = where
			return true
		})
		if inspectErr != nil {
			return nil, inspectErr
		}
	}
	return out, nil
}

// patternOperation splits a net/http ServeMux pattern ("GET /api/v1/x") into a
// method and a path. Go 1.22 patterns may also carry a host ("GET
// example.com/x"); none here do, and a hostful pattern is reported as no
// operation rather than being silently mis-split.
func patternOperation(pattern string) (operation, bool) {
	p := strings.TrimSpace(pattern)
	method := ""
	if i := strings.Index(p, " "); i >= 0 {
		method = strings.TrimSuffix(strings.TrimSpace(p[:i]), " ")
		p = strings.TrimSpace(p[i+1:])
	}
	if !strings.HasPrefix(p, "/") {
		return operation{}, false
	}
	if p == "/" {
		return operation{}, false // the catch-all the embedded UI is mounted on
	}
	return operation{method: strings.ToUpper(method), path: p}, true
}

// parseSpecOperations reads the operations declared in the OpenAPI document at
// path, mapped to the file:line they are declared on.
//
// A missing file is not an error: before the hand-written spec lands there is
// nothing to compare against, and reporting every route as undocumented is more
// useful than refusing to run. The returned note explains what happened.
func parseSpecOperations(path string) (map[operation]string, string, error) {
	rel := filepath.ToSlash(relOrAbs(path))
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[operation]string{}, fmt.Sprintf("%s does not exist yet, so every route below is undocumented.", rel), nil
	}
	if err != nil {
		return nil, "", err
	}

	var doc struct {
		Paths yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, "", fmt.Errorf("%s is not valid YAML: %w", rel, err)
	}
	out := map[operation]string{}
	if doc.Paths.Kind == 0 {
		return out, fmt.Sprintf("%s declares no `paths:`, so every route below is undocumented.", rel), nil
	}
	if doc.Paths.Kind != yaml.MappingNode {
		return nil, "", fmt.Errorf("%s: `paths` must be a mapping, got %s", rel, nodeKind(doc.Paths.Kind))
	}

	// A YAML mapping node stores keys and values as alternating children.
	for i := 0; i+1 < len(doc.Paths.Content); i += 2 {
		pathKey, item := doc.Paths.Content[i], doc.Paths.Content[i+1]
		if item.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(item.Content); j += 2 {
			methodKey := item.Content[j]
			if !httpMethods[strings.ToLower(methodKey.Value)] {
				continue
			}
			op := operation{method: strings.ToUpper(methodKey.Value), path: pathKey.Value}
			out[op] = fmt.Sprintf("%s:%d", rel, methodKey.Line)
		}
	}
	return out, "", nil
}

func nodeKind(k yaml.Kind) string {
	switch k {
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "sequence"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	default:
		return "unknown"
	}
}

func sortedOps(m map[operation]string) []operation {
	ops := make([]operation, 0, len(m))
	for op := range m {
		ops = append(ops, op)
	}
	sort.Slice(ops, func(i, j int) bool {
		if ops[i].path != ops[j].path {
			return ops[i].path < ops[j].path
		}
		return ops[i].method < ops[j].method
	})
	return ops
}

// relOrAbs shortens a path to be relative to the working directory when that is
// shorter, so the output is clickable from a `make` invocation at the repo root
// and still unambiguous from anywhere else.
func relOrAbs(path string) string {
	wd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(wd, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return rel
}
