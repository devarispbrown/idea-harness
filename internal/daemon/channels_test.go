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
// message into a draft initiative in the registered project.
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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := &telegramHub{}
	hub.register(ctx, rt, "fake-token", projectID)

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

// TestTelegramHubOnePollerPerToken: a second project naming the same bot token
// does not start a second poller (which would split getUpdates).
func TestTelegramHubOnePollerPerToken(t *testing.T) {
	rt, _, _, _ := newIntegrationServer(t, "", "", nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hub := &telegramHub{}
	hub.register(ctx, rt, "shared-token", "p1")
	hub.register(ctx, rt, "shared-token", "p2")
	hub.mu.Lock()
	n := len(hub.pollers)
	hub.mu.Unlock()
	if n != 1 {
		t.Fatalf("pollers = %d, want 1 (one per token)", n)
	}
}
