package daemon

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// TestTelegramPollerCapturesDrafts: the native poller turns a Telegram text
// message into a draft initiative in the configured project.
func TestTelegramPollerCapturesDrafts(t *testing.T) {
	rt, _, _, projectID := newIntegrationServer(t, "", "", nil)

	var served atomic.Int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/botfake-token/getUpdates" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if served.Add(1) == 1 {
			fmt.Fprint(w, `{"ok":true,"result":[{"update_id":1,"message":{"text":"A recipe app for home cooks"}}]}`)
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":[]}`)
	}))
	defer fake.Close()
	old := telegramAPIBase
	telegramAPIBase = fake.URL
	t.Cleanup(func() { telegramAPIBase = old })

	t.Setenv("TG_BOT_TOKEN", "fake-token")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt.startTelegram(ctx, TelegramChannelConfig{BotTokenEnv: "TG_BOT_TOKEN", Project: projectID})

	var captured string
	waitFor(t, "draft captured", func() bool {
		pr, err := rt.EnsureProject(context.Background(), projectID)
		if err != nil {
			return false
		}
		inis, err := pr.db.ListInitiatives(context.Background())
		if err != nil {
			return false
		}
		for _, ini := range inis {
			if ini.Status == domain.InitiativeDraft && ini.Title == "A recipe app for home cooks" {
				captured = ini.ID
				return true
			}
		}
		return false
	})
	if captured == "" {
		t.Fatal("no draft was captured")
	}
}

// TestTelegramUnconfiguredDoesNothing: no token env, no poller, no panic.
func TestTelegramUnconfiguredDoesNothing(t *testing.T) {
	rt, _, _, _ := newIntegrationServer(t, "", "", nil)
	t.Setenv("TG_BOT_TOKEN", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rt.startTelegram(ctx, TelegramChannelConfig{BotTokenEnv: "TG_BOT_TOKEN", Project: "p"})
	// If we got here without a panic and without an API call, it's correct.
}
