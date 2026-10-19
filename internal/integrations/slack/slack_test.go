package slack

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/integrations"
	"github.com/devarispbrown/idea-harness/internal/store"
)

func testDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestNotifierThreadsPerInitiativeAndEscapes(t *testing.T) {
	f := newFakeSlack(t)
	db := testDB(t)
	ctx := context.Background()
	ini := &domain.Initiative{Title: "t", Description: "t"}
	if err := db.CreateInitiative(ctx, ini); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateQuestion(ctx, &domain.Question{InitiativeID: ini.ID, Question: "Which auth provider?"}); err != nil {
		t.Fatal(err)
	}
	n := &Notifier{
		Cfg:    config.SlackConfig{Channel: "C1", MentionOnJudgment: []string{"here", "U0AAAA1"}},
		Client: &Client{APIBase: f.apiBase(), BotToken: "xoxb-test"},
		DB:     db, UIBase: "http://127.0.0.1:8787",
		Redact: func(s string) string { return strings.ReplaceAll(s, "sk-secret", "[REDACTED]") },
	}
	created := integrations.Event{Kind: integrations.KindInitiativeCreated, ProjectID: "p1", InitiativeID: ini.ID,
		InitiativeTitle: "<!channel> pwn <https://evil.example|click>", SourceEventID: "e1"}
	if !n.Wants(created) {
		t.Fatal("initiative.created is a default event")
	}
	if n.Wants(integrations.Event{Kind: integrations.KindTaskStatusChanged}) {
		t.Fatal("task.status_changed is not a default slack event")
	}
	if err := n.Deliver(ctx, created); err != nil {
		t.Fatal(err)
	}
	judgment := integrations.Event{Kind: integrations.KindNeedsJudgment, ProjectID: "p1", InitiativeID: ini.ID,
		InitiativeTitle: "t", Reason: "prd_review", SourceEventID: "e2"}
	if err := n.Deliver(ctx, judgment); err != nil {
		t.Fatal(err)
	}
	failed := integrations.Event{Kind: integrations.KindRunFailed, ProjectID: "p1", InitiativeID: ini.ID,
		InitiativeTitle: "t", Reason: "provider said sk-secret is invalid", SourceEventID: "e3"}
	if err := n.Deliver(ctx, failed); err != nil {
		t.Fatal(err)
	}

	posts := f.postsSnapshot()
	if len(posts) != 3 {
		t.Fatalf("posts = %d", len(posts))
	}
	if _, threaded := posts[0]["thread_ts"]; threaded {
		t.Fatal("the first event must be the thread root")
	}
	if posts[1]["thread_ts"] != "1700000000.000001" || posts[2]["thread_ts"] != "1700000000.000001" {
		t.Fatalf("follow-ups not threaded: %v / %v", posts[1]["thread_ts"], posts[2]["thread_ts"])
	}
	if posts[1]["reply_broadcast"] != true {
		t.Fatal("needs_judgment must be broadcast to the channel")
	}
	if posts[2]["reply_broadcast"] == true {
		t.Fatal("only needs_judgment is broadcast")
	}
	root := rawJSON(posts[0])
	if strings.Contains(string(root), "<!channel>") || strings.Contains(string(root), "<https://evil") {
		t.Fatalf("untrusted title not escaped: %s", root)
	}
	j := rawJSON(posts[1])
	for _, want := range []string{"<!here>", "<@U0AAAA1>", "Which auth provider?", ActionAnswerQuestion, "http://127.0.0.1:8787/#/i/" + ini.ID + "/define", "only on the machine running the daemon"} {
		if !strings.Contains(string(j), want) {
			t.Errorf("judgment message lacks %q: %s", want, j)
		}
	}
	if fj := rawJSON(posts[2]); strings.Contains(fj, "sk-secret") {
		t.Fatalf("secret reached Slack: %s", fj)
	}
	f.mu.Lock()
	if f.authHeader[0] != "Bearer xoxb-test" {
		t.Errorf("auth header = %q", f.authHeader[0])
	}
	f.mu.Unlock()

	// Moving the channel starts a new thread there.
	n.Cfg.Channel = "C2"
	if err := n.Deliver(ctx, failed); err != nil {
		t.Fatal(err)
	}
	if posts := f.postsSnapshot(); posts[3]["thread_ts"] != nil {
		t.Fatal("a thread from another channel was reused")
	}
}

func TestReadyForReviewHasApproveRejectButtons(t *testing.T) {
	ev := integrations.Event{Kind: integrations.KindChangeSetReady, ProjectID: "p1", InitiativeID: "i1", ChangeSetID: "cs1", ChangeSetRef: "CHG-2", TaskCode: "TASK-1", TaskTitle: "x"}
	b, _ := json.Marshal(buildBlocks(ev, "", "", JudgmentItems{}))
	for _, want := range []string{ActionApproveChangeSet, ActionRejectChangeSet, `\"cs\":\"cs1\"`, `\"p\":\"p1\"`, "confirm"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("blocks lack %q: %s", want, b)
		}
	}
}

func TestClientErrorMapping(t *testing.T) {
	f := newFakeSlack(t)
	c := &Client{APIBase: f.apiBase(), BotToken: "xoxb-test"}
	ctx := context.Background()

	f.rateLimit = true
	_, err := c.PostMessage(ctx, Message{Channel: "C1", Text: "x"})
	if after, ok := integrations.RetryAfter(err); !ok || after != 7*time.Second {
		t.Fatalf("429 should carry Retry-After 7s, got %v (%v)", after, err)
	}
	f.postError = "channel_not_found"
	_, err = c.PostMessage(ctx, Message{Channel: "C1", Text: "x"})
	if !integrations.IsPermanent(err) {
		t.Fatalf("channel_not_found should be permanent: %v", err)
	}
	f.postError = "internal_error"
	_, err = c.PostMessage(ctx, Message{Channel: "C1", Text: "x"})
	if err == nil || integrations.IsPermanent(err) {
		t.Fatalf("internal_error should be retryable: %v", err)
	}
	if _, err := (&Client{APIBase: f.apiBase()}).PostMessage(ctx, Message{}); !integrations.IsPermanent(err) {
		t.Fatalf("no token should be permanent: %v", err)
	}
}

// fakeActions records what an interaction asked the daemon to do.
type fakeActions struct {
	mu       sync.Mutex
	calls    []string
	reviewFn func(verdict string) error
}

func (a *fakeActions) record(s string) {
	a.mu.Lock()
	a.calls = append(a.calls, s)
	a.mu.Unlock()
}
func (a *fakeActions) callList() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}
func (a *fakeActions) ReviewChangeSet(_ context.Context, p, cs, verdict string) error {
	a.record("review " + p + " " + cs + " " + verdict)
	if a.reviewFn != nil {
		return a.reviewFn(verdict)
	}
	return nil
}
func (a *fakeActions) AcceptRecommendations(_ context.Context, p, ini string) ([]string, error) {
	a.record("accept " + p + " " + ini)
	return []string{"DEC-1"}, nil
}
func (a *fakeActions) AnswerQuestion(_ context.Context, p, q, ans string) error {
	a.record("answer " + p + " " + q + " " + ans)
	return nil
}
func (a *fakeActions) CreateInitiative(_ context.Context, p string, in integrations.NewInitiative) (string, error) {
	a.record("new " + p + " " + in.Title)
	return "ini-9", nil
}
func (a *fakeActions) ListInitiatives(_ context.Context, p string) ([]integrations.InitiativeSummary, error) {
	a.record("list " + p)
	return []integrations.InitiativeSummary{{ID: "ini-1", Title: "Dark mode", Status: "needs_human_judgment", JudgmentReason: "prd_review"}}, nil
}
func (a *fakeActions) ApproveInitiative(_ context.Context, p, ini string) error {
	a.record("approve " + p + " " + ini)
	return nil
}
func (a *fakeActions) AdvanceInitiative(_ context.Context, p, ini string) error {
	a.record("advance " + p + " " + ini)
	return nil
}
func (a *fakeActions) ExecuteInitiative(_ context.Context, p, ini string) error {
	a.record("execute " + p + " " + ini)
	return nil
}
func (a *fakeActions) ApplyChangeSet(_ context.Context, p, cs string) error {
	a.record("apply " + p + " " + cs)
	return nil
}

func startHub(t *testing.T, f *fakeSlack, routes ...Route) (*SocketHub, *fakeSocket) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	hub := &SocketHub{APIBase: f.apiBase(), Parent: ctx, ResponseURLAllowed: func(u string) bool { return strings.HasPrefix(u, f.srv.URL+"/response/") }}
	for _, r := range routes {
		hub.Register("xapp-test", r)
	}
	t.Cleanup(func() { cancel(); hub.Close() })
	return hub, f.waitSocket(t)
}

func blockAction(user, actionID string, v actionValue, responseURL string) map[string]any {
	return map[string]any{
		"type": "interactive", "envelope_id": "env-" + actionID + user,
		"payload": map[string]any{
			"type": "block_actions", "user": map[string]any{"id": user}, "trigger_id": "trig-1",
			"response_url": responseURL,
			"actions":      []map[string]any{{"action_id": actionID, "value": v.encode()}},
		},
	}
}

func TestSocketAllowlistRefusesAndPermits(t *testing.T) {
	f := newFakeSlack(t)
	acts := &fakeActions{}
	route := Route{ProjectID: "p1", Cfg: config.SlackConfig{Channel: "C1", AllowedUsers: []string{"U0GOOD1"}}, Actions: acts,
		Bot: &Client{APIBase: f.apiBase(), BotToken: "xoxb-test"}}
	hub, sock := startHub(t, f, route)

	v := actionValue{Project: "p1", Initiative: "i1", ChangeSet: "cs1"}
	ack := sock.send(t, blockAction("U0EVIL1", ActionApproveChangeSet, v, f.responseURL()))
	if ack["envelope_id"] != "env-"+ActionApproveChangeSet+"U0EVIL1" {
		t.Fatalf("ack = %v", ack)
	}
	resp := f.waitResponses(t, 1)
	if !strings.Contains(resp[0]["text"].(string), "not in `integrations.slack.allowed_users`") || resp[0]["response_type"] != "ephemeral" {
		t.Fatalf("refusal = %v", resp[0])
	}
	if calls := acts.callList(); len(calls) != 0 {
		t.Fatalf("a non-allowlisted user reached the daemon: %v", calls)
	}

	sock.send(t, blockAction("U0GOOD1", ActionApproveChangeSet, v, f.responseURL()))
	resp = f.waitResponses(t, 2)
	if !strings.Contains(resp[1]["text"].(string), "Approved") {
		t.Fatalf("approve reply = %v", resp[1])
	}
	if calls := acts.callList(); len(calls) != 1 || calls[0] != "review p1 cs1 approved" {
		t.Fatalf("calls = %v", calls)
	}

	// The daemon's refusal (a gate) is shown verbatim, not swallowed.
	acts.reviewFn = func(string) error {
		return &integrations.ActionError{Status: 400, Message: "change set has open blocking findings"}
	}
	sock.send(t, blockAction("U0GOOD1", ActionRejectChangeSet, v, f.responseURL()))
	resp = f.waitResponses(t, 3)
	if !strings.Contains(resp[2]["text"].(string), "open blocking findings") {
		t.Fatalf("gate refusal not relayed: %v", resp[2])
	}

	// Answer: opens a modal; the submission answers through Actions.
	sock.send(t, blockAction("U0GOOD1", ActionAnswerQuestion, actionValue{Project: "p1", Question: "q1"}, f.responseURL()))
	deadline := time.Now().Add(5 * time.Second)
	for {
		f.mu.Lock()
		n := len(f.views)
		f.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("answer modal never opened")
		}
		time.Sleep(5 * time.Millisecond)
	}
	submit := func(user string) map[string]any {
		return sock.send(t, map[string]any{
			"type": "interactive", "envelope_id": "env-view-" + user,
			"payload": map[string]any{
				"type": "view_submission", "user": map[string]any{"id": user},
				"view": map[string]any{
					"callback_id": CallbackAnswerModal, "private_metadata": actionValue{Project: "p1", Question: "q1"}.encode(),
					"state": map[string]any{"values": map[string]any{answerBlockID: map[string]any{answerInputID: map[string]any{"value": "Okta"}}}},
				},
			},
		})
	}
	if ack := submit("U0EVIL1"); ack["payload"] == nil {
		t.Fatal("a non-allowlisted submitter must get an inline error")
	}
	if ack := submit("U0GOOD1"); ack["payload"] != nil {
		t.Fatalf("allowed answer should close the modal, got %v", ack["payload"])
	}
	calls := acts.callList()
	if calls[len(calls)-1] != "answer p1 q1 Okta" {
		t.Fatalf("calls = %v", calls)
	}
	if st, ok := hub.State("p1"); !ok || !st.Connected {
		t.Fatalf("state = %+v %v", st, ok)
	}
}

func TestSocketEmptyAllowlistIsNotifyOnly(t *testing.T) {
	f := newFakeSlack(t)
	acts := &fakeActions{}
	_, sock := startHub(t, f, Route{ProjectID: "p1", Cfg: config.SlackConfig{Channel: "C1"}, Actions: acts})
	sock.send(t, blockAction("U0ANY01", ActionAcceptRecommendations, actionValue{Project: "p1", Initiative: "i1"}, f.responseURL()))
	resp := f.waitResponses(t, 1)
	if !strings.Contains(resp[0]["text"].(string), "notify-only") {
		t.Fatalf("refusal = %v", resp[0])
	}
	ack := sock.send(t, map[string]any{"type": "slash_commands", "envelope_id": "s1", "accepts_response_payload": true,
		"payload": map[string]any{"command": "/idea", "text": "new hack it", "user_id": "U0ANY01", "channel_id": "C1"}})
	if p, _ := ack["payload"].(map[string]any); p == nil || !strings.Contains(p["text"].(string), "notify-only") {
		t.Fatalf("slash with empty allowlist = %v", ack)
	}
	if calls := acts.callList(); len(calls) != 0 {
		t.Fatalf("empty allowlist reached the daemon: %v", calls)
	}
}

func TestSlashCommands(t *testing.T) {
	f := newFakeSlack(t)
	acts := &fakeActions{}
	_, sock := startHub(t, f,
		Route{ProjectID: "p1", Cfg: config.SlackConfig{Channel: "C1", AllowedUsers: []string{"U0GOOD1"}}, Actions: acts},
		Route{ProjectID: "p2", Cfg: config.SlackConfig{Channel: "C2", AllowedUsers: []string{"U0GOOD1"}}, Actions: acts},
	)
	slash := func(channel, text string) string {
		ack := sock.send(t, map[string]any{"type": "slash_commands", "envelope_id": "s-" + channel + text, "accepts_response_payload": true,
			"payload": map[string]any{"command": "/idea", "text": text, "user_id": "U0GOOD1", "channel_id": channel}})
		p, _ := ack["payload"].(map[string]any)
		if p == nil {
			t.Fatalf("no reply payload: %v", ack)
		}
		return p["text"].(string)
	}
	if got := slash("C2", "new Add dark mode"); !strings.Contains(got, "ini-9") {
		t.Fatalf("new reply = %q", got)
	}
	if got := slash("C1", "status"); !strings.Contains(got, "Dark mode") || !strings.Contains(got, "prd_review") {
		t.Fatalf("status reply = %q", got)
	}
	if got := slash("C9", "status"); !strings.Contains(got, "not linked") {
		t.Fatalf("unrouted channel reply = %q", got)
	}
	if got := slash("C1", "frobnicate"); !strings.Contains(got, "Usage") {
		t.Fatalf("usage reply = %q", got)
	}
	if got := slash("C1", "approve ini-1"); !strings.Contains(got, "Approved") {
		t.Fatalf("approve reply = %q", got)
	}
	if got := slash("C1", "advance ini-1"); !strings.Contains(got, "Advanced") {
		t.Fatalf("advance reply = %q", got)
	}
	if got := slash("C1", "execute ini-1"); !strings.Contains(got, "Execute started") {
		t.Fatalf("execute reply = %q", got)
	}
	if got := slash("C1", "review CHG-1 approve"); !strings.Contains(got, "Reviewed") {
		t.Fatalf("review reply = %q", got)
	}
	if got := slash("C1", "apply CHG-1"); !strings.Contains(got, "Apply started") {
		t.Fatalf("apply reply = %q", got)
	}
	calls := acts.callList()
	want := []string{"new p2 Add dark mode", "list p1", "approve p1 ini-1", "advance p1 ini-1", "execute p1 ini-1", "review p1 CHG-1 approve", "apply p1 CHG-1"}
	if len(calls) != len(want) {
		t.Fatalf("slash routed wrong: %v", calls)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("slash routed wrong: %v", calls)
		}
	}
}

// A disconnect Slack asks for (refresh_requested) reconnects at once through
// a fresh apps.connections.open; a dropped socket reconnects with backoff.
func TestSocketReconnects(t *testing.T) {
	f := newFakeSlack(t)
	_, sock := startHub(t, f, Route{ProjectID: "p1", Cfg: config.SlackConfig{Channel: "C1"}, Actions: &fakeActions{}})
	data, _ := json.Marshal(map[string]any{"type": "disconnect", "reason": "refresh_requested"})
	if err := sock.conn.Write(context.Background(), 1, data); err != nil {
		t.Fatal(err)
	}
	second := f.waitSocket(t)
	second.conn.CloseNow() // an unrequested drop
	f.waitSocket(t)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.opens < 3 {
		t.Fatalf("apps.connections.open called %d times; want 3", f.opens)
	}
}

func TestSocketURLMustBeTLS(t *testing.T) {
	for url, ok := range map[string]bool{
		"wss://wss-primary.slack.com/link/?ticket=x": true,
		"ws://127.0.0.1:9999/socket":                 true,
		"ws://wss-primary.slack.com/link":            false,
		"https://example.com":                        false,
	} {
		if err := checkSocketURL(url); (err == nil) != ok {
			t.Errorf("%s: err=%v want ok=%v", url, err, ok)
		}
	}
	if defaultResponseURLAllowed("https://evil.example/x") || !defaultResponseURLAllowed("https://hooks.slack.com/actions/T/1/x") {
		t.Fatal("response_url vetting wrong")
	}
}

// rawJSON marshals without HTML escaping, so assertions see the < and > Slack
// would actually receive rather than JSON unicode escapes.
func rawJSON(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return b.String()
}

func TestCheck(t *testing.T) {
	f := newFakeSlack(t)
	bot := &Client{APIBase: f.apiBase(), BotToken: "xoxb-test"}
	res, err := Check(context.Background(), bot, config.SlackConfig{Channel: "C1"}, "xapp-test")
	if err != nil || res.Team != "Acme" || res.MessageTS == "" || res.SocketMode != "ok" {
		t.Fatalf("check = %+v %v", res, err)
	}
	res, err = Check(context.Background(), bot, config.SlackConfig{Channel: "C1"}, "")
	if err != nil || !strings.Contains(res.SocketMode, "SLACK_APP_TOKEN") {
		t.Fatalf("no app token = %+v %v", res, err)
	}
	f.mu.Lock()
	f.postError = "not_in_channel"
	f.mu.Unlock()
	if _, err := Check(context.Background(), bot, config.SlackConfig{Channel: "C1"}, ""); err == nil || !strings.Contains(err.Error(), "not_in_channel") {
		t.Fatalf("not_in_channel should surface: %v", err)
	}
}
