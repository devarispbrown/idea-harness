package quality

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"path"
	"sort"
	"strings"
)

// GoSymbol is one exported identifier of an importable Go package, rendered
// so two versions can be compared without type-checking: the shape a client
// compiles against (a func's parameter and result types, a struct's exported
// fields, an interface's method set), never names a client cannot see
// (parameter names, unexported fields).
type GoSymbol struct {
	Key  string // "<dir>.<Name>", "<dir>.<Type>.<Method>" or "<dir>.<Type>.<Field>"
	Name string // what a requirement would mention: "Parse", "Config.Timeout"
	Kind string // func, method, type, field, interface_method, const, var
	Sig  string
	Path string
	Line int
	// Documented is whether the declaration carries a doc comment (read by
	// docs_completeness, not by the API diff).
	Documented bool
}

// IsPublicGoPath reports whether a .go file is part of an importable public
// API: not a test, not under internal/ (unimportable outside its module),
// not under testdata/ or vendor/. A package main is excluded later, from its
// package clause.
func IsPublicGoPath(p string) bool {
	if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
		return false
	}
	for _, seg := range strings.Split(path.Dir(p), "/") {
		switch seg {
		case "internal", "testdata", "vendor":
			return false
		}
	}
	return true
}

// ExportedGoAPI parses the given files (workspace path -> content; every file
// must belong to the package of its directory) and returns their exported
// API keyed by GoSymbol.Key. It uses go/parser only: no type checker, no
// module loading, nothing that needs the network or a build. A file that does
// not parse is reported in the error and skipped; package main is skipped
// silently (it exports nothing importable).
func ExportedGoAPI(files map[string][]byte) (map[string]GoSymbol, error) {
	out := map[string]GoSymbol{}
	var errs []string
	names := make([]string, 0, len(files))
	for p := range files {
		names = append(names, p)
	}
	sort.Strings(names)
	for _, p := range names {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, files[p], parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if f.Name.Name == "main" {
			continue
		}
		collectGoFile(out, path.Dir(p), p, fset, f)
	}
	if len(errs) > 0 {
		return out, fmt.Errorf("quality: %s", strings.Join(errs, "; "))
	}
	return out, nil
}

func collectGoFile(out map[string]GoSymbol, dir, file string, fset *token.FileSet, f *ast.File) {
	add := func(key, name, kind, sig string, pos token.Pos, documented bool) {
		out[dir+"."+key] = GoSymbol{Key: dir + "." + key, Name: name, Kind: kind, Sig: sig, Path: file, Line: fset.Position(pos).Line, Documented: documented}
	}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if !d.Name.IsExported() {
				continue
			}
			sig := renderFuncType(fset, d.Type)
			if d.Recv == nil {
				add(d.Name.Name, d.Name.Name, "func", sig, d.Pos(), d.Doc != nil)
				continue
			}
			recv := receiverTypeName(d.Recv)
			if recv == "" || !ast.IsExported(recv) {
				continue
			}
			add(recv+"."+d.Name.Name, recv+"."+d.Name.Name, "method", sig, d.Pos(), d.Doc != nil)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if !s.Name.IsExported() {
						continue
					}
					documented := d.Doc != nil || s.Doc != nil
					switch t := s.Type.(type) {
					case *ast.StructType:
						add(s.Name.Name, s.Name.Name, "type", "struct", s.Pos(), documented)
						for _, fld := range t.Fields.List {
							typ := render(fset, fld.Type)
							if len(fld.Names) == 0 { // embedded
								name := strings.TrimPrefix(typ, "*")
								if i := strings.LastIndex(name, "."); i >= 0 {
									name = name[i+1:]
								}
								if ast.IsExported(name) {
									add(s.Name.Name+"."+name, s.Name.Name+"."+name, "field", "embedded "+typ, fld.Pos(), true)
								}
								continue
							}
							for _, n := range fld.Names {
								if n.IsExported() {
									add(s.Name.Name+"."+n.Name, s.Name.Name+"."+n.Name, "field", typ, n.Pos(), true)
								}
							}
						}
					case *ast.InterfaceType:
						add(s.Name.Name, s.Name.Name, "type", "interface", s.Pos(), documented)
						for _, m := range t.Methods.List {
							for _, n := range m.Names {
								if n.IsExported() {
									add(s.Name.Name+"."+n.Name, s.Name.Name+"."+n.Name, "interface_method", render(fset, m.Type), n.Pos(), true)
								}
							}
							if len(m.Names) == 0 { // embedded interface
								add(s.Name.Name+".<embed "+render(fset, m.Type)+">", s.Name.Name, "interface_method", "embedded "+render(fset, m.Type), m.Pos(), true)
							}
						}
					default:
						kind := "type"
						sig := render(fset, s.Type)
						if s.Assign.IsValid() {
							sig = "= " + sig
						}
						add(s.Name.Name, s.Name.Name, kind, sig, s.Pos(), documented)
					}
				case *ast.ValueSpec:
					kind := "var"
					if d.Tok == token.CONST {
						kind = "const"
					}
					typ := ""
					if s.Type != nil {
						typ = render(fset, s.Type)
					}
					for _, n := range s.Names {
						if n.IsExported() {
							add(n.Name, n.Name, kind, typ, n.Pos(), d.Doc != nil || s.Doc != nil || s.Comment != nil)
						}
					}
				}
			}
		}
	}
}

// renderFuncType renders a signature's parameter and result TYPES only:
// renaming a parameter is not a change a caller can observe.
func renderFuncType(fset *token.FileSet, ft *ast.FuncType) string {
	list := func(fl *ast.FieldList) string {
		if fl == nil {
			return ""
		}
		var parts []string
		for _, f := range fl.List {
			n := len(f.Names)
			if n == 0 {
				n = 1
			}
			for i := 0; i < n; i++ {
				parts = append(parts, render(fset, f.Type))
			}
		}
		return strings.Join(parts, ", ")
	}
	tp := ""
	if ft.TypeParams != nil {
		tp = "[" + list(ft.TypeParams) + "]"
	}
	return tp + "(" + list(ft.Params) + ") (" + list(ft.Results) + ")"
}

func receiverTypeName(recv *ast.FieldList) string {
	if recv == nil || len(recv.List) == 0 {
		return ""
	}
	t := recv.List[0].Type
	for {
		switch x := t.(type) {
		case *ast.StarExpr:
			t = x.X
		case *ast.IndexExpr:
			t = x.X
		case *ast.IndexListExpr:
			t = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}

func render(fset *token.FileSet, n ast.Node) string {
	var b bytes.Buffer
	if err := printer.Fprint(&b, fset, n); err != nil {
		return ""
	}
	// Collapse formatting so a reflowed struct literal type compares equal.
	return strings.Join(strings.Fields(b.String()), " ")
}

// DiffGoAPI compares two exported-API sets. Breaking: a symbol removed, a
// func/method signature or a field type changed, a type's kind changed, or a
// method added to an exported interface (every implementation outside the
// package stops satisfying it). Additions are reported as not breaking.
//
// It does not see changes only a type checker would: a changed underlying
// type behind a named type used in a signature, method-set changes through
// embedding, or a constant whose value changed. Those are honest gaps, not
// "no change".
func DiffGoAPI(before, after map[string]GoSymbol) []APIChange {
	var out []APIChange
	keys := make([]string, 0, len(before))
	for k := range before {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b := before[k]
		a, ok := after[k]
		if !ok {
			if parent := parentTypeKey(b); parent != "" {
				if _, typeKept := after[parent]; !typeKept {
					continue // the type itself is reported removed; its members are not news
				}
			}
			out = append(out, APIChange{Breaking: true, Kind: "go_" + b.Kind + "_removed", Subject: b.Name,
				Detail: fmt.Sprintf("exported %s %s was removed (package %s)", b.Kind, b.Name, path.Dir(b.Path)), Path: b.Path, Line: b.Line})
			continue
		}
		if a.Kind != b.Kind {
			out = append(out, APIChange{Breaking: true, Kind: "go_kind_changed", Subject: b.Name,
				Detail: fmt.Sprintf("%s changed from a %s to a %s", b.Name, b.Kind, a.Kind), Path: a.Path, Line: a.Line})
			continue
		}
		if a.Sig != b.Sig && !(b.Kind == "type" && (b.Sig == "struct" || b.Sig == "interface")) {
			out = append(out, APIChange{Breaking: true, Kind: "go_" + b.Kind + "_changed", Subject: b.Name,
				Detail: fmt.Sprintf("%s %s changed: %s -> %s", b.Kind, b.Name, b.Sig, a.Sig), Path: a.Path, Line: a.Line})
		}
	}
	keys = keys[:0]
	for k := range after {
		if _, ok := before[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		a := after[k]
		if a.Kind == "interface_method" {
			// Only breaking when the interface itself existed before: a brand-new
			// interface has no implementations to break.
			if _, existed := before[parentTypeKey(a)]; existed {
				out = append(out, APIChange{Breaking: true, Kind: "go_interface_method_added", Subject: a.Name,
					Detail: fmt.Sprintf("method %s was added to an existing exported interface; implementations outside the package no longer satisfy it", a.Name), Path: a.Path, Line: a.Line})
				continue
			}
		}
		out = append(out, APIChange{Breaking: false, Kind: "go_" + a.Kind + "_added", Subject: a.Name,
			Detail: fmt.Sprintf("exported %s %s was added", a.Kind, a.Name), Path: a.Path, Line: a.Line})
	}
	return out
}

// parentTypeKey returns the key of the type a field or method belongs to, or
// "" for a top-level symbol.
func parentTypeKey(s GoSymbol) string {
	switch s.Kind {
	case "field", "method", "interface_method":
	default:
		return ""
	}
	dir := path.Dir(s.Path)
	rest := strings.TrimPrefix(s.Key, dir+".")
	typ, _, ok := strings.Cut(rest, ".")
	if !ok {
		return ""
	}
	return dir + "." + typ
}
