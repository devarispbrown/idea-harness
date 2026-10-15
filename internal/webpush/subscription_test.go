package webpush

import (
	"strings"
	"testing"
)

func TestSubscriptionValidate(t *testing.T) {
	_, _, good := newTestUA(t, "https://push.example.com/send/abc?token=secret")
	if err := good.Validate(); err != nil {
		t.Fatalf("good subscription rejected: %v", err)
	}

	// A point with valid length and prefix but not on the curve: take the
	// RFC user-agent key and perturb Y.
	off := mustB64(t, rfcUAPublic)
	off[64] ^= 0x01

	compressed := mustB64(t, rfcUAPublic)[:33]
	compressed[0] = 0x02

	mut := func(f func(*Subscription)) Subscription {
		s := good
		f(&s)
		return s
	}
	cases := map[string]Subscription{
		"http endpoint":     mut(func(s *Subscription) { s.Endpoint = "http://push.example.com/send/abc" }),
		"userinfo":          mut(func(s *Subscription) { s.Endpoint = "https://user:pw@push.example.com/x" }),
		"no host":           mut(func(s *Subscription) { s.Endpoint = "https:///x" }),
		"relative":          mut(func(s *Subscription) { s.Endpoint = "/push/x" }),
		"empty endpoint":    mut(func(s *Subscription) { s.Endpoint = "" }),
		"other scheme":      mut(func(s *Subscription) { s.Endpoint = "file:///etc/passwd" }),
		"opaque":            mut(func(s *Subscription) { s.Endpoint = "https:push.example.com" }),
		"short p256dh":      mut(func(s *Subscription) { s.P256dh = b64(make([]byte, 64)) }),
		"compressed p256dh": mut(func(s *Subscription) { s.P256dh = b64(compressed) }),
		"off-curve p256dh":  mut(func(s *Subscription) { s.P256dh = b64(off) }),
		"bad b64 p256dh":    mut(func(s *Subscription) { s.P256dh = "***" }),
		"short auth":        mut(func(s *Subscription) { s.Auth = b64(make([]byte, 15)) }),
		"long auth":         mut(func(s *Subscription) { s.Auth = b64(make([]byte, 17)) }),
		"empty auth":        mut(func(s *Subscription) { s.Auth = "" }),
	}
	for name, sub := range cases {
		t.Run(name, func(t *testing.T) {
			err := sub.Validate()
			if err == nil {
				t.Fatal("accepted")
			}
			// Errors may name the host but never the path, query or keys.
			for _, secret := range []string{"secret", "/send/abc", "pw", good.Auth, good.P256dh} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error %q leaks %q", err, secret)
				}
			}
		})
	}

	// Padded and standard-alphabet key spellings are accepted.
	padded := good
	padded.P256dh = strings.NewReplacer("-", "+", "_", "/").Replace(good.P256dh) + "="
	padded.Auth = good.Auth + "=="
	if err := padded.Validate(); err != nil {
		t.Fatalf("padded/std spelling rejected: %v", err)
	}

	// validate(true) is the AllowInsecure path: http becomes acceptable.
	insecure := mut(func(s *Subscription) { s.Endpoint = "http://127.0.0.1:1234/x" })
	if err := insecure.validate(true); err != nil {
		t.Fatalf("insecure validate: %v", err)
	}
}
