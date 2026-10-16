package daemon

import (
	"encoding/json"
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

// sprayFrom spends the whole per-client budget of n distinct clients: n *
// loginClientBudget wrong codes, the distributed attacker the old global
// ceiling turned into a lock-out for everyone.
func sprayFrom(t *testing.T, ts *httptest.Server, subnet string, n int) {
	t.Helper()
	for c := 0; c < n; c++ {
		guessUntil(t, client(t, ts, fmt.Sprintf("%s.%d", subnet, c)), loginClientBudget)
	}
}

func pairingResponse(t *testing.T, ts *httptest.Server, token string) pairingCodeResponse {
	t.Helper()
	resp := doReq(t, "POST", ts.URL+"/api/v1/ui/pairing-codes", token, "", `{}`)
	defer resp.Body.Close()
	var out pairingCodeResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("pairing code: %d %v", resp.StatusCode, err)
	}
	return out
}

// TestDistributedGuessingBurnsTheCodeNotTheEndpoint is the lock-out this
// design removes: enough clients to spend the old global ceiling now burn the
// codes they were guessing at, and nothing else. A fresh code -- which the
// owner mints locally -- works at once, and the owner is told codes burned.
func TestDistributedGuessingBurnsTheCodeNotTheEndpoint(t *testing.T) {
	rt, ts := newRemoteTestServer(t)
	hunted := pairingCode(t, ts, rt.Token())
	if out := pairingResponse(t, ts, rt.Token()); out.BurnedCodes != 0 || out.LastBurnedAt != nil {
		t.Fatalf("nothing has burned yet: %+v", out.burnedCodesView)
	}
	sprayFrom(t, ts, "100.64.3", pairingCodeFailureBudget/loginClientBudget)

	owner := client(t, ts, "100.64.0.7")
	resp, body := owner.login(hunted)
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "too many wrong codes") {
		t.Fatalf("the hunted code after %d failures: %d %s, want 401 saying it was burned", pairingCodeFailureBudget, resp.StatusCode, body)
	}

	fresh := pairingResponse(t, ts, rt.Token())
	// Both codes outstanding during the spray burned (the second was minted
	// above, before it), so the owner is told about two.
	if fresh.BurnedCodes != 2 || fresh.LastBurnedAt == nil {
		t.Fatalf("idea ui pair must report the burns: %+v", fresh.burnedCodesView)
	}
	if resp, body := owner.login(fresh.Code); resp.StatusCode != http.StatusOK {
		t.Fatalf("the owner's fresh code, with no wait: %d %s", resp.StatusCode, body)
	}
	resp, body = owner.do("GET", "/api/v1/ui/sessions", "", nil)
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"burned_codes":2`) {
		t.Fatalf("the session list must report the burns: %d %s", resp.StatusCode, body)
	}

	// More attackers, from new addresses, lock nobody out either: a code
	// minted after their failures starts at zero.
	sprayFrom(t, ts, "100.64.4", 2*pairingCodeFailureBudget/loginClientBudget)
	if resp, body := client(t, ts, "100.64.0.8").login(pairingCode(t, ts, rt.Token())); resp.StatusCode != http.StatusOK {
		t.Fatalf("a code minted after the spray: %d %s", resp.StatusCode, body)
	}
}

// TestLoginLoopbackIsNeverRefused: a browser on this machine is neither
// charged nor refused, however many codes it or anybody else gets wrong.
func TestLoginLoopbackIsNeverRefused(t *testing.T) {
	rt, ts := newRemoteTestServer(t)
	sprayFrom(t, ts, "100.64.3", pairingCodeFailureBudget/loginClientBudget)
	local := &browser{t: t, ts: ts, host: "127.0.0.1", headers: map[string]string{"X-Forwarded-For": ""}}
	guessUntil(t, local, 3*loginClientBudget)
	if resp, body := local.login(pairingCode(t, ts, rt.Token())); resp.StatusCode != http.StatusOK {
		t.Fatalf("loopback sign-in after its own and others' failures: %d %s", resp.StatusCode, body)
	}
}

// TestLoginSpraySparesSignedInSessions: a signed-in browser keeps working
// through any amount of guessing, including from its own address.
func TestLoginSpraySparesSignedInSessions(t *testing.T) {
	rt, ts := newRemoteTestServer(t)
	phone := client(t, ts, "100.64.0.7")
	if resp, body := phone.login(pairingCode(t, ts, rt.Token())); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	pairingCode(t, ts, rt.Token()) // an outstanding code for the spray to burn
	sprayFrom(t, ts, "100.64.3", 2*pairingCodeFailureBudget/loginClientBudget)
	guessUntil(t, phone, loginClientBudget)
	if resp, _ := phone.login("AAAAA-AAAAA"); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("the phone's address past its budget: %d, want 429", resp.StatusCode)
	}
	if resp, body := phone.do("GET", "/api/v1/projects", "", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("a signed-in session during a spray: %d %s", resp.StatusCode, body)
	}
	if resp, body := phone.do("GET", "/api/v1/ui/session", "", nil); resp.StatusCode != http.StatusOK || !strings.Contains(body, `"auth":"session"`) {
		t.Fatalf("session view during a spray: %d %s", resp.StatusCode, body)
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

// TestLoginLimiterUnit pins the bookkeeping: window pruning, that one
// client's budget is its own, that memory is released as failures age out,
// and that the map is bounded.
func TestLoginLimiterUnit(t *testing.T) {
	var l loginLimiter
	t0 := time.Unix(1_000_000, 0)
	for i := 0; i < loginClientBudget; i++ {
		if ok, _ := l.check("ip:10.0.0.1", t0); !ok {
			t.Fatalf("failure %d refused early", i)
		}
		l.fail("ip:10.0.0.1", t0)
	}
	if ok, wait := l.check("ip:10.0.0.1", t0.Add(time.Minute)); ok || wait != loginFailureWindow-time.Minute {
		t.Fatalf("after the budget: %v %v", ok, wait)
	}
	if ok, _ := l.check("ip:10.0.0.2", t0.Add(time.Minute)); !ok {
		t.Fatal("another client was refused")
	}
	if ok, _ := l.check("ip:10.0.0.1", t0.Add(loginFailureWindow)); !ok {
		t.Fatal("still refused after the window")
	}
	if len(l.perClient) != 0 {
		t.Fatalf("aged-out failures kept: %d clients", len(l.perClient))
	}

	for i := 0; i < loginMaxClients+50; i++ {
		l.fail(fmt.Sprintf("ip:10.%d.%d.%d", i>>16, (i>>8)&255, i&255), t0.Add(time.Duration(i)*time.Millisecond))
	}
	if len(l.perClient) != loginMaxClients {
		t.Fatalf("%d clients tracked, want the cap %d", len(l.perClient), loginMaxClients)
	}
	if _, kept := l.perClient["ip:10.0.0.0"]; kept {
		t.Fatal("the stalest client should have been evicted first")
	}
}

// TestPairingCodeSpaceArithmetic keeps the security-model numbers honest: a
// code is 50 bits and absorbs at most pairingCodeFailureBudget wrong guesses,
// so one code falls with p <= 100/2^50 (about 8.9e-14), and a hundred thousand
// codes together stay below one in a hundred million.
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
	perCode := float64(pairingCodeFailureBudget) / math.Pow(2, bits)
	if perCode > 9e-14 {
		t.Fatalf("one code falls with p=%g; the docs say about 8.9e-14", perCode)
	}
	if p := 1e5 * perCode; p > 1e-8 {
		t.Fatalf("a hundred thousand codes fall with p=%g; the docs promise < 1e-8", p)
	}
}
