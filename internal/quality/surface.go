package quality

import (
	"regexp"
	"sort"
	"strings"
)

// SurfaceItem is one public-surface declaration found in source text: an
// HTTP route, a CLI flag, or an environment variable the code reads.
type SurfaceItem struct {
	Kind string // "route", "flag", "env"
	Name string // "/v1/items", "--dry-run", "APP_PORT"
	Path string
	Line int
}

// Each pattern below is precise on purpose: it recognizes the declaration
// forms of the common routers, flag libraries and env readers, and nothing
// that merely looks similar. A form it does not know is not reported -- the
// docs check then has nothing to demand a mention for, which errs toward
// silence, never toward a false failure.
var (
	// Go net/http (1.22 patterns included), gorilla/mux, chi, gin, echo,
	// fiber; Express/Fastify/Koa-router and Flask/FastAPI decorators.
	routePatterns = []*regexp.Regexp{
		regexp.MustCompile(`\.(?:HandleFunc|Handle)\(\s*"(?:(?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS) )?(/[^"\s]*)"`),
		regexp.MustCompile(`\.(?:GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS|Get|Post|Put|Patch|Delete|Head|Options|Any|Group|Route)\(\s*"(/[^"\s]*)"`),
		regexp.MustCompile(`\b(?:app|router|server|fastify|api)\.(?:get|post|put|patch|delete|head|options|all|route)\(\s*['"\x60](/[^'"\x60\s]*)['"\x60]`),
		regexp.MustCompile(`@(?:app|router|api|bp|blueprint)\.(?:get|post|put|patch|delete|route|api_route)\(\s*['"](/[^'"\s]*)['"]`),
	}
	// Go flag/pflag/cobra, Python argparse/click, Node commander/yargs.
	flagPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\b(?:flag|fs|flags|f|cmd\.Flags\(\)|cmd\.PersistentFlags\(\)|Flags\(\)|PersistentFlags\(\))\.(?:String|Bool|Int|Int64|Uint|Uint64|Float64|Duration|StringSlice|StringArray|IntSlice|Func|BoolFunc|TextVar|Var)(?:Var)?P?\(\s*(?:&[\w.]+\s*,\s*)?"([a-zA-Z][\w-]*)"`),
		regexp.MustCompile(`\badd_argument\(\s*(?:['"]-\w['"]\s*,\s*)?['"]--([a-zA-Z][\w-]*)['"]`),
		regexp.MustCompile(`@click\.option\(\s*(?:['"]-\w['"]\s*,\s*)?['"]--([a-zA-Z][\w-]*)['"]`),
		regexp.MustCompile(`\.option\(\s*['"](?:-\w,\s*)?--([a-zA-Z][\w-]*)`),
	}
	envPatterns = []*regexp.Regexp{
		regexp.MustCompile(`\bos\.(?:Getenv|LookupEnv)\(\s*"([A-Z][A-Z0-9_]*)"`),
		regexp.MustCompile(`\bprocess\.env\.([A-Z][A-Z0-9_]*)\b`),
		regexp.MustCompile(`\bprocess\.env\[\s*['"]([A-Z][A-Z0-9_]*)['"]\s*\]`),
		regexp.MustCompile(`\bos\.(?:getenv|environ\.get)\(\s*['"]([A-Z][A-Z0-9_]*)['"]`),
		regexp.MustCompile(`\bos\.environ\[\s*['"]([A-Z][A-Z0-9_]*)['"]\s*\]`),
	}
)

// sourceExts are the files the surface patterns run over. Docs and specs are
// excluded: a route mentioned in a README is documentation, not a declaration.
var sourceExts = []string{".go", ".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx", ".py"}

// IsSourcePath reports whether p is a source file the surface scan reads.
// Tests are excluded: a route a test registers is not product surface.
func IsSourcePath(p string) bool {
	lp := strings.ToLower(p)
	if strings.HasSuffix(lp, "_test.go") || strings.Contains(lp, ".test.") || strings.Contains(lp, ".spec.") ||
		strings.HasPrefix(baseName(lp), "test_") {
		return false
	}
	for _, seg := range strings.Split(lp, "/") {
		if seg == "testdata" || seg == "node_modules" || seg == "vendor" || seg == "__tests__" {
			return false
		}
	}
	for _, ext := range sourceExts {
		if strings.HasSuffix(lp, ext) {
			return true
		}
	}
	return false
}

// SurfaceItems scans one file for routes, flags and env reads.
func SurfaceItems(path, content string) []SurfaceItem {
	var out []SurfaceItem
	lines := strings.Split(content, "\n")
	scan := func(kind string, pats []*regexp.Regexp, name func(string) string) {
		for i, line := range lines {
			for _, re := range pats {
				for _, m := range re.FindAllStringSubmatch(line, -1) {
					out = append(out, SurfaceItem{Kind: kind, Name: name(m[1]), Path: path, Line: i + 1})
				}
			}
		}
	}
	scan("route", routePatterns, func(s string) string { return s })
	scan("flag", flagPatterns, func(s string) string { return "--" + s })
	scan("env", envPatterns, func(s string) string { return s })
	return dedupeSurface(out)
}

func dedupeSurface(items []SurfaceItem) []SurfaceItem {
	seen := map[string]bool{}
	out := items[:0]
	for _, it := range items {
		k := it.Kind + "\x00" + it.Name
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, it)
	}
	return out
}

// AddedSurface returns the items present in after and absent from before, by
// (kind, name) -- so moving a route between files is not "added".
func AddedSurface(before, after []SurfaceItem) []SurfaceItem {
	had := map[string]bool{}
	for _, it := range before {
		had[it.Kind+"\x00"+it.Name] = true
	}
	var out []SurfaceItem
	for _, it := range after {
		if !had[it.Kind+"\x00"+it.Name] {
			out = append(out, it)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// routeParamRe normalizes route parameters so `/items/{id}`, `/items/:id`
// and `/items/<id>` all compare equal to each other.
var routeParamRe = regexp.MustCompile(`\{[^}/]*\}|:[A-Za-z_]\w*|<[^>/]*>`)

// NormalizeRoute reduces a route to its comparable shape.
func NormalizeRoute(r string) string {
	r = routeParamRe.ReplaceAllString(r, "{}")
	if len(r) > 1 {
		r = strings.TrimSuffix(r, "/")
	}
	return r
}

// MentionedIn reports whether a surface item is mentioned in any of docs
// (path -> content). A route matches on its normalized shape anywhere in the
// text; a flag must appear as `--name` (not followed by more name characters,
// so `--dry` is not satisfied by `--dry-run`); an env var as a whole word.
func MentionedIn(it SurfaceItem, docs map[string]string) (string, bool) {
	for _, p := range sortedDocKeys(docs) {
		text := docs[p]
		switch it.Kind {
		case "route":
			want := NormalizeRoute(it.Name)
			for _, m := range docRouteRe.FindAllString(text, -1) {
				if NormalizeRoute(m) == want {
					return p, true
				}
			}
		case "flag":
			if containsBounded(text, it.Name) {
				return p, true
			}
		case "env":
			if containsBounded(text, it.Name) {
				return p, true
			}
		}
	}
	return "", false
}

var docRouteRe = regexp.MustCompile(`/[A-Za-z0-9_{}:<>.\-/]*`)

// containsBounded reports whether s contains word not followed by a name
// character and not preceded by one (a flag/env-var whole-token match).
func containsBounded(s, word string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], word)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(word)
		before := start == 0 || !isNameByte(s[start-1])
		after := end >= len(s) || !isNameByte(s[end])
		if before && after {
			return true
		}
		i = start + 1
	}
}

func isNameByte(c byte) bool {
	return c == '_' || c == '-' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func sortedDocKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func baseName(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}
