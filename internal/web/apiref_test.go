package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

var assetRef = regexp.MustCompile(`(?:src|href)="(/assets/[^"]+)"`)

// The reference page is only useful if every asset it names is also embedded.
// dist/ is populated by `make build` and gitignored, so on a plain checkout
// there is nothing to check and the test skips rather than failing — but when a
// build *has* run, a page pointing at bytes the binary does not carry is a
// blank screen for the user and must be caught here.
func TestAPIReferencePageAssetsAreEmbedded(t *testing.T) {
	page, ok := APIReferencePage()
	if !ok {
		t.Skip("no built UI in internal/web/dist; run `make build` to exercise this")
	}
	refs := assetRef.FindAllStringSubmatch(string(page), -1)
	if len(refs) == 0 {
		t.Fatalf("api-reference.html references no /assets/ file:\n%s", page)
	}

	handler := Handler("test-token")
	for _, m := range refs {
		path := m[1]
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("%s referenced by api-reference.html: got %d, want 200", path, rec.Code)
		}
	}
}

// The page must not carry the API token: it is a static document served with
// ordinary caching, and it has no reason to authenticate anything.
func TestAPIReferencePageCarriesNoToken(t *testing.T) {
	page, ok := APIReferencePage()
	if !ok {
		t.Skip("no built UI in internal/web/dist; run `make build` to exercise this")
	}
	if strings.Contains(string(page), "__IDEA_TOKEN__") || strings.Contains(string(page), `name="idea-token"`) {
		t.Errorf("api-reference.html has a token placeholder in it:\n%s", page)
	}
}
