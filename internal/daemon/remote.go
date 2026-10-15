package daemon

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Remote use of the web UI (docs/security-model.md "Remote access").
//
// The daemon always serves loopback, exactly as before: the token is injected
// into the page and every local client keeps working unchanged. Remote use is
// an ADDITIONAL, opt-in listener (daemon.listen), or a reverse proxy in front
// of loopback (`tailscale serve`, the recommended path) whose hostname is
// named in daemon.allowed_hosts. Either way a request that is not provably
// local never sees the token: the page it gets has none, and it signs in with
// a one-time pairing code for a session cookie instead (uisession.go).

// DaemonConfig is the daemon-wide configuration: <state root>/config.yaml,
// section `daemon:`. Daemon-wide rather than per project because there is one
// daemon, one listener and one token for every project it serves --
// harness.yaml is the wrong home for any of them.
type DaemonConfig struct {
	// Listen is an extra, non-loopback address to serve on ("100.101.1.2:8787").
	// Empty: loopback only.
	Listen string `yaml:"listen"`
	// TLSCert/TLSKey serve Listen over HTTPS. Both or neither.
	TLSCert string `yaml:"tls_cert"`
	TLSKey  string `yaml:"tls_key"`
	// AllowedHosts are extra hostnames the Host/Origin guard accepts -- the
	// name a browser uses to reach this daemon through a proxy
	// (`laptop.tailnet-1234.ts.net` under `tailscale serve`).
	AllowedHosts []string `yaml:"allowed_hosts"`
	// SessionTTL bounds a browser session's absolute lifetime. Default 30 days.
	SessionTTL Duration `yaml:"session_ttl"`
	// PushContact is the VAPID `sub` claim push services may use to reach
	// whoever runs this daemon (mailto: or https:). Default: the project page.
	PushContact string `yaml:"push_contact"`
}

// Duration is a time.Duration that unmarshals from "720h"-style YAML.
type Duration time.Duration

// UnmarshalYAML parses a Go duration string.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("session_ttl: %w", err)
	}
	*d = Duration(v)
	return nil
}

const (
	defaultSessionTTL  = 30 * 24 * time.Hour
	defaultPushContact = "https://github.com/devarispbrown/idea-harness"
)

// DaemonConfigPath is where the daemon config lives under a state root.
func DaemonConfigPath(stateRoot string) string { return filepath.Join(stateRoot, "config.yaml") }

// LoadDaemonConfig reads <stateRoot>/config.yaml. A missing file is the
// default config (loopback only); a malformed one is an error, because a
// typo in `listen` silently meaning "loopback only" would be confusing and a
// typo in `allowed_hosts` silently widening nothing is fine -- but neither
// should be guessed at.
func LoadDaemonConfig(stateRoot string) (DaemonConfig, error) {
	var wrapper struct {
		Daemon DaemonConfig `yaml:"daemon"`
	}
	b, err := os.ReadFile(DaemonConfigPath(stateRoot))
	if errors.Is(err, os.ErrNotExist) {
		return DaemonConfig{}, nil
	}
	if err != nil {
		return DaemonConfig{}, err
	}
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err := dec.Decode(&wrapper); err != nil && !errors.Is(err, io.EOF) {
		return DaemonConfig{}, fmt.Errorf("%s: %w", DaemonConfigPath(stateRoot), err)
	}
	return wrapper.Daemon, nil
}

func (c DaemonConfig) sessionTTL() time.Duration {
	if c.SessionTTL > 0 {
		return time.Duration(c.SessionTTL)
	}
	return defaultSessionTTL
}

func (c DaemonConfig) pushContact() string {
	if c.PushContact != "" {
		return c.PushContact
	}
	return defaultPushContact
}

// tailscaleV4 and tailscaleV6 are the ranges Tailscale assigns node addresses
// from: the CGNAT block and its ULA prefix.
var (
	_, tailscaleV4, _ = net.ParseCIDR("100.64.0.0/10")
	_, tailscaleV6, _ = net.ParseCIDR("fd7a:115c:a1e0::/48")
)

func isTailscaleIP(ip net.IP) bool { return tailscaleV4.Contains(ip) || tailscaleV6.Contains(ip) }

// ListenPlan is a validated daemon.listen.
type ListenPlan struct {
	Addr string
	TLS  *tls.Config
	// Warning is logged, loudly, at startup.
	Warning string
	// host is the listen IP, accepted by the Host guard.
	host string
}

// planRemoteListen validates daemon.listen against the remote-listen policy:
//
//   - the address is an IP literal: a hostname resolves to whatever it resolves
//     to today, and the policy below is about which interface is exposed;
//   - loopback is refused: it is already served, and a second loopback
//     listener would hand the token to whatever proxies it;
//   - with TLS any address is allowed, including all interfaces;
//   - without TLS only a Tailscale address (WireGuard already encrypts it) or a
//     private-network address is allowed, with a warning; a public, link-local
//     or unspecified (all interfaces) address is refused, because the session
//     cookie and every diff would cross it in clear text.
//
// Auth cannot be turned off, so "only with auth" is structural: a remote
// listener never serves the token and every API request on it needs a header
// token or a paired session.
func planRemoteListen(c DaemonConfig) (*ListenPlan, error) {
	if c.Listen == "" {
		if c.TLSCert != "" || c.TLSKey != "" {
			return nil, errors.New("daemon.tls_cert/tls_key are set but daemon.listen is not: TLS applies to the remote listener only (loopback stays plain HTTP)")
		}
		return nil, nil
	}
	host, port, err := net.SplitHostPort(c.Listen)
	if err != nil {
		return nil, fmt.Errorf("daemon.listen %q: want ip:port: %w", c.Listen, err)
	}
	if port == "" {
		return nil, fmt.Errorf("daemon.listen %q: a port is required", c.Listen)
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return nil, fmt.Errorf("daemon.listen %q: must be an IP address, not a hostname (the policy is about which interface is exposed)", c.Listen)
	}
	if ip.IsLoopback() {
		return nil, fmt.Errorf("daemon.listen %q is a loopback address: loopback is always served; daemon.listen is for a non-loopback interface", c.Listen)
	}
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return nil, errors.New("daemon.tls_cert and daemon.tls_key must be set together")
	}
	plan := &ListenPlan{Addr: c.Listen, host: ip.String()}
	if ip.IsUnspecified() {
		plan.host = ""
	}
	if c.TLSCert != "" {
		cert, err := tls.LoadX509KeyPair(c.TLSCert, c.TLSKey)
		if err != nil {
			return nil, fmt.Errorf("daemon.tls_cert/tls_key: %w", err)
		}
		plan.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		if ip.IsUnspecified() {
			plan.Warning = "daemon.listen binds every interface (over TLS): any network this machine joins can reach the sign-in page; prefer a specific address"
		}
		return plan, nil
	}
	switch {
	case isTailscaleIP(ip):
		plan.Warning = "daemon.listen " + c.Listen + " serves plain HTTP on a Tailscale address: WireGuard encrypts it, but every device on the tailnet can reach the sign-in page, and browsers will not install the app or allow push without HTTPS. `tailscale serve` (docs/configuration.md) gives you HTTPS with nothing to configure here"
	case ip.IsPrivate():
		plan.Warning = "daemon.listen " + c.Listen + " serves PLAIN HTTP on a private network: anyone on this network can read your session cookie and every diff in transit. Configure daemon.tls_cert/tls_key, or use Tailscale"
	case ip.IsUnspecified():
		return nil, fmt.Errorf("daemon.listen %q binds every interface without TLS: refused (set daemon.tls_cert/tls_key, or bind a Tailscale or private address)", c.Listen)
	default:
		return nil, fmt.Errorf("daemon.listen %q is not a Tailscale or private address: refused without TLS (set daemon.tls_cert/tls_key)", c.Listen)
	}
	return plan, nil
}

// hostPolicy is what the Host/Origin guard accepts beyond loopback.
type hostPolicy struct {
	extra map[string]bool // lower-cased hostnames / IPs
}

func newHostPolicy(c DaemonConfig, plan *ListenPlan) hostPolicy {
	p := hostPolicy{extra: map[string]bool{}}
	for _, h := range c.AllowedHosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if hp, _, err := net.SplitHostPort(h); err == nil {
			h = hp
		}
		h = strings.Trim(h, "[]")
		if h != "" {
			p.extra[h] = true
		}
	}
	if plan != nil && plan.host != "" {
		p.extra[strings.ToLower(plan.host)] = true
	}
	return p
}

func hostOnly(hostport string) string {
	h := hostport
	if hp, _, err := net.SplitHostPort(hostport); err == nil {
		h = hp
	}
	return strings.ToLower(strings.Trim(h, "[]"))
}

func (p hostPolicy) allowsHost(host string) bool {
	return isLoopbackHost(host) || p.extra[hostOnly(host)]
}

func (p hostPolicy) allowsOrigin(origin string) bool {
	if isLoopbackOrigin(origin) {
		return true
	}
	rest, ok := strings.CutPrefix(origin, "https://")
	if !ok {
		rest, ok = strings.CutPrefix(origin, "http://")
	}
	if !ok || strings.ContainsAny(rest, "/@?#") {
		return false
	}
	return p.extra[hostOnly(rest)]
}

// proxyHeaders are the headers a reverse proxy adds. Their presence on a
// loopback connection means the browser is somewhere else.
var proxyHeaders = []string{"X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto", "Forwarded", "X-Real-Ip", "Tailscale-User-Login"}

// isLocalRequest reports whether a request provably comes from a browser on
// this machine -- the only kind that is handed the API token.
//
// All three must hold: the connection arrived on a loopback socket (not the
// remote listener), the Host is a loopback name (a proxy that forwards a
// tailnet hostname fails this), and no proxy header is present (a proxy that
// rewrites Host to 127.0.0.1 still adds X-Forwarded-For; `tailscale serve`
// adds both it and Tailscale-User-Login). A proxy that strips all of them and
// rewrites Host is indistinguishable from a local browser -- documented as
// unsupported in security-model.md.
func isLocalRequest(r *http.Request) bool {
	if !isLoopbackHost(r.Host) {
		return false
	}
	for _, h := range proxyHeaders {
		if r.Header.Get(h) != "" {
			return false
		}
	}
	la, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		// An in-process call (httptest's ResponseRecorder, the integrations'
		// own handler) has no connection at all; it is local by construction.
		return true
	}
	tcp, ok := la.(*net.TCPAddr)
	return ok && tcp.IP.IsLoopback()
}

// requestIsHTTPS reports whether the browser reached us over HTTPS -- directly
// (the TLS listener) or through a TLS-terminating proxy. It only decides the
// cookie's Secure flag, so a spoofed X-Forwarded-Proto costs the spoofer a
// cookie their own browser will not send back over plain HTTP, nothing more.
func requestIsHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// configureRemote applies the daemon config to the runtime. Run calls it
// before the server is built; nothing changes it afterwards, so the fields it
// sets are read without a lock.
func (r *Runtime) configureRemote(c DaemonConfig, plan *ListenPlan) {
	r.remote = c
	r.hosts = newHostPolicy(c, plan)
	var urls []string
	for _, h := range c.AllowedHosts {
		if h = strings.TrimSpace(h); h != "" {
			// An allowed host is a proxy's name, and the proxies this is for
			// (`tailscale serve`, Caddy) terminate TLS.
			urls = append(urls, "https://"+h)
		}
	}
	if plan != nil && plan.host != "" {
		scheme := "http://"
		if plan.TLS != nil {
			scheme = "https://"
		}
		urls = append(urls, scheme+plan.Addr)
	}
	r.remoteBase = urls
}

// remoteURLs are the addresses another device can reach the UI at.
func (r *Runtime) remoteURLs() []string { return append([]string(nil), r.remoteBase...) }
