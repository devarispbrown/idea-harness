package quality

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// ChangedFile is one file the initiative's applied change sets touched, with
// its content before the first of them and as it is in the workspace now.
// BeforeKnown is false when the pre-image could not be recovered (it was
// never persisted); a check that needs it then reports not_measured.
type ChangedFile struct {
	Path         string
	Before       string
	BeforeExists bool
	BeforeKnown  bool
	After        string
	AfterExists  bool
}

// IsDocPath reports whether p is documentation the docs checks read for
// mentions: Markdown/MDX/reStructuredText/AsciiDoc anywhere, plus OpenAPI
// specs (callers add those by content).
func IsDocPath(p string) bool {
	lp := strings.ToLower(p)
	for _, ext := range []string{".md", ".mdx", ".markdown", ".rst", ".adoc"} {
		if strings.HasSuffix(lp, ext) {
			return !strings.Contains(lp, "node_modules/") && !strings.Contains(lp, "vendor/")
		}
	}
	return false
}

// FindReadme returns the root README's path among files, preferring
// README.md. "" when the project has none at its root.
func FindReadme(paths []string) string {
	best := ""
	for _, p := range paths {
		if strings.Contains(p, "/") {
			continue
		}
		lp := strings.ToLower(p)
		if lp == "readme.md" {
			return p
		}
		if lp == "readme" || strings.HasPrefix(lp, "readme.") {
			best = p
		}
	}
	return best
}

// Heading is one Markdown heading (ATX `#` or setext underline) and its line.
type Heading struct {
	Text string
	Line int
}

var atxHeadingRe = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.+?)\s*#*\s*$`)

// Headings extracts a Markdown (or reStructuredText-style underlined)
// document's headings, skipping fenced code blocks so a `# comment` inside a
// shell example is not read as a section.
func Headings(text string) []Heading {
	lines := strings.Split(text, "\n")
	var out []Heading
	inFence := false
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if m := atxHeadingRe.FindStringSubmatch(line); m != nil {
			out = append(out, Heading{Text: m[1], Line: i + 1})
			continue
		}
		if i > 0 && t != "" && (strings.Trim(t, "=") == "" || strings.Trim(t, "-") == "") && len(t) >= 3 {
			prev := strings.TrimSpace(lines[i-1])
			if prev != "" && !strings.HasPrefix(prev, "#") {
				out = append(out, Heading{Text: prev, Line: i})
			}
		}
	}
	return out
}

// The README sections docs_completeness looks for, by heading. A heading
// matches when any of its words starts one of these phrases; the lists are
// the conventional names, and a README that uses another is reported as a
// warning (never a fail) so the cost of an unusual name is a note.
var (
	installHeadingRe = regexp.MustCompile(`(?i)\b(install|installation|installing|setup|set up|getting started|quick ?start|build|building|requirements|prerequisites|development)\b`)
	usageHeadingRe   = regexp.MustCompile(`(?i)\b(usage|using|use|examples?|getting started|quick ?start|how to|running|run|tutorial|cli|commands|api)\b`)
	configHeadingRe  = regexp.MustCompile(`(?i)\b(config|configuration|configuring|environment|env|settings|options|variables)\b`)
)

func findHeading(hs []Heading, re *regexp.Regexp) (Heading, bool) {
	for _, h := range hs {
		if re.MatchString(h.Text) {
			return h, true
		}
	}
	return Heading{}, false
}

// DocsInput is what docs_completeness judges.
type DocsInput struct {
	// Paths is every workspace file path (for the README lookup).
	Paths []string
	// Docs is every documentation file's content (IsDocPath, plus OpenAPI
	// specs), path -> text.
	Docs map[string]string
	// Changed are the initiative's changed files.
	Changed []ChangedFile
	// GoAdded are exported Go symbols the change added (from DiffGoAPI's
	// additions, with Documented filled in).
	GoAdded []GoSymbol
	// OpenAPIAdded are operations the change added to a spec, each with
	// whether it carries a summary or description.
	OpenAPIAdded []OpenAPIOperation
}

// OpenAPIOperation is one operation of an OpenAPI spec.
type OpenAPIOperation struct {
	Method, Route, SpecPath string
	Line                    int
	Described               bool
}

// DocsChecks runs docs_completeness's deterministic checks:
//
//  1. readme: a README exists at the workspace root (fail otherwise).
//  2. readme_install / readme_usage: it has a section whose heading names
//     installation/setup and one naming usage/examples (warn otherwise).
//  3. readme_configuration: when the change added environment-variable
//     reads, the README has a configuration/environment section (warn).
//  4. routes_documented / flags_documented: every HTTP route and CLI flag
//     the change added in source is mentioned in some doc or OpenAPI spec
//     (fail, citing the declaring line).
//  5. env_documented: every environment variable the change started reading
//     is mentioned in some doc (warn).
//  6. go_doc_comments: every exported Go func, method, type, const or var
//     the change added carries a doc comment (warn).
//  7. openapi_described: every operation the change added to a spec has a
//     summary or description (warn).
//
// A check with nothing to judge is not_applicable.
func DocsChecks(in DocsInput) []domain.QualityCheck {
	det := domain.QualityMethodDeterministic
	var out []domain.QualityCheck
	readme := FindReadme(in.Paths)
	if readme == "" {
		out = append(out, domain.QualityCheck{Name: "readme", Status: domain.QualityFail, Method: det,
			Detail: "no README at the workspace root: a fresh clone has nothing telling a reader how to install, use or configure it"})
	} else {
		out = append(out, domain.QualityCheck{Name: "readme", Status: domain.QualityPass, Method: det,
			Detail: readme + " exists", Citations: []domain.QualityCitation{{Path: readme}}})
	}
	hs := Headings(in.Docs[readme])

	section := func(name, what string, re *regexp.Regexp) domain.QualityCheck {
		if readme == "" {
			return domain.QualityCheck{Name: name, Status: domain.QualityFail, Method: det, Detail: "no README to hold a " + what + " section"}
		}
		if h, ok := findHeading(hs, re); ok {
			return domain.QualityCheck{Name: name, Status: domain.QualityPass, Method: det,
				Detail: fmt.Sprintf("%s section %q", what, h.Text), Citations: []domain.QualityCitation{{Path: readme, Line: h.Line}}}
		}
		return domain.QualityCheck{Name: name, Status: domain.QualityWarn, Method: det,
			Detail: fmt.Sprintf("%s has no heading naming %s", readme, what), Citations: []domain.QualityCitation{{Path: readme}}}
	}
	out = append(out, section("readme_install", "installation/setup", installHeadingRe))
	out = append(out, section("readme_usage", "usage/examples", usageHeadingRe))

	var before, after []SurfaceItem
	for _, cf := range in.Changed {
		if !IsSourcePath(cf.Path) {
			continue
		}
		if cf.BeforeExists {
			before = append(before, SurfaceItems(cf.Path, cf.Before)...)
		}
		if cf.AfterExists {
			after = append(after, SurfaceItems(cf.Path, cf.After)...)
		}
	}
	added := AddedSurface(before, after)
	byKind := map[string][]SurfaceItem{}
	for _, it := range added {
		byKind[it.Kind] = append(byKind[it.Kind], it)
	}

	if len(byKind["env"]) == 0 {
		out = append(out, domain.QualityCheck{Name: "readme_configuration", Status: domain.QualityNotApplicable, Method: det,
			Detail: "the change added no environment-variable reads"})
	} else {
		c := section("readme_configuration", "configuration/environment", configHeadingRe)
		out = append(out, c)
	}

	mention := func(name, kind string, missing domain.QualityStatus, noun string) domain.QualityCheck {
		items := byKind[kind]
		if len(items) == 0 {
			return domain.QualityCheck{Name: name, Status: domain.QualityNotApplicable, Method: det, Detail: "the change added no " + noun}
		}
		var undocumented []SurfaceItem
		var cites []domain.QualityCitation
		for _, it := range items {
			if where, ok := MentionedIn(it, in.Docs); ok {
				cites = append(cites, domain.QualityCitation{Path: where})
				continue
			}
			undocumented = append(undocumented, it)
		}
		if len(undocumented) == 0 {
			return domain.QualityCheck{Name: name, Status: domain.QualityPass, Method: det,
				Detail: fmt.Sprintf("all %d added %s are mentioned in the docs", len(items), noun), Citations: dedupeCites(cites)}
		}
		var names []string
		cites = cites[:0]
		for _, it := range undocumented {
			names = append(names, it.Name)
			cites = append(cites, domain.QualityCitation{Path: it.Path, Line: it.Line})
		}
		return domain.QualityCheck{Name: name, Status: missing, Method: det,
			Detail:    fmt.Sprintf("%d of %d added %s are not mentioned in any doc: %s", len(undocumented), len(items), noun, strings.Join(names, ", ")),
			Citations: cites}
	}
	out = append(out, mention("routes_documented", "route", domain.QualityFail, "HTTP routes"))
	out = append(out, mention("flags_documented", "flag", domain.QualityFail, "CLI flags"))
	out = append(out, mention("env_documented", "env", domain.QualityWarn, "environment variables"))

	var undocumented []GoSymbol
	considered := 0
	for _, s := range in.GoAdded {
		switch s.Kind {
		case "func", "method", "type", "const", "var":
		default:
			continue
		}
		considered++
		if !s.Documented {
			undocumented = append(undocumented, s)
		}
	}
	switch {
	case considered == 0:
		out = append(out, domain.QualityCheck{Name: "go_doc_comments", Status: domain.QualityNotApplicable, Method: det,
			Detail: "the change added no exported Go declarations to an importable package"})
	case len(undocumented) == 0:
		out = append(out, domain.QualityCheck{Name: "go_doc_comments", Status: domain.QualityPass, Method: det,
			Detail: fmt.Sprintf("all %d added exported Go declarations have doc comments", considered)})
	default:
		var names []string
		var cites []domain.QualityCitation
		for _, s := range undocumented {
			names = append(names, s.Name)
			cites = append(cites, domain.QualityCitation{Path: s.Path, Line: s.Line})
		}
		out = append(out, domain.QualityCheck{Name: "go_doc_comments", Status: domain.QualityWarn, Method: det,
			Detail:    fmt.Sprintf("%d added exported Go declarations have no doc comment: %s", len(undocumented), strings.Join(names, ", ")),
			Citations: cites})
	}

	if len(in.OpenAPIAdded) == 0 {
		out = append(out, domain.QualityCheck{Name: "openapi_described", Status: domain.QualityNotApplicable, Method: det,
			Detail: "the change added no OpenAPI operations"})
	} else {
		var bare []string
		var cites []domain.QualityCitation
		for _, op := range in.OpenAPIAdded {
			if !op.Described {
				bare = append(bare, strings.ToUpper(op.Method)+" "+op.Route)
				cites = append(cites, domain.QualityCitation{Path: op.SpecPath, Line: op.Line})
			}
		}
		if len(bare) == 0 {
			out = append(out, domain.QualityCheck{Name: "openapi_described", Status: domain.QualityPass, Method: det,
				Detail: fmt.Sprintf("all %d added OpenAPI operations have a summary or description", len(in.OpenAPIAdded))})
		} else {
			out = append(out, domain.QualityCheck{Name: "openapi_described", Status: domain.QualityWarn, Method: det,
				Detail: "added OpenAPI operations without a summary or description: " + strings.Join(bare, ", "), Citations: cites})
		}
	}
	return out
}

// OpenAPIOperations lists a spec's operations with whether each is described.
func OpenAPIOperations(specPath string, content []byte) []OpenAPIOperation {
	doc, err := parseYAMLDoc(content)
	if err != nil {
		return nil
	}
	paths, _ := mapGet(doc, "paths")
	var out []OpenAPIOperation
	for _, kv := range mapPairs(paths) {
		for _, m := range openAPIMethods {
			op, key := mapGet(kv.value, m)
			if op == nil {
				continue
			}
			out = append(out, OpenAPIOperation{Method: m, Route: kv.key.Value, SpecPath: specPath, Line: key.Line,
				Described: scalar(op, "summary") != "" || scalar(op, "description") != ""})
		}
	}
	return out
}

func dedupeCites(cs []domain.QualityCitation) []domain.QualityCitation {
	seen := map[string]bool{}
	var out []domain.QualityCitation
	for _, c := range cs {
		k := fmt.Sprintf("%s:%d:%s", c.Path, c.Line, c.Requirement)
		if !seen[k] {
			seen[k] = true
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
