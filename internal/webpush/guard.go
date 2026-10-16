package webpush

import (
	"context"
	"fmt"
	"net"
)

// The endpoint URL comes from a browser, i.e. from whoever can call the
// subscribe API, so without a guard "send a push" is "make this server POST
// to any address it can reach": the cloud metadata service, a database admin
// port on localhost, the office LAN. This mirrors internal/tools' fetch
// guard (kept separate so this package stays self-contained).

// ipResolver is the slice of *net.Resolver the guard needs; tests substitute
// a fake to exercise the guard without DNS.
type ipResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// guardedDial returns a DialContext that resolves the host, refuses the
// connection if ANY resolved address is non-public, and then dials the
// addresses it just checked.
//
// Dialing the checked IPs rather than the hostname closes the DNS-rebinding
// hole: a resolver controlled by the endpoint's owner could otherwise answer
// with a public address for the check and 127.0.0.1 for the connect. Every
// checked address is tried in turn so multi-address push services keep
// their failover.
func guardedDial(res ipResolver, dial dialFunc) dialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := res.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("webpush: no addresses for %s", host)
		}
		for _, ip := range ips {
			if !isPublic(ip.IP) {
				return nil, fmt.Errorf("webpush: refused non-public address for %s", host)
			}
		}
		var lastErr error
		for _, ip := range ips {
			conn, err := dial(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		return nil, lastErr
	}
}

// isPublic reports whether ip is routable public address space. It rejects
// loopback, RFC 1918 / ULA private ranges, link-local (which covers the
// 169.254.169.254 metadata address), multicast, unspecified, and CGNAT
// 100.64.0.0/10 (used by some clouds for internal services).
func isPublic(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		if v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
			return false
		}
		// 0.0.0.0/8 ("this network") is not only the unspecified address;
		// Linux routes 0.x.y.z to the local host.
		if v4[0] == 0 {
			return false
		}
	}
	return true
}
