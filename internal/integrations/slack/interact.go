package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/integrations"
)

// Route is one project's registration on a Socket Mode connection.
type Route struct {
	ProjectID string
	Cfg       config.SlackConfig
	// Bot is the project's bot-token client, used for views.open.
	Bot     *Client
	Actions integrations.Actions
}

// SocketHub owns one Socket Mode connection per distinct app-level token and
// routes each interaction to the project it belongs to.
//
// Per token, not per project, because Slack load-balances a Socket Mode app's
// frames across ALL of its open connections: two projects each opening their
// own connection with the same app token would each receive a random half of
// the other's button clicks. One connection per token, with the project id
// carried in every button's value, is the only shape where every click
// reaches the project it was for.
type SocketHub struct {
	// APIBase and HTTP configure the Web API client used for
	// apps.connections.open (tests).
	APIBase string
	HTTP    *http.Client
	Log     *slog.Logger
	// ResponseURLAllowed vets an interaction's response_url before anything
	// is POSTed to it. Default: only https://hooks.slack.com/.
	ResponseURLAllowed func(string) bool
	// Parent is the lifetime every connection derives from.
	Parent context.Context

	mu    sync.Mutex
	conns map[string]*hubConn // by app token
}

type hubConn struct {
	client *SocketClient
	cancel context.CancelFunc
	done   chan struct{}
	routes map[string]Route
}

func (h *SocketHub) log() *slog.Logger {
	if h.Log == nil {
		return slog.Default()
	}
	return h.Log
}

// Register attaches a project to the connection for appToken, opening that
// connection if it is the first project to use it. Re-registering a project
// replaces its route (a config reload).
func (h *SocketHub) Register(appToken string, r Route) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conns == nil {
		h.conns = map[string]*hubConn{}
	}
	h.unregisterLocked(r.ProjectID, appToken)
	hc, ok := h.conns[appToken]
	if !ok {
		parent := h.Parent
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithCancel(parent)
		hc = &hubConn{cancel: cancel, done: make(chan struct{}), routes: map[string]Route{}}
		hc.client = &SocketClient{
			API: &Client{APIBase: h.APIBase, HTTP: h.HTTP, AppToken: appToken},
			Log: h.log(),
		}
		hc.client.Handler = func(ctx context.Context, env Envelope) any { return h.handle(ctx, hc, env) }
		h.conns[appToken] = hc
		go func() {
			defer close(hc.done)
			hc.client.Run(ctx)
		}()
	}
	hc.routes[r.ProjectID] = r
}

// Unregister detaches a project, closing its connection when it was the last.
func (h *SocketHub) Unregister(projectID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.unregisterLocked(projectID, "")
}

// unregisterLocked removes projectID from every connection except keep's.
func (h *SocketHub) unregisterLocked(projectID, keep string) {
	for tok, hc := range h.conns {
		if tok == keep {
			continue
		}
		if _, ok := hc.routes[projectID]; !ok {
			continue
		}
		delete(hc.routes, projectID)
		if len(hc.routes) == 0 {
			hc.cancel()
			delete(h.conns, tok)
		}
	}
}

// State returns the health of the connection projectID is registered on.
func (h *SocketHub) State(projectID string) (SocketState, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, hc := range h.conns {
		if _, ok := hc.routes[projectID]; ok {
			return hc.client.State(), true
		}
	}
	return SocketState{}, false
}

// Close stops every connection and waits for them to end.
func (h *SocketHub) Close() {
	h.mu.Lock()
	var waits []chan struct{}
	for tok, hc := range h.conns {
		hc.cancel()
		waits = append(waits, hc.done)
		delete(h.conns, tok)
	}
	h.mu.Unlock()
	for _, w := range waits {
		<-w
	}
}

func (h *SocketHub) routesOf(hc *hubConn) map[string]Route {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make(map[string]Route, len(hc.routes))
	for k, v := range hc.routes {
		out[k] = v
	}
	return out
}

// ---------- interaction payloads ----------

type slackUser struct {
	ID string `json:"id"`
}

type blockActionsPayload struct {
	Type        string    `json:"type"`
	User        slackUser `json:"user"`
	TriggerID   string    `json:"trigger_id"`
	ResponseURL string    `json:"response_url"`
	Actions     []struct {
		ActionID string `json:"action_id"`
		Value    string `json:"value"`
	} `json:"actions"`
	View struct {
		CallbackID      string `json:"callback_id"`
		PrivateMetadata string `json:"private_metadata"`
		State           struct {
			Values map[string]map[string]struct {
				Value string `json:"value"`
			} `json:"values"`
		} `json:"state"`
	} `json:"view"`
}

type slashPayload struct {
	Command     string `json:"command"`
	Text        string `json:"text"`
	UserID      string `json:"user_id"`
	ChannelID   string `json:"channel_id"`
	ResponseURL string `json:"response_url"`
}

// ephemeral is a reply only the acting user sees.
func ephemeral(text string) map[string]any {
	return map[string]any{"response_type": "ephemeral", "replace_original": false, "text": text}
}

// handle is the EnvelopeHandler for one hub connection.
func (h *SocketHub) handle(ctx context.Context, hc *hubConn, env Envelope) any {
	routes := h.routesOf(hc)
	switch env.Type {
	case "slash_commands":
		var p slashPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil
		}
		return h.handleSlash(ctx, routes, p)
	case "interactive":
		var p blockActionsPayload
		if err := json.Unmarshal(env.Payload, &p); err != nil {
			return nil
		}
		switch p.Type {
		case "block_actions":
			// Ack now, act after: a review can take longer than Slack's
			// three-second ack window. The outcome goes to response_url.
			go h.handleBlockAction(context.WithoutCancel(ctx), routes, p)
			return nil
		case "view_submission":
			return h.handleViewSubmission(ctx, routes, p)
		}
	}
	// events_api and anything else: acknowledged, ignored. The manifest
	// subscribes to no events; an ack keeps Slack from retrying.
	return nil
}

// authorize applies the allowlist. An empty allowlist refuses everyone: a
// Slack workspace's members are not this project's approvers until someone
// says they are.
func authorize(r Route, userID string) error {
	if len(r.Cfg.AllowedUsers) == 0 {
		return errors.New("interactive actions are disabled for this project: `integrations.slack.allowed_users` in harness.yaml is empty, so Slack is notify-only. Use the CLI or the local UI")
	}
	if !slices.Contains(r.Cfg.AllowedUsers, userID) {
		return fmt.Errorf("<@%s> is not in `integrations.slack.allowed_users` for this project, so this action was refused", userID)
	}
	return nil
}

func (h *SocketHub) handleBlockAction(ctx context.Context, routes map[string]Route, p blockActionsPayload) {
	if len(p.Actions) == 0 {
		return
	}
	a := p.Actions[0]
	if a.ActionID == ActionOpenUI {
		return // a URL button; Slack opened the link, nothing to do
	}
	v, err := decodeValue(a.Value)
	if err != nil {
		h.respond(ctx, p.ResponseURL, ":warning: That button's payload is unreadable.")
		return
	}
	r, ok := routes[v.Project]
	if !ok {
		h.respond(ctx, p.ResponseURL, ":warning: That project is not connected to Slack on this daemon any more.")
		return
	}
	if err := authorize(r, p.User.ID); err != nil {
		h.log().Warn("slack: refused interactive action", "project", r.ProjectID, "user", p.User.ID, "action", a.ActionID, "why", "allowlist")
		h.respond(ctx, p.ResponseURL, ":lock: "+err.Error())
		return
	}
	h.log().Info("slack: interactive action", "project", r.ProjectID, "user", p.User.ID, "action", a.ActionID,
		"initiative", v.Initiative, "change_set", v.ChangeSet, "question", v.Question)
	actx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	switch a.ActionID {
	case ActionApproveChangeSet, ActionRejectChangeSet:
		verdict, verb := "approved", "Approved"
		if a.ActionID == ActionRejectChangeSet {
			verdict, verb = "rejected", "Rejected"
		}
		if err := r.Actions.ReviewChangeSet(actx, r.ProjectID, v.ChangeSet, verdict); err != nil {
			h.respond(ctx, p.ResponseURL, ":no_entry: Not "+strings.ToLower(verb)+": "+escape(actionMessage(err)))
			return
		}
		h.respond(ctx, p.ResponseURL, ":white_check_mark: "+verb+". Applying it is still a separate step (`idea apply`).")
	case ActionAcceptRecommendations:
		resolved, err := r.Actions.AcceptRecommendations(actx, r.ProjectID, v.Initiative)
		if err != nil {
			h.respond(ctx, p.ResponseURL, ":no_entry: "+escape(actionMessage(err)))
			return
		}
		if len(resolved) == 0 {
			h.respond(ctx, p.ResponseURL, "No open decision had a recommendation to accept.")
			return
		}
		h.respond(ctx, p.ResponseURL, ":white_check_mark: Resolved "+escape(strings.Join(resolved, ", "))+" with their recommendations.")
	case ActionAnswerQuestion:
		bot := r.Bot
		if bot == nil {
			h.respond(ctx, p.ResponseURL, ":warning: No bot token: cannot open the answer dialog.")
			return
		}
		if err := bot.OpenView(actx, p.TriggerID, answerModal(v)); err != nil {
			h.respond(ctx, p.ResponseURL, ":warning: Could not open the answer dialog: "+escape(err.Error()))
		}
	default:
		h.respond(ctx, p.ResponseURL, ":warning: Unknown action.")
	}
}

func answerModal(v actionValue) map[string]any {
	return map[string]any{
		"type":             "modal",
		"callback_id":      CallbackAnswerModal,
		"private_metadata": v.encode(),
		"title":            map[string]any{"type": "plain_text", "text": "Answer question"},
		"submit":           map[string]any{"type": "plain_text", "text": "Answer"},
		"close":            map[string]any{"type": "plain_text", "text": "Cancel"},
		"blocks": []Block{{
			"type":     "input",
			"block_id": answerBlockID,
			"label":    map[string]any{"type": "plain_text", "text": "Your answer"},
			"element":  map[string]any{"type": "plain_text_input", "action_id": answerInputID, "multiline": true},
		}},
	}
}

// handleViewSubmission answers a question from the modal. Synchronous, so a
// refusal can be shown inline in the modal via response_action=errors.
func (h *SocketHub) handleViewSubmission(ctx context.Context, routes map[string]Route, p blockActionsPayload) any {
	if p.View.CallbackID != CallbackAnswerModal {
		return nil
	}
	fail := func(msg string) any {
		return map[string]any{"response_action": "errors", "errors": map[string]string{answerBlockID: clip(msg, 140)}}
	}
	v, err := decodeValue(p.View.PrivateMetadata)
	if err != nil {
		return fail("This dialog's payload is unreadable.")
	}
	r, ok := routes[v.Project]
	if !ok {
		return fail("That project is not connected to Slack on this daemon any more.")
	}
	// Re-checked here, not trusted from the click that opened the modal: the
	// submitter is who acts.
	if err := authorize(r, p.User.ID); err != nil {
		h.log().Warn("slack: refused answer", "project", r.ProjectID, "user", p.User.ID, "why", "allowlist")
		return fail("You are not in integrations.slack.allowed_users for this project.")
	}
	answer := strings.TrimSpace(p.View.State.Values[answerBlockID][answerInputID].Value)
	if answer == "" {
		return fail("An answer is required.")
	}
	h.log().Info("slack: answering question", "project", r.ProjectID, "user", p.User.ID, "question", v.Question)
	actx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	if err := r.Actions.AnswerQuestion(actx, r.ProjectID, v.Question, answer); err != nil {
		return fail(actionMessage(err))
	}
	return nil
}

const slashUsage = "Usage: `/idea new <your idea>` · `/idea status` · `/idea approve <id>` · `/idea advance <id>` · `/idea execute <id>` · `/idea review <change-set> approve|reject` · `/idea apply <change-set>`"

// handleSlash serves /idea. The reply rides on the ack itself
// (accepts_response_payload), so both subcommands answer synchronously.
func (h *SocketHub) handleSlash(ctx context.Context, routes map[string]Route, p slashPayload) any {
	r, err := routeForChannel(routes, p.ChannelID)
	if err != nil {
		return ephemeral(":warning: " + err.Error())
	}
	if err := authorize(r, p.UserID); err != nil {
		h.log().Warn("slack: refused slash command", "project", r.ProjectID, "user", p.UserID, "why", "allowlist")
		return ephemeral(":lock: " + err.Error())
	}
	sub, rest, _ := strings.Cut(strings.TrimSpace(p.Text), " ")
	rest = strings.TrimSpace(rest)
	actx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	switch strings.ToLower(sub) {
	case "new":
		if rest == "" {
			return ephemeral(slashUsage)
		}
		h.log().Info("slack: /idea new", "project", r.ProjectID, "user", p.UserID)
		id, err := r.Actions.CreateInitiative(actx, r.ProjectID, integrations.NewInitiative{Title: rest, Description: rest})
		if err != nil {
			return ephemeral(":no_entry: " + escape(actionMessage(err)))
		}
		return ephemeral(":rocket: Started initiative `" + escape(id) + "`. Updates will thread in <#" + r.Cfg.Channel + ">.")
	case "status":
		list, err := r.Actions.ListInitiatives(actx, r.ProjectID)
		if err != nil {
			return ephemeral(":no_entry: " + escape(actionMessage(err)))
		}
		return ephemeral(statusText(list))
	case "approve":
		if rest == "" {
			return ephemeral(slashUsage)
		}
		if err := r.Actions.ApproveInitiative(actx, r.ProjectID, rest); err != nil {
			return ephemeral(":no_entry: " + escape(actionMessage(err)))
		}
		return ephemeral(":white_check_mark: Approved initiative `" + escape(rest) + "`.")
	case "advance":
		if rest == "" {
			return ephemeral(slashUsage)
		}
		if err := r.Actions.AdvanceInitiative(actx, r.ProjectID, rest); err != nil {
			return ephemeral(":no_entry: " + escape(actionMessage(err)))
		}
		return ephemeral(":white_check_mark: Advanced initiative `" + escape(rest) + "`. See `/idea status` for where it landed.")
	case "execute":
		if rest == "" {
			return ephemeral(slashUsage)
		}
		if err := r.Actions.ExecuteInitiative(actx, r.ProjectID, rest); err != nil {
			return ephemeral(":no_entry: " + escape(actionMessage(err)))
		}
		return ephemeral(":rocket: Execute started for initiative `" + escape(rest) + "`. Poll `/idea status`.")
	case "apply":
		if rest == "" {
			return ephemeral(slashUsage)
		}
		if err := r.Actions.ApplyChangeSet(actx, r.ProjectID, rest); err != nil {
			return ephemeral(":no_entry: " + escape(actionMessage(err)))
		}
		return ephemeral(":white_check_mark: Apply started for change set `" + escape(rest) + "`.")
	case "review":
		cs, verdict, _ := strings.Cut(rest, " ")
		verdict = strings.ToLower(strings.TrimSpace(verdict))
		if cs == "" || (verdict != "approve" && verdict != "reject") {
			return ephemeral(slashUsage)
		}
		if err := r.Actions.ReviewChangeSet(actx, r.ProjectID, cs, verdict); err != nil {
			return ephemeral(":no_entry: " + escape(actionMessage(err)))
		}
		return ephemeral(":white_check_mark: Reviewed change set `" + escape(cs) + "` → " + escape(verdict) + ".")
	}
	return ephemeral(slashUsage)
}

func routeForChannel(routes map[string]Route, channelID string) (Route, error) {
	var match []Route
	for _, r := range routes {
		if r.Cfg.Channel == channelID {
			match = append(match, r)
		}
	}
	switch {
	case len(match) == 1:
		return match[0], nil
	case len(match) > 1:
		return Route{}, errors.New("more than one idea-harness project posts to this channel; run /idea from a channel only one of them uses")
	case len(routes) == 1:
		for _, r := range routes {
			return r, nil
		}
	}
	return Route{}, errors.New("this channel is not linked to an idea-harness project (set integrations.slack.channel)")
}

const maxStatusLines = 15

func statusText(list []integrations.InitiativeSummary) string {
	if len(list) == 0 {
		return "No initiatives yet. Start one with `/idea new <your idea>`."
	}
	var b strings.Builder
	b.WriteString("*Initiatives*\n")
	// Newest last in the list the daemon returns; show the newest.
	start := max(0, len(list)-maxStatusLines)
	for _, ini := range list[start:] {
		title := ini.ShortTitle
		if title == "" {
			title = ini.Title
		}
		status := ini.Status
		if ini.JudgmentReason != "" && status == "needs_human_judgment" {
			status += " (" + ini.JudgmentReason + ")"
		}
		fmt.Fprintf(&b, "• `%s` %s: %s\n", escape(ini.ID), escape(clip(title, 80)), escape(status))
	}
	if start > 0 {
		fmt.Fprintf(&b, "_…and %d older._", start)
	}
	return b.String()
}

// actionMessage is the user-facing text of an Actions error: the daemon
// handler's own message when it refused, the error otherwise.
func actionMessage(err error) string {
	var ae *integrations.ActionError
	if errors.As(err, &ae) {
		return ae.Message
	}
	return err.Error()
}

func defaultResponseURLAllowed(u string) bool {
	return strings.HasPrefix(u, "https://hooks.slack.com/")
}

// respond POSTs an ephemeral reply to an interaction's response_url.
func (h *SocketHub) respond(ctx context.Context, responseURL, text string) {
	allowed := h.ResponseURLAllowed
	if allowed == nil {
		allowed = defaultResponseURLAllowed
	}
	if responseURL == "" || !allowed(responseURL) {
		// Never POST to an arbitrary URL a payload names: the payload came
		// over an authenticated socket, but the check costs nothing.
		h.log().Warn("slack: not replying to an unexpected response_url")
		return
	}
	body, _ := json.Marshal(ephemeral(text))
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodPost, responseURL, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := h.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		h.log().Warn("slack: response_url reply failed", "err", err)
		return
	}
	resp.Body.Close()
}
