// Package tools implements executable capabilities (Tools, §49). It is
// strictly separated from Skills (knowledge) and Executors (coding runtimes).
package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Fetcher is the controlled research web tool (§46). It fetches http(s) URLs
// to PUBLIC addresses only, blocking SSRF into loopback/private/link-local/
// metadata networks (§15, §73). It is deterministic and never takes URLs from
// raw model output — callers pass validated, config-supplied URLs.
type Fetcher struct {
	client   *http.Client
	maxBytes int64
}

// ipResolver resolves a host to addresses. Injectable so the rebinding guard
// below can be tested: the defence is specifically about a resolver that answers
// differently on a second lookup, which cannot be exercised against the real DNS.
type ipResolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// dialFunc is the inner dialer. Injectable so a test can assert what address
// the guard actually connects to -- which is the whole property, and cannot be
// observed by counting resolver calls, because Go's dialer would resolve a
// hostname through the system resolver rather than the injected one.
type dialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// guardedDial returns a DialContext that resolves the host, refuses any
// non-public address, and then connects to the address it just checked.
func guardedDial(res ipResolver, dial dialFunc) func(context.Context, string, string) (net.Conn, error) {
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
			return nil, fmt.Errorf("tools: no addresses for %s", host)
		}
		for _, ip := range ips {
			if !isPublic(ip.IP) {
				return nil, fmt.Errorf("tools: refused non-public address %s", ip.IP)
			}
		}
		// Dial the address that was just checked, not the hostname.
		//
		// Passing addr here would hand the dialer the original host:port and let
		// it resolve a second time, so the IP validated above need not be the IP
		// connected to: a DNS server the URL's owner controls can answer public
		// on the first lookup and 127.0.0.1 or 169.254.169.254 on the second,
		// which is exactly the metadata endpoint this guard exists to block.
		// URLs reach here from harness.yaml's research.urls, so a shared or
		// cloned project can carry the hostile name.
		return dial(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
}

// NewFetcher builds a fetcher with SSRF-safe dialing and redirect limits.
func NewFetcher() *Fetcher { return newFetcherWithResolver(net.DefaultResolver) }

func newFetcherWithResolver(res ipResolver) *Fetcher {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{DialContext: guardedDial(res, dialer.DialContext)}
	return &Fetcher{
		client: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("tools: too many redirects")
				}
				if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
					return errors.New("tools: redirect to non-http scheme blocked")
				}
				return nil
			},
		},
		maxBytes: 2 << 20, // 2 MiB
	}
}

// Fetch retrieves url, extracts readable text, and returns it (truncated to the
// size cap). Only public http(s) addresses are reachable.
func (f *Fetcher) Fetch(ctx context.Context, url string) (string, error) {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return "", fmt.Errorf("tools: only http(s) URLs are supported")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "idea-harness-research/0.1")
	req.Header.Set("Accept", "text/html,text/plain")

	resp, err := f.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tools: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, f.maxBytes))
	if err != nil {
		return "", err
	}
	return truncate(stripHTML(string(body)), int(f.maxBytes)), nil
}

// isPublic reports whether an IP is routable public space. Rejects loopback,
// private (RFC1918), link-local unicast/multicast, multicast, unspecified, and
// cloud metadata (169.254.169.254).
func isPublic(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return false
	}
	// CGNAT 100.64.0.0/10.
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return false
	}
	return true
}

var (
	scriptStyleRe = regexp.MustCompile(`(?is)<(script|style|noscript)[^>]*>.*?</(script|style|noscript)>`)
	tagRe         = regexp.MustCompile(`(?s)<[^>]+>`)
	wsRe          = regexp.MustCompile(`[ \t\r\n]+`)
)

// stripHTML removes script/style blocks and tags, then collapses whitespace.
func stripHTML(s string) string {
	s = scriptStyleRe.ReplaceAllString(s, " ")
	s = tagRe.ReplaceAllString(s, " ")
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = wsRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
