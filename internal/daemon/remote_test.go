package daemon

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------- listen policy ----------

func writeSelfSigned(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "idea-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kb, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return certPath, keyPath
}

// TestRemoteListenPolicy is the daemon.listen matrix: which addresses are
// refused, which are allowed with a warning, and what TLS changes.
func TestRemoteListenPolicy(t *testing.T) {
	cert, key := writeSelfSigned(t)
	cases := []struct {
		name     string
		cfg      DaemonConfig
		wantErr  string // substring; "" = allowed
		wantWarn bool
		wantTLS  bool
	}{
		{name: "unset is loopback only", cfg: DaemonConfig{}},
		{name: "TLS without listen", cfg: DaemonConfig{TLSCert: cert, TLSKey: key}, wantErr: "daemon.listen is not"},
		{name: "hostname refused", cfg: DaemonConfig{Listen: "laptop.local:8787"}, wantErr: "not a hostname"},
		{name: "no port", cfg: DaemonConfig{Listen: "100.100.1.2"}, wantErr: "want ip:port"},
		{name: "loopback refused", cfg: DaemonConfig{Listen: "127.0.0.1:9000"}, wantErr: "loopback"},
		{name: "loopback v6 refused", cfg: DaemonConfig{Listen: "[::1]:9000"}, wantErr: "loopback"},
		{name: "tailscale v4 plaintext allowed, warned", cfg: DaemonConfig{Listen: "100.101.102.103:8787"}, wantWarn: true},
		{name: "tailscale v6 plaintext allowed, warned", cfg: DaemonConfig{Listen: "[fd7a:115c:a1e0::1]:8787"}, wantWarn: true},
		{name: "private LAN plaintext allowed, warned", cfg: DaemonConfig{Listen: "192.168.1.20:8787"}, wantWarn: true},
		{name: "private 10/8 plaintext allowed, warned", cfg: DaemonConfig{Listen: "10.0.0.5:8787"}, wantWarn: true},
		{name: "public plaintext refused", cfg: DaemonConfig{Listen: "203.0.113.7:8787"}, wantErr: "refused without TLS"},
		{name: "link-local plaintext refused", cfg: DaemonConfig{Listen: "169.254.1.1:8787"}, wantErr: "refused without TLS"},
		{name: "all interfaces plaintext refused", cfg: DaemonConfig{Listen: "0.0.0.0:8787"}, wantErr: "every interface without TLS"},
		{name: "all interfaces v6 plaintext refused", cfg: DaemonConfig{Listen: "[::]:8787"}, wantErr: "every interface without TLS"},
		{name: "public over TLS allowed", cfg: DaemonConfig{Listen: "203.0.113.7:8443", TLSCert: cert, TLSKey: key}, wantTLS: true},
		{name: "all interfaces over TLS allowed, warned", cfg: DaemonConfig{Listen: "0.0.0.0:8443", TLSCert: cert, TLSKey: key}, wantTLS: true, wantWarn: true},
		{name: "cert without key", cfg: DaemonConfig{Listen: "203.0.113.7:8443", TLSCert: cert}, wantErr: "set together"},
		{name: "unreadable cert", cfg: DaemonConfig{Listen: "203.0.113.7:8443", TLSCert: cert + ".missing", TLSKey: key}, wantErr: "tls_cert/tls_key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, err := planRemoteListen(tc.cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.cfg.Listen == "" {
				if plan != nil {
					t.Fatalf("plan = %+v, want nil", plan)
				}
				return
			}
			if (plan.Warning != "") != tc.wantWarn {
				t.Errorf("warning = %q, want warning: %v", plan.Warning, tc.wantWarn)
			}
			if (plan.TLS != nil) != tc.wantTLS {
				t.Errorf("TLS = %v, want %v", plan.TLS != nil, tc.wantTLS)
			}
		})
	}
}

func TestLoadDaemonConfig(t *testing.T) {
	root := t.TempDir()
	c, err := LoadDaemonConfig(root)
	if err != nil || c.Listen != "" {
		t.Fatalf("missing file: %+v, %v; want the zero config", c, err)
	}
	os.WriteFile(DaemonConfigPath(root), []byte("daemon:\n  listen: 100.64.0.9:8787\n  allowed_hosts: [laptop.tail1.ts.net]\n  session_ttl: 48h\n"), 0o600)
	c, err = LoadDaemonConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "100.64.0.9:8787" || len(c.AllowedHosts) != 1 || c.sessionTTL() != 48*time.Hour {
		t.Fatalf("parsed %+v", c)
	}
	os.WriteFile(DaemonConfigPath(root), []byte("daemon:\n  lisen: 100.64.0.9:8787\n"), 0o600)
	if _, err := LoadDaemonConfig(root); err == nil {
		t.Fatal("a misspelled key must be an error, not loopback-only in silence")
	}
	os.WriteFile(DaemonConfigPath(root), []byte(""), 0o600)
	if _, err := LoadDaemonConfig(root); err != nil {
		t.Fatalf("an empty file is the default config: %v", err)
	}
}

// TestRunRefusesABadListenBeforeBinding: a refused daemon.listen stops the
// daemon at start rather than leaving it quietly loopback-only.
func TestRunRefusesABadListenBeforeBinding(t *testing.T) {
	root := t.TempDir()
	err := RunWith("127.0.0.1:0", root, slog.Default(), nil, func(c *DaemonConfig) { c.Listen = "203.0.113.7:8787" })
	if err == nil || !strings.Contains(err.Error(), "refused without TLS") {
		t.Fatalf("err = %v", err)
	}
}

// ---------- host policy and token injection ----------

func TestHostPolicy(t *testing.T) {
	plan := &ListenPlan{Addr: "100.101.1.2:8787", host: "100.101.1.2"}
	p := newHostPolicy(DaemonConfig{AllowedHosts: []string{"Laptop.Tail1.ts.net", " other.example:443 "}}, plan)
	for _, h := range []string{"127.0.0.1:8787", "localhost", "laptop.tail1.ts.net", "LAPTOP.tail1.ts.net:443", "other.example", "100.101.1.2:8787"} {
		if !p.allowsHost(h) {
			t.Errorf("host %q refused", h)
		}
	}
	for _, h := range []string{"evil.example", "laptop.tail1.ts.net.evil.example", "100.101.1.3:8787"} {
		if p.allowsHost(h) {
			t.Errorf("host %q allowed", h)
		}
	}
	for _, o := range []string{"https://laptop.tail1.ts.net", "http://100.101.1.2:8787", "http://127.0.0.1:8787"} {
		if !p.allowsOrigin(o) {
			t.Errorf("origin %q refused", o)
		}
	}
	for _, o := range []string{"https://evil.example", "https://laptop.tail1.ts.net/x", "https://u@laptop.tail1.ts.net", "ftp://laptop.tail1.ts.net", "laptop.tail1.ts.net"} {
		if p.allowsOrigin(o) {
			t.Errorf("origin %q allowed", o)
		}
	}
	// No config: loopback only, exactly as before.
	none := newHostPolicy(DaemonConfig{}, nil)
	if none.allowsHost("laptop.tail1.ts.net") || !none.allowsHost("127.0.0.1:1") {
		t.Error("the zero policy must be loopback-only")
	}
}

func TestIsLocalRequest(t *testing.T) {
	withLocal := func(r *http.Request, ip string) *http.Request {
		return r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP(ip), Port: 8787}))
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Host = "127.0.0.1:8787"
	if !isLocalRequest(withLocal(r, "127.0.0.1")) {
		t.Error("loopback socket + loopback host must be local")
	}
	if isLocalRequest(withLocal(r, "100.101.1.2")) {
		t.Error("a request on the remote listener must never be local, whatever its Host says")
	}
	r2 := r.Clone(r.Context())
	r2.Header.Set("X-Forwarded-For", "100.64.0.7")
	if isLocalRequest(withLocal(r2, "127.0.0.1")) {
		t.Error("a proxied request must not be local")
	}
	r3 := r.Clone(r.Context())
	r3.Header.Set("Tailscale-User-Login", "me@example.com")
	if isLocalRequest(withLocal(r3, "127.0.0.1")) {
		t.Error("a tailscale serve request must not be local")
	}
	r4 := r.Clone(r.Context())
	r4.Host = "laptop.tail1.ts.net"
	if isLocalRequest(withLocal(r4, "127.0.0.1")) {
		t.Error("a proxy's hostname must not be local")
	}
}

// newRemoteTestServer is newTestServer with daemon.allowed_hosts set, which
// is how `tailscale serve` in front of loopback looks to the daemon.
func newRemoteTestServer(t *testing.T) (*Runtime, *httptest.Server) {
	t.Helper()
	rt, err := NewRuntime(filepath.Join(t.TempDir(), "state"), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rt.Close() })
	rt.configureRemote(DaemonConfig{AllowedHosts: []string{"laptop.tail1.ts.net"}}, nil)
	ts := httptest.NewServer(NewServer(rt).Handler())
	t.Cleanup(ts.Close)
	return rt, ts
}

const remoteHost = "laptop.tail1.ts.net"
const remoteOrigin = "https://laptop.tail1.ts.net"

func getPage(t *testing.T, ts *httptest.Server, host string, hdr map[string]string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", ts.URL+"/", nil)
	if host != "" {
		req.Host = host
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / as %q: %d %s", host, resp.StatusCode, b)
	}
	return string(b)
}

// TestTokenOnlyInTheLocalPage: loopback keeps the token flow unchanged; a
// remote browser, or one behind a proxy, gets the page without it.
func TestTokenOnlyInTheLocalPage(t *testing.T) {
	rt, ts := newRemoteTestServer(t)
	if page := getPage(t, ts, "", nil); !strings.Contains(page, rt.Token()) {
		t.Fatal("the loopback page must carry the token, exactly as before remote use existed")
	}
	if page := getPage(t, ts, remoteHost, map[string]string{"X-Forwarded-Proto": "https"}); strings.Contains(page, rt.Token()) {
		t.Fatal("a page served under an allowed remote host carried the API token")
	}
	if page := getPage(t, ts, "", map[string]string{"X-Forwarded-For": "100.64.0.7"}); strings.Contains(page, rt.Token()) {
		t.Fatal("a proxied page (Host rewritten to loopback) carried the API token")
	}
	// An unconfigured host is still refused outright.
	resp := doReq(t, "GET", ts.URL+"/", "", "evil.example", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unknown host: %d, want 403", resp.StatusCode)
	}
}

// ---------- sessions ----------

type browser struct {
	t       *testing.T
	ts      *httptest.Server
	cookies []*http.Cookie
	csrf    string
	https   bool
}

func (b *browser) do(method, path, body string, hdr map[string]string) (*http.Response, string) {
	b.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, b.ts.URL+path, rd)
	req.Host = remoteHost
	if b.https {
		req.Header.Set("X-Forwarded-Proto", "https")
	}
	req.Header.Set("X-Forwarded-For", "100.64.0.7")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range b.cookies {
		req.AddCookie(c)
	}
	for k, v := range hdr {
		if v == "" {
			req.Header.Del(k)
			continue
		}
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		b.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, string(out)
}

func pairingCode(t *testing.T, ts *httptest.Server, token string) string {
	t.Helper()
	resp := doReq(t, "POST", ts.URL+"/api/v1/ui/pairing-codes", token, "", `{"label":"phone"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("pairing code: %d %s", resp.StatusCode, b)
	}
	var out pairingCodeResponse
	json.NewDecoder(resp.Body).Decode(&out)
	if len(out.Code) != 11 || out.Code[5] != '-' {
		t.Fatalf("code %q is not XXXXX-XXXXX", out.Code)
	}
	if !out.Remote || len(out.URLs) == 0 || out.URLs[0] != "https://laptop.tail1.ts.net" {
		t.Fatalf("pairing response should name the remote URL: %+v", out)
	}
	return out.Code
}

func (b *browser) login(code string) (*http.Response, string) {
	resp, body := b.do("POST", "/api/v1/ui/login", `{"code":"`+code+`"}`, map[string]string{"Origin": remoteOrigin})
	if resp.StatusCode == http.StatusOK {
		b.cookies = resp.Cookies()
		var v sessionView
		json.Unmarshal([]byte(body), &v)
		b.csrf = v.CSRFToken
	}
	return resp, body
}

func TestRemoteSignInSessionAndCSRF(t *testing.T) {
	rt, ts := newRemoteTestServer(t)
	token := rt.Token()
	b := &browser{t: t, ts: ts, https: true}

	// Before signing in: nothing.
	if resp, _ := b.do("GET", "/api/v1/projects", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unsigned remote request: %d", resp.StatusCode)
	}

	code := pairingCode(t, ts, token)

	// The sign-in endpoint's own checks.
	if resp, _ := b.do("POST", "/api/v1/ui/login", `{"code":"`+code+`"}`, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("login without Origin: %d, want 403", resp.StatusCode)
	}
	if resp, _ := b.do("POST", "/api/v1/ui/login", `{"code":"`+code+`"}`, map[string]string{"Origin": "https://evil.example"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("login from a foreign origin: %d, want 403", resp.StatusCode)
	}
	if resp, _ := b.do("POST", "/api/v1/ui/login", `code=`+code, map[string]string{"Origin": remoteOrigin, "Content-Type": "application/x-www-form-urlencoded"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("form-encoded login: %d, want 403", resp.StatusCode)
	}

	// Lower case and no dash still work: people type what they read.
	resp, body := b.login(strings.ToLower(strings.ReplaceAll(code, "-", "")))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(body, token) {
		t.Fatal("the login response carried the API token")
	}
	var sc *http.Cookie
	for _, c := range b.cookies {
		if strings.Contains(c.Name, "idea_session") {
			sc = c
		}
	}
	if sc == nil || sc.Name != sessionCookieSecure || !sc.HttpOnly || !sc.Secure || sc.SameSite != http.SameSiteStrictMode || sc.Path != "/" || sc.Domain != "" {
		t.Fatalf("session cookie over https must be __Host-, HttpOnly, Secure, SameSite=Strict, Path=/: %+v", sc)
	}
	if b.csrf == "" {
		t.Fatal("login returned no CSRF token")
	}

	// A code is single use.
	b2 := &browser{t: t, ts: ts}
	if resp, _ := b2.login(code); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("reused code: %d, want 401", resp.StatusCode)
	}

	// Reads with the cookie alone.
	if resp, body := b.do("GET", "/api/v1/projects", "", nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("signed-in read: %d %s", resp.StatusCode, body)
	}
	if resp, body := b.do("GET", "/api/v1/ui/session", "", nil); resp.StatusCode != http.StatusOK || !strings.Contains(body, b.csrf) {
		t.Fatalf("GET /ui/session must return the CSRF token to the page: %d %s", resp.StatusCode, body)
	}

	dir := filepath.Join(t.TempDir(), "proj")
	os.MkdirAll(dir, 0o755)
	create := `{"root_path":"` + dir + `"}`
	// Writes need the CSRF token AND an allowed Origin.
	if resp, _ := b.do("POST", "/api/v1/projects", create, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cookie write without CSRF/Origin: %d, want 403", resp.StatusCode)
	}
	if resp, _ := b.do("POST", "/api/v1/projects", create, map[string]string{"Origin": remoteOrigin}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cookie write without CSRF: %d, want 403", resp.StatusCode)
	}
	if resp, _ := b.do("POST", "/api/v1/projects", create, map[string]string{csrfHeader: b.csrf}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cookie write without Origin: %d, want 403", resp.StatusCode)
	}
	if resp, _ := b.do("POST", "/api/v1/projects", create, map[string]string{csrfHeader: "wrong", "Origin": remoteOrigin}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cookie write with a wrong CSRF token: %d, want 403", resp.StatusCode)
	}
	if resp, _ := b.do("POST", "/api/v1/projects", create, map[string]string{csrfHeader: b.csrf, "Origin": "https://evil.example"}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cookie write from a foreign origin: %d, want 403", resp.StatusCode)
	}
	if resp, body := b.do("POST", "/api/v1/projects", create, map[string]string{csrfHeader: b.csrf, "Origin": remoteOrigin}); resp.StatusCode != http.StatusOK {
		t.Fatalf("a signed-in write: %d %s", resp.StatusCode, body)
	}

	// A session cannot mint pairing codes.
	if resp, _ := b.do("POST", "/api/v1/ui/pairing-codes", `{}`, map[string]string{csrfHeader: b.csrf, "Origin": remoteOrigin}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("session minting a pairing code: %d, want 403", resp.StatusCode)
	}

	// A wrong token never falls through to the cookie.
	if resp, _ := b.do("GET", "/api/v1/projects", "", map[string]string{"X-Idea-Token": "nope"}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong token + valid cookie: %d, want 401", resp.StatusCode)
	}

	// Listed, then logged out.
	resp = doReq(t, "GET", ts.URL+"/api/v1/ui/sessions", token, "", "")
	var list struct {
		Sessions []struct {
			ID    string `json:"id"`
			Label string `json:"label"`
		} `json:"sessions"`
	}
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list.Sessions) != 1 || list.Sessions[0].Label != "phone" {
		t.Fatalf("sessions: %+v", list)
	}
	if strings.Contains(strings.ToLower(mustReadAll(t, doReq(t, "GET", ts.URL+"/api/v1/ui/sessions", token, "", ""))), "secret") {
		t.Fatal("the session list exposes a secret field")
	}
	if resp, _ := b.do("POST", "/api/v1/ui/logout", "", map[string]string{csrfHeader: b.csrf, "Origin": remoteOrigin}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if resp, _ := b.do("GET", "/api/v1/projects", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("after logout: %d, want 401", resp.StatusCode)
	}
}

func mustReadAll(t *testing.T, resp *http.Response) string {
	t.Helper()
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func TestPlainHTTPSessionCookieIsNotSecure(t *testing.T) {
	rt, ts := newRemoteTestServer(t)
	b := &browser{t: t, ts: ts}
	if resp, body := b.login(pairingCode(t, ts, rt.Token())); resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d %s", resp.StatusCode, body)
	}
	c := b.cookies[0]
	// A Secure cookie over plain HTTP (a Tailscale IP listener) is one the
	// browser would refuse to store: sign-in would silently not stick.
	if c.Name != sessionCookiePlain || c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("plain-HTTP cookie: %+v", c)
	}
}

func TestSessionRevocationExpiryAndIdle(t *testing.T) {
	rt, ts := newRemoteTestServer(t)
	token := rt.Token()

	signIn := func() *browser {
		b := &browser{t: t, ts: ts}
		if resp, body := b.login(pairingCode(t, ts, token)); resp.StatusCode != http.StatusOK {
			t.Fatalf("login: %d %s", resp.StatusCode, body)
		}
		return b
	}

	// Revoked by id from the CLI's side.
	a := signIn()
	resp, body := a.do("GET", "/api/v1/ui/session", "", nil)
	var v sessionView
	json.Unmarshal([]byte(body), &v)
	if resp.StatusCode != 200 || v.Session == nil {
		t.Fatalf("session view: %d %s", resp.StatusCode, body)
	}
	r := doReq(t, "DELETE", ts.URL+"/api/v1/ui/sessions/"+v.Session.ID, token, "", "")
	r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", r.StatusCode)
	}
	if resp, _ := a.do("GET", "/api/v1/projects", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked session: %d", resp.StatusCode)
	}

	// Revoke-all.
	b, c := signIn(), signIn()
	r = doReq(t, "DELETE", ts.URL+"/api/v1/ui/sessions", token, "", "")
	r.Body.Close()
	for _, x := range []*browser{b, c} {
		if resp, _ := x.do("GET", "/api/v1/projects", "", nil); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("after revoke-all: %d", resp.StatusCode)
		}
	}

	// Absolute expiry and idle expiry, by moving the clock.
	ua := rt.uiAuth()
	real := ua.now
	t.Cleanup(func() { ua.now = real })
	d := signIn()
	ua.now = func() time.Time { return real().Add(defaultSessionTTL + time.Minute) }
	if resp, _ := d.do("GET", "/api/v1/projects", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired session: %d", resp.StatusCode)
	}
	ua.now = real
	e := signIn()
	ua.now = func() time.Time { return real().Add(sessionIdleTTL + time.Minute) }
	if resp, _ := e.do("GET", "/api/v1/projects", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("idle session: %d", resp.StatusCode)
	}
}

func TestPairingCodeExpires(t *testing.T) {
	rt, ts := newRemoteTestServer(t)
	code := pairingCode(t, ts, rt.Token())
	ua := rt.uiAuth()
	real := ua.now
	t.Cleanup(func() { ua.now = real })
	ua.now = func() time.Time { return real().Add(pairingCodeTTL + time.Second) }
	b := &browser{t: t, ts: ts}
	if resp, _ := b.login(code); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired code: %d, want 401", resp.StatusCode)
	}
}

// TestLoginFailureBudget: ten wrong guesses lock sign-in and cancel every
// outstanding code -- including the right one, which an attacker was
// guessing at.
func TestLoginFailureBudget(t *testing.T) {
	rt, ts := newRemoteTestServer(t)
	good := pairingCode(t, ts, rt.Token())
	b := &browser{t: t, ts: ts}
	for i := 0; i < loginFailureBudget; i++ {
		if resp, _ := b.login("AAAAA-AAAAA"); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("guess %d: %d", i, resp.StatusCode)
		}
	}
	if resp, _ := b.login(good); resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("after the budget: %d, want 429", resp.StatusCode)
	}
	ua := rt.uiAuth()
	real := ua.now
	t.Cleanup(func() { ua.now = real })
	ua.now = func() time.Time { return real().Add(loginFailureWindow + time.Second) }
	if resp, _ := b.login(good); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("the code outstanding during the attack must have been cancelled: %d", resp.StatusCode)
	}
}

func TestNormalizePairingCode(t *testing.T) {
	for in, want := range map[string]string{"ab1cd-efghj": "AB1CDEFGHJ", "O0 il": "0011", " K-M-N ": "KMN"} {
		if got := normalizePairingCode(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
	seen := map[string]bool{}
	for i := 0; i < 200; i++ {
		c, err := newPairingCode()
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(c, "ILOU") {
			t.Fatalf("code %q has an ambiguous letter", c)
		}
		if normalizePairingCode(c) != strings.ReplaceAll(c, "-", "") {
			t.Fatalf("code %q does not survive normalization", c)
		}
		seen[c] = true
	}
	if len(seen) < 199 {
		t.Fatalf("codes repeat: %d unique of 200", len(seen))
	}
}
