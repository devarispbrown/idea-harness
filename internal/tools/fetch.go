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

// NewFetcher builds a fetcher with SSRF-safe dialing and redirect limits.
func NewFetcher() *Fetcher {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, _, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
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
			return dialer.DialContext(ctx, network, addr)
		},
	}
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
