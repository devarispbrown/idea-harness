package tools

import (
	"net"
	"strings"
	"testing"
)

func TestIsPublic(t *testing.T) {
	public := []string{"8.8.8.8", "1.1.1.1", "151.101.1.69"}
	for _, s := range public {
		if !isPublic(net.ParseIP(s)) {
			t.Errorf("%s should be public", s)
		}
	}
	blocked := []string{
		"127.0.0.1", "::1",                 // loopback
		"10.0.0.1", "192.168.1.1", "172.16.0.1", // private
		"169.254.169.254", // metadata
		"169.254.1.1",     // link-local
		"100.64.0.1",      // CGNAT
		"0.0.0.0", "::",   // unspecified
		"224.0.0.1",       // multicast
		"fe80::1",         // link-local v6
		"fc00::1",         // private v6 (unique-local)
	}
	for _, s := range blocked {
		if isPublic(net.ParseIP(s)) {
			t.Errorf("%s should be blocked", s)
		}
	}
}

func TestStripHTML(t *testing.T) {
	in := `<html><head><style>.x{}</style><script>bad()</script></head><body><h1>Hello &amp; world</h1><p>Some&nbsp;text</p></body></html>`
	out := stripHTML(in)
	if out == "" {
		t.Fatal("expected non-empty text")
	}
	for _, bad := range []string{"<script>", "<style>", "bad()", "<h1>", "</p>"} {
		if strings.Contains(out, bad) {
			t.Errorf("stripped text should not contain %q: %q", bad, out)
		}
	}
}

// TestFetchJSONIsGuardedToo: an outcome metric's url source goes through the
// same dial guard -- a loopback/metadata address is refused, and a non-http
// scheme never reaches the client.
func TestFetchJSONIsGuardedToo(t *testing.T) {
	f := NewFetcher()
	for _, u := range []string{"http://127.0.0.1:9/stats.json", "http://169.254.169.254/latest/meta-data"} {
		if _, err := f.FetchJSON(t.Context(), u); err == nil {
			t.Errorf("%s should be refused", u)
		}
	}
	if _, err := f.FetchJSON(t.Context(), "file:///etc/passwd"); err == nil || !strings.Contains(err.Error(), "http(s)") {
		t.Errorf("file:// should be refused before any request, got %v", err)
	}
}

func TestFetchRefusesLoopback(t *testing.T) {
	f := NewFetcher()
	// Even if a server were listening, the dial guard rejects loopback first.
	if _, err := f.Fetch(t.Context(), "http://127.0.0.1:9"); err == nil {
		t.Fatal("expected fetch to loopback to be refused")
	}
}
