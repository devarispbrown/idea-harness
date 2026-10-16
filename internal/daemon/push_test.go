package daemon

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/devarispbrown/idea-harness/internal/store"
	"github.com/devarispbrown/idea-harness/internal/webpush"
)

// fakeBrowser is the receiving end of a push subscription: the key pair and
// auth secret a browser generates, and an RFC 8291 decryptor written here
// independently of internal/webpush.
type fakeBrowser struct {
	priv *ecdh.PrivateKey
	auth []byte
}

func newFakeBrowser(t *testing.T) *fakeBrowser {
	t.Helper()
	priv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	return &fakeBrowser{priv: priv, auth: auth}
}

func (b *fakeBrowser) keys() (p256dh, auth string) {
	return base64.RawURLEncoding.EncodeToString(b.priv.PublicKey().Bytes()), base64.RawURLEncoding.EncodeToString(b.auth)
}

func (b *fakeBrowser) decrypt(body []byte) ([]byte, error) {
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	_ = rs
	asPubBytes := body[21 : 21+idlen]
	asPub, err := ecdh.P256().NewPublicKey(asPubBytes)
	if err != nil {
		return nil, err
	}
	secret, err := b.priv.ECDH(asPub)
	if err != nil {
		return nil, err
	}
	prkKey, _ := hkdf.Extract(sha256.New, secret, b.auth)
	info := append(append([]byte("WebPush: info\x00"), b.priv.PublicKey().Bytes()...), asPubBytes...)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, string(info), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[21+idlen:], nil)
	if err != nil {
		return nil, err
	}
	plain = bytes.TrimRight(plain, "\x00")
	return plain[:len(plain)-1], nil // drop the 0x02 last-record delimiter
}

// fakePushService stands in for FCM / Mozilla autopush / Apple: it records
// what each endpoint received, decrypted with that endpoint's browser keys.
type fakePushService struct {
	srv      *httptest.Server
	mu       sync.Mutex
	browsers map[string]*fakeBrowser // by path
	status   map[string]int
	got      map[string][]map[string]any
	authHdrs []string
}

func newFakePushService(t *testing.T) *fakePushService {
	f := &fakePushService{browsers: map[string]*fakeBrowser{}, status: map[string]int{}, got: map[string][]map[string]any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if st := f.status[r.URL.Path]; st != 0 {
			w.WriteHeader(st)
			return
		}
		if r.Header.Get("Content-Encoding") != "aes128gcm" || !strings.HasPrefix(r.Header.Get("Authorization"), "vapid t=") {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		f.authHdrs = append(f.authHdrs, r.Header.Get("Authorization"))
		b := f.browsers[r.URL.Path]
		body, _ := io.ReadAll(r.Body)
		plain, err := b.decrypt(body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		var p map[string]any
		json.Unmarshal(plain, &p)
		f.got[r.URL.Path] = append(f.got[r.URL.Path], p)
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakePushService) received(path string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.got[path]...)
}

// subscribe registers a fake browser at path and posts its subscription to
// the daemon.
func (f *fakePushService) subscribe(t *testing.T, ts *httptest.Server, token, projectID, path, events string) pushSubscriptionView {
	t.Helper()
	b := newFakeBrowser(t)
	f.mu.Lock()
	f.browsers[path] = b
	f.mu.Unlock()
	p, a := b.keys()
	body := `{"endpoint":"` + f.srv.URL + path + `","keys":{"p256dh":"` + p + `","auth":"` + a + `"}` + events + `,"label":"phone"}`
	resp := doReq(t, "POST", ts.URL+"/api/v1/projects/"+projectID+"/push/subscriptions", token, "", body)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("subscribe: %d %s", resp.StatusCode, raw)
	}
	if strings.Contains(string(raw), path) || strings.Contains(string(raw), p) {
		t.Fatalf("the subscription view leaks the endpoint path or keys: %s", raw)
	}
	var v pushSubscriptionView
	json.Unmarshal(raw, &v)
	return v
}

func newPushServer(t *testing.T) (*Runtime, *httptest.Server, string, string) {
	t.Helper()
	keys, err := webpush.GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	rt, ts, token, projectID := newIntegrationServer(t, "", "", nil)
	rt.push.override = &webpush.Sender{Keys: keys, Subject: "mailto:ops@example.com", AllowInsecure: true}
	return rt, ts, token, projectID
}

// The whole loop: a browser subscribes, an engine write becomes an encrypted
// Web Push to it through the integration bus, once, with a deep link; a
// subscription that does not want that kind gets nothing; one the push
// service reports gone is removed.
func TestPushDeliversThroughTheIntegrationBus(t *testing.T) {
	rt, ts, token, projectID := newPushServer(t)
	fake := newFakePushService(t)

	all := fake.subscribe(t, ts, token, projectID, "/push/all", "")
	if strings.Join(all.Events, ",") != "initiative.needs_judgment,change_set.ready_for_review,verify.failed,outcome.drifted" {
		t.Fatalf("default events = %v, want the four needs-you kinds", all.Events)
	}
	if all.EndpointHost == "" {
		t.Fatal("endpoint_host is empty")
	}
	verifyOnly := fake.subscribe(t, ts, token, projectID, "/push/verify-only", `,"events":["verify.failed"]`)
	gone := fake.subscribe(t, ts, token, projectID, "/push/gone", "")
	fake.mu.Lock()
	fake.status["/push/gone"] = http.StatusGone
	fake.mu.Unlock()

	if v, _ := rt.global.DaemonMeta(context.Background(), pushMetaKey(projectID)); v == "" {
		t.Fatal("a project with push subscriptions must be marked for opening at daemon start")
	}

	pr, err := rt.EnsureProject(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{all.ID, verifyOnly.ID, gone.ID} {
		waitFor(t, "cursor for "+id, func() bool {
			_, ok, _ := pr.db.IntegrationCursor(context.Background(), store.PushSubscriberName(id))
			return ok
		})
	}
	cs := reviewableChangeSet(t, pr.db)

	waitFor(t, "a push to the subscribed browser", func() bool { return len(fake.received("/push/all")) == 1 })
	p := fake.received("/push/all")[0]
	if !strings.HasPrefix(p["title"].(string), "Ready for review") {
		t.Fatalf("title = %v", p["title"])
	}
	wantURL := "/#/p/" + projectID + "/i/" + cs.InitiativeID + "/build"
	if p["url"] != wantURL {
		t.Fatalf("url = %v, want %s", p["url"], wantURL)
	}

	waitFor(t, "the gone subscription removed", func() bool {
		_, err := pr.db.GetPushSubscription(context.Background(), gone.ID)
		return err != nil
	})

	// Effectively once: more ticks deliver nothing new, and the filtered
	// browser never heard of a change set.
	time.Sleep(200 * time.Millisecond)
	if n := len(fake.received("/push/all")); n != 1 {
		t.Fatalf("delivered %d times, want exactly once", n)
	}
	if n := len(fake.received("/push/verify-only")); n != 0 {
		t.Fatalf("a verify.failed-only browser got %d pushes for a change set", n)
	}
	got, _ := pr.db.GetPushSubscription(context.Background(), all.ID)
	if got.LastDeliveredAt == nil || got.LastError != "" {
		t.Fatalf("delivery not recorded: %+v", got)
	}
}

func TestPushSubscriptionAPI(t *testing.T) {
	rt, ts, token, projectID := newPushServer(t)
	fake := newFakePushService(t)
	base := ts.URL + "/api/v1/projects/" + projectID + "/push/subscriptions"

	resp := doReq(t, "GET", ts.URL+"/api/v1/push/vapid-public-key", token, "", "")
	var cfg struct {
		PublicKey  string   `json:"public_key"`
		EventKinds []string `json:"event_kinds"`
	}
	json.NewDecoder(resp.Body).Decode(&cfg)
	resp.Body.Close()
	if raw, err := base64.RawURLEncoding.DecodeString(cfg.PublicKey); err != nil || len(raw) != 65 || raw[0] != 4 {
		t.Fatalf("public key %q is not an uncompressed P-256 point", cfg.PublicKey)
	}
	if len(cfg.EventKinds) != 4 {
		t.Fatalf("event kinds = %v", cfg.EventKinds)
	}

	b := newFakeBrowser(t)
	pk, auth := b.keys()
	for name, body := range map[string]string{
		"unknown kind": `{"endpoint":"` + fake.srv.URL + `/x","keys":{"p256dh":"` + pk + `","auth":"` + auth + `"},"events":["run.failed"]}`,
		"bad key":      `{"endpoint":"` + fake.srv.URL + `/x","keys":{"p256dh":"AAAA","auth":"` + auth + `"}}`,
		"no endpoint":  `{"keys":{"p256dh":"` + pk + `","auth":"` + auth + `"}}`,
	} {
		resp := doReq(t, "POST", base, token, "", body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, resp.StatusCode)
		}
	}

	sub := fake.subscribe(t, ts, token, projectID, "/push/p", "")
	// Subscribing the same browser again updates, never duplicates.
	again := doReq(t, "POST", base, token, "", `{"endpoint":"`+fake.srv.URL+`/push/p","keys":{"p256dh":"`+pk+`","auth":"`+auth+`"},"events":["verify.failed"]}`)
	var v pushSubscriptionView
	json.NewDecoder(again.Body).Decode(&v)
	again.Body.Close()
	if again.StatusCode != http.StatusOK || v.ID != sub.ID {
		t.Fatalf("re-subscribe: %d id=%s, want 200 and the same id %s", again.StatusCode, v.ID, sub.ID)
	}
	fake.mu.Lock()
	fake.browsers["/push/p"] = b // the keys were just rotated to b's
	fake.mu.Unlock()

	patch := doReq(t, "PATCH", base+"/"+sub.ID, token, "", `{"events":["outcome.drifted","initiative.needs_judgment"]}`)
	json.NewDecoder(patch.Body).Decode(&v)
	patch.Body.Close()
	if patch.StatusCode != http.StatusOK || strings.Join(v.Events, ",") != "initiative.needs_judgment,outcome.drifted" {
		t.Fatalf("patch: %d %v", patch.StatusCode, v.Events)
	}

	test := doReq(t, "POST", base+"/"+sub.ID+"/test", token, "", "")
	var tr map[string]any
	json.NewDecoder(test.Body).Decode(&tr)
	test.Body.Close()
	if tr["ok"] != true {
		t.Fatalf("test push: %v", tr)
	}
	if got := fake.received("/push/p"); len(got) != 1 || !strings.Contains(got[0]["body"].(string), "Notifications are on") {
		t.Fatalf("test push payload: %v", got)
	}

	del := doReq(t, "DELETE", base+"/"+sub.ID, token, "", "")
	del.Body.Close()
	if del.StatusCode != http.StatusNoContent {
		t.Fatalf("delete: %d", del.StatusCode)
	}
	pr, _ := rt.EnsureProject(context.Background(), projectID)
	if _, ok, _ := pr.db.IntegrationCursor(context.Background(), store.PushSubscriberName(sub.ID)); ok {
		t.Fatal("deleting a subscription must remove its bus cursor")
	}
	if v, _ := rt.global.DaemonMeta(context.Background(), pushMetaKey(projectID)); v != "" {
		t.Fatal("the start-up marker must be cleared with the last subscription")
	}
}

// Without the test-only AllowInsecure, a plain-http endpoint is refused at
// subscribe time, not at the first delivery.
func TestPushRefusesAnInsecureEndpoint(t *testing.T) {
	rt, ts, token, projectID := newIntegrationServer(t, "", "", nil)
	keys, _ := webpush.GenerateKeys()
	rt.push.override = &webpush.Sender{Keys: keys, Subject: "mailto:ops@example.com"}
	b := newFakeBrowser(t)
	pk, auth := b.keys()
	resp := doReq(t, "POST", ts.URL+"/api/v1/projects/"+projectID+"/push/subscriptions", token, "",
		`{"endpoint":"http://push.example.com/x","keys":{"p256dh":"`+pk+`","auth":"`+auth+`"}}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("http endpoint: %d, want 400", resp.StatusCode)
	}
}

// The VAPID key pair is created on first use under the state root, 0600,
// and reused after that -- every subscription is bound to it.
func TestVAPIDKeysLiveInTheStateRoot(t *testing.T) {
	rt, _, _ := newTestServer(t)
	s1, err := rt.pushSender()
	if err != nil {
		t.Fatal(err)
	}
	rt2, err := NewRuntime(rt.stateRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer rt2.Close()
	s2, err := rt2.pushSender()
	if err != nil {
		t.Fatal(err)
	}
	if s1.Keys.PublicKey() != s2.Keys.PublicKey() {
		t.Fatal("a restart generated a new VAPID key: every subscription would be orphaned")
	}
	if s1.Subject != defaultPushContact {
		t.Fatalf("subject = %q", s1.Subject)
	}
}
