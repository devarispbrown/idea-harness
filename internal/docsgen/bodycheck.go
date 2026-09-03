package main

// Request-body field checking.
//
// `make api-check` proves every route has an operation in the spec and vice
// versa. It deliberately does not read schemas -- and that gap is what let
// `idea verify --fail` and `--waive` ship: the handler decoded them, the daemon
// mapped them to real verification statuses, and neither the spec nor any
// document mentioned them. Both existing guards were blind to it, because
// api-check compares paths and docs-check regenerates the CLI table from the
// same summary string the table is built from.
//
// Field names only, not types. The point is to catch a field that exists on one
// side and not the other, which is the drift that actually happens; a type
// mismatch needs a schema library and is worth much less.
//
// This is cheap because the handlers are uniform: every body-decoding handler
// in internal/daemon uses `var body struct{…}` with json tags, immediately
// followed by json.NewDecoder(r.Body).Decode(&body).

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// handlerBodyFields maps a handler function name to the JSON field names it
// decodes from the request body.
func handlerBodyFields(dir string) (map[string][]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	out := map[string][]string{}

	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if fields := bodyFieldsIn(fn.Body); len(fields) > 0 {
				out[fn.Name.Name] = fields
			}
		}
	}
	return out, nil
}

// bodyFieldsIn finds `var body struct{…}` in a function body and returns its
// json tag names.
func bodyFieldsIn(body *ast.BlockStmt) []string {
	var fields []string
	ast.Inspect(body, func(n ast.Node) bool {
		decl, ok := n.(*ast.GenDecl)
		if !ok || decl.Tok != token.VAR {
			return true
		}
		for _, spec := range decl.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) != 1 || vs.Names[0].Name != "body" {
				continue
			}
			st, ok := vs.Type.(*ast.StructType)
			if !ok {
				continue
			}
			for _, f := range st.Fields.List {
				if f.Tag == nil {
					continue
				}
				tag, err := strconv.Unquote(f.Tag.Value)
				if err != nil {
					continue
				}
				name := strings.Split(reflect.StructTag(tag).Get("json"), ",")[0]
				if name != "" && name != "-" {
					fields = append(fields, name)
				}
			}
		}
		return true
	})
	sort.Strings(fields)
	return fields
}

// operationNode is the part of an OpenAPI operation this check reads.
//
// Decoded per method key rather than as a map of methods, because a path item
// also carries a `parameters:` sequence, which is not an operation.
type operationNode struct {
	OperationID string `yaml:"operationId"`
	RequestBody struct {
		Content map[string]struct {
			Schema struct {
				Properties map[string]yaml.Node `yaml:"properties"`
			} `yaml:"schema"`
		} `yaml:"content"`
	} `yaml:"requestBody"`
}

// eachOperation decodes every method entry under `paths:` and calls fn.
func eachOperation(path string, fn func(op operation, node operationNode)) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var doc struct {
		Paths map[string]map[string]yaml.Node `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return err
	}
	for p, item := range doc.Paths {
		for method, node := range item {
			if !httpMethods[strings.ToLower(method)] {
				continue // "parameters", "summary", a $ref: not an operation
			}
			var op operationNode
			if err := node.Decode(&op); err != nil {
				return fmt.Errorf("%s %s: %w", strings.ToUpper(method), p, err)
			}
			fn(operation{method: strings.ToUpper(method), path: p}, op)
		}
	}
	return nil
}

// specRequestFields maps operationId to the property names declared under its
// JSON request body.
func specRequestFields(path string) (map[string][]string, error) {
	out := map[string][]string{}
	err := eachOperation(path, func(_ operation, op operationNode) {
		if op.OperationID == "" {
			return
		}
		content, ok := op.RequestBody.Content["application/json"]
		if !ok {
			return
		}
		var names []string
		for name := range content.Schema.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		out[op.OperationID] = names
	})
	return out, err
}

// diffFields reports names present in got but not want, and vice versa.
func diffFields(got, want []string) (extra, missing []string) {
	inWant := map[string]bool{}
	for _, w := range want {
		inWant[w] = true
	}
	inGot := map[string]bool{}
	for _, g := range got {
		inGot[g] = true
		if !inWant[g] {
			extra = append(extra, g)
		}
	}
	for _, w := range want {
		if !inGot[w] {
			missing = append(missing, w)
		}
	}
	return extra, missing
}

func fmtNames(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = strconv.Quote(n)
	}
	return strings.Join(q, ", ")
}

// routeHandlers maps each registered operation to the handler function name it
// dispatches to, e.g. `s.handleVerify` -> "handleVerify".
func routeHandlers(dir string) (map[operation]string, error) {
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
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "HandleFunc" {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			pattern, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			op, ok := patternOperation(pattern)
			if !ok {
				return true
			}
			if h, ok := call.Args[1].(*ast.SelectorExpr); ok {
				out[op] = h.Sel.Name
			}
			return true
		})
	}
	return out, nil
}

// specOperationIDs maps each documented operation to its operationId.
func specOperationIDs(path string) (map[operation]string, error) {
	out := map[operation]string{}
	err := eachOperation(path, func(key operation, op operationNode) {
		if op.OperationID != "" {
			out[key] = op.OperationID
		}
	})
	return out, err
}

// runBodyCheck compares each handler's decoded request-body fields against the
// properties its operation declares in the spec.
func runBodyCheck(routesDir, specPath string) error {
	handlers, err := routeHandlers(routesDir)
	if err != nil {
		return err
	}
	fields, err := handlerBodyFields(routesDir)
	if err != nil {
		return err
	}
	specFields, err := specRequestFields(specPath)
	if err != nil {
		return err
	}
	ids, err := specOperationIDs(specPath)
	if err != nil {
		return err
	}

	var problems []string
	for op, handler := range handlers {
		got, decodes := fields[handler]
		if !decodes {
			continue // handler reads no JSON body
		}
		opID, documented := ids[op]
		if !documented {
			continue // api-check reports undocumented routes already
		}
		want, hasBody := specFields[opID]
		if !hasBody {
			problems = append(problems, fmt.Sprintf(
				"  %s (%s) decodes %s but the spec declares no JSON request body",
				op, opID, fmtNames(got)))
			continue
		}
		extra, missing := diffFields(got, want)
		if len(extra) > 0 {
			problems = append(problems, fmt.Sprintf(
				"  %s (%s) decodes %s, which the spec does not document",
				op, opID, fmtNames(extra)))
		}
		if len(missing) > 0 {
			problems = append(problems, fmt.Sprintf(
				"  %s (%s) documents %s, which the handler does not decode",
				op, opID, fmtNames(missing)))
		}
	}
	if len(problems) == 0 {
		return nil
	}
	sort.Strings(problems)
	return fmt.Errorf("request bodies disagree with %s:\n%s\n\n"+
		"Field names are compared, not types. A flag the handler accepts and the spec omits is how\n"+
		"`idea verify --fail` and `--waive` shipped undocumented.",
		filepath.ToSlash(relOrAbs(specPath)), strings.Join(problems, "\n"))
}
