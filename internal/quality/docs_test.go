package quality

import (
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

const goodReadme = "# Widget\n\n## Installation\n\n```sh\nmake build\n```\n\n## Usage\n\nRun `widget serve --port 8080`. `GET /v1/widgets/{id}` returns one.\n\n## Configuration\n\nSet `WIDGET_DB_URL`.\n"

func checkByName(t *testing.T, checks []domain.QualityCheck, name string) domain.QualityCheck {
	t.Helper()
	for _, c := range checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check %q in %+v", name, checks)
	return domain.QualityCheck{}
}

const serverBefore = "package main\n\nfunc main() {\n\tmux.HandleFunc(\"GET /v1/widgets\", list)\n}\n"
const serverAfter = "package main\n\nimport \"os\"\n\nfunc main() {\n\tport := flag.Int(\"port\", 8080, \"\")\n\tdb := os.Getenv(\"WIDGET_DB_URL\")\n\tmux.HandleFunc(\"GET /v1/widgets\", list)\n\tmux.HandleFunc(\"GET /v1/widgets/:id\", get)\n}\n"

func TestDocsChecksPassWhenEverythingIsDocumented(t *testing.T) {
	checks := DocsChecks(DocsInput{
		Paths:   []string{"README.md", "main.go"},
		Docs:    map[string]string{"README.md": goodReadme},
		Changed: []ChangedFile{{Path: "main.go", Before: serverBefore, BeforeExists: true, BeforeKnown: true, After: serverAfter, AfterExists: true}},
	})
	if s := Rollup(checks); s != domain.QualityPass {
		t.Fatalf("want pass, got %s: %+v", s, checks)
	}
	if c := checkByName(t, checks, "readme_install"); len(c.Citations) == 0 || c.Citations[0].Line != 3 {
		t.Errorf("install section must cite its heading line: %+v", c)
	}
}

func TestDocsChecksFailOnUndocumentedRouteAndFlag(t *testing.T) {
	readme := "# Widget\n\n## Install\n\nmake\n\n## Usage\n\nrun it\n"
	checks := DocsChecks(DocsInput{
		Paths:   []string{"README.md", "main.go"},
		Docs:    map[string]string{"README.md": readme},
		Changed: []ChangedFile{{Path: "main.go", Before: serverBefore, BeforeExists: true, BeforeKnown: true, After: serverAfter, AfterExists: true}},
	})
	if s := Rollup(checks); s != domain.QualityFail {
		t.Fatalf("want fail, got %s", s)
	}
	routes := checkByName(t, checks, "routes_documented")
	if routes.Status != domain.QualityFail || !strings.Contains(routes.Detail, "/v1/widgets/:id") {
		t.Fatalf("route check: %+v", routes)
	}
	if routes.Citations[0].Path != "main.go" || routes.Citations[0].Line != 9 {
		t.Errorf("route citation must point at the declaring line: %+v", routes.Citations)
	}
	if c := checkByName(t, checks, "flags_documented"); c.Status != domain.QualityFail || !strings.Contains(c.Detail, "--port") {
		t.Errorf("flag check: %+v", c)
	}
	if c := checkByName(t, checks, "env_documented"); c.Status != domain.QualityWarn {
		t.Errorf("an undocumented env var is a warning: %+v", c)
	}
	if c := checkByName(t, checks, "readme_configuration"); c.Status != domain.QualityWarn {
		t.Errorf("new env reads need a configuration section: %+v", c)
	}
	// The pre-existing route is not "added" and must not be demanded.
	if strings.Contains(routes.Detail, "/v1/widgets,") {
		t.Errorf("a route that existed before was reported: %s", routes.Detail)
	}
}

func TestDocsChecksMissingReadmeFails(t *testing.T) {
	checks := DocsChecks(DocsInput{Paths: []string{"main.go"}, Docs: map[string]string{}})
	if c := checkByName(t, checks, "readme"); c.Status != domain.QualityFail {
		t.Fatalf("no README must fail: %+v", c)
	}
	if c := checkByName(t, checks, "routes_documented"); c.Status != domain.QualityNotApplicable {
		t.Errorf("nothing added is not_applicable: %+v", c)
	}
}

func TestDocsChecksGoDocCommentsAndOpenAPIDescriptions(t *testing.T) {
	checks := DocsChecks(DocsInput{
		Paths: []string{"README.md"},
		Docs:  map[string]string{"README.md": goodReadme},
		GoAdded: []GoSymbol{
			{Name: "Parse", Kind: "func", Path: "lib/p.go", Line: 4, Documented: false},
			{Name: "Config.Timeout", Kind: "field", Path: "lib/p.go", Line: 9},
		},
		OpenAPIAdded: []OpenAPIOperation{{Method: "get", Route: "/a", SpecPath: "openapi.yaml", Line: 7}},
	})
	if c := checkByName(t, checks, "go_doc_comments"); c.Status != domain.QualityWarn || !strings.Contains(c.Detail, "Parse") || strings.Contains(c.Detail, "Timeout") {
		t.Errorf("go doc check: %+v", c)
	}
	if c := checkByName(t, checks, "openapi_described"); c.Status != domain.QualityWarn || c.Citations[0].Line != 7 {
		t.Errorf("openapi check: %+v", c)
	}
}

func TestHeadingsSkipFencedComments(t *testing.T) {
	hs := Headings("# Title\n```sh\n# not a heading\n```\nSetup\n=====\n")
	if len(hs) != 2 || hs[1].Text != "Setup" || hs[1].Line != 5 {
		t.Fatalf("headings: %+v", hs)
	}
}

func TestSurfaceMentions(t *testing.T) {
	docs := map[string]string{"docs/api.md": "Call `GET /v1/items/{itemId}` with `--dry-run`. Set APP_PORT."}
	for _, tc := range []struct {
		it   SurfaceItem
		want bool
	}{
		{SurfaceItem{Kind: "route", Name: "/v1/items/:id"}, true},
		{SurfaceItem{Kind: "route", Name: "/v1/items"}, false},
		{SurfaceItem{Kind: "flag", Name: "--dry-run"}, true},
		{SurfaceItem{Kind: "flag", Name: "--dry"}, false},
		{SurfaceItem{Kind: "env", Name: "APP_PORT"}, true},
		{SurfaceItem{Kind: "env", Name: "APP"}, false},
	} {
		if _, got := MentionedIn(tc.it, docs); got != tc.want {
			t.Errorf("MentionedIn(%+v)=%v want %v", tc.it, got, tc.want)
		}
	}
}

func TestSurfaceItemsAcrossLanguages(t *testing.T) {
	js := "app.get('/api/users', h)\nconst p = process.env.PORT\nprogram.option('-v, --verbose')\n"
	py := "@app.post(\"/jobs\")\nparser.add_argument(\"--limit\")\nx = os.environ.get(\"JOB_QUEUE\")\n"
	var got []string
	for _, it := range append(SurfaceItems("a.js", js), SurfaceItems("a.py", py)...) {
		got = append(got, it.Kind+" "+it.Name)
	}
	for _, want := range []string{"route /api/users", "env PORT", "flag --verbose", "route /jobs", "flag --limit", "env JOB_QUEUE"} {
		if !contains(got, want) {
			t.Errorf("missing %q in %v", want, got)
		}
	}
	if IsSourcePath("a_test.go") || IsSourcePath("web/x.test.ts") || !IsSourcePath("web/x.ts") {
		t.Error("IsSourcePath misclassified a test")
	}
}
