package config

import (
	"fmt"
	"regexp"
	"strings"
)

// IntegrationsConfig is harness.yaml's `integrations` section. Each
// integration is enabled by its section being present -- there is no separate
// `enabled` flag to forget -- and disabled by removing it.
type IntegrationsConfig struct {
	Slack   *SlackConfig          `json:"slack,omitempty" yaml:"slack,omitempty"`
	Linear  *LinearConfig         `json:"linear,omitempty" yaml:"linear,omitempty"`
	Email   *EmailConfig          `json:"email,omitempty" yaml:"email,omitempty"`
	Telegram *TelegramChannelConfig `json:"telegram,omitempty" yaml:"telegram,omitempty"`
}

// Configured reports whether any integration is configured.
func (c IntegrationsConfig) Configured() bool { return c.Slack != nil || c.Linear != nil || c.Email != nil || c.Telegram != nil }

// IntegrationEventKinds is the vocabulary of domain events the integration bus
// publishes (internal/integrations: Kind). It lives here, in the leaf config
// package, so `integrations.slack.events` can be validated at load time; the
// integrations package pins its own constants to this list in a test.
var IntegrationEventKinds = []string{
	"initiative.created",
	"initiative.needs_judgment",
	"review.prd_approved",
	"review.plan_approved",
	"change_set.created",
	"change_set.ready_for_review",
	"change_set.approved",
	"change_set.rejected",
	"change_set.applied",
	"change_set.reverted",
	"verify.passed",
	"verify.failed",
	"run.failed",
	"task.status_changed",
	"outcome.drifted",
	"initiative.budget_exceeded",
	"eval.regressed",
}

// DefaultSlackEvents is what Slack posts when `events` is unset: every kind a
// human might act on or want to know about, minus the per-task and
// "a change set started" chatter that would bury them.
var DefaultSlackEvents = []string{
	"initiative.created",
	"initiative.needs_judgment",
	"review.prd_approved",
	"review.plan_approved",
	"change_set.ready_for_review",
	"change_set.approved",
	"change_set.rejected",
	"change_set.applied",
	"change_set.reverted",
	"verify.passed",
	"verify.failed",
	"run.failed",
	"outcome.drifted",
	"initiative.budget_exceeded",
	"eval.regressed",
}

// SlackConfig is `integrations.slack`.
type SlackConfig struct {
	// Channel is the channel ID (C0123...) notifications post to. An ID rather
	// than a #name because slash commands are routed back to a project by the
	// channel ID Slack reports, and a name would never match it.
	Channel string `json:"channel" yaml:"channel"`
	// Events narrows what is posted (IntegrationEventKinds). Empty means
	// DefaultSlackEvents.
	Events []string `json:"events,omitempty" yaml:"events,omitempty"`
	// MentionOnJudgment is who to mention when an initiative parks on
	// needs_human_judgment: Slack user IDs (U0123...), or "here"/"channel".
	MentionOnJudgment []string `json:"mention_on_judgment,omitempty" yaml:"mention_on_judgment,omitempty"`
	// AllowedUsers are the Slack user IDs whose button clicks and slash
	// commands are acted on. Empty (the default) means none are: Slack is
	// notify-only until someone is named here. A Slack workspace's membership
	// is not this project's approver list, so it is never assumed to be.
	AllowedUsers []string `json:"allowed_users,omitempty" yaml:"allowed_users,omitempty"`
	// SocketMode turns on the interactive half (buttons, /idea) over a Socket
	// Mode websocket, which needs SLACK_APP_TOKEN. Defaults to true; it is
	// skipped, with a status warning, when no app token is available.
	SocketMode *bool `json:"socket_mode,omitempty" yaml:"socket_mode,omitempty"`
	// BotTokenEnv / AppTokenEnv name the environment variables holding the
	// xoxb- bot token and xapp- app-level token. Default SLACK_BOT_TOKEN and
	// SLACK_APP_TOKEN.
	BotTokenEnv string `json:"bot_token_env,omitempty" yaml:"bot_token_env,omitempty"`
	AppTokenEnv string `json:"app_token_env,omitempty" yaml:"app_token_env,omitempty"`
	// UIBaseURL is prefixed to deep links. Defaults to the daemon's own
	// address -- which is loopback, so the links only open on this machine.
	UIBaseURL string `json:"ui_base_url,omitempty" yaml:"ui_base_url,omitempty"`
}

// EffectiveEvents returns Events, or DefaultSlackEvents when unset.
func (s SlackConfig) EffectiveEvents() []string {
	if len(s.Events) == 0 {
		return DefaultSlackEvents
	}
	return s.Events
}

// EmailConfig is `integrations.email`: outbound notifications over SMTP. Like
// Slack, it is notifications-only — email is never a capture channel. The
// password is an env-var NAME, never a value.
type EmailConfig struct {
	// To is the recipient address (one address; a distribution list is fine).
	To string `json:"to" yaml:"to"`
	// From is the From address.
	From string `json:"from" yaml:"from"`
	// SMTPHost / SMTPPort are the submission endpoint (default port 587).
	SMTPHost string `json:"smtp_host" yaml:"smtp_host"`
	SMTPPort int    `json:"smtp_port,omitempty" yaml:"smtp_port,omitempty"`
	// SMTPUser is the login; empty means unauthenticated.
	SMTPUser string `json:"smtp_user,omitempty" yaml:"smtp_user,omitempty"`
	// SMTPPasswordEnv names the environment variable holding the password.
	SMTPPasswordEnv string `json:"smtp_password_env,omitempty" yaml:"smtp_password_env,omitempty"`
	// Events narrows what is emailed (IntegrationEventKinds). Empty means
	// DefaultSlackEvents.
	Events []string `json:"events,omitempty" yaml:"events,omitempty"`
	// UIBaseURL is prefixed to deep links, like Slack's.
	UIBaseURL string `json:"ui_base_url,omitempty" yaml:"ui_base_url,omitempty"`
}

// EffectiveEvents returns Events, or DefaultSlackEvents when unset.
func (e EmailConfig) EffectiveEvents() []string {
	if len(e.Events) == 0 {
		return DefaultSlackEvents
	}
	return e.Events
}

// EffectivePort returns SMTPPort, or 587 when unset.
func (e EmailConfig) EffectivePort() int {
	if e.SMTPPort == 0 {
		return 587
	}
	return e.SMTPPort
}

// SMTPPasswordEnvName returns the password's environment variable name.
func (e EmailConfig) SMTPPasswordEnvName() string { return orDefault(e.SMTPPasswordEnv, "SMTP_PASSWORD") }

// TelegramChannelConfig is `integrations.telegram`: the native capture bot. The
// daemon long-polls the bot and turns each text message into a draft initiative
// in THIS project, so the project is implicit — no `project` field.
type TelegramChannelConfig struct {
	// BotTokenEnv names the environment variable holding the @BotFather token.
	BotTokenEnv string `json:"bot_token_env" yaml:"bot_token_env"`
}

// BotTokenEnvName returns the bot token's environment variable name.
func (t TelegramChannelConfig) BotTokenEnvName() string { return orDefault(t.BotTokenEnv, "TELEGRAM_BOT_TOKEN") }

// SocketModeEnabled reports the effective socket_mode (default true).
func (s SlackConfig) SocketModeEnabled() bool { return s.SocketMode == nil || *s.SocketMode }

// BotTokenEnvName returns the bot token's environment variable name.
func (s SlackConfig) BotTokenEnvName() string { return orDefault(s.BotTokenEnv, "SLACK_BOT_TOKEN") }

// AppTokenEnvName returns the app-level token's environment variable name.
func (s SlackConfig) AppTokenEnvName() string { return orDefault(s.AppTokenEnv, "SLACK_APP_TOKEN") }

// LinearStateMapping maps the harness's task lifecycle onto the names of the
// team's Linear workflow states.
type LinearStateMapping struct {
	Todo       string `json:"todo,omitempty" yaml:"todo,omitempty"`
	InProgress string `json:"in_progress,omitempty" yaml:"in_progress,omitempty"`
	InReview   string `json:"in_review,omitempty" yaml:"in_review,omitempty"`
	Done       string `json:"done,omitempty" yaml:"done,omitempty"`
	Canceled   string `json:"canceled,omitempty" yaml:"canceled,omitempty"`
}

// LinearConfig is `integrations.linear`.
type LinearConfig struct {
	// TeamKey is the Linear team key (the "ENG" in ENG-123).
	TeamKey string `json:"team_key" yaml:"team_key"`
	// Project optionally files created issues under a Linear project (by name).
	Project string `json:"project,omitempty" yaml:"project,omitempty"`
	// InboundLabel is the label that turns a Linear issue into an initiative.
	// Default "idea".
	InboundLabel string `json:"inbound_label,omitempty" yaml:"inbound_label,omitempty"`
	// Inbound turns the label poll on or off. Default true.
	Inbound *bool `json:"inbound,omitempty" yaml:"inbound,omitempty"`
	// PollIntervalSeconds is how often Linear is polled. Default 60, minimum 15
	// (Linear's API is rate limited per key, and nothing here is urgent).
	PollIntervalSeconds int `json:"poll_interval_seconds,omitempty" yaml:"poll_interval_seconds,omitempty"`
	// StateMapping overrides the workflow state names. Unset entries default
	// to Todo, In Progress, In Review, Done and Canceled.
	StateMapping LinearStateMapping `json:"state_mapping,omitempty" yaml:"state_mapping,omitempty"`
	// APIKeyEnv names the environment variable holding the Linear API key.
	// Default LINEAR_API_KEY.
	APIKeyEnv string `json:"api_key_env,omitempty" yaml:"api_key_env,omitempty"`
}

// MinLinearPollSeconds is the floor on poll_interval_seconds.
const MinLinearPollSeconds = 15

// EffectiveInboundLabel returns InboundLabel or "idea".
func (l LinearConfig) EffectiveInboundLabel() string { return orDefault(l.InboundLabel, "idea") }

// InboundEnabled reports the effective inbound flag (default true).
func (l LinearConfig) InboundEnabled() bool { return l.Inbound == nil || *l.Inbound }

// EffectivePollSeconds returns the poll interval with its default applied.
func (l LinearConfig) EffectivePollSeconds() int {
	if l.PollIntervalSeconds <= 0 {
		return 60
	}
	return l.PollIntervalSeconds
}

// APIKeyEnvName returns the API key's environment variable name.
func (l LinearConfig) APIKeyEnvName() string { return orDefault(l.APIKeyEnv, "LINEAR_API_KEY") }

// EffectiveStates returns the state mapping with defaults applied.
func (l LinearConfig) EffectiveStates() LinearStateMapping {
	m := l.StateMapping
	return LinearStateMapping{
		Todo:       orDefault(m.Todo, "Todo"),
		InProgress: orDefault(m.InProgress, "In Progress"),
		InReview:   orDefault(m.InReview, "In Review"),
		Done:       orDefault(m.Done, "Done"),
		Canceled:   orDefault(m.Canceled, "Canceled"),
	}
}

func orDefault(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}

var (
	slackUserIDRe = regexp.MustCompile(`^[UW][A-Z0-9]{2,}$`)
	envNameRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// validate checks what the JSON Schema cannot express: cross-field rules and
// the shape of Slack IDs. The schema already rejects unknown keys and bad
// event names.
func (c IntegrationsConfig) validate() error {
	if s := c.Slack; s != nil {
		if strings.TrimSpace(s.Channel) == "" {
			return fmt.Errorf("config: integrations.slack.channel is required")
		}
		for _, u := range s.AllowedUsers {
			if !slackUserIDRe.MatchString(u) {
				return fmt.Errorf("config: integrations.slack.allowed_users entry %q is not a Slack user ID (U… or W…)", u)
			}
		}
		for _, m := range s.MentionOnJudgment {
			if m != "here" && m != "channel" && !slackUserIDRe.MatchString(m) {
				return fmt.Errorf("config: integrations.slack.mention_on_judgment entry %q must be a Slack user ID, \"here\" or \"channel\"", m)
			}
		}
		for _, name := range []string{s.BotTokenEnv, s.AppTokenEnv} {
			if name != "" && !envNameRe.MatchString(name) {
				return fmt.Errorf("config: integrations.slack token env name %q is not an environment variable name", name)
			}
		}
	}
	if l := c.Linear; l != nil {
		if strings.TrimSpace(l.TeamKey) == "" {
			return fmt.Errorf("config: integrations.linear.team_key is required")
		}
		if l.PollIntervalSeconds != 0 && l.PollIntervalSeconds < MinLinearPollSeconds {
			return fmt.Errorf("config: integrations.linear.poll_interval_seconds must be >= %d", MinLinearPollSeconds)
		}
		if l.APIKeyEnv != "" && !envNameRe.MatchString(l.APIKeyEnv) {
			return fmt.Errorf("config: integrations.linear.api_key_env %q is not an environment variable name", l.APIKeyEnv)
		}
	}
	return nil
}
