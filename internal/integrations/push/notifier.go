// Package push delivers integration-bus events to browsers as Web Push
// notifications (RFC 8030 delivery, RFC 8291 encryption, RFC 8292 VAPID; the
// wire work is internal/webpush).
//
// Each browser subscription is its own bus subscriber ("webpush:<id>"), not
// one subscriber fanning out to many browsers. That is what gives a push the
// same delivery semantics as a Slack post: its own cursor, its own outbox
// rows keyed by an idempotency key, its own retries and dead letters. A
// fan-out subscriber would have had to retry the whole event when one phone
// was offline -- re-notifying every browser that had already shown it.
package push

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/devarispbrown/idea-harness/internal/integrations"
	"github.com/devarispbrown/idea-harness/internal/webpush"
)

// Kinds are the event kinds a browser can ask to be notified about: the four
// that mean "a person is needed", which is what a notification on a phone is
// for. Pinned against integrations.AllKinds by a test.
var Kinds = []integrations.Kind{
	integrations.KindNeedsJudgment,
	integrations.KindChangeSetReady,
	integrations.KindVerifyFailed,
	integrations.KindOutcomeDrifted,
}

// ValidKind reports whether k is one of Kinds.
func ValidKind(k string) bool {
	for _, x := range Kinds {
		if string(x) == k {
			return true
		}
	}
	return false
}

// Notifier is the bus subscriber for one browser subscription.
type Notifier struct {
	SubscriptionID string
	Subscription   webpush.Subscription
	Events         map[integrations.Kind]bool
	Sender         *webpush.Sender
	// Redact masks credentials in every string that leaves the machine. The
	// payload is end-to-end encrypted to the browser, but it is still shown
	// on a lock screen.
	Redact func(string) string
	// OnResult records each attempt's outcome ("" on success).
	OnResult func(ctx context.Context, errText string)
	// OnGone is called when the push service says the subscription no
	// longer exists; the caller deletes it.
	OnGone func(ctx context.Context)
}

// Name implements integrations.Subscriber.
func (n *Notifier) Name() string { return "webpush:" + n.SubscriptionID }

// Wants implements integrations.Subscriber.
func (n *Notifier) Wants(ev integrations.Event) bool { return n.Events[ev.Kind] }

// Payload is the JSON the service worker receives (web/public/sw.js).
type Payload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	// URL is a same-origin path + hash the notification opens. Relative, so
	// it lands on whichever address the browser subscribed from.
	URL  string `json:"url"`
	Tag  string `json:"tag"`
	Kind string `json:"kind"`
}

// BuildPayload renders ev as a notification.
func BuildPayload(ev integrations.Event, redact func(string) string) Payload {
	if redact == nil {
		redact = func(s string) string { return s }
	}
	title := ev.InitiativeTitle
	if title == "" {
		title = "an initiative"
	}
	base := "/#/p/" + url.PathEscape(ev.ProjectID)
	ini := base + "/i/" + url.PathEscape(ev.InitiativeID)
	p := Payload{Kind: string(ev.Kind), URL: base + "/queue", Tag: string(ev.Kind) + ":" + ev.InitiativeID}
	switch ev.Kind {
	case integrations.KindNeedsJudgment:
		p.Title = "Needs you: " + title
		p.Body = "Waiting on your judgment"
		if ev.Reason != "" {
			p.Body += " (" + strings.ReplaceAll(ev.Reason, "_", " ") + ")"
		}
		p.Body += "."
		p.URL = base + "/queue"
	case integrations.KindChangeSetReady:
		ref := ev.ChangeSetRef
		if ref == "" {
			ref = "A change set"
		}
		p.Title = "Ready for review: " + ref
		p.Body = title
		if ev.TaskCode != "" {
			p.Body += " · " + ev.TaskCode
			if ev.TaskTitle != "" {
				p.Body += " " + ev.TaskTitle
			}
		}
		p.URL = ini + "/build"
		p.Tag = string(ev.Kind) + ":" + ev.ChangeSetID
	case integrations.KindVerifyFailed:
		p.Title = "Verification failed: " + title
		p.Body = strings.TrimSpace(ev.RequirementCode + " " + ev.Reason)
		p.URL = ini + "/verify"
	case integrations.KindOutcomeDrifted:
		p.Title = "Outcome drifting: " + title
		p.Body = strings.TrimSpace(fmt.Sprintf("%s %s is %s", ev.MetricCode, ev.MetricName, strings.ReplaceAll(ev.Status, "_", " ")))
		p.URL = ini + "/outcomes"
	default:
		p.Title = title
		p.Body = string(ev.Kind)
	}
	p.Title = clip(redact(p.Title), 120)
	p.Body = clip(redact(p.Body), 400)
	return p
}

// Deliver implements integrations.Subscriber, mapping push-service answers
// onto the dispatcher's retry semantics: a dead subscription is permanent
// (and deleted), a 429's Retry-After is honoured, a 4xx that retrying cannot
// fix is permanent, anything else is retried with backoff.
func (n *Notifier) Deliver(ctx context.Context, ev integrations.Event) error {
	body, err := json.Marshal(BuildPayload(ev, n.Redact))
	if err != nil {
		return integrations.Permanent(err)
	}
	urgency := "high"
	if ev.Kind == integrations.KindOutcomeDrifted {
		urgency = "normal"
	}
	err = n.Sender.Send(ctx, n.Subscription, webpush.Message{Payload: body, Urgency: urgency})
	n.record(ctx, err)
	if err == nil {
		return nil
	}
	if webpush.IsGone(err) {
		if n.OnGone != nil {
			n.OnGone(ctx)
		}
		return integrations.Permanent(err)
	}
	var se *webpush.StatusError
	if errors.As(err, &se) {
		if se.Permanent {
			return integrations.Permanent(err)
		}
		if se.RetryAfter > 0 {
			return &integrations.RetryAfterError{Err: err, After: se.RetryAfter}
		}
	}
	return err
}

func (n *Notifier) record(ctx context.Context, err error) {
	if n.OnResult == nil {
		return
	}
	if err == nil {
		n.OnResult(ctx, "")
		return
	}
	msg := err.Error()
	if n.Redact != nil {
		msg = n.Redact(msg)
	}
	n.OnResult(ctx, msg)
}

// clip shortens s to at most n bytes on a rune boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s + "…"
}
