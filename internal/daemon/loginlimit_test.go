package daemon

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// guessUntil sends n wrong codes as b and requires each to be an ordinary 401.
func guessUntil(t *testing.T, b *browser, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if resp, body := b.login("AAAAA-AAAAA"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("guess %d: %d %s, want 401", i, resp.StatusCode, body)
		}
	}
}

func client(t *testing.T, ts *httptest.Server, xff string) *browser {
	return &browser{t: t, ts: ts, headers: map[string]string{"X-Forwarded-For": xff}}
}

// TestLoginClientBudgetDoesNotLockOutTheOwner is the bug this design fixes:
// one guesser spending its budget used to lock everyone out and cancel the
// owner's code. Now only the guesser is refused.
func TestLoginClientBudgetDoesNotLockOutTheOwner(t *testing.T) {
	rt, ts := newRemoteTestServer(t)
	good := pairingCode(t, ts, rt.Token())

	guesser := client(t, ts, "100.64.0.66")
	guessUntil(t, guesser, loginClientBudget)
	resp, body := guesser.login(good)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("the guesser past its budget: %d, want 429", resp.StatusCode)
	}
	if !strings.Contains(body, "this device") {
		t.Fatalf("a per-client lock must say it is per client: %s", body)
	}
	if ra, err := strconv.Atoi(resp.Header.Get("Retry-After")); err != nil || ra <= 0 || ra > int(loginFailureWindow/time.Second) {
		t.Fatalf("Retry-After = %q, want seconds within the window", resp.Header.Get("Retry-After"))
	}

	owner := client(t, ts, "100.64.0.7")
	if resp, body := owner.login(good); resp.StatusCode != http.StatusOK {
		t.Fatalf("the owner, on another device, with the code the guesser was hunting: %d %s", resp.StatusCode, body)
	}

	// The guesser's lock lifts with the window.
	ua := rt.uiAuth()
	real := ua.now
	t.Cleanup(func() { ua.now = real })
	ua.now = func() time.Time { return real().Add(loginFailureWindow + time.Second) }
	if resp, _ := guesser.login("AAAAA-AAAAA"); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after the window: %d, want an ordinary 401", resp.StatusCode)
	}
}

// TestLoginClientIdentityIsNotSpoofable: what a client can write into a
// header must not buy it a fresh budget.
func TestLoginClientIdentityIsNotSpoofable(t *testing.T) {
	cases := []struct {
		name string
		// per-request headers/host for guess i
		host    string
		headers func(i int) map[string]string
	}{
		{
			// Through the trusted proxy, a client can only PREPEND to
			// X-Forwarded-For; the proxy appends the address it saw.
			name: "prepended X-Forwarded-For entries",
			headers: func(i int) map[string]string {
				return map[string]string{"X-Forwarded-For": fmt.Sprintf("203.0.113.%d, 100.64.0.66", i)}
			},
		},
		{
			// A loopback Host is not the configured proxy, so no proxy header
			// is believed: every request is charged to the loopback peer.
			name: "X-Forwarded-For on a loopback Host",
			host: "127.0.0.1",
			headers: func(i int) map[string]string {
				return map[string]string{"X-Forwarded-For": fmt.Sprintf("100.64.1.%d", i)}
			},
		},
		{
			name: "Tailscale-User-Login on a loopback Host",
			host: "127.0.0.1",
			headers: func(i int) map[string]string {
				return map[string]string{"X-Forwarded-For": "", "Tailscale-User-Login": fmt.Sprintf("user%d@example.com", i)}
			},
		},
		{
			// One tailnet identity, many devices: the identity is the client.
			name: "one tailnet login from many addresses",
			headers: func(i int) map[string]string {
				return map[string]string{"X-Forwarded-For": fmt.Sprintf("100.64.2.%d", i), "Tailscale-User-Login": "mallory@example.com"}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ts := newRemoteTestServer(t)
			for i := 0; i < loginClientBudget; i++ {
				b := &browser{t: t, ts: ts, host: tc.host, headers: tc.headers(i)}
				guessUntil(t, b, 1)
			}
			b := &browser{t: t, ts: ts, host: tc.host, headers: tc.headers(loginClientBudget)}
			if resp, _ := b.login("AAAAA-AAAAA"); resp.StatusCode != http.StatusTooManyRequests {
				t.Fatalf("guess %d with a varied header: %d, want 429 (one client)", loginClientBudget, resp.StatusCode)
			}
		})
	}
}

// TestLoginGlobalCeiling: many clients together still hit the old wall --
// everyone is paused and every outstanding code is cancelled.
func TestLoginGlobalCeiling(t *testing.T) {
	rt, ts := newRemoteTestServer(t)
	good := pairingCode(t, ts, rt.Token())
	clients := loginGlobalCeiling / loginClientBudget
	for c := 0; c < clients; c++ {
		guessUntil(t, client(t, ts, fmt.Sprintf("100.64.3.%d", c)), loginClientBudget)
	}
	fresh := client(t, ts, "100.64.0.7")
	resp, body := fresh.login(good)
	if resp.StatusCode != http.StatusTooManyRequests || !strings.Contains(body, "everyone") {
		t.Fatalf("a fresh client after the ceiling: %d %s, want the global 429", resp.StatusCode, body)
	}
	ua := rt.uiAuth()
	real := ua.now
	t.Cleanup(func() { ua.now = real })
	ua.now = func() time.Time { return real().Add(loginFailureWindow + time.Second) }
	if resp, _ := fresh.login(good); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the code outstanding when the ceiling was hit must have been cancelled: %d", resp.StatusCode)
	}
	if resp, body := fresh.login(pairingCode(t, ts, rt.Token())); resp.StatusCode != http.StatusOK {
		t.Fatalf("a new code after the window: %d %s", resp.StatusCode, body)
	}
}

// TestLoginSuccessClearsTheClient: a mistyped code or two does not follow the
// owner into the next sign-in.
func TestLoginSuccessClearsTheClient(t *testing.T) {
	rt, ts := newRemoteTestServer(t)
	owner := client(t, ts, "100.64.0.7")
	guessUntil(t, owner, loginClientBudget-1)
	if resp, body := owner.login(pairingCode(t, ts, rt.Token())); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	guessUntil(t, owner, loginClientBudget-1)
}

// TestLoginClientKey is the identity matrix, request by request.
func TestLoginClientKey(t *testing.T) {
	hosts := newHostPolicy(DaemonConfig{AllowedHosts: []string{"laptop.tail1.ts.net"}}, &ListenPlan{host: "100.101.1.2"})
	req := func(peer, host string, hdr map[string]string) *http.Request {
		r := httptest.NewRequest("POST", "/api/v1/ui/login", nil)
		r.RemoteAddr, r.Host = peer, host
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	for _, tc := range []struct {
		name string
		r    *http.Request
		want string
	}{
		{"trusted proxy, X-Forwarded-For", req("127.0.0.1:5000", "laptop.tail1.ts.net", map[string]string{"X-Forwarded-For": "100.64.0.7"}), "ip:100.64.0.7"},
		{"trusted proxy, rightmost entry wins", req("127.0.0.1:5000", "laptop.tail1.ts.net", map[string]string{"X-Forwarded-For": "1.2.3.4, 100.64.0.7"}), "ip:100.64.0.7"},
		{"trusted proxy, Tailscale identity wins", req("[::1]:5000", "laptop.tail1.ts.net:443", map[string]string{"X-Forwarded-For": "100.64.0.7", "Tailscale-User-Login": "Me@Example.com"}), "tailscale:me@example.com"},
		{"trusted proxy, garbage X-Forwarded-For", req("127.0.0.1:5000", "laptop.tail1.ts.net", map[string]string{"X-Forwarded-For": "not-an-ip"}), "ip:127.0.0.1"},
		{"loopback Host: headers ignored", req("127.0.0.1:5000", "127.0.0.1:8787", map[string]string{"X-Forwarded-For": "100.64.0.7", "Tailscale-User-Login": "me@example.com"}), "ip:127.0.0.1"},
		{"remote listener: headers ignored", req("100.64.0.9:5000", "100.101.1.2:8787", map[string]string{"X-Forwarded-For": "100.64.0.7", "Tailscale-User-Login": "me@example.com"}), "ip:100.64.0.9"},
		{"remote listener reached under the proxy name: headers ignored", req("100.64.0.9:5000", "laptop.tail1.ts.net", map[string]string{"X-Forwarded-For": "100.64.0.7"}), "ip:100.64.0.9"},
		{"IPv6 peer charged by its /64", req("[2001:db8:1:2:aaaa::1]:5000", "100.101.1.2:8787", nil), "ip:2001:db8:1:2::/64"},
		{"IPv6 forwarded client charged by its /64", req("127.0.0.1:5000", "laptop.tail1.ts.net", map[string]string{"X-Forwarded-For": "fd7a:115c:a1e0:ab12:4843:cd96:625b:e016"}), "ip:fd7a:115c:a1e0:ab12::/64"},
	} {
		if got := loginClientKey(tc.r, hosts); got != tc.want {
			t.Errorf("%s: key = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestLoginLimiterUnit pins the bookkeeping: window pruning, the exact
// ceiling crossing, and that memory is released as failures age out.
func TestLoginLimiterUnit(t *testing.T) {
	var l loginLimiter
	t0 := time.Unix(1_000_000, 0)
	spentAt := -1
	for i := 0; i < loginGlobalCeiling; i++ {
		key := fmt.Sprintf("ip:10.0.0.%d", i/loginClientBudget)
		if lock, _ := l.check(key, t0); lock != loginOpen {
			t.Fatalf("failure %d refused early: %v", i, lock)
		}
		if l.fail(key, t0) {
			spentAt = i
		}
	}
	if spentAt != loginGlobalCeiling-1 {
		t.Fatalf("the ceiling reported spent at failure %d, want exactly %d", spentAt, loginGlobalCeiling-1)
	}
	if lock, wait := l.check("ip:10.9.9.9", t0.Add(time.Minute)); lock != loginLockedGlobal || wait != loginFailureWindow-time.Minute {
		t.Fatalf("after the ceiling: %v %v", lock, wait)
	}
	if lock, _ := l.check("ip:10.9.9.9", t0.Add(loginFailureWindow)); lock != loginOpen {
		t.Fatalf("after the window: %v", lock)
	}
	if len(l.perClient) != 0 || len(l.global) != 0 {
		t.Fatalf("aged-out failures kept: %d clients, %d global", len(l.perClient), len(l.global))
	}
}

// TestPairingCodeSpaceArithmetic keeps the security-model numbers honest: a
// code is 50 bits, and a year of guessing at the global ceiling against one
// outstanding code stays below one in a hundred million.
func TestPairingCodeSpaceArithmetic(t *testing.T) {
	c, err := newPairingCode()
	if err != nil {
		t.Fatal(err)
	}
	symbols := len(normalizePairingCode(c))
	bits := float64(symbols) * math.Log2(float64(len(pairingAlphabet)))
	if bits != 50 {
		t.Fatalf("a pairing code carries %v bits, the docs say 50", bits)
	}
	// newPairingCode maps a byte onto the alphabet with %; that is unbiased
	// only because 256 is a multiple of the alphabet size.
	if 256%len(pairingAlphabet) != 0 {
		t.Fatal("pairing alphabet size no longer divides 256: codes would be biased")
	}
	perYear := float64(loginGlobalCeiling) * (365 * 24 * float64(time.Hour) / float64(loginFailureWindow))
	if p := perYear / math.Pow(2, bits); p > 1e-8 {
		t.Fatalf("a year at the ceiling succeeds with p=%g per outstanding code; the docs promise < 1e-8", p)
	}
}
