package daemon

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// wildcardRe captures each {name} in a route pattern.
var wildcardRe = regexp.MustCompile(`\{([a-zA-Z0-9_]+)\}`)

// TestInitiativeRoutesUseOneWildcardName guards the defect behind DX audit
// finding v, which is not the one the finding first named.
//
// The loud failure -- two patterns differing only by wildcard name -- is already
// well handled: ServeMux panics at registration and names both patterns with
// their file:line, so NewServer cannot start with that mistake in it.
//
// The silent failure is the one worth a test. A route registered under
// {initiativeId} whose handler reads PathValue("id") gets "" back: no panic, no
// compile error, just a permanent 404. 0.1.0 shipped both names -- {id} on 32
// initiative routes and {initiativeId} on ten -- so the mistake was one
// copy-paste away, and reading the wrong one is invisible to every other check
// in this repo.
func TestInitiativeRoutesUseOneWildcardName(t *testing.T) {
	src, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string][]string{}
	for _, line := range strings.Split(string(src), "\n") {
		i := strings.Index(line, "/initiatives/{")
		if i < 0 {
			continue
		}
		m := wildcardRe.FindStringSubmatch(line[i:])
		if m == nil {
			continue
		}
		names[m[1]] = append(names[m[1]], strings.TrimSpace(line))
	}
	if len(names) == 0 {
		t.Fatal("found no /initiatives/{…} routes; this test is not reading routes.go")
	}
	if len(names) > 1 {
		var got []string
		for n, lines := range names {
			got = append(got, n+" ("+itoa(len(lines))+" routes)")
		}
		sort.Strings(got)
		t.Errorf("initiative routes use %d wildcard names: %s\n"+
			"Use one. A handler reading the other name gets \"\" from PathValue with no panic "+
			"and no compile error -- a permanent 404.", len(names), strings.Join(got, ", "))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
