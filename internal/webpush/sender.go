package webpush

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	defaultTTL = 24 * time.Hour
	// maxErrorBody bounds how much of a push service's error response is
	// kept. Enough to read a JSON error reason; not enough for a hostile or
	// broken service to balloon memory or logs.
	maxErrorBody = 512
)

// Sender delivers Web Push messages. The zero value is not usable: Keys and
// Subject are required.
type Sender struct {
	Keys    *Keys
	Subject string // VAPID "sub": mailto: or https: contact for the operator.
	// HTTP is the client used for delivery. nil selects a built-in client
	// with a 30s timeout that refuses non-public addresses and never follows
	// redirects. A caller-supplied client is used as is, so it carries the
	// caller's own SSRF and redirect policy.
	HTTP *http.Client
	// AllowInsecure permits http:// endpoints and, when HTTP is nil, skips
	// the public-address dial check. It exists for tests against
	// httptest.Server and must never be set in production.
	AllowInsecure bool
	Now           func() time.Time
}

// Message is one push to deliver.
type Message struct {
	Payload []byte
	// TTL is how long the push service should hold the message for an
	// offline device. 0 means 24h. Sub-second precision is truncated.
	TTL time.Duration
	// Urgency is "", "very-low", "low", "normal" or "high" (RFC 8030 5.3).
	Urgency string
	// Topic, when set, lets a newer message replace an undelivered older one
	// with the same topic (RFC 8030 5.4): at most 32 base64url characters.
	Topic string
}

// GoneError means the push service says the subscription no longer exists
// (404 or 410). It will never succeed again; the caller should delete it.
type GoneError struct {
	Host   string
	Status int
}

func (e *GoneError) Error() string {
	return fmt.Sprintf("webpush: subscription gone at %s (status %d)", e.Host, e.Status)
}

// StatusError is any other non-2xx response. Permanent separates "this
// request will never succeed as sent" (bad payload, bad VAPID, too large)
// from "try again later" (rate limiting, push-service outage).
type StatusError struct {
	Host       string
	Status     int
	Body       string        // first 512 bytes of the response body
	RetryAfter time.Duration // from Retry-After, 0 if absent or unparseable
	Permanent  bool
}

func (e *StatusError) Error() string {
	kind := "transient"
	if e.Permanent {
		kind = "permanent"
	}
	msg := fmt.Sprintf("webpush: %s returned status %d (%s)", e.Host, e.Status, kind)
	if e.RetryAfter > 0 {
		msg += fmt.Sprintf(", retry after %s", e.RetryAfter)
	}
	return msg
}

// IsGone reports whether err (or anything it wraps) is a *GoneError.
func IsGone(err error) bool {
	var g *GoneError
	return errors.As(err, &g)
}

// ValidateSubscription is Subscription.Validate under this sender's own
// scheme policy, so a caller accepting subscriptions (the daemon's POST
// handler) refuses exactly what Send would later refuse.
func (s *Sender) ValidateSubscription(sub Subscription) error { return sub.validate(s.AllowInsecure) }

// EndpointHost is the endpoint's host alone -- what may be shown or logged
// about a subscription. The path is a capability and is never shown.
func EndpointHost(endpoint string) string { return endpointHost(endpoint) }

// Send encrypts msg for sub and POSTs it to the subscription's endpoint.
// It returns nil on any 2xx (push services answer 201 Created).
//
// Errors are typed for retry decisions: *GoneError (delete the
// subscription), *StatusError (see Permanent and RetryAfter), or a wrapped
// transport error, which is always transient.
func (s *Sender) Send(ctx context.Context, sub Subscription, msg Message) error {
	if s.Keys == nil {
		return errors.New("webpush: sender has no VAPID keys")
	}
	if err := sub.validate(s.AllowInsecure); err != nil {
		return err
	}
	headers, err := msg.headers()
	if err != nil {
		return err
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	auth, err := s.Keys.VAPIDAuthorization(sub.Endpoint, s.Subject, now(), 0)
	if err != nil {
		return err
	}
	body, err := Encrypt(msg.Payload, sub)
	if err != nil {
		return err
	}

	host := endpointHost(sub.Endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webpush: build request for %s", host)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", auth)

	resp, err := s.client().Do(req)
	if err != nil {
		return sanitizeTransportError(host, err)
	}
	defer resp.Body.Close()
	errBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	// Drain a bounded amount more so the connection can be reused without
	// letting a hostile server stream forever into us.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone {
		return &GoneError{Host: host, Status: resp.StatusCode}
	}
	se := &StatusError{
		Host:       host,
		Status:     resp.StatusCode,
		Body:       string(errBody),
		RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), now()),
	}
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusRequestEntityTooLarge:
		// Malformed request, VAPID rejected (wrong key or expired token
		// under our control), or payload too big: resending the same bytes
		// cannot succeed.
		se.Permanent = true
	}
	return se
}

// sanitizeTransportError strips the request URL out of net/http's error.
// *url.Error renders the full endpoint, whose path and query are the
// subscription's bearer capability; the host is all a log line needs. The
// underlying error stays wrapped so context cancellation and timeouts are
// still detectable with errors.Is / errors.As.
func sanitizeTransportError(host string, err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("webpush: post to %s: %w", host, err)
}

// headers validates msg's delivery options and renders them as headers.
func (m Message) headers() (map[string]string, error) {
	h := map[string]string{}
	ttl := m.TTL
	if ttl == 0 {
		ttl = defaultTTL
	}
	if ttl < 0 {
		return nil, errors.New("webpush: TTL must not be negative")
	}
	h["TTL"] = strconv.FormatInt(int64(ttl/time.Second), 10)

	switch m.Urgency {
	case "":
	case "very-low", "low", "normal", "high":
		h["Urgency"] = m.Urgency
	default:
		return nil, fmt.Errorf("webpush: invalid urgency %q", m.Urgency)
	}

	if m.Topic != "" {
		if len(m.Topic) > 32 || strings.TrimLeft(m.Topic, base64URLAlphabet) != "" {
			return nil, errors.New("webpush: topic must be at most 32 base64url characters")
		}
		h["Topic"] = m.Topic
	}
	return h, nil
}

const base64URLAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_"

// parseRetryAfter accepts both RFC 9110 forms: delta-seconds or an
// HTTP-date. Unparseable or past values yield 0, which callers treat as "use
// your own backoff" rather than "retry immediately".
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if secs, err := strconv.ParseInt(v, 10, 64); err == nil {
		if secs <= 0 {
			return 0
		}
		// Cap absurd values so the multiplication cannot overflow.
		if secs > int64(7*24*time.Hour/time.Second) {
			secs = int64(7 * 24 * time.Hour / time.Second)
		}
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// The default clients are package-level so every Sender shares one
// connection pool (and Sender stays a plain copyable struct).
var (
	guardedClient  = newClient(net.DefaultResolver, true)
	insecureClient = newClient(net.DefaultResolver, false)
)

func (s *Sender) client() *http.Client {
	if s.HTTP != nil {
		return s.HTTP
	}
	if s.AllowInsecure {
		return insecureClient
	}
	return guardedClient
}

// newClient builds the default delivery client. Redirects are never
// followed: push services do not redirect, so a 3xx means a misconfigured
// or hostile endpoint, and following it would let a stored subscription
// steer our POST (with a valid VAPID token) anywhere, including past the
// dial guard's intent via a different host.
func newClient(res ipResolver, guard bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// The environment proxy would make the proxy, not the push service, the
	// address the guard checks; push delivery goes direct.
	transport.Proxy = nil
	if guard {
		transport.DialContext = guardedDial(res, dialer.DialContext)
	} else {
		transport.DialContext = dialer.DialContext
	}
	return &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
