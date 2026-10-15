package slack

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/integrations"
)

// Block is one Block Kit block. A map rather than a typed tree: this package
// emits a handful of shapes, and Slack -- not a Go type -- is the schema.
type Block = map[string]any

// Action ids. The value of every button is an actionValue, JSON-encoded, so
// the interaction handler can route a click back to its project without any
// state of its own.
const (
	ActionApproveChangeSet      = "idea.cs.approve"
	ActionRejectChangeSet       = "idea.cs.reject"
	ActionAcceptRecommendations = "idea.judgment.accept_recommendations"
	ActionAnswerQuestion        = "idea.question.answer"
	ActionOpenUI                = "idea.open_ui"
	CallbackAnswerModal         = "idea.answer"
	answerBlockID               = "answer_block"
	answerInputID               = "answer_input"
)

// actionValue is a button's routing payload. Short keys: Slack caps a
// button value at 2000 characters.
type actionValue struct {
	Project    string `json:"p"`
	Initiative string `json:"i,omitempty"`
	ChangeSet  string `json:"cs,omitempty"`
	Question   string `json:"q,omitempty"`
}

func (v actionValue) encode() string {
	b, _ := json.Marshal(v)
	return string(b)
}

func decodeValue(s string) (actionValue, error) {
	var v actionValue
	err := json.Unmarshal([]byte(s), &v)
	return v, err
}

// escape makes dynamic text inert in Slack mrkdwn. Slack's own rule is that
// &, < and > must be escaped; the point here is security, not typography: an
// initiative title is often text from outside (a Linear issue, a pasted
// idea), and an unescaped "<!channel>" or "<https://evil|click me>" in it
// would ping everyone or forge a link inside a message our bot signs.
func escape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	return s
}

// clip bounds a dynamic string; Block Kit rejects a section over 3000 chars.
func clip(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

func section(text string) Block {
	return Block{"type": "section", "text": Block{"type": "mrkdwn", "text": clip(text, 2900)}}
}

func contextBlock(text string) Block {
	return Block{"type": "context", "elements": []Block{{"type": "mrkdwn", "text": clip(text, 1900)}}}
}

func button(text, actionID, value, style string) Block {
	b := Block{"type": "button", "text": Block{"type": "plain_text", "text": text}, "action_id": actionID}
	if value != "" {
		b["value"] = value
	}
	if style != "" {
		b["style"] = style
	}
	return b
}

func linkButton(text, url string) Block {
	return Block{"type": "button", "text": Block{"type": "plain_text", "text": text}, "url": url, "action_id": ActionOpenUI}
}

func withConfirm(b Block, title, text, confirm string) Block {
	b["confirm"] = Block{
		"title":   Block{"type": "plain_text", "text": title},
		"text":    Block{"type": "mrkdwn", "text": text},
		"confirm": Block{"type": "plain_text", "text": confirm},
		"deny":    Block{"type": "plain_text", "text": "Cancel"},
	}
	return b
}

// judgmentReasonText explains a JudgmentReason in one line.
func judgmentReasonText(reason string) string {
	switch domain.JudgmentReason(reason) {
	case domain.JudgmentPRDReview:
		return "the PRD review did not converge"
	case domain.JudgmentPlanReview:
		return "the execution-plan review raised blocking findings"
	case domain.JudgmentPlanDependencies:
		return "the execution plan has unresolved task dependencies"
	case domain.JudgmentVerification:
		return "a requirement failed verification after completion"
	case "":
		return "a human decision is needed"
	}
	return reason
}

// headline renders the one-line summary of ev used both as the notification
// text (what a push notification or screen reader shows) and as the first
// section of the message.
func headline(ev integrations.Event) string {
	title := "*" + escape(clip(ev.InitiativeTitle, 150)) + "*"
	cs := escape(ev.ChangeSetRef)
	if ev.TaskCode != "" {
		cs += " (" + escape(ev.TaskCode) + ": " + escape(clip(ev.TaskTitle, 80)) + ")"
	}
	switch ev.Kind {
	case integrations.KindInitiativeCreated:
		return ":bulb: New initiative " + title
	case integrations.KindNeedsJudgment:
		return ":raising_hand: " + title + " needs your judgment: " + escape(judgmentReasonText(ev.Reason))
	case integrations.KindPRDApproved:
		return ":white_check_mark: PRD approved for " + title
	case integrations.KindPlanApproved:
		return ":clipboard: Execution plan approved for " + title
	case integrations.KindChangeSetCreated:
		return ":hammer_and_wrench: " + cs + " started"
	case integrations.KindChangeSetReady:
		return ":mag: " + cs + " is ready for review"
	case integrations.KindChangeSetApproved:
		return ":+1: " + cs + " approved"
	case integrations.KindChangeSetRejected:
		return ":no_entry_sign: " + cs + " rejected"
	case integrations.KindChangeSetApplied:
		return ":package: " + cs + " applied to the working tree"
	case integrations.KindChangeSetReverted:
		return ":rewind: " + cs + " reverted"
	case integrations.KindVerifyPassed:
		return ":tada: " + title + " passed verification and is complete"
	case integrations.KindVerifyFailed:
		msg := ":x: " + escape(ev.RequirementCode) + " failed verification"
		if ev.Reason != "" {
			msg += ": " + escape(clip(ev.Reason, 300))
		}
		return msg
	case integrations.KindRunFailed:
		msg := ":warning: A run failed for " + title
		if ev.Reason != "" {
			msg += ": " + escape(clip(ev.Reason, 300))
		}
		return msg
	case integrations.KindTaskStatusChanged:
		return fmt.Sprintf(":arrow_right: %s: %s is now %s", escape(ev.TaskCode), escape(clip(ev.TaskTitle, 80)), escape(ev.Status))
	case integrations.KindOutcomeDrifted:
		what := "off track"
		if ev.Status == "regressed" {
			what = "regressed after a change set landed"
		}
		msg := ":chart_with_downwards_trend: " + escape(ev.MetricCode) + " " + escape(clip(ev.MetricName, 80)) + " is " + what
		if ev.ObjectiveCode != "" {
			msg += " (" + escape(ev.ObjectiveCode) + ")"
		}
		return msg + " for " + title
	}
	return escape(string(ev.Kind))
}

// uiPhase picks the UI page a deep link for ev should open.
func uiPhase(ev integrations.Event) string {
	switch ev.Kind {
	case integrations.KindNeedsJudgment:
		switch domain.JudgmentReason(ev.Reason) {
		case domain.JudgmentPRDReview:
			return "define"
		case domain.JudgmentVerification:
			return "verify"
		}
		return "plan"
	case integrations.KindPRDApproved:
		return "define"
	case integrations.KindPlanApproved, integrations.KindTaskStatusChanged:
		return "plan"
	case integrations.KindVerifyPassed, integrations.KindVerifyFailed:
		return "verify"
	case integrations.KindInitiativeCreated, integrations.KindRunFailed:
		return "explore"
	case integrations.KindOutcomeDrifted:
		return "health"
	}
	return "build"
}

// JudgmentItems are the open items attached to a needs_judgment message.
type JudgmentItems struct {
	Questions                   []domain.Question
	DecisionsWithRecommendation int
}

// maxQuestionButtons bounds the per-question Answer buttons; Slack allows 50
// blocks per message, and a wall of buttons helps nobody.
const maxQuestionButtons = 5

// buildBlocks renders ev as Block Kit. uiURL is the deep link ("" for none);
// mention is the already-formatted mention prefix for needs_judgment.
func buildBlocks(ev integrations.Event, uiURL, mention string, items JudgmentItems) []Block {
	text := headline(ev)
	if ev.Kind == integrations.KindNeedsJudgment && mention != "" {
		text = mention + " " + text
	}
	blocks := []Block{section(text)}
	val := actionValue{Project: ev.ProjectID, Initiative: ev.InitiativeID}

	var actions []Block
	switch ev.Kind {
	case integrations.KindChangeSetReady:
		v := val
		v.ChangeSet = ev.ChangeSetID
		actions = append(actions,
			withConfirm(button("Approve", ActionApproveChangeSet, v.encode(), "primary"),
				"Approve "+ev.ChangeSetRef+"?", "Approves the change set exactly as `idea review "+ev.ChangeSetRef+" approve` would. Applying it is a separate step.", "Approve"),
			withConfirm(button("Reject", ActionRejectChangeSet, v.encode(), "danger"),
				"Reject "+ev.ChangeSetRef+"?", "Rejects the change set exactly as `idea review "+ev.ChangeSetRef+" reject` would.", "Reject"),
		)
	case integrations.KindNeedsJudgment:
		for i, q := range items.Questions {
			if i == maxQuestionButtons {
				blocks = append(blocks, contextBlock(fmt.Sprintf("…and %d more open question(s) in the UI.", len(items.Questions)-maxQuestionButtons)))
				break
			}
			v := val
			v.Question = q.ID
			b := section(":grey_question: " + escape(clip(q.Question, 500)))
			b["accessory"] = button("Answer", ActionAnswerQuestion, v.encode(), "")
			blocks = append(blocks, b)
		}
		if items.DecisionsWithRecommendation > 0 {
			actions = append(actions, withConfirm(
				button(fmt.Sprintf("Accept %d recommendation(s)", items.DecisionsWithRecommendation), ActionAcceptRecommendations, val.encode(), "primary"),
				"Accept recommendations?", "Resolves every open decision with its own recommendation, exactly as `idea accept-recommendations` would.", "Accept"))
		}
	}
	if uiURL != "" {
		actions = append(actions, linkButton("Open in idea-harness", uiURL))
	}
	if len(actions) > 0 {
		blocks = append(blocks, Block{"type": "actions", "elements": actions})
	}
	ctx := "initiative `" + escape(ev.InitiativeID) + "`"
	if uiURL != "" {
		// Said, because a teammate clicking it on their own laptop gets a
		// connection refused and deserves to know why.
		ctx += " · links open the local UI, so only on the machine running the daemon"
	}
	blocks = append(blocks, contextBlock(ctx))
	return blocks
}

// plainText strips the mrkdwn emphasis from a headline for the notification
// fallback text.
func plainText(s string) string {
	return strings.NewReplacer("*", "", "`", "").Replace(s)
}
