package main

// Conflict-response checking.
//
// The DX audit found `advance` and `revise` returning 500 where four sibling
// handlers returned 409 for the identical condition. The fix gave preconditions
// a type so writeErr answers 409 wherever one surfaces -- but the spec is a
// separate artifact, and four operations that started returning 409 did not
// declare it. A generated client would have had no branch for the new status.
//
// api-check compares paths and bodycheck compares request fields, so neither
// could see it. This closes that: an operation whose handler can produce a 409
// must document one.
//
// "Can produce a 409" is derived, not listed:
//
//   - the handler writes http.StatusConflict directly, or
//   - the handler calls a Runtime method whose body contains preconditionf(),
//     which writeErr maps to 409 regardless of the status the handler passed.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// conflictSources returns the handler names that can answer 409.
func conflictSources(dir string) (map[string]bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		files[name] = f
	}

	// Runtime methods that can return a precondition error.
	preconditionMethods := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "preconditionf" {
					preconditionMethods[fn.Name.Name] = true
				}
				return true
			})
		}
	}

	out := map[string]bool{}
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				// Direct: writeError(w, http.StatusConflict, …)
				if id, ok := call.Fun.(*ast.Ident); ok && id.Name == "writeError" && len(call.Args) >= 2 {
					if isStatusConflict(call.Args[1]) {
						out[fn.Name.Name] = true
					}
				}
				// Indirect: a call to a Runtime method that returns preconditionf.
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && preconditionMethods[sel.Sel.Name] {
					out[fn.Name.Name] = true
				}
				return true
			})
		}
	}
	return out, nil
}

func isStatusConflict(e ast.Expr) bool {
	sel, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "http" && sel.Sel.Name == "StatusConflict"
}

// specResponseCodes maps operationId to the status codes it declares.
func specResponseCodes(path string) (map[string]map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := map[string]map[string]bool{}
	for _, item := range doc.Paths {
		for method, node := range item {
			if !httpMethods[strings.ToLower(method)] {
				continue
			}
			var op struct {
				OperationID string               `yaml:"operationId"`
				Responses   map[string]yaml.Node `yaml:"responses"`
			}
			if node.Decode(&op) != nil || op.OperationID == "" {
				continue
			}
			codes := map[string]bool{}
			for code := range op.Responses {
				codes[code] = true
			}
			out[op.OperationID] = codes
		}
	}
	return out, nil
}

// runConflictCheck fails when an operation that can answer 409 does not say so.
func runConflictCheck(routesDir, specPath string) error {
	sources, err := conflictSources(routesDir)
	if err != nil {
		return err
	}
	handlers, err := routeHandlers(routesDir)
	if err != nil {
		return err
	}
	ids, err := specOperationIDs(specPath)
	if err != nil {
		return err
	}
	codes, err := specResponseCodes(specPath)
	if err != nil {
		return err
	}

	var problems []string
	for op, handler := range handlers {
		if !sources[handler] {
			continue
		}
		opID, ok := ids[op]
		if !ok {
			continue // api-check reports undocumented routes
		}
		if !codes[opID]["409"] {
			problems = append(problems, fmt.Sprintf(
				"  %s (%s) can answer 409 via %s, but declares no 409 response", op, opID, handler))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("conflict responses missing from %s:\n%s\n\n"+
		"A precondition failure reaches the client as 409 wherever writeErr sees one, so a client\n"+
		"generated from this spec would have no branch for it. Add:\n"+
		"  \"409\": { $ref: \"#/components/responses/Conflict\" }",
		filepath.ToSlash(relOrAbs(specPath)), strings.Join(problems, "\n"))
}
