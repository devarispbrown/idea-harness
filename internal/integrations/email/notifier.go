// Package email is the outbound email notifier: a bus Subscriber that sends
// each wanted domain event as a plain-text message over SMTP. Notifications
// only — email is never a capture channel.
package email

import (
	"context"
	"fmt"
	"net/smtp"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/integrations"
)

// SubscriberName keys email's outbox rows and cursor.
const SubscriberName = "email"

// Notifier delivers events over SMTP.
type Notifier struct {
	Cfg    config.EmailConfig
	// Password is the resolved SMTP password (empty for no auth).
	Password string
	// UIBase is the deep-link root; empty disables links.
	UIBase string
	// Redact scrubs known secrets from outbound text.
	Redact func(string) string

	wants map[integrations.Kind]bool
}

// Name implements integrations.Subscriber.
func (n *Notifier) Name() string { return SubscriberName }

// Wants implements integrations.Subscriber.
func (n *Notifier) Wants(ev integrations.Event) bool {
	if n.wants == nil {
		n.wants = map[integrations.Kind]bool{}
		for _, k := range n.Cfg.EffectiveEvents() {
			n.wants[integrations.Kind(k)] = true
		}
	}
	return n.wants[ev.Kind]
}

// Deliver implements integrations.Subscriber.
func (n *Notifier) Deliver(ctx context.Context, ev integrations.Event) error {
	if n.Redact != nil {
		ev.InitiativeTitle = n.Redact(ev.InitiativeTitle)
		ev.Reason = n.Redact(ev.Reason)
		ev.TaskTitle = n.Redact(ev.TaskTitle)
	}
	return n.send(headline(ev), body(ev, n.uiURL(ev)))
}

func (n *Notifier) uiURL(ev integrations.Event) string {
	if n.UIBase == "" || ev.InitiativeID == "" {
		return ""
	}
	return strings.TrimRight(n.UIBase, "/") + "/#/i/" + ev.InitiativeID
}

func (n *Notifier) send(subject, text string) error {
	addr := fmt.Sprintf("%s:%d", n.Cfg.SMTPHost, n.Cfg.EffectivePort())
	var auth smtp.Auth
	if n.Cfg.SMTPUser != "" {
		auth = smtp.PlainAuth("", n.Cfg.SMTPUser, n.Password, n.Cfg.SMTPHost)
	}
	msg := []byte(
		"To: " + n.Cfg.To + "\r\n" +
			"From: " + n.Cfg.From + "\r\n" +
			"Subject: " + subject + "\r\n" +
			"MIME-Version: 1.0\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n" +
			"\r\n" +
			text)
	return smtp.SendMail(addr, auth, n.Cfg.From, []string{n.Cfg.To}, msg)
}

// headline is a one-line subject for an event.
func headline(ev integrations.Event) string {
	name := ev.InitiativeTitle
	if name == "" {
		name = ev.ChangeSetRef
	}
	if name == "" {
		name = "idea-harness"
	}
	return fmt.Sprintf("idea-harness: %s — %s", humanKind(ev.Kind), name)
}

func body(ev integrations.Event, url string) string {
	var b strings.Builder
	b.WriteString(humanKind(ev.Kind))
	if ev.InitiativeTitle != "" {
		b.WriteString("\nInitiative: " + ev.InitiativeTitle)
	}
	if ev.ChangeSetRef != "" {
		b.WriteString("\nChange set: " + ev.ChangeSetRef)
	}
	if ev.Reason != "" {
		b.WriteString("\nReason: " + ev.Reason)
	}
	if url != "" {
		b.WriteString("\nOpen: " + url)
	}
	return b.String()
}

func humanKind(k integrations.Kind) string {
	switch k {
	case integrations.KindNeedsJudgment:
		return "Needs your judgment"
	case integrations.KindChangeSetReady:
		return "Change set ready for review"
	case integrations.KindVerifyFailed:
		return "Verification failed"
	case integrations.KindOutcomeDrifted:
		return "Outcome metric drifted"
	default:
		return strings.ReplaceAll(strings.ReplaceAll(string(k), "_", " "), ".", " ")
	}
}
