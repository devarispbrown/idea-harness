package daemon

import (
	"net"
	"net/http"
	"strings"
	"time"
)

// Sign-in rate limiting (docs/security-model.md "Remote access").
//
// The first version kept one daemon-wide failure count: ten wrong codes in
// fifteen minutes locked sign-in for everybody and cancelled every outstanding
// code. That bounded guessing, and it also handed a lock-out button to anyone
// who could reach the page -- one guesser on the tailnet could keep the owner
// out indefinitely, ten requests every fifteen minutes.
//
// Now there are two budgets:
//
//   - per client (loginClientBudget): a client that spends it is refused for
//     the rest of the window. Nobody else is affected and no code is
//     cancelled, so the owner, on another device, signs in as usual;
//   - global (loginGlobalCeiling): a backstop against a guesser with many
//     addresses. Spending it is the old behaviour -- everyone is locked out
//     for the window and every outstanding code is cancelled -- but it takes
//     ten times the failures, and on `tailscale serve` that many distinct
//     tailnet identities or addresses.
//
// Why the global ceiling is still safe at 100: a code is 50 bits (10 symbols
// of 5 bits each, newPairingCode) and lives ten minutes. With k codes
// outstanding, each guess hits one with probability k/2^50 (about k * 8.9e-16).
// The ceiling caps guesses at 100 per 15-minute window -- 9,600 a day,
// 3.5 million a year -- so a guesser running flat out for a year against
// k outstanding codes succeeds with probability about k * 3e-9. The
// per-client budget only moves where the wall is; the ceiling is what bounds
// the arithmetic.
//
// What a client IS (loginClientKey) is the part that must not be spoofable:
// a header a guesser can set freely would give them a fresh budget per
// request. So a proxy's headers are believed only for a request that came
// through a proxy this daemon was told about (trustedProxyClient).
const (
	loginClientBudget  = 10
	loginGlobalCeiling = 100
	loginFailureWindow = 15 * time.Minute
)

// loginLimiter is the failure bookkeeping. Guarded by uiAuth.mu.
//
// Memory is bounded without a sweep: a failure is recorded only while the
// global ceiling is unspent, so at most loginGlobalCeiling timestamps exist
// inside a window, spread over at most that many keys; older ones are pruned
// on every check.
type loginLimiter struct {
	perClient map[string][]time.Time
	global    []time.Time
}

// loginLock says why a sign-in is refused before its code is even looked at.
type loginLock int

const (
	loginOpen loginLock = iota
	loginLockedClient
	loginLockedGlobal
)

func pruneWindow(ts []time.Time, now time.Time) []time.Time {
	kept := ts[:0]
	for _, t := range ts {
		if now.Sub(t) < loginFailureWindow {
			kept = append(kept, t)
		}
	}
	return kept
}

// retryAfter is how long until the oldest failure in ts leaves the window --
// the moment the budget it filled has room again.
func retryAfter(ts []time.Time, now time.Time) time.Duration {
	if len(ts) == 0 {
		return 0
	}
	d := loginFailureWindow - now.Sub(ts[0])
	if d < time.Second {
		d = time.Second
	}
	return d
}

// check prunes the windows and reports whether key may try a code now, and
// if not, how long until it may.
func (l *loginLimiter) check(key string, now time.Time) (loginLock, time.Duration) {
	if l.perClient == nil {
		l.perClient = map[string][]time.Time{}
	}
	l.global = pruneWindow(l.global, now)
	for k, ts := range l.perClient {
		if ts = pruneWindow(ts, now); len(ts) == 0 {
			delete(l.perClient, k)
		} else {
			l.perClient[k] = ts
		}
	}
	if len(l.global) >= loginGlobalCeiling {
		return loginLockedGlobal, retryAfter(l.global, now)
	}
	if ts := l.perClient[key]; len(ts) >= loginClientBudget {
		return loginLockedClient, retryAfter(ts, now)
	}
	return loginOpen, 0
}

// fail records a wrong code from key and reports whether that spent the
// global ceiling -- the caller then cancels every outstanding code.
func (l *loginLimiter) fail(key string, now time.Time) (globalSpent bool) {
	if l.perClient == nil {
		l.perClient = map[string][]time.Time{}
	}
	l.global = append(l.global, now)
	l.perClient[key] = append(l.perClient[key], now)
	return len(l.global) == loginGlobalCeiling
}

// succeed forgets key's failures: the owner who mistyped twice and then got
// it right should not carry those two into the next sign-in. The global count
// is left alone -- it is evidence about everyone, not about this client.
func (l *loginLimiter) succeed(key string) { delete(l.perClient, key) }

// trustedProxyClient returns the client a trusted proxy says it is serving,
// and whether the request came through one.
//
// "Trusted" is the same line isLocalRequest draws, from the other side: the
// connection arrived on a loopback socket (so the peer is a process on this
// machine), and its Host is a name in daemon.allowed_hosts -- the name the
// operator configured a proxy to answer to. Only then are proxy headers
// believed. A request on the remote listener, or to a loopback Host, is
// judged by its peer address alone, whatever headers it carries: there is no
// proxy there whose word counts.
//
// Of the headers, Tailscale-User-Login wins -- `tailscale serve` sets it from
// the tailnet identity and drops any copy the client sent, so it names a
// person, not a device -- and otherwise the RIGHTMOST X-Forwarded-For entry:
// the address the proxy itself saw. The leftmost entry is whatever the client
// wrote before the proxy appended to it. One proxy hop is assumed; a chain of
// proxies collapses to the last one's peer, which over-shares a budget rather
// than handing out fresh ones.
func trustedProxyClient(r *http.Request, hosts hostPolicy) (string, bool) {
	if !loopbackSocket(r) || !hosts.extra[hostOnly(r.Host)] {
		return "", false
	}
	if login := strings.TrimSpace(r.Header.Get("Tailscale-User-Login")); login != "" {
		return "tailscale:" + strings.ToLower(truncateBytes(login, 128)), true
	}
	if ip := lastForwardedFor(r); ip != "" {
		return ip, true
	}
	return "", false
}

// lastForwardedFor is the rightmost X-Forwarded-For entry, if it is an IP.
func lastForwardedFor(r *http.Request) string {
	xff := r.Header.Values("X-Forwarded-For")
	if len(xff) == 0 {
		return ""
	}
	last := xff[len(xff)-1]
	if i := strings.LastIndexByte(last, ','); i >= 0 {
		last = last[i+1:]
	}
	if ip := net.ParseIP(strings.TrimSpace(last)); ip != nil {
		return ip.String()
	}
	return ""
}

// loopbackSocket reports whether the connection's PEER is loopback. Unlike
// isLocalRequest's local-address test, an in-process request (no connection)
// does not count: it has no proxy in front of it to trust.
func loopbackSocket(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// peerHost is the connection's peer address without its port.
func peerHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// loginClientKey is the identity a sign-in failure is charged to: the trusted
// proxy's client when there is one, the connection's peer otherwise. An IPv6
// address is charged by its /64, because a single host is usually handed a
// whole /64 and can pick a fresh address per request.
func loginClientKey(r *http.Request, hosts hostPolicy) string {
	addr, ok := trustedProxyClient(r, hosts)
	if ok && strings.HasPrefix(addr, "tailscale:") {
		return addr
	}
	if !ok {
		addr = peerHost(r)
	}
	ip := net.ParseIP(addr)
	if ip == nil {
		return "addr:" + addr
	}
	if ip.To4() == nil {
		return "ip:" + ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
	}
	return "ip:" + ip.String()
}
