package slack

import (
	"context"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// CheckResult is what `idea integrations test slack` reports.
type CheckResult struct {
	Team       string `json:"team"`
	BotUser    string `json:"bot_user"`
	MessageTS  string `json:"message_ts"`
	SocketMode string `json:"socket_mode"`
}

// Check validates the bot token (auth.test), posts a test message to the
// configured channel -- which is what proves the bot is IN the channel, the
// most common setup miss -- and, when socket_mode is on, that the app-level
// token can open a Socket Mode connection.
func Check(ctx context.Context, bot *Client, cfg config.SlackConfig, appToken string) (CheckResult, error) {
	var res CheckResult
	id, err := bot.AuthTest(ctx)
	if err != nil {
		return res, err
	}
	res.Team, res.BotUser = id.Team, id.User
	ts, err := bot.PostMessage(ctx, Message{
		Channel: cfg.Channel,
		Text:    ":wave: idea-harness test message: notifications for this project will post here.",
	})
	if err != nil {
		return res, err
	}
	res.MessageTS = ts
	switch {
	case !cfg.SocketModeEnabled():
		res.SocketMode = "off (integrations.slack.socket_mode: false)"
	case appToken == "":
		res.SocketMode = "skipped: no " + cfg.AppTokenEnvName() + " (buttons and /idea need it)"
	default:
		app := &Client{APIBase: bot.APIBase, HTTP: bot.HTTP, AppToken: appToken}
		if _, err := app.OpenConnection(ctx); err != nil {
			return res, err
		}
		res.SocketMode = "ok"
	}
	return res, nil
}
