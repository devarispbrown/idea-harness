package main

// Route-to-prose checking for docs/api.md.
//
// `make api-check` proves routes match docs/openapi.yaml. The spec was correct
// -- all 81 operations -- while docs/api.md described 71 and README advertised
// it as covering "every endpoint". Nothing compared routes to the human
// document, so ten endpoints were missing and both existing guards stayed
// green. This is that second axis.
//
// It proves row *existence*, not row correctness: a row with the right path and
// a wrong description is still invisible here. Said plainly rather than sold as
// "routes match the prose".
//
// The tables are not generated wholesale, because the Purpose column is real
// editorial writing and the section grouping is an editorial judgement. Moving
// 81 prose lines into routes.go would trade this drift for a worse one. Instead
// this mirrors the docs/docs-check pair: `make api-docs` adds what is missing
// with a TODO, and `make api-docs-check` fails while anything is missing.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const apiDocsPath = "docs/api.md"

// apiDocsHeader is the header every endpoint table carries. The three
// non-endpoint tables in the document use different headers, so keying on this
// scopes the scan safely.
const apiDocsHeader = "| Method | Path | Purpose |"

// apiRowRe matches an endpoint row: `| GET | `/path` | purpose |`.
var apiRowRe = regexp.MustCompile("^\\| *([A-Z]+) *\\| *`([^`]+)` *\\|(.*)\\|\\s*$")

// docRow is one endpoint row in docs/api.md.
type docRow struct {
	op   operation
	line int
}

// parseAPIDocs reads the endpoint rows out of docs/api.md.
//
// Paths in the document omit the /api/v1 prefix (stated once under "Base URL")
// and some carry query strings, so both are normalised before comparison.
func parseAPIDocs(path string) ([]docRow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(string(data), "\n")

	var rows []docRow
	inTable := false
	for i, line := range lines {
		if strings.HasPrefix(line, "#") {
			inTable = false
			continue
		}
		if strings.HasPrefix(line, apiDocsHeader) {
			inTable = true
			continue
		}
		if strings.TrimSpace(line) == "" {
			inTable = false
			continue
		}
		if !inTable {
			continue
		}
		m := apiRowRe.FindStringSubmatch(line)
		if m == nil {
			continue // the |---|---|---| separator
		}
		rows = append(rows, docRow{
			op:   operation{method: m[1], path: docPathToRoute(m[2])},
			line: i + 1,
		})
	}
	return rows, nil
}

// docPathToRoute turns a documented path into the pattern routes.go registers:
// restores the /api/v1 prefix and drops any query string.
func docPathToRoute(p string) string {
	if i := strings.IndexByte(p, '?'); i >= 0 {
		p = p[:i]
	}
	if !strings.HasPrefix(p, "/api/v1") {
		p = "/api/v1" + p
	}
	return p
}

// routeToDocPath is the inverse, for rows this tool writes.
func routeToDocPath(p string) string { return strings.TrimPrefix(p, "/api/v1") }

// runAPIDocsCheck compares the endpoint tables against the registered routes.
func runAPIDocsCheck() error {
	missing, stale, err := apiDocsDiff()
	if err != nil {
		return err
	}
	if len(missing) == 0 && len(stale) == 0 {
		return nil
	}
	var b strings.Builder
	if len(missing) > 0 {
		var names []string
		for _, op := range missing {
			names = append(names, "  "+op.String())
		}
		sort.Strings(names)
		fmt.Fprintf(&b, "%d route(s) missing from %s:\n%s\n\n",
			len(missing), apiDocsPath, strings.Join(names, "\n"))
	}
	if len(stale) > 0 {
		var names []string
		for _, op := range stale {
			names = append(names, "  "+op.String())
		}
		sort.Strings(names)
		fmt.Fprintf(&b, "%d row(s) in %s that no route serves:\n%s\n\n",
			len(stale), apiDocsPath, strings.Join(names, "\n"))
	}
	b.WriteString("Run `make api-docs` to add the missing rows with a TODO purpose, write the purpose,\n")
	b.WriteString("and remove the stale ones. This check proves a row exists per route, not that its\n")
	b.WriteString("description is right.")
	return fmt.Errorf("%s", b.String())
}

// apiDocsDiff returns routes with no row, and rows with no route.
func apiDocsDiff() (missing, stale []operation, err error) {
	root, err := repoRoot()
	if err != nil {
		return nil, nil, err
	}
	routes, err := parseRoutes(filepath.Join(root, routesPkg))
	if err != nil {
		return nil, nil, err
	}
	rows, err := parseAPIDocs(filepath.Join(root, apiDocsPath))
	if err != nil {
		return nil, nil, err
	}
	documented := map[operation]bool{}
	for _, r := range rows {
		documented[r.op] = true
	}
	for op := range routes {
		// Same scope as api-check: only paths under /api/. The documentation
		// endpoints (/openapi.yaml, /openapi.json, /api-reference) sit outside it
		// because the token middleware guards /api/ only -- they are plumbing for
		// the spec, not part of the surface it describes, and api.md introduces
		// them in prose rather than in the endpoint tables.
		if !strings.HasPrefix(op.path, "/api/") {
			continue
		}
		if !documented[op] {
			missing = append(missing, op)
		}
	}
	for _, r := range rows {
		if _, ok := routes[r.op]; !ok {
			stale = append(stale, r.op)
		}
	}
	return missing, stale, nil
}

// runAPIDocsGenerate appends a row per undocumented route, under a heading that
// names them, so the writer moves each into the right section and describes it.
//
// It appends rather than guessing a section: putting a row in the wrong table
// is worse than putting it in an obvious to-do list, and the check stays red
// until the rows are real.
func runAPIDocsGenerate() error {
	missing, stale, err := apiDocsDiff()
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		fmt.Printf("api-docs: %s already documents every route\n", apiDocsPath)
		if len(stale) > 0 {
			fmt.Printf("api-docs: %d row(s) describe routes that no longer exist; remove them by hand\n", len(stale))
		}
		return nil
	}
	root, err := repoRoot()
	if err != nil {
		return err
	}
	full := filepath.Join(root, apiDocsPath)
	data, err := os.ReadFile(full)
	if err != nil {
		return err
	}
	sort.Slice(missing, func(i, j int) bool {
		if missing[i].path != missing[j].path {
			return missing[i].path < missing[j].path
		}
		return missing[i].method < missing[j].method
	})
	var b strings.Builder
	b.WriteString("\n## Undocumented routes (added by `make api-docs`)\n\n")
	b.WriteString("These routes are registered but had no row. Move each into the section it belongs to\n")
	b.WriteString("and replace the TODO with what it does, then delete this heading.\n\n")
	b.WriteString(apiDocsHeader + "\n| --- | --- | --- |\n")
	for _, op := range missing {
		fmt.Fprintf(&b, "| %s | `%s` | TODO |\n", op.method, routeToDocPath(op.path))
	}
	if err := os.WriteFile(full, append(data, []byte(b.String())...), 0o644); err != nil {
		return err
	}
	fmt.Printf("api-docs: added %d row(s) to %s under \"Undocumented routes\"\n", len(missing), apiDocsPath)
	return nil
}
