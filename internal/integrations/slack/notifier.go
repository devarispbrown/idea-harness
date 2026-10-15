package slack

import (
	"context"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/integrations"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// SubscriberName keys Slack's outbox rows and cursor.
const SubscriberName = "slack"

// linkKindThread is the integration_links kind for an initiative's thread:
// local id = initiative id, remote id = the root message ts, remote key =
// the channel it lives in.
const linkKindThread = "thread"

// Notifier is the outbound half: a bus Subscriber that posts each wanted
// event to the configured channel, threaded per initiative.
type Notifier struct {
	Cfg    config.SlackConfig
	Client *Client
	DB     *store.DB
	// UIBase is the deep-link root, e.g. http://127.0.0.1:8787. Empty
	// disables links.
	UIBase string
	// Redact scrubs known secrets from outbound text. Slack is egress: a
	// failure reason can quote an error that quotes a key.
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
//
// The first event for an initiative becomes the root of its thread; every
// later one replies in that thread, and a needs_judgment reply is also
// broadcast to the channel (with the configured mentions) because a human
// decision buried in a thread is a decision nobody makes.
//
// Delivery is at-least-once. Slack's chat.postMessage has no idempotency
// key, so a daemon killed between Slack accepting a post and the outbox
// recording it re-posts that one message on restart; the outbox guarantees
// that window is the only one.
func (n *Notifier) Deliver(ctx context.Context, ev integrations.Event) error {
	ev = n.redactEvent(ev)
	var items JudgmentItems
	if ev.Kind == integrations.KindNeedsJudgment {
		items = n.judgmentItems(ctx, ev.InitiativeID)
	}
	blocks := buildBlocks(ev, n.uiURL(ev), n.mention(ev), items)
	msg := Message{Channel: n.Cfg.Channel, Text: plainText(headline(ev)), Blocks: blocks}

	var thread *store.IntegrationLink
	if ev.InitiativeID != "" {
		l, err := n.DB.GetIntegrationLink(ctx, SubscriberName, linkKindThread, ev.InitiativeID)
		if err != nil {
			return err
		}
		// A thread in a channel this project no longer posts to is not a
		// thread to reply in: start a fresh one where the messages now go.
		if l != nil && l.RemoteKey == n.Cfg.Channel {
			thread = l
		}
	}
	if thread != nil {
		msg.ThreadTS = thread.RemoteID
		msg.ReplyBroadcast = ev.Kind == integrations.KindNeedsJudgment
	}
	ts, err := n.Client.PostMessage(ctx, msg)
	if err != nil {
		return err
	}
	if thread == nil && ev.InitiativeID != "" {
		return n.DB.PutIntegrationLink(ctx, store.IntegrationLink{
			Integration: SubscriberName, Kind: linkKindThread, LocalID: ev.InitiativeID,
			RemoteID: ts, RemoteKey: n.Cfg.Channel,
		})
	}
	return nil
}

func (n *Notifier) redactEvent(ev integrations.Event) integrations.Event {
	if n.Redact == nil {
		return ev
	}
	ev.InitiativeTitle = n.Redact(ev.InitiativeTitle)
	ev.Reason = n.Redact(ev.Reason)
	ev.TaskTitle = n.Redact(ev.TaskTitle)
	return ev
}

func (n *Notifier) uiURL(ev integrations.Event) string {
	if n.UIBase == "" || ev.InitiativeID == "" {
		return ""
	}
	return strings.TrimRight(n.UIBase, "/") + "/#/i/" + ev.InitiativeID + "/" + uiPhase(ev)
}

func (n *Notifier) mention(ev integrations.Event) string {
	if ev.Kind != integrations.KindNeedsJudgment {
		return ""
	}
	var parts []string
	for _, m := range n.Cfg.MentionOnJudgment {
		switch m {
		case "here", "channel":
			parts = append(parts, "<!"+m+">")
		default:
			parts = append(parts, "<@"+m+">")
		}
	}
	return strings.Join(parts, " ")
}

// judgmentItems lists what a human can act on from the message. Best-effort:
// a read failure here degrades the message to a link, it does not fail the
// delivery.
func (n *Notifier) judgmentItems(ctx context.Context, initiativeID string) JudgmentItems {
	var items JudgmentItems
	if qs, err := n.DB.ListQuestions(ctx, initiativeID); err == nil {
		for _, q := range qs {
			if q.Status == domain.QuestionOpen {
				if n.Redact != nil {
					q.Question = n.Redact(q.Question)
				}
				items.Questions = append(items.Questions, q)
			}
		}
	}
	if ds, err := n.DB.ListDecisions(ctx, initiativeID); err == nil {
		for _, d := range ds {
			if d.Status == domain.DecisionOpen && strings.TrimSpace(d.Recommendation) != "" {
				items.DecisionsWithRecommendation++
			}
		}
	}
	return items
}
