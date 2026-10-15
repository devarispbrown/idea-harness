package push

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/integrations"
	"github.com/devarispbrown/idea-harness/internal/webpush"
)

// Every push kind is a real bus kind: a typo here would be a toggle in
// Settings that never fires.
func TestKindsAreBusKinds(t *testing.T) {
	all := map[integrations.Kind]bool{}
	for _, k := range integrations.AllKinds {
		all[k] = true
	}
	for _, k := range Kinds {
		if !all[k] {
			t.Errorf("push kind %q is not in integrations.AllKinds", k)
		}
		if !ValidKind(string(k)) {
			t.Errorf("ValidKind(%q) = false", k)
		}
	}
	if ValidKind("run.failed") || ValidKind("") {
		t.Error("ValidKind accepts a kind outside the push set")
	}
}

func TestBuildPayload(t *testing.T) {
	redact := func(s string) string { return strings.ReplaceAll(s, "sk-secret", "[REDACTED]") }
	cases := []struct {
		ev        integrations.Event
		title     string
		urlSuffix string
	}{
		{integrations.Event{Kind: integrations.KindNeedsJudgment, ProjectID: "p1", InitiativeID: "i1", InitiativeTitle: "Dark mode", Reason: "plan_review"},
			"Needs you: Dark mode", "/#/p/p1/queue"},
		{integrations.Event{Kind: integrations.KindChangeSetReady, ProjectID: "p1", InitiativeID: "i1", InitiativeTitle: "Dark mode", ChangeSetRef: "CHG-3", TaskCode: "T2"},
			"Ready for review: CHG-3", "/#/p/p1/i/i1/build"},
		{integrations.Event{Kind: integrations.KindVerifyFailed, ProjectID: "p1", InitiativeID: "i1", InitiativeTitle: "x", RequirementCode: "REQ-2", Reason: "key sk-secret leaked"},
			"Verification failed: x", "/#/p/p1/i/i1/verify"},
		{integrations.Event{Kind: integrations.KindOutcomeDrifted, ProjectID: "p1", InitiativeID: "i1", InitiativeTitle: "x", MetricCode: "MET-1", Status: "off_track"},
			"Outcome drifting: x", "/#/p/p1/i/i1/outcomes"},
	}
	for _, tc := range cases {
		p := BuildPayload(tc.ev, redact)
		if p.Title != tc.title || p.URL != tc.urlSuffix {
			t.Errorf("%s: title %q url %q", tc.ev.Kind, p.Title, p.URL)
		}
		if strings.Contains(p.Body, "sk-secret") {
			t.Errorf("%s: body not redacted: %q", tc.ev.Kind, p.Body)
		}
	}
	long := BuildPayload(integrations.Event{Kind: integrations.KindNeedsJudgment, InitiativeTitle: strings.Repeat("é", 300)}, nil)
	if len(long.Title) > 125 {
		t.Errorf("title not clipped: %d bytes", len(long.Title))
	}
}

func testSub(t *testing.T, endpoint string) webpush.Subscription {
	priv, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)
	return webpush.Subscription{Endpoint: endpoint,
		P256dh: base64.RawURLEncoding.EncodeToString(priv.PublicKey().Bytes()),
		Auth:   base64.RawURLEncoding.EncodeToString(auth)}
}

// Deliver maps push-service answers onto the dispatcher's semantics.
func TestDeliverErrorMapping(t *testing.T) {
	status := http.StatusCreated
	retryAfter := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	keys, _ := webpush.GenerateKeys()
	var gone bool
	var results []string
	n := &Notifier{
		SubscriptionID: "s1", Subscription: testSub(t, srv.URL+"/x"),
		Events: map[integrations.Kind]bool{integrations.KindChangeSetReady: true},
		Sender: &webpush.Sender{Keys: keys, Subject: "mailto:a@b.c", AllowInsecure: true},
		OnGone: func(context.Context) { gone = true }, OnResult: func(_ context.Context, e string) { results = append(results, e) },
	}
	ev := integrations.Event{Kind: integrations.KindChangeSetReady, ProjectID: "p", InitiativeID: "i"}
	if n.Name() != "webpush:s1" || !n.Wants(ev) || n.Wants(integrations.Event{Kind: integrations.KindVerifyFailed}) {
		t.Fatal("Name/Wants")
	}
	ctx := context.Background()

	if err := n.Deliver(ctx, ev); err != nil {
		t.Fatalf("201: %v", err)
	}
	status = http.StatusGone
	if err := n.Deliver(ctx, ev); !integrations.IsPermanent(err) || !gone {
		t.Fatalf("410: err=%v gone=%v, want permanent and OnGone", err, gone)
	}
	status, retryAfter = http.StatusTooManyRequests, "7"
	err := n.Deliver(ctx, ev)
	if d, ok := integrations.RetryAfter(err); !ok || d != 7*time.Second || integrations.IsPermanent(err) {
		t.Fatalf("429: %v", err)
	}
	status, retryAfter = http.StatusBadRequest, ""
	if err := n.Deliver(ctx, ev); !integrations.IsPermanent(err) {
		t.Fatalf("400 must be permanent: %v", err)
	}
	status = http.StatusBadGateway
	if err := n.Deliver(ctx, ev); err == nil || integrations.IsPermanent(err) {
		t.Fatalf("502 must be retried: %v", err)
	}
	if len(results) != 5 || results[0] != "" || results[1] == "" {
		t.Fatalf("results recorded: %q", results)
	}
}
