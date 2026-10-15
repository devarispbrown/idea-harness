package daemon

import (
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// bogusID is an id no fixture will ever mint.
const bogusID = "ffffffffffffffffffffffffffffffff"

// notFoundMessage is what a "no such row" answer must look like. Two forms are
// accepted because the API uses both: `no <kind> matching "<id>"` from the
// store's NotFoundError, and `<kind> not found` hand-written in nine handlers.
// Unifying those is a separate change (deferred as audit 6 / A13b); what this
// regex exists to reject is the three defect classes:
//
//   - a bare `not found` — a raw sql.ErrNoRows reaching writeErr, naming nothing
//   - a `store:` or `workflow:` layer prefix leaking to a client
//   - a state claim about a row that does not exist, e.g.
//     `decision ff… is not decided`, which is the one that reads as true
var notFoundMessage = regexp.MustCompile(`^(no [a-z ]+ matching "|[a-z ]+ not found$)`)

// routeBodies supplies a request body valid enough to clear each mutating
// route's own validation, because several handlers check the body before they
// look anything up. A route missing from this map is a test failure, not a skip
// -- see the completeness assertion in TestEveryIDRouteAnswers404.
var routeBodies = map[string]string{
	"POST /api/v1/projects/{projectId}/decisions/{id}/resolve":           `{"resolution":"use X"}`,
	"POST /api/v1/projects/{projectId}/decisions/{id}/reopen":            `{}`,
	"POST /api/v1/projects/{projectId}/decisions/{id}/discuss":           `{"message":"hi"}`,
	"POST /api/v1/projects/{projectId}/questions/{id}/answer":            `{"answer":"x"}`,
	"POST /api/v1/projects/{projectId}/assertions/{id}/status":           `{"status":"accepted"}`,
	"POST /api/v1/projects/{projectId}/findings/{id}/note":               `{"note":"n"}`,
	"POST /api/v1/projects/{projectId}/findings/{id}/disposition":        `{"disposition":"accepted_risk"}`,
	"POST /api/v1/projects/{projectId}/artifacts/{id}/comments":          `{"text":"t"}`,
	"POST /api/v1/projects/{projectId}/comments/{id}/promote":            `{}`,
	"PATCH /api/v1/projects/{projectId}/initiatives/{id}/tasks/{taskId}": `{"title":"t"}`,
	// A body that clears validation, so the 404 under test is about the id and
	// not about the body. Without it this route answers 400 `invalid body`.
	"POST /api/v1/projects/{projectId}/initiatives/{id}/verify": `{"requirement":"REQ-1","pass":true}`,
}

// wantStatus records the routes that deliberately answer something other than
// 404 for an unknown id, each with the reason. It starts empty of excuses:
// every entry is a convention (validate the body before the lookup), not a
// defect being tolerated.
var wantStatus = map[string]int{
	// Body validated before the lookup. Defensible, and reversing it buys a
	// client nothing -- both are 4xx and both name the real problem.
	//
	// retry used to be in this list too: it required `model` before ever
	// looking up the initiative, so a bogus id answered 400 "model is
	// required" instead of 404. M4 (review fix) made `model` optional
	// (lenient decoding, like `fresh` on /execute), which incidentally fixed
	// this too -- a bogus id now reaches the real lookup and gets a real 404.
	"POST /api/v1/projects/{projectId}/initiatives":                   http.StatusBadRequest,
	"POST /api/v1/projects/{projectId}/initiatives/{id}/reopen-phase": http.StatusBadRequest,
}

// TestEveryIDRouteAnswers404 walks the route table rather than a hand-written
// list, so a route added later cannot quietly answer 400 or 409 -- or 200 -- for
// an id that matches nothing.
//
// Five routes did exactly that, and four of them said something actively false:
// `decision ff… is not decided` is a claim about a decision that does not exist.
// One returned `200 []`, the one shape a client cannot distinguish from a real
// empty result.
func TestEveryIDRouteAnswers404(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID, csID := appliedChangeSet(t, rt, ts, token)

	real := map[string]string{
		"{projectId}": projectID,
		"{id}":        iniID,
		"{taskId}":    "t-unused",
		"{docId}":     "d-unused",
		"{reqId}":     "REQ-1",
		"{staleId}":   "s-unused",
		"{name}":      "slack",
		// The demo PRD defines one objective with one metric.
		"{objectiveId}": "OBJ-1",
		"{metricId}":    "MET-1",
	}

	routes := idRoutes(t)
	if len(routes) == 0 {
		t.Fatal("no id-taking routes parsed from routes.go; this test is reading nothing")
	}
	assertNoStaleExceptions(t, routes)

	checked := 0
	for _, rt2 := range routes {
		for _, wildcard := range wildcardsIn(rt2.path) {
			// One wildcard bogus at a time, the rest real, so the 404 can only
			// be about the id under test.
			url := rt2.path
			for w, v := range real {
				if w == wildcard {
					continue
				}
				url = strings.ReplaceAll(url, w, v)
			}
			url = strings.ReplaceAll(url, wildcard, bogusID)
			if strings.Contains(url, "{") {
				// Loud. This dropped two pairs in silence until `real` gained
				// {reqId}: a wildcard with no fixture value is a gap in the
				// walk, not a route that is fine.
				t.Errorf("%s %s: no fixture value for a wildcard in %q, so the %s pair was never exercised",
					rt2.method, rt2.path, url, wildcard)
				continue
			}
			// {id} on a change-set route is a change set, not an initiative.
			if wildcard != "{id}" && strings.Contains(rt2.path, "change-sets") {
				url = strings.ReplaceAll(url, iniID, csID)
			}

			want := http.StatusNotFound
			if s, ok := wantStatus[rt2.key()]; ok {
				want = s
			}
			resp := doReq(t, rt2.method, ts.URL+url, token, "", routeBodies[rt2.key()])
			var body struct {
				Error string `json:"error"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&body)
			status := resp.StatusCode
			resp.Body.Close()

			if status != want {
				t.Errorf("%s %s with a bogus %s → %d %q, want %d",
					rt2.method, rt2.path, wildcard, status, body.Error, want)
				continue
			}
			checked++
			if want != http.StatusNotFound {
				continue
			}
			if kind := expectedKind(rt2.path, wildcard); kind != "" &&
				!strings.Contains(body.Error, kind) {
				t.Errorf("%s %s with a bogus %s → 404 %q; the message names the wrong thing, want %q in it",
					rt2.method, rt2.path, wildcard, body.Error, kind)
			}
			if !notFoundMessage.MatchString(body.Error) {
				t.Errorf("%s %s with a bogus %s → 404 %q; want a message like `no <kind> matching \"<id>\"` "+
					"(a bare \"not found\", a `store:` prefix, or a state claim about a missing row all fail here)",
					rt2.method, rt2.path, wildcard, body.Error)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no route was actually exercised")
	}
	t.Logf("checked %d id-taking route/wildcard pair(s)", checked)
}

// assertNoStaleExceptions removes the other half of the drift.
//
// A new route needs no entry here — the walk defaults to expecting 404, so an
// added route is exercised whether anyone thought about it or not. What rots is
// the exception list: an entry naming a route that has been renamed or deleted
// silently excuses nothing while looking like it excuses something.
func assertNoStaleExceptions(t *testing.T, routes []idRoute) {
	t.Helper()
	have := map[string]bool{}
	for _, r := range routes {
		have[r.key()] = true
	}
	var stale []string
	for k := range wantStatus {
		if !have[k] {
			stale = append(stale, k)
		}
	}
	for k := range routeBodies {
		if !have[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Errorf("wantStatus/routeBodies name routes that no longer exist: %v", stale)
	}
}

// expectedKind is the noun a 404 for this wildcard must name. Shape alone is
// not enough: `notFoundOrState` takes the kind as an argument, and ignoring it
// made a question route answer `no decision matching "<id>"` -- a message
// exactly as false as the `decision <id> is not decided` this change removed,
// and every test stayed green.
func expectedKind(path, wildcard string) string {
	switch wildcard {
	case "{projectId}":
		return "project"
	case "{taskId}":
		return "task"
	case "{docId}":
		return "doc"
	case "{reqId}":
		return "requirement"
	case "{objectiveId}":
		return "objective"
	case "{metricId}":
		return "metric"
	}
	if wildcard != "{id}" {
		// An unlisted wildcard (e.g. {staleId}) has no fixed expected noun here;
		// the loop below only knows how to name what precedes an `{id}` segment,
		// and reusing it for the wrong wildcard misnamed the actual route under
		// test. Falling through unconditionally made `{staleId}` inherit `{id}`'s
		// "initiative" from the very next segment, which is a different pair
		// entirely.
		return ""
	}
	// {id} means whatever collection precedes it.
	segs := strings.Split(strings.TrimPrefix(path, "/api/v1/"), "/")
	for i, seg := range segs {
		if seg != "{id}" || i == 0 {
			continue
		}
		switch segs[i-1] {
		case "initiatives":
			return "initiative"
		case "decisions":
			return "decision"
		case "questions":
			return "question"
		case "change-sets":
			return "change set"
		case "operations":
			return "operation"
		case "findings":
			return "finding"
		case "artifacts":
			return "artifact"
		case "comments":
			return "doc comment"
		}
	}
	return ""
}

type idRoute struct {
	method string
	path   string
}

func (r idRoute) key() string { return r.method + " " + r.path }

var handleFuncRe = regexp.MustCompile(`m\.HandleFunc\("([A-Z]+) (/api/v1/[^"]*)"`)

// idRoutes reads routes.go as text, the same way route_wildcards_test.go does,
// so the set under test is the registered one and not a copy that can drift.
func idRoutes(t *testing.T) []idRoute {
	t.Helper()
	raw, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []idRoute
	for _, m := range handleFuncRe.FindAllStringSubmatch(string(raw), -1) {
		if !strings.Contains(m[2], "{") {
			continue
		}
		out = append(out, idRoute{method: m[1], path: m[2]})
	}
	return out
}

func wildcardsIn(path string) []string {
	var out []string
	for _, seg := range strings.Split(path, "/") {
		if strings.HasPrefix(seg, "{") {
			out = append(out, seg)
		}
	}
	return out
}

// TestMissingRequirementIsTheSameEverywhere is the smoking gun expressed
// directly, and the walker cannot see it: the requirement is in the request
// body, not the path, so varying path wildcards never reaches it.
//
// Both endpoints returned byte-identical text — `store: requirement "REQ-999"
// not found` — under two different status codes, because GetRequirementByRef
// returned a plain error and each handler guessed. That is what makes it
// accidental rather than designed.
func TestMissingRequirementIsTheSameEverywhere(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	base := ts.URL + "/api/v1/projects/" + projectID + "/initiatives/" + iniID

	a := doReq(t, http.MethodPost, base+"/verify", token, "", `{"requirement":"REQ-999","pass":true}`)
	b := doReq(t, http.MethodPost, base+"/requirements/REQ-999/verification-task", token, "", `{}`)
	defer a.Body.Close()
	defer b.Body.Close()

	var ea, eb struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(a.Body).Decode(&ea)
	_ = json.NewDecoder(b.Body).Decode(&eb)

	if a.StatusCode != b.StatusCode {
		t.Errorf("the same missing requirement answers %d on /verify and %d on /verification-task",
			a.StatusCode, b.StatusCode)
	}
	if a.StatusCode != http.StatusNotFound {
		t.Errorf("a missing requirement answers %d, want 404", a.StatusCode)
	}
	if ea.Error != eb.Error {
		t.Errorf("different text for the same condition:\n  /verify              %q\n  /verification-task   %q", ea.Error, eb.Error)
	}
	if strings.HasPrefix(ea.Error, "store:") {
		t.Errorf("the store's layer prefix reached the client: %q", ea.Error)
	}
}

// TestWrongStateIsDistinguishableFromMissing is the half the audit missed and
// the strongest argument for the change: on a REAL question that was already
// answered, the response was byte-identical to the one for a question that does
// not exist. A client could not tell them apart at all.
func TestWrongStateIsDistinguishableFromMissing(t *testing.T) {
	rt, ts, token := newTestServer(t)
	projectID, iniID := newTestProjectInitiative(t, rt, ts, token)
	pr, err := rt.EnsureProject(t.Context(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	q := &domain.Question{InitiativeID: iniID, Question: "why?", Status: domain.QuestionOpen}
	if err := pr.db.CreateQuestion(t.Context(), q); err != nil {
		t.Fatal(err)
	}
	base := ts.URL + "/api/v1/projects/" + projectID + "/questions/"

	first := doReq(t, http.MethodPost, base+q.ID+"/answer", token, "", `{"answer":"because"}`)
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("answering an open question: %d", first.StatusCode)
	}

	// Same question, now answered: a state refusal.
	again := doReq(t, http.MethodPost, base+q.ID+"/answer", token, "", `{"answer":"again"}`)
	var stateErr struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(again.Body).Decode(&stateErr)
	againStatus := again.StatusCode
	again.Body.Close()

	// A question that does not exist.
	missing := doReq(t, http.MethodPost, base+bogusID+"/answer", token, "", `{"answer":"x"}`)
	var missErr struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(missing.Body).Decode(&missErr)
	missStatus := missing.StatusCode
	missing.Body.Close()

	if againStatus == missStatus && stateErr.Error == missErr.Error {
		t.Fatalf("an answered question and a nonexistent one are indistinguishable: both %d %q",
			againStatus, stateErr.Error)
	}
	if missStatus != http.StatusNotFound {
		t.Errorf("a nonexistent question answers %d, want 404", missStatus)
	}
	if againStatus != http.StatusBadRequest {
		t.Errorf("an already-answered question answers %d, want 400", againStatus)
	}
}
