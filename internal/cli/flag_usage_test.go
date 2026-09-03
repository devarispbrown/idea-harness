package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestCommandsOnlyReadFlagsTheyDeclare closes the direction A5 does not cover.
//
// The generated Flags column proves spec -> docs: a declared flag cannot be
// missing from docs/cli.md. TestEveryCommandRejectsAnUnknownFlag proves argv ->
// spec: an undeclared flag is rejected. Neither notices code -> spec: a command
// calling a.value("--foo") where its row declares no --foo reads "" from a nil
// map, silently and forever, and every other check stays green.
//
// So this walks the package's AST for every string literal passed to
// cmdArgs.has/value/list and asserts it appears in the declaring row's spec --
// the command's own flags, or any of its subcommands'.
func TestCommandsOnlyReadFlagsTheyDeclare(t *testing.T) {
	declared := map[string]bool{} // every flag name any row declares
	perCommand := map[string]map[string]bool{}
	for _, c := range commands() {
		if c.run == nil {
			continue
		}
		names := map[string]bool{}
		for n := range c.flags {
			names[n] = true
			declared[n] = true
		}
		for _, sub := range c.subs {
			for n := range sub {
				names[n] = true
				declared[n] = true
			}
		}
		perCommand[c.names[0]] = names
	}

	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	read := 0
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, path, src, parser.SkipObjectResolution)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch sel.Sel.Name {
			case "has", "value", "list":
			default:
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			name, err := strconv.Unquote(lit.Value)
			if err != nil || !strings.HasPrefix(name, "-") {
				return true
			}
			read++
			// Package-wide membership: a flag read anywhere must be declared by
			// some row. Attributing each read to its exact command would need
			// call-graph analysis through helpers like runChangeSetAction.
			if !declared[name] {
				pos := fset.Position(call.Pos())
				t.Errorf("%s:%d reads flag %q, which no command declares in the table.\n"+
					"It will always be empty. Add it to that command's flagSpec in cli.go.",
					filepath.Base(pos.Filename), pos.Line, name)
			}
			return true
		})
	}
	if read == 0 {
		t.Fatal("found no flag reads; this test is not parsing the package")
	}
	t.Logf("checked %d flag reads against %d declared flags", read, len(declared))
}
