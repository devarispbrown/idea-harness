package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// renewReq is what web/public/sw-push.js sends on pushsubscriptionchange:
// no token header, no cookie, a JSON body carrying the renewal token.
func renewReq(t *testing.T, ts *httptest.Server, projectID, subID, body string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("POST", ts.URL+"/api/v1/projects/"+projectID+"/push/subscriptions/"+subID+"/renew", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://127.0.0.1")
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// newEndpoint registers a fresh fake browser at path and returns the JSON
// subscription fields for it.
func (f *fakePushService) newEndpoint(t *testing.T, path string) string {
	t.Helper()
	b := newFakeBrowser(t)
	f.mu.Lock()
	f.browsers[path] = b
	f.mu.Unlock()
	p, a := b.keys()
	return `"endpoint":"` + f.srv.URL + path + `","keys":{"p256dh":"` + p + `","auth":"` + a + `"}`
}

// TestPushSubscriptionRenewal: the service worker re-points a subscription at
// the push service's new endpoint with nothing but the renewal token it was
// given at subscribe time; deliveries follow; the token rotates.
func TestPushSubscriptionRenewal(t *testing.T) {
	_, ts, token, projectID := newPushServer(t)
	fake := newFakePushService(t)
	sub := fake.subscribe(t, ts, token, projectID, "/push/old", `,"events":["verify.failed"]`)
	if len(sub.RenewalToken) < 40 {
		t.Fatalf("subscribe must return a renewal token: %+v", sub)
	}

	// The token is never listed.
	list := doReq(t, "GET", ts.URL+"/api/v1/projects/"+projectID+"/push/subscriptions", token, "", "")
	if body := mustReadAll(t, list); strings.Contains(body, sub.RenewalToken) || strings.Contains(body, "renewal_token") {
		t.Fatalf("the subscription list exposes a renewal token: %s", body)
	}

	newSub := fake.newEndpoint(t, "/push/new")
	code, body := renewReq(t, ts, projectID, sub.ID, `{"renewal_token":"`+sub.RenewalToken+`",`+newSub+`}`, nil)
	if code != http.StatusOK {
		t.Fatalf("renew: %d %s", code, body)
	}
	var got pushSubscriptionView
	json.Unmarshal([]byte(body), &got)
	if got.ID != sub.ID || got.RenewalToken == "" || got.RenewalToken == sub.RenewalToken {
		t.Fatalf("renew must keep the id and rotate the token: %+v", got)
	}
	if strings.Join(got.Events, ",") != "verify.failed" {
		t.Fatalf("renewal must keep the events: %v", got.Events)
	}

	// Deliveries now go to the new endpoint.
	resp := doReq(t, "POST", ts.URL+"/api/v1/projects/"+projectID+"/push/subscriptions/"+sub.ID+"/test", token, "", "")
	if b := mustReadAll(t, resp); !strings.Contains(b, `"ok":true`) {
		t.Fatalf("test push after renewal: %s", b)
	}
	if len(fake.received("/push/new")) != 1 || len(fake.received("/push/old")) != 0 {
		t.Fatalf("delivery after renewal: new=%d old=%d", len(fake.received("/push/new")), len(fake.received("/push/old")))
	}

	// The spent token is dead; the rotated one works.
	again := fake.newEndpoint(t, "/push/newer")
	if code, _ := renewReq(t, ts, projectID, sub.ID, `{"renewal_token":"`+sub.RenewalToken+`",`+again+`}`, nil); code != http.StatusUnauthorized {
		t.Fatalf("a spent renewal token: %d, want 401", code)
	}
	if code, body := renewReq(t, ts, projectID, sub.ID, `{"renewal_token":"`+got.RenewalToken+`",`+again+`}`, nil); code != http.StatusOK {
		t.Fatalf("the rotated token: %d %s", code, body)
	}
}

// TestPushRenewalRefusals: every way to be refused, and that the refusals do
// not tell an unauthenticated caller which ids exist.
func TestPushRenewalRefusals(t *testing.T) {
	_, ts, token, projectID := newPushServer(t)
	fake := newFakePushService(t)
	sub := fake.subscribe(t, ts, token, projectID, "/push/a", "")
	newSub := fake.newEndpoint(t, "/push/b")
	good := `{"renewal_token":"` + sub.RenewalToken + `",` + newSub + `}`

	var denied []string
	for _, tc := range []struct {
		name, project, id, body string
	}{
		// "!" is not in the base64url alphabet, so this is wrong no matter what
		// the real token starts with. (An earlier version used "x", which made
		// the "wrong" token equal the real one whenever the token began with x.)
		{"wrong token", projectID, sub.ID, `{"renewal_token":"!` + sub.RenewalToken[1:] + `",` + newSub + `}`},
		{"no token", projectID, sub.ID, `{` + newSub + `}`},
		{"unknown subscription", projectID, "nope", good},
		{"unknown project", "nope", sub.ID, good},
		{"another subscription's id", projectID, fake.subscribe(t, ts, token, projectID, "/push/c", "").ID, good},
	} {
		code, body := renewReq(t, ts, tc.project, tc.id, tc.body, nil)
		if code != http.StatusUnauthorized {
			t.Errorf("%s: %d %s, want 401", tc.name, code, body)
		}
		denied = append(denied, body)
	}
	for _, b := range denied[1:] {
		if b != denied[0] {
			t.Errorf("refusals differ, which tells a caller which ids exist:\n%s\n%s", denied[0], b)
		}
	}

	// The login endpoint's own checks: JSON, and an Origin the guard vetted.
	if code, _ := renewReq(t, ts, projectID, sub.ID, good, map[string]string{"Origin": ""}); code != http.StatusForbidden {
		t.Errorf("no Origin: %d, want 403", code)
	}
	if code, _ := renewReq(t, ts, projectID, sub.ID, good, map[string]string{"Origin": "https://evil.example"}); code != http.StatusForbidden {
		t.Errorf("foreign Origin: %d, want 403", code)
	}
	if code, _ := renewReq(t, ts, projectID, sub.ID, good, map[string]string{"Content-Type": "text/plain"}); code != http.StatusForbidden {
		t.Errorf("not JSON: %d, want 403", code)
	}
	// The new endpoint passes the subscribe validation.
	if code, _ := renewReq(t, ts, projectID, sub.ID, `{"renewal_token":"`+sub.RenewalToken+`","endpoint":"not a url","keys":{"p256dh":"x","auth":"y"}}`, nil); code != http.StatusBadRequest {
		t.Errorf("an invalid endpoint: %d, want 400", code)
	}
	// None of the refusals spent the token.
	if code, body := renewReq(t, ts, projectID, sub.ID, good, nil); code != http.StatusOK {
		t.Fatalf("the good renewal after the refusals: %d %s", code, body)
	}
	// Only the renew shape is let through without auth.
	for _, p := range []string{
		"/api/v1/projects/" + projectID + "/push/subscriptions/" + sub.ID + "/test",
		"/api/v1/projects/" + projectID + "/push/subscriptions/" + sub.ID + "/renew/x",
		"/api/v1/projects/" + projectID + "/push/subscriptions//renew",
	} {
		if isPushRenewPath(p) {
			t.Errorf("isPushRenewPath(%q) = true", p)
		}
	}
}

// TestPushRenewalOntoAnExistingRow: the page re-subscribed in this project
// before the worker got to renew, so the new endpoint already has a row. The
// renewed row is folded into it rather than tripping the endpoint's
// uniqueness, and the worker gets that row's (new) token.
func TestPushRenewalOntoAnExistingRow(t *testing.T) {
	rt, ts, token, projectID := newPushServer(t)
	fake := newFakePushService(t)
	old := fake.subscribe(t, ts, token, projectID, "/push/old", "")
	cur := fake.subscribe(t, ts, token, projectID, "/push/current", `,"events":["verify.failed"]`)
	fake.mu.Lock()
	b := fake.browsers["/push/current"]
	fake.mu.Unlock()
	p, a := b.keys()
	code, body := renewReq(t, ts, projectID, old.ID, `{"renewal_token":"`+old.RenewalToken+`","endpoint":"`+fake.srv.URL+`/push/current","keys":{"p256dh":"`+p+`","auth":"`+a+`"}}`, nil)
	if code != http.StatusOK {
		t.Fatalf("renew onto an existing endpoint: %d %s", code, body)
	}
	var got pushSubscriptionView
	json.Unmarshal([]byte(body), &got)
	if got.ID != cur.ID || got.RenewalToken == "" {
		t.Fatalf("the surviving row should be the existing one, with a token: %+v", got)
	}
	pr, _ := rt.EnsureProject(context.Background(), projectID)
	if _, err := pr.db.GetPushSubscription(context.Background(), old.ID); err == nil {
		t.Fatal("the renewed row should have been folded into the existing one")
	}
	if code, _ := renewReq(t, ts, projectID, cur.ID, `{"renewal_token":"`+cur.RenewalToken+`",`+fake.newEndpoint(t, "/push/x")+`}`, nil); code != http.StatusUnauthorized {
		t.Fatalf("the existing row's previous token must be rotated out: %d", code)
	}
}
