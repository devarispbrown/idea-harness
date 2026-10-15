package quality

import (
	"strings"
	"testing"
)

const goBefore = `package lib

// Client talks to the service.
type Client struct {
	Timeout int
	BaseURL string
	secret  string
}

// Store persists things.
type Store interface {
	Get(key string) (string, error)
}

// New builds a Client.
func New(base string) *Client { return &Client{BaseURL: base} }

// Do sends a request.
func (c *Client) Do(path string, retries int) error { return nil }

func (c *Client) internalHelper() {}

const Version = "1"

var ErrMissing error
`

func goAPI(t *testing.T, src string) map[string]GoSymbol {
	t.Helper()
	m, err := ExportedGoAPI(map[string][]byte{"pkg/lib/lib.go": []byte(src)})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func goKinds(changes []APIChange) (breaking, other []string) {
	for _, c := range changes {
		if c.Breaking {
			breaking = append(breaking, c.Kind+":"+c.Subject)
		} else {
			other = append(other, c.Kind+":"+c.Subject)
		}
	}
	return
}

func TestExportedGoAPIIgnoresUnexportedAndParamNames(t *testing.T) {
	api := goAPI(t, goBefore)
	for _, k := range []string{"pkg/lib.Client", "pkg/lib.Client.Timeout", "pkg/lib.Store.Get", "pkg/lib.New", "pkg/lib.Client.Do", "pkg/lib.Version", "pkg/lib.ErrMissing"} {
		if _, ok := api[k]; !ok {
			t.Errorf("missing %s", k)
		}
	}
	for k := range api {
		if strings.Contains(k, "secret") || strings.Contains(k, "internalHelper") {
			t.Errorf("unexported symbol leaked: %s", k)
		}
	}
	renamed := strings.Replace(goBefore, "func New(base string)", "func New(baseURL string)", 1)
	if b, _ := goKinds(DiffGoAPI(api, goAPI(t, renamed))); len(b) != 0 {
		t.Fatalf("renaming a parameter is not breaking, got %v", b)
	}
	if !api["pkg/lib.New"].Documented || api["pkg/lib.Version"].Documented {
		t.Fatalf("doc-comment detection wrong: New=%v Version=%v", api["pkg/lib.New"].Documented, api["pkg/lib.Version"].Documented)
	}
}

func TestDiffGoAPIBreaking(t *testing.T) {
	cases := []struct {
		name, from, to, want string
	}{
		{"func removed", "// New builds a Client.\nfunc New(base string) *Client { return &Client{BaseURL: base} }\n", "", "go_func_removed:New"},
		{"signature changed", "func New(base string) *Client", "func New(base string, opts ...int) *Client", "go_func_changed:New"},
		{"method signature changed", "Do(path string, retries int) error", "Do(path string) error", "go_method_changed:Client.Do"},
		{"field removed", "\tTimeout int\n", "", "go_field_removed:Client.Timeout"},
		{"field type changed", "\tTimeout int\n", "\tTimeout float64\n", "go_field_changed:Client.Timeout"},
		{"interface method added", "\tGet(key string) (string, error)\n", "\tGet(key string) (string, error)\n\tDelete(key string) error\n", "go_interface_method_added:Store.Delete"},
		{"var removed", "var ErrMissing error\n", "", "go_var_removed:ErrMissing"},
	}
	before := goAPI(t, goBefore)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := strings.Replace(goBefore, tc.from, tc.to, 1)
			if src == goBefore {
				t.Fatal("fixture edit did not apply")
			}
			b, _ := goKinds(DiffGoAPI(before, goAPI(t, src)))
			if !contains(b, tc.want) {
				t.Fatalf("want %s, got %v", tc.want, b)
			}
		})
	}
}

func TestDiffGoAPIRemovedTypeReportsOnlyTheType(t *testing.T) {
	src := strings.Replace(goBefore, "// Store persists things.\ntype Store interface {\n\tGet(key string) (string, error)\n}\n", "", 1)
	b, _ := goKinds(DiffGoAPI(goAPI(t, goBefore), goAPI(t, src)))
	if len(b) != 1 || b[0] != "go_type_removed:Store" {
		t.Fatalf("want only the type removal, got %v", b)
	}
}

func TestDiffGoAPIAdditionsAreNotBreaking(t *testing.T) {
	src := goBefore + "\n// Close releases it.\nfunc (c *Client) Close() error { return nil }\n\ntype Reader interface{ Read() }\n"
	src = strings.Replace(src, "\tBaseURL string\n", "\tBaseURL string\n\tRetries int\n", 1)
	b, o := goKinds(DiffGoAPI(goAPI(t, goBefore), goAPI(t, src)))
	if len(b) != 0 {
		t.Fatalf("additions reported breaking: %v", b)
	}
	for _, want := range []string{"go_method_added:Client.Close", "go_field_added:Client.Retries", "go_type_added:Reader", "go_interface_method_added:Reader.Read"} {
		if !contains(o, want) {
			t.Errorf("missing %s in %v", want, o)
		}
	}
}

func TestIsPublicGoPath(t *testing.T) {
	for p, want := range map[string]bool{
		"lib.go": true, "pkg/api/x.go": true, "internal/x/y.go": false, "pkg/internal/z.go": false,
		"x_test.go": false, "testdata/a.go": false, "vendor/m/a.go": false, "README.md": false,
	} {
		if got := IsPublicGoPath(p); got != want {
			t.Errorf("IsPublicGoPath(%q)=%v want %v", p, got, want)
		}
	}
}

func TestExportedGoAPISkipsMainAndReportsParseErrors(t *testing.T) {
	m, err := ExportedGoAPI(map[string][]byte{"cmd/x/main.go": []byte("package main\nfunc Exported() {}\n")})
	if err != nil || len(m) != 0 {
		t.Fatalf("package main exports nothing importable: %v %v", m, err)
	}
	if _, err := ExportedGoAPI(map[string][]byte{"a/a.go": []byte("package a\nfunc (")}); err == nil {
		t.Fatal("a file that does not parse must be reported")
	}
}
