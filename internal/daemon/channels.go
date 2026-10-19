package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
)

// telegramAPIBase is the Telegram Bot API origin; a var so tests point it at a
// fake server.
var telegramAPIBase = "https://api.telegram.org"

// telegramHub owns one poller per distinct bot token, mirroring the Slack
// SocketHub: two projects that name the same bot token must not each poll
// getUpdates, or they would split each other's updates. A token belongs to one
// project; a second project naming it logs a warning and does not poll.
type telegramHub struct {
	mu      sync.Mutex
	pollers map[string]*telegramPoller
}

// register attaches a project to the poller for botToken, starting it on first
// use. ctx is the daemon's lifetime.
func (h *telegramHub) register(ctx context.Context, rt *Runtime, botToken, projectID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.pollers == nil {
		h.pollers = map[string]*telegramPoller{}
	}
	if p, ok := h.pollers[botToken]; ok {
		if p.project != projectID {
			rt.log.Warn("telegram: this bot token is already polling for another project; a bot belongs to one project",
				"existing", p.project, "ignored", projectID)
		}
		return
	}
	p := &telegramPoller{rt: rt, botToken: botToken, project: projectID, log: rt.log}
	h.pollers[botToken] = p
	go p.run(ctx)
}

// telegramPoller long-polls getUpdates and turns each text message into a draft
// in its project.
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
		OK      bool             `json:"ok"`
		Result  []telegramUpdate `json:"result"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("telegram getUpdates: ok=false")
	}
	return out.Result, nil
}

// resolveTelegramToken reads the bot token from the environment or the project's
// .env, by the name the config gives.
func resolveTelegramToken(cfg config.TelegramChannelConfig, dotEnv map[string]string) string {
	return lookupEnv(cfg.BotTokenEnvName(), dotEnv)
}
