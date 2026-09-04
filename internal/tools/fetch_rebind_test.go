package tools

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

var errStub = errors.New("stub dialer")

// rebindResolver answers public on the first lookup and loopback on every one
// after — the DNS-rebinding shape this guard exists to stop.
type rebindResolver struct{ calls int }

func (r *rebindResolver) LookupIPAddr(_ context.Context, _ string) ([]net.IPAddr, error) {
	r.calls++
	if r.calls == 1 {
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}, nil // TEST-NET-3, public
	}
	return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
}

// TestDialUsesTheValidatedAddress pins the rebinding fix.
//
// The guard used to resolve a host, check every returned IP was public, then
// dial the original host:port — which Go's dialer resolves a second time. A
// resolver under the URL owner's control could answer public first and
// 127.0.0.1 or 169.254.169.254 second, reaching exactly the loopback and
// metadata endpoints the check exists to block. research.urls comes from
// harness.yaml, so a cloned project can carry the hostile name.
//
// The assertion is the lookup count, not the connection outcome: with the fix
// there is exactly one resolution and the dial targets the address that was
// checked, so a second answer is never consulted. Nothing listens on TEST-NET-3,
// so the dial fails either way — which is why counting is the honest test.
func TestDialUsesTheValidatedAddress(t *testing.T) {
	res := &rebindResolver{}
	var dialled string
	dial := guardedDial(res, func(_ context.Context, _, addr string) (net.Conn, error) {
		dialled = addr
		return nil, errStub
	})

	if _, err := dial(context.Background(), "tcp", "attacker-controlled.example:80"); err != errStub {
		t.Fatalf("expected to reach the dialer, got %v", err)
	}
	host, _, err := net.SplitHostPort(dialled)
	if err != nil {
		t.Fatalf("dialled a malformed address %q: %v", dialled, err)
	}
	if net.ParseIP(host) == nil {
		t.Fatalf("dialled the hostname %q, so the dialer resolves again and the "+
			"validated address is not the one connected to", dialled)
	}
	if host != "203.0.113.10" {
		t.Errorf("dialled %s, want the validated 203.0.113.10", host)
	}
}

// TestNonPublicAddressIsRefused keeps the plain case working: a host that
// resolves to loopback never gets dialled at all.
func TestNonPublicAddressIsRefused(t *testing.T) {
	dial := guardedDial(staticResolver{net.ParseIP("127.0.0.1")}, nil)
	if _, err := dial(context.Background(), "tcp", "localhost.example:80"); err == nil {
		t.Fatal("a loopback address was accepted")
	} else if !strings.Contains(err.Error(), "refused non-public") {
		t.Errorf("wrong rejection reason: %v", err)
	}
}

type staticResolver struct{ ip net.IP }

func (s staticResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: s.ip}}, nil
}

// TestDialFallsBackToLaterValidatedAddresses guards the reliability half.
//
// Pinning to ips[0] closed the rebinding hole and silently removed the
// multi-address fallback that dialing a hostname had given for free: a
// round-robin or multi-region endpoint whose first address is down would fail
// outright, with the reachable one never tried.
func TestDialFallsBackToLaterValidatedAddresses(t *testing.T) {
	res := staticResolver2{[]net.IP{net.ParseIP("203.0.113.10"), net.ParseIP("203.0.113.11")}}
	var tried []string
	dial := guardedDial(res, func(_ context.Context, _, addr string) (net.Conn, error) {
		tried = append(tried, addr)
		if addr == "203.0.113.11:80" {
			return nil, nil // the second address answers
		}
		return nil, errStub
	})
	if _, err := dial(context.Background(), "tcp", "multi.example:80"); err != nil {
		t.Fatalf("dial failed while a validated address was still reachable: %v", err)
	}
	if len(tried) != 2 {
		t.Errorf("tried %v; both validated addresses must be attempted", tried)
	}
}

type staticResolver2 struct{ ips []net.IP }

func (s staticResolver2) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	out := make([]net.IPAddr, 0, len(s.ips))
	for _, ip := range s.ips {
		out = append(out, net.IPAddr{IP: ip})
	}
	return out, nil
}
