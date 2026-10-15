package webpush

import (
	"context"
	"crypto/ecdh"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakePushService is a push service in miniature: it checks what a real one
// checks (encoding, TTL, VAPID signature and audience) and decrypts the body
// with the user agent's private key, recording what it saw.
type fakePushService struct {
	t      *testing.T
	srv    *httptest.Server
	uaPriv *ecdh.PrivateKey
	auth   []byte
	keys   *Keys

	mu       sync.Mutex
	status   int
	extra    http.Header
	respBody string
	got      []string // decrypted plaintexts
	headers  []http.Header
	hits     int
}

func newFakePushService(t *testing.T, keys *Keys) (*fakePushService, Subscription) {
	f := &fakePushService{t: t, keys: keys, status: http.StatusCreated}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	var sub Subscription
	f.uaPriv, f.auth, sub = newTestUA(t, f.srv.URL+"/push/abc123?secret=tok")
	return f, sub
}

func (f *fakePushService) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.hits++
	f.headers = append(f.headers, r.Header.Clone())
	fail := func(msg string) {
		f.t.Errorf("push service: %s", msg)
		http.Error(w, msg, http.StatusBadRequest)
	}
	if r.Method != http.MethodPost {
		fail("method " + r.Method)
		return
	}
	if r.URL.Path != "/push/abc123" || r.URL.Query().Get("secret") != "tok" {
		fail("wrong endpoint path " + r.URL.String())
		return
	}
	if ce := r.Header.Get("Content-Encoding"); ce != "aes128gcm" {
		fail("Content-Encoding " + ce)
		return
	}
	if ct := r.Header.Get("Content-Type"); ct != "application/octet-stream" {
		fail("Content-Type " + ct)
		return
	}
	if r.Header.Get("TTL") == "" {
		fail("missing TTL")
		return
	}
	claims, k, err := verifyVAPID(r.Header.Get("Authorization"))
	if err != nil {
		fail("vapid: " + err.Error())
		return
	}
	if k != f.keys.PublicKey() {
		fail("vapid k is not the sender's key")
		return
	}
	if claims.Aud != f.srv.URL {
		fail("aud " + claims.Aud + " != " + f.srv.URL)
		return
	}
	if claims.Sub != "mailto:ops@example.com" {
		fail("sub " + claims.Sub)
		return
	}
	if exp := time.Unix(claims.Exp, 0); time.Until(exp) <= 0 || time.Until(exp) > 24*time.Hour {
		fail("exp out of range")
		return
	}
	body, _ := io.ReadAll(r.Body)
	if len(body) > maxBodySize {
		fail("body too large")
		return
	}
	pt, err := testDecrypt(body, f.uaPriv, f.auth)
	if err != nil {
		fail("decrypt: " + err.Error())
		return
	}
	f.got = append(f.got, string(pt))
	for k, vs := range f.extra {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(f.status)
	io.WriteString(w, f.respBody)
}

func (f *fakePushService) set(status int, respBody string, extra http.Header) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.respBody, f.extra = status, respBody, extra
}

func testSender(t *testing.T) *Sender {
	keys, err := GenerateKeys()
	if err != nil {
		t.Fatal(err)
	}
	return &Sender{Keys: keys, Subject: "mailto:ops@example.com", AllowInsecure: true}
}

func TestSendDelivers(t *testing.T) {
	s := testSender(t)
	f, sub := newFakePushService(t, s.Keys)
	msg := Message{Payload: []byte(`{"title":"hi"}`), TTL: 90 * time.Second, Urgency: "high", Topic: "run-42_x"}
	if err := s.Send(context.Background(), sub, msg); err != nil {
		t.Fatal(err)
	}
	if len(f.got) != 1 || f.got[0] != `{"title":"hi"}` {
		t.Fatalf("push service decrypted %q", f.got)
	}
	h := f.headers[0]
	if h.Get("TTL") != "90" || h.Get("Urgency") != "high" || h.Get("Topic") != "run-42_x" {
		t.Fatalf("headers TTL=%q Urgency=%q Topic=%q", h.Get("TTL"), h.Get("Urgency"), h.Get("Topic"))
	}

	// Defaults: TTL 24h, no Urgency/Topic headers.
	if err := s.Send(context.Background(), sub, Message{Payload: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	h = f.headers[1]
	if h.Get("TTL") != "86400" {
		t.Errorf("default TTL = %q", h.Get("TTL"))
	}
	if _, ok := h["Urgency"]; ok {
		t.Error("Urgency sent when unset")
	}
	if _, ok := h["Topic"]; ok {
		t.Error("Topic sent when unset")
	}
}

func TestSendUsesNow(t *testing.T) {
	s := testSender(t)
	f, sub := newFakePushService(t, s.Keys)
	fixed := time.Now().Add(time.Hour).Truncate(time.Second)
	s.Now = func() time.Time { return fixed }
	if err := s.Send(context.Background(), sub, Message{Payload: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	c, _, err := verifyVAPID(f.headers[0].Get("Authorization"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Exp != fixed.Add(12*time.Hour).Unix() {
		t.Fatalf("exp %d, want Now()+12h", c.Exp)
	}
}

func TestSendStatusMapping(t *testing.T) {
	s := testSender(t)
	f, sub := newFakePushService(t, s.Keys)
	ctx := context.Background()
	msg := Message{Payload: []byte("ping")}

	for _, code := range []int{200, 201, 202, 204} {
		f.set(code, "", nil)
		if err := s.Send(ctx, sub, msg); err != nil {
			t.Errorf("%d: %v", code, err)
		}
	}

	for _, code := range []int{404, 410} {
		f.set(code, "gone", nil)
		err := s.Send(ctx, sub, msg)
		if !IsGone(err) {
			t.Errorf("%d: IsGone false for %v", code, err)
		}
		var g *GoneError
		if !errors.As(err, &g) || g.Status != code {
			t.Errorf("%d: %#v", code, err)
		}
	}

	cases := []struct {
		code      int
		header    http.Header
		permanent bool
		retry     time.Duration
	}{
		{429, http.Header{"Retry-After": {"7"}}, false, 7 * time.Second},
		{400, nil, true, 0},
		{401, nil, true, 0},
		{403, nil, true, 0},
		{413, nil, true, 0},
		{500, nil, false, 0},
		{503, http.Header{"Retry-After": {"120"}}, false, 2 * time.Minute},
		{502, nil, false, 0},
	}
	for _, tc := range cases {
		f.set(tc.code, strings.Repeat("e", 2000), tc.header)
		err := s.Send(ctx, sub, msg)
		var se *StatusError
		if !errors.As(err, &se) {
			t.Fatalf("%d: want *StatusError, got %v", tc.code, err)
		}
		if IsGone(err) {
			t.Errorf("%d: IsGone true", tc.code)
		}
		if se.Status != tc.code || se.Permanent != tc.permanent || se.RetryAfter != tc.retry {
			t.Errorf("%d: got status=%d permanent=%v retry=%s", tc.code, se.Status, se.Permanent, se.RetryAfter)
		}
		if len(se.Body) != maxErrorBody {
			t.Errorf("%d: body %d bytes, want truncated to %d", tc.code, len(se.Body), maxErrorBody)
		}
		assertNoLeak(t, err, sub)
	}
}

func TestSendDoesNotFollowRedirects(t *testing.T) {
	s := testSender(t)
	var targetHits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits++
		w.WriteHeader(http.StatusCreated)
	}))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/elsewhere", http.StatusFound)
	}))
	defer redir.Close()
	_, _, sub := newTestUA(t, redir.URL+"/push/x")

	err := s.Send(context.Background(), sub, Message{Payload: []byte("x")})
	var se *StatusError
	if !errors.As(err, &se) || se.Status != http.StatusFound {
		t.Fatalf("want StatusError 302, got %v", err)
	}
	if targetHits != 0 {
		t.Fatal("redirect was followed")
	}
}

func TestSendRejectsBadInput(t *testing.T) {
	s := testSender(t)
	f, sub := newFakePushService(t, s.Keys)
	ctx := context.Background()
	bad := map[string]Message{
		"urgency":       {Urgency: "urgent"},
		"topic chars":   {Topic: "has space"},
		"topic length":  {Topic: strings.Repeat("a", 33)},
		"negative TTL":  {TTL: -time.Second},
		"payload large": {Payload: make([]byte, MaxPayloadSize+1)},
	}
	for name, m := range bad {
		if err := s.Send(ctx, sub, m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := s.Send(ctx, sub, Message{Payload: make([]byte, MaxPayloadSize+1)}); !errors.Is(err, ErrPayloadTooLarge) {
		t.Errorf("payload: %v", err)
	}
	if err := s.Send(ctx, sub, Message{Topic: strings.Repeat("a", 32)}); err != nil {
		t.Errorf("32-char topic rejected: %v", err)
	}

	noSubject := *s
	noSubject.Subject = ""
	if err := noSubject.Send(ctx, sub, Message{}); err == nil {
		t.Error("empty subject accepted")
	}
	noKeys := *s
	noKeys.Keys = nil
	if err := noKeys.Send(ctx, sub, Message{}); err == nil {
		t.Error("nil keys accepted")
	}
	if f.hits != 1 {
		t.Errorf("push service hit %d times; invalid messages must not be sent", f.hits)
	}
}

func TestSendNetworkErrorSanitized(t *testing.T) {
	s := testSender(t)
	// Grab a free port and close it so the connection is refused.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	_, _, sub := newTestUA(t, "http://"+addr+"/push/abc123?secret=tok")
	err = s.Send(context.Background(), sub, Message{Payload: []byte("x")})
	if err == nil {
		t.Fatal("expected network error")
	}
	var se *StatusError
	if errors.As(err, &se) || IsGone(err) {
		t.Fatalf("network error mistyped: %v", err)
	}
	assertNoLeak(t, err, sub)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, sub2 := newFakePushService(t, s.Keys)
	if err := s.Send(ctx, sub2, Message{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled context: %v", err)
	}
}

func assertNoLeak(t *testing.T, err error, sub Subscription) {
	t.Helper()
	for _, secret := range []string{"abc123", "secret", "tok", sub.Auth, sub.P256dh} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error %q leaks %q", err, secret)
		}
	}
}

func TestDefaultSenderRefusesInsecureTargets(t *testing.T) {
	keys, _ := GenerateKeys()
	s := &Sender{Keys: keys, Subject: "mailto:ops@example.com"}
	ctx := context.Background()

	// A plain http endpoint fails validation before any network use.
	_, _, sub := newTestUA(t, "http://push.example.com/x")
	if err := s.Send(ctx, sub, Message{}); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("http endpoint: %v", err)
	}

	// An https endpoint on loopback is refused by the dial guard. A TLS
	// test server proves the refusal is the guard, not a missing listener.
	var hits int
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer srv.Close()
	_, _, sub = newTestUA(t, srv.URL+"/push/x")
	err := s.Send(ctx, sub, Message{})
	if err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("loopback endpoint: %v", err)
	}
	if hits != 0 {
		t.Fatal("guarded sender reached loopback")
	}
}

type fakeResolver map[string][]string

func (f fakeResolver) LookupIPAddr(_ context.Context, host string) ([]net.IPAddr, error) {
	var out []net.IPAddr
	for _, s := range f[host] {
		out = append(out, net.IPAddr{IP: net.ParseIP(s)})
	}
	return out, nil
}

func TestGuardedDial(t *testing.T) {
	res := fakeResolver{
		"loopback.test":  {"127.0.0.1"},
		"metadata.test":  {"169.254.169.254"},
		"private.test":   {"10.1.2.3"},
		"cgnat.test":     {"100.64.0.1"},
		"v6local.test":   {"::1"},
		"ula.test":       {"fd00::1"},
		"mapped.test":    {"::ffff:127.0.0.1"},
		"zero.test":      {"0.0.0.0"},
		"zeronet.test":   {"0.1.2.3"},
		"multicast.test": {"224.0.0.1"},
		"mixed.test":     {"8.8.8.8", "192.168.1.1"},
		"empty.test":     nil,
		"public.test":    {"203.0.113.9", "198.51.100.7"},
	}
	var dialed []string
	dial := func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed = append(dialed, addr)
		if strings.HasPrefix(addr, "203.0.113.9") {
			return nil, errors.New("first address down")
		}
		c1, c2 := net.Pipe()
		c2.Close()
		return c1, nil
	}
	guard := guardedDial(res, dial)
	for host := range res {
		if host == "public.test" {
			continue
		}
		if _, err := guard(context.Background(), "tcp", host+":443"); err == nil {
			t.Errorf("%s: dial allowed", host)
		}
	}
	if len(dialed) != 0 {
		t.Fatalf("refused hosts were dialed: %v", dialed)
	}
	conn, err := guard(context.Background(), "tcp", "public.test:443")
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	// The checked IPs are dialed (never the name), with fallback in order.
	if len(dialed) != 2 || dialed[0] != "203.0.113.9:443" || dialed[1] != "198.51.100.7:443" {
		t.Fatalf("dialed %v", dialed)
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	cases := map[string]time.Duration{
		"":               0,
		"7":              7 * time.Second,
		" 30 ":           30 * time.Second,
		"0":              0,
		"-5":             0,
		"soon":           0,
		"99999999999999": 7 * 24 * time.Hour,
		now.Add(90 * time.Second).Format(http.TimeFormat): 90 * time.Second,
		now.Add(-time.Hour).Format(http.TimeFormat):       0,
	}
	for in, want := range cases {
		if got := parseRetryAfter(in, now); got != want {
			t.Errorf("parseRetryAfter(%q) = %s, want %s", in, got, want)
		}
	}
}
