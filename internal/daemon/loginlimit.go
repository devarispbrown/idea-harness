package daemon

import (
	"net"
	"net/http"
	"strings"
	"time"
)

// Sign-in rate limiting (docs/security-model.md "Remote access").
//
// History. The first version kept one daemon-wide failure count: ten wrong
// codes in fifteen minutes locked sign-in for everybody and cancelled every
// outstanding code. The second gave each client its own budget and kept a
// daemon-wide ceiling of 100 as the backstop -- which still handed a
// lock-out button to anyone with ten addresses: spend the ceiling and sign-in
// was paused for everyone, the owner included, for fifteen minutes.
//
// Now nothing is locked daemon-wide. There are two budgets, and neither
// refuses anyone but the guesser:
//
//   - per client (loginClientBudget): a client that spends it is refused for
//     the rest of the window. Nobody else is affected. This slows one guesser
//     down; it is not what bounds guessing.
//   - per code (pairingCodeFailureBudget): every wrong code is a guess at
//     every code outstanding at that moment, so each of them counts it
//     (store.RedeemPairingCode). A code that has absorbed 100 wrong guesses is
//     burned: it can never be redeemed. The endpoint stays open, and the owner
//     mints a fresh code with `idea ui pair` on the daemon's machine, which
//     starts at zero and works at once. `idea ui pair`, `idea ui sessions` and
//     Settings say a code was burned, so the owner knows someone is probing.
//
// The guessing bound. A code is 50 bits (10 symbols of 5 bits, newPairingCode)
// and faces at most pairingCodeFailureBudget = 100 wrong guesses in its life,
// so any one code is guessed with probability at most 100/2^50, about
// 8.9e-14. Over n codes ever minted it is at most n * 8.9e-14: a hundred
// thousand codes is below 1e-8. That is the same per-code exposure as the old
// global ceiling (100 guesses per 15-minute window, and a code lives ten
// minutes, so at most 100 guesses reached any one code), with no time-based
// lock-out, and the counts live in global.db, so a restart does not reset
// them (the old window was in memory). Guesses made while no code is
// outstanding are worth nothing and cost the owner nothing.
//
// Burning as denial of service. An attacker cannot see when a code exists, so
// to burn the owner's code they must spray continuously: 100 failures within
// however long the owner takes to type the code. Each client gets 10 per 15
// minutes, so a steady sprayer with M clients lands M/1.5 a minute; burning a
// code inside its ten-minute life takes about 15 clients, and inside the
// minute an owner usually needs, about 150. Every burn is surfaced.
//
// What a client IS (loginClientKey) is the part that must not be spoofable:
// a header a guesser can set freely would give them a fresh budget per
// request. So a proxy's headers are believed only for a request that came
// through a proxy this daemon was told about (trustedProxyClient). A request
// from this machine itself (isLocalRequest) is never charged or refused: it
// already holds the API token, and can mint codes.
const (
	loginClientBudget        = 10
	loginFailureWindow       = 15 * time.Minute
	pairingCodeFailureBudget = 100
	// loginMaxClients bounds the per-client map. With no daemon-wide ceiling
	// nothing else bounds the number of keys, and a client's budget only
	// slows it down -- the per-code budget is the bound -- so evicting the
	// stalest client when full costs nothing that matters.
	loginMaxClients = 4096
)

// loginLimiter is the per-client failure bookkeeping. Guarded by uiAuth.mu.
// In memory on purpose: it is a speed limit, not the guessing bound, which is
// per code and in global.db.
type loginLimiter struct {
	perClient map[string][]time.Time
}

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
func (l *loginLimiter) check(key string, now time.Time) (ok bool, wait time.Duration) {
	if l.perClient == nil {
		l.perClient = map[string][]time.Time{}
	}
	for k, ts := range l.perClient {
		if ts = pruneWindow(ts, now); len(ts) == 0 {
			delete(l.perClient, k)
		} else {
			l.perClient[k] = ts
		}
	}
	if ts := l.perClient[key]; len(ts) >= loginClientBudget {
		return false, retryAfter(ts, now)
	}
	return true, 0
}

// fail records a wrong code from key.
func (l *loginLimiter) fail(key string, now time.Time) {
	if l.perClient == nil {
		l.perClient = map[string][]time.Time{}
	}
	if _, known := l.perClient[key]; !known && len(l.perClient) >= loginMaxClients {
		stalest, at := "", now
		for k, ts := range l.perClient {
			if last := ts[len(ts)-1]; !last.After(at) {
				stalest, at = k, last
			}
		}
		delete(l.perClient, stalest)
	}
	l.perClient[key] = append(l.perClient[key], now)
}

// succeed forgets key's failures: the owner who mistyped twice and then got
// it right should not carry those two into the next sign-in.
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
