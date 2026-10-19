package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// ChannelsConfig is the daemon-wide channel configuration: <state root>/config.yaml,
// section `channels:`. Credentials are env-var NAMES, never values, so the file
// stays committable.
type ChannelsConfig struct {
	Telegram TelegramChannelConfig `yaml:"telegram"`
	// Slack and email follow.
}

// TelegramChannelConfig wires a Telegram bot as a capture channel: the daemon
// long-polls the bot and creates a draft initiative for each text message.
type TelegramChannelConfig struct {
	// BotTokenEnv names the environment variable holding the bot token from
	// @BotFather.
	BotTokenEnv string `yaml:"bot_token_env"`
	// Project is the project id captured briefs land in as drafts.
	Project string `yaml:"project"`
}

// LoadChannelsConfig reads the `channels:` section of config.yaml. A missing
// file or section is the zero config.
func LoadChannelsConfig(stateRoot string) (ChannelsConfig, error) {
	var wrapper struct {
		Channels ChannelsConfig `yaml:"channels"`
	}
	b, err := os.ReadFile(filepath.Join(stateRoot, "config.yaml"))
	if errors.Is(err, os.ErrNotExist) {
		return ChannelsConfig{}, nil
	}
	if err != nil {
		return ChannelsConfig{}, err
	}
	if err := yaml.Unmarshal(b, &wrapper); err != nil {
		return ChannelsConfig{}, fmt.Errorf("%s: %w", filepath.Join(stateRoot, "config.yaml"), err)
	}
	return wrapper.Channels, nil
}

// telegramAPIBase is the Telegram Bot API origin; a var so tests point it at a
// fake server.
var telegramAPIBase = "https://api.telegram.org"

// startTelegram runs the Telegram poller until ctx is cancelled, when the config
// is non-empty and the token is resolvable. It never returns an error: a broken
// bot token is logged and retried forever, since the user fixes it in their env.
func (r *Runtime) startTelegram(ctx context.Context, cfg TelegramChannelConfig) {
	if cfg.BotTokenEnv == "" || cfg.Project == "" {
		return
	}
	token := os.Getenv(cfg.BotTokenEnv)
	if token == "" {
		r.log.Warn("channels.telegram configured but the token env is unset; the bot is not polled",
			"env", cfg.BotTokenEnv)
		return
	}
	p := &telegramPoller{rt: r, botToken: token, project: cfg.Project, log: r.log}
	go p.run(ctx)
}

// telegramPoller long-polls getUpdates and turns each text message into a draft.
type telegramPoller struct {
	rt       *Runtime
	botToken string
	project  string
	log      *slog.Logger
	offset   int64
}

func (p *telegramPoller) run(ctx context.Context) {
	client := &http.Client{Timeout: 35 * time.Second} // Telegram long-poll is 30s
	for {
		if ctx.Err() != nil {
			return
		}
		updates, err := p.getUpdates(ctx, client)
		if err != nil {
			if ctx.Err() == nil {
				p.log.Warn("telegram: getUpdates failed; retrying", "err", err)
			}
			time.Sleep(2 * time.Second)
			continue
		}
		for _, u := range updates {
			if u.UpdateID >= p.offset {
				p.offset = u.UpdateID + 1
			}
			text := strings.TrimSpace(u.Message.Text)
			if text == "" {
				continue
			}
			if _, err := p.rt.createDraft(ctx, p.project, text); err != nil {
				p.log.Warn("telegram: capture failed", "err", err)
				continue
			}
			p.log.Info("telegram: captured a draft", "project", p.project)
		}
	}
}

type telegramUpdate struct {
	UpdateID int64 `json:"update_id"`
	Message  struct {
		Text string `json:"text"`
	} `json:"message"`
}

func (p *telegramPoller) getUpdates(ctx context.Context, client *http.Client) ([]telegramUpdate, error) {
	url := fmt.Sprintf("%s/bot%s/getUpdates?timeout=30&offset=%d", telegramAPIBase, p.botToken, p.offset)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("telegram answered %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	var out struct {
		OK     bool            `json:"ok"`
		Result []telegramUpdate `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("telegram getUpdates: ok=false")
	}
	return out.Result, nil
}
