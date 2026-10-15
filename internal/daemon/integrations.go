package daemon

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/integrations"
	"github.com/devarispbrown/idea-harness/internal/integrations/linear"
	"github.com/devarispbrown/idea-harness/internal/integrations/slack"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// integrationHub runs the Slack and Linear integrations for every project
// that configures them (harness.yaml `integrations`).
//
// Per project: one integrations.Dispatcher tailing the project's event log
// into its subscribers, plus the Linear poller. Shared: one Socket Mode
// connection per Slack app token (slack.SocketHub).
//
// Everything here runs on goroutines of its own and reads the event log
// after the fact. Nothing in the engine, the store or a handler waits on it:
// Slack being down delays a notification, never a transition.
type integrationHub struct {
	mu       sync.Mutex
	projects map[string]*projectIntegrations
	sockets  *slack.SocketHub
	// handler is the daemon's own authenticated HTTP handler; every Actions
	// call goes through it (daemonActions). Set by NewServer.
	handler http.Handler
	// uiBase is the deep-link root (the daemon's own address), set by Run.
	uiBase string

	// Test hooks: the fake Slack/Linear servers and the dispatcher's poll
	// interval. Zero values mean the real endpoints and defaults.
	slackAPI       string
	linearEndpoint string
	httpClient     *http.Client
	responseURLOK  func(string) bool
	dispatchOpts   integrations.Options
}

type projectIntegrations struct {
	fingerprint string
	cancel      context.CancelFunc
	done        chan struct{}
	ready       chan struct{} // closed once the goroutines have started

	dispatcher *integrations.Dispatcher
	syncer     *linear.Syncer
	problems   map[string][]string
	tokens     map[string]map[string]bool
}

// integrations returns the runtime's hub, creating it on first use (a
// Runtime built as a struct literal in a test has none until then).
func (r *Runtime) integrations() *integrationHub {
	r.integOnce.Do(func() {
		r.integ = &integrationHub{projects: map[string]*projectIntegrations{}}
	})
	return r.integ
}

func (r *Runtime) setIntegrationHandler(h http.Handler) {
	hub := r.integrations()
	hub.mu.Lock()
	hub.handler = h
	hub.mu.Unlock()
}

func (r *Runtime) setIntegrationUIBase(base string) {
	hub := r.integrations()
	hub.mu.Lock()
	hub.uiBase = base
	hub.mu.Unlock()
}

// integrationsFingerprint identifies a configuration + token set, so a
// harness.yaml reload that did not touch integrations restarts nothing. The
// tokens are hashed, never stored.
func integrationsFingerprint(cfg config.IntegrationsConfig, tokens ...string) string {
	h := sha256.New()
	b, _ := json.Marshal(cfg)
	h.Write(b)
	for _, t := range tokens {
		h.Write([]byte{0})
		h.Write([]byte(t))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// configureIntegrations (re)starts projectID's integrations to match pr's
// config. Called whenever a project runtime is built or reloaded.
//
// It never waits for the previous generation to stop: that generation's
// Linear poller may be the very goroutine whose request triggered this
// reload (an imported issue's POST resolves its project, which auto-reloads
// a changed harness.yaml), and waiting for it here would wait on itself.
// Instead the new generation starts only once the old one has exited, so two
// dispatchers never deliver from the same outbox at once.
func (r *Runtime) configureIntegrations(projectID string, pr *projectRuntime) {
	hub := r.integrations()
	cfg := pr.cfg.Harness.Integrations

	var slackBot, slackApp, linearKey string
	if s := cfg.Slack; s != nil {
		slackBot = lookupEnv(s.BotTokenEnvName(), pr.dotEnv)
		slackApp = lookupEnv(s.AppTokenEnvName(), pr.dotEnv)
	}
	if l := cfg.Linear; l != nil {
		linearKey = lookupEnv(l.APIKeyEnvName(), pr.dotEnv)
	}
	fp := integrationsFingerprint(cfg, slackBot, slackApp, linearKey)

	hub.mu.Lock()
	defer hub.mu.Unlock()
	old := hub.projects[projectID]
	if old != nil && old.fingerprint == fp {
		return
	}
	if old != nil {
		old.cancel()
		delete(hub.projects, projectID)
	}
	if hub.sockets != nil {
		hub.sockets.Unregister(projectID)
	}
	if !cfg.Configured() {
		return
	}

	pi := &projectIntegrations{
		fingerprint: fp, done: make(chan struct{}), ready: make(chan struct{}),
		problems: map[string][]string{}, tokens: map[string]map[string]bool{},
	}
	actions := &daemonActions{rt: r}
	redact := pr.redactor.RedactString
	var subs []integrations.Subscriber

	if s := cfg.Slack; s != nil {
		pi.tokens["slack"] = map[string]bool{s.BotTokenEnvName(): slackBot != "", s.AppTokenEnvName(): slackApp != ""}
		bot := &slack.Client{APIBase: hub.slackAPI, HTTP: hub.httpClient, BotToken: slackBot}
		switch {
		case slackBot == "":
			pi.problems["slack"] = append(pi.problems["slack"], s.BotTokenEnvName()+" is not set (environment or the project's .env): Slack is off")
		case !strings.HasPrefix(slackBot, "xoxb-"):
			pi.problems["slack"] = append(pi.problems["slack"], s.BotTokenEnvName()+" does not look like a bot token (xoxb-…)")
		}
		if slackBot != "" {
			uiBase := s.UIBaseURL
			if uiBase == "" {
				uiBase = hub.uiBase
			}
			subs = append(subs, &slack.Notifier{Cfg: *s, Client: bot, DB: pr.db, UIBase: uiBase, Redact: redact})
		}
		if s.SocketModeEnabled() {
			switch {
			case slackApp == "":
				pi.problems["slack"] = append(pi.problems["slack"], s.AppTokenEnvName()+" is not set: buttons and /idea are off (notifications still post)")
			case !strings.HasPrefix(slackApp, "xapp-"):
				pi.problems["slack"] = append(pi.problems["slack"], s.AppTokenEnvName()+" does not look like an app-level token (xapp-…)")
			}
			if slackApp != "" {
				if hub.sockets == nil {
					hub.sockets = &slack.SocketHub{APIBase: hub.slackAPI, HTTP: hub.httpClient, Log: r.log,
						ResponseURLAllowed: hub.responseURLOK, Parent: r.lifetimeContext()}
				}
				hub.sockets.Register(slackApp, slack.Route{ProjectID: projectID, Cfg: *s, Bot: bot, Actions: actions})
			}
		}
		if len(s.AllowedUsers) == 0 {
			pi.problems["slack"] = append(pi.problems["slack"], "allowed_users is empty: Slack is notify-only (buttons and /idea are refused)")
		}
	}

	if l := cfg.Linear; l != nil {
		pi.tokens["linear"] = map[string]bool{l.APIKeyEnvName(): linearKey != ""}
		if linearKey == "" {
			pi.problems["linear"] = append(pi.problems["linear"], l.APIKeyEnvName()+" is not set (environment or the project's .env): Linear is off")
		} else {
			engine := pr.engine
			pi.syncer = &linear.Syncer{
				ProjectID: projectID, Cfg: *l, DB: pr.db, Actions: actions, Redact: redact, Log: r.log,
				OnPoll: func(rec linear.PollRecord) { r.recordLinearPoll(pr.db, rec) },
				Client: &linear.Client{Endpoint: hub.linearEndpoint, APIKey: linearKey, HTTP: hub.httpClient},
				Why: func(ctx context.Context, initiativeID, taskID string) ([]linear.WhyStep, error) {
					items, err := engine.Why(ctx, initiativeID, "task", taskID)
					if err != nil {
						return nil, err
					}
					out := make([]linear.WhyStep, 0, len(items))
					for _, it := range items {
						out = append(out, linear.WhyStep{Edge: it.EdgeType, From: orLabel(it.FromLabel, it.FromType), To: orLabel(it.ToLabel, it.ToType)})
					}
					return out, nil
				},
			}
			subs = append(subs, pi.syncer)
		}
	}

	pi.dispatcher = integrations.NewDispatcher(pr.db, projectID, subs, r.log, hub.dispatchOpts)
	ctx, cancel := context.WithCancel(r.lifetimeContext())
	pi.cancel = cancel
	hub.projects[projectID] = pi

	var prevDone chan struct{}
	if old != nil {
		prevDone = old.done
	}
	go func() {
		defer close(pi.done)
		if prevDone != nil {
			select {
			case <-prevDone:
			case <-ctx.Done():
				close(pi.ready)
				return
			}
		}
		var wg sync.WaitGroup
		wg.Add(1)
		go func() { defer wg.Done(); pi.dispatcher.Run(ctx) }()
		if pi.syncer != nil {
			wg.Add(1)
			go func() { defer wg.Done(); pi.syncer.RunPoller(ctx) }()
		}
		close(pi.ready)
		wg.Wait()
	}()
	r.log.Info("integrations configured", "project", projectID, "slack", cfg.Slack != nil, "linear", cfg.Linear != nil)
}

func orLabel(label, typ string) string {
	if label != "" {
		return label
	}
	return typ
}

// stopIntegrations stops every project's integrations and waits for them.
func (r *Runtime) stopIntegrations() {
	hub := r.integrations()
	hub.mu.Lock()
	var waits []chan struct{}
	for id, pi := range hub.projects {
		pi.cancel()
		waits = append(waits, pi.done)
		delete(hub.projects, id)
	}
	sockets := hub.sockets
	hub.sockets = nil
	hub.mu.Unlock()
	if sockets != nil {
		sockets.Close()
	}
	for _, w := range waits {
		<-w
	}
}

// startConfiguredIntegrations opens, at daemon start, every registered
// project whose harness.yaml configures an integration -- otherwise a
// project's Slack and Linear would stay silent after a restart until
// something else happened to touch it. Projects without integrations stay
// lazily opened, as before.
func (r *Runtime) startConfiguredIntegrations(ctx context.Context) {
	projects, err := r.ListProjects(ctx)
	if err != nil {
		r.log.Warn("integrations: listing projects", "err", err)
		return
	}
	for _, p := range projects {
		cfg, err := config.Load(filepath.Join(p.RootPath, config.Dir))
		if err != nil || !cfg.Harness.Integrations.Configured() {
			continue
		}
		if _, err := r.EnsureProject(ctx, p.ID); err != nil {
			r.log.Warn("integrations: opening project", "project", p.ID, "err", err)
		}
	}
}

// ---------- Actions: through the daemon's own handler ----------

// daemonActions implements integrations.Actions by sending each call through
// the daemon's own authenticated HTTP handler, in process. Not a shortcut to
// the engine: the handler is where preconditions, gates and error mapping
// live, and an integration must meet every one of them exactly as the CLI
// does. The token is the daemon's own; the request never leaves the process.
type daemonActions struct{ rt *Runtime }

func (a *daemonActions) do(ctx context.Context, method, path string, body, out any) error {
	hub := a.rt.integrations()
	hub.mu.Lock()
	h := hub.handler
	hub.mu.Unlock()
	if h == nil {
		return errors.New("daemon: integration actions are unavailable (no handler)")
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1/api/v1"+path, rd)
	if err != nil {
		return err
	}
	req.Host = "127.0.0.1"
	req.Header.Set("X-Idea-Token", a.rt.Token())
	req.Header.Set("Content-Type", "application/json")
	rec := &bufferedResponse{header: http.Header{}, status: http.StatusOK}
	h.ServeHTTP(rec, req)
	if rec.status >= 400 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(rec.body.Bytes(), &e)
		if e.Error == "" {
			e.Error = http.StatusText(rec.status)
		}
		return &integrations.ActionError{Status: rec.status, Message: e.Error}
	}
	if out != nil {
		if err := json.Unmarshal(rec.body.Bytes(), out); err != nil {
			return fmt.Errorf("daemon: decoding %s %s: %w", method, path, err)
		}
	}
	return nil
}

// bufferedResponse is a minimal in-memory http.ResponseWriter.
type bufferedResponse struct {
	header http.Header
	status int
	wrote  bool
	body   bytes.Buffer
}

func (b *bufferedResponse) Header() http.Header { return b.header }
func (b *bufferedResponse) WriteHeader(code int) {
	if !b.wrote {
		b.status, b.wrote = code, true
	}
}
func (b *bufferedResponse) Write(p []byte) (int, error) {
	b.wrote = true
	return b.body.Write(p)
}

func pp(projectID string) string { return "/projects/" + url.PathEscape(projectID) }

func (a *daemonActions) ReviewChangeSet(ctx context.Context, projectID, changeSetID, verdict string) error {
	return a.do(ctx, http.MethodPost, pp(projectID)+"/change-sets/"+url.PathEscape(changeSetID)+"/review",
		map[string]any{"verdict": verdict}, nil)
}

func (a *daemonActions) AcceptRecommendations(ctx context.Context, projectID, initiativeID string) ([]string, error) {
	var out struct {
		Resolved []string `json:"resolved"`
	}
	err := a.do(ctx, http.MethodPost, pp(projectID)+"/initiatives/"+url.PathEscape(initiativeID)+"/decisions/accept-recommendations", nil, &out)
	return out.Resolved, err
}

func (a *daemonActions) AnswerQuestion(ctx context.Context, projectID, questionID, answer string) error {
	return a.do(ctx, http.MethodPost, pp(projectID)+"/questions/"+url.PathEscape(questionID)+"/answer",
		map[string]any{"answer": answer}, nil)
}

func (a *daemonActions) CreateInitiative(ctx context.Context, projectID string, in integrations.NewInitiative) (string, error) {
	docs := []map[string]string{}
	for _, d := range in.Docs {
		docs = append(docs, map[string]string{"content": d.Content, "kind": d.Kind, "title": d.Title})
	}
	body := map[string]any{"title": in.Title, "description": in.Description}
	if len(docs) > 0 {
		body["docs"] = docs
	}
	var out struct {
		InitiativeID string `json:"initiative_id"`
	}
	err := a.do(ctx, http.MethodPost, pp(projectID)+"/initiatives", body, &out)
	return out.InitiativeID, err
}

func (a *daemonActions) ListInitiatives(ctx context.Context, projectID string) ([]integrations.InitiativeSummary, error) {
	var out []integrations.InitiativeSummary
	err := a.do(ctx, http.MethodGet, pp(projectID)+"/initiatives", nil, &out)
	return out, err
}

// ---------- status + test endpoints ----------

// IntegrationStatus is one integration's health (GET .../integrations).
type IntegrationStatus struct {
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
	// Active is true when the integration is running: configured, and the
	// token it cannot work without is present.
	Active bool `json:"active"`
	// Tokens maps each environment variable the integration reads to
	// whether it is set. Never the value.
	Tokens      map[string]bool    `json:"tokens,omitempty"`
	Problems    []string           `json:"problems"`
	Outbox      *store.OutboxStats `json:"outbox,omitempty"`
	IngestError string             `json:"ingest_error,omitempty"`
	Socket      *slack.SocketState `json:"socket,omitempty"`
	Poll        *linear.PollState  `json:"poll,omitempty"`
	// LinkedIssues counts Linear issues linked to initiatives and tasks.
	LinkedIssues int `json:"linked_issues,omitempty"`
	// Config is the integration's harness.yaml section as written (null when
	// not configured), for the Settings editor. It holds environment
	// variable NAMES only; Tokens says whether each is set.
	Config any `json:"config"`
	// TokenEnvs names the variables the integration reads (defaults
	// applied), so an unconfigured integration's editor can say which ones
	// it will need.
	TokenEnvs []string `json:"token_envs"`
}

var integrationNames = []string{"slack", "linear"}

func (s *Server) handleIntegrationsStatus(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	pr, err := s.rt.EnsureProject(r.Context(), projectID)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	out, err := s.rt.integrationStatus(r.Context(), projectID, pr)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"integrations": out,
		// The event vocabulary and Slack's default selection, so the editor
		// lists what harness.yaml accepts rather than a copy of its own.
		"event_kinds":          config.IntegrationEventKinds,
		"default_slack_events": config.DefaultSlackEvents,
	})
}

func (r *Runtime) integrationStatus(ctx context.Context, projectID string, pr *projectRuntime) ([]IntegrationStatus, error) {
	hub := r.integrations()
	hub.mu.Lock()
	pi := hub.projects[projectID]
	sockets := hub.sockets
	hub.mu.Unlock()
	cfg := pr.cfg.Harness.Integrations

	out := []IntegrationStatus{}
	for _, name := range integrationNames {
		st := IntegrationStatus{Name: name, Problems: []string{}}
		st.Configured = (name == "slack" && cfg.Slack != nil) || (name == "linear" && cfg.Linear != nil)
		switch {
		case name == "slack" && cfg.Slack != nil:
			st.Config = cfg.Slack
			st.TokenEnvs = []string{cfg.Slack.BotTokenEnvName(), cfg.Slack.AppTokenEnvName()}
		case name == "slack":
			st.TokenEnvs = []string{config.SlackConfig{}.BotTokenEnvName(), config.SlackConfig{}.AppTokenEnvName()}
		case name == "linear" && cfg.Linear != nil:
			st.Config = cfg.Linear
			st.TokenEnvs = []string{cfg.Linear.APIKeyEnvName()}
		default:
			st.TokenEnvs = []string{config.LinearConfig{}.APIKeyEnvName()}
		}
		if st.Tokens == nil {
			// Set-or-not for every variable, even before the integration is
			// running (the hub only fills Tokens for a started one).
			st.Tokens = map[string]bool{}
			for _, env := range st.TokenEnvs {
				st.Tokens[env] = lookupEnv(env, pr.dotEnv) != ""
			}
		}
		if st.Configured && pi != nil {
			st.Tokens = pi.tokens[name]
			st.Problems = append(st.Problems, pi.problems[name]...)
			for _, sub := range pi.dispatcher.Subscribers() {
				if sub.Name() == name {
					st.Active = true
				}
			}
			st.IngestError = pi.dispatcher.IngestError(name)
		}
		if st.Configured {
			stats, err := pr.db.IntegrationOutboxStats(ctx, name)
			if err != nil {
				return nil, err
			}
			st.Outbox = &stats
			if stats.Dead > 0 {
				st.Problems = append(st.Problems, fmt.Sprintf("%d event(s) could not be delivered and were dropped after retrying; last error: %s", stats.Dead, stats.LastError))
			}
		}
		if name == "slack" && st.Configured && sockets != nil {
			if ss, ok := sockets.State(projectID); ok {
				st.Socket = &ss
			}
		}
		if name == "linear" && st.Configured {
			if pi != nil && pi.syncer != nil {
				ps := pi.syncer.PollState()
				st.Poll = &ps
				if ps.LastError != "" {
					st.Problems = append(st.Problems, "last poll failed: "+ps.LastError)
				}
			}
			for _, kind := range []string{linear.LinkInitiative, linear.LinkTask} {
				links, err := pr.db.ListIntegrationLinks(ctx, linear.SubscriberName, kind)
				if err != nil {
					return nil, err
				}
				st.LinkedIssues += len(links)
			}
		}
		out = append(out, st)
	}
	return out, nil
}

func (s *Server) handleIntegrationTest(w http.ResponseWriter, r *http.Request) {
	projectID, name := r.PathValue("projectId"), r.PathValue("name")
	pr, err := s.rt.EnsureProject(r.Context(), projectID)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	hub := s.rt.integrations()
	cfg := pr.cfg.Harness.Integrations
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	var result any
	var testErr error
	switch name {
	case "slack":
		if cfg.Slack == nil {
			writeError(w, http.StatusBadRequest, "integrations.slack is not configured in harness.yaml")
			return
		}
		sc := cfg.Slack
		bot := lookupEnv(sc.BotTokenEnvName(), pr.dotEnv)
		if bot == "" {
			writeError(w, http.StatusBadRequest, sc.BotTokenEnvName()+" is not set (environment or the project's .env)")
			return
		}
		app := ""
		if sc.SocketModeEnabled() {
			app = lookupEnv(sc.AppTokenEnvName(), pr.dotEnv)
		}
		result, testErr = slack.Check(ctx, &slack.Client{APIBase: hub.slackAPI, HTTP: hub.httpClient, BotToken: bot}, *sc, app)
	case "linear":
		if cfg.Linear == nil {
			writeError(w, http.StatusBadRequest, "integrations.linear is not configured in harness.yaml")
			return
		}
		lc := cfg.Linear
		key := lookupEnv(lc.APIKeyEnvName(), pr.dotEnv)
		if key == "" {
			writeError(w, http.StatusBadRequest, lc.APIKeyEnvName()+" is not set (environment or the project's .env)")
			return
		}
		syncer := &linear.Syncer{Cfg: *lc, Client: &linear.Client{Endpoint: hub.linearEndpoint, APIKey: key, HTTP: hub.httpClient}}
		result, testErr = syncer.Check(ctx)
	default:
		writeError(w, http.StatusNotFound, fmt.Sprintf("no integration matching %q (use slack or linear)", name))
		return
	}
	resp := map[string]any{"name": name, "ok": testErr == nil, "result": result}
	if testErr != nil {
		resp["error"] = pr.redactor.RedactString(testErr.Error())
	}
	writeJSON(w, http.StatusOK, resp)
}
