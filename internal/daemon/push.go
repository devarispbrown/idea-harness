package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/devarispbrown/idea-harness/internal/config"
	"github.com/devarispbrown/idea-harness/internal/integrations"
	"github.com/devarispbrown/idea-harness/internal/integrations/push"
	"github.com/devarispbrown/idea-harness/internal/store"
	"github.com/devarispbrown/idea-harness/internal/webpush"
)

// Web Push notifications (docs/ui.md "Notifications").
//
// A browser that turns notifications on posts its PushSubscription here; each
// one becomes its own subscriber on the project's integration bus
// (internal/integrations/push), so a push has the same delivery semantics as
// a Slack post -- outbox, idempotency key, retries, dead letters.
//
// The VAPID key pair is generated on first use and kept in
// <state root>/push/vapid.json (0600). It is daemon-wide, like the token: one
// application server, one key the browsers bind their subscriptions to.
// Rotating it (deleting the file) silently orphans every subscription, which
// is why nothing here ever does.

// pushState is the daemon's VAPID sender, loaded once.
type pushState struct {
	mu     sync.Mutex
	sender *webpush.Sender
	// override replaces the real sender (tests: a fake push service).
	override *webpush.Sender
}

func (r *Runtime) pushSender() (*webpush.Sender, error) {
	ps := &r.push
	ps.mu.Lock()
	defer ps.mu.Unlock()
	if ps.override != nil {
		return ps.override, nil
	}
	if ps.sender != nil {
		return ps.sender, nil
	}
	keys, err := webpush.LoadOrCreateKeys(filepath.Join(r.stateRoot, "push"))
	if err != nil {
		return nil, fmt.Errorf("push: VAPID keys: %w", err)
	}
	ps.sender = &webpush.Sender{Keys: keys, Subject: r.remote.pushContact()}
	return ps.sender, nil
}

// pushMetaKey marks, in global.db, a project that has push subscriptions, so
// the daemon opens it at start and its notifications resume after a restart
// (startConfiguredIntegrations) without opening every project's database.
func pushMetaKey(projectID string) string { return "push:" + projectID }

// pushFingerprint folds the subscriptions into the integrations fingerprint:
// adding, removing or re-targeting one restarts the dispatcher with the new
// subscriber set; a delivery result (last_error) does not.
func pushFingerprint(list []store.PushSubscription) string {
	h := sha256.New()
	for _, s := range list {
		fmt.Fprintf(h, "%s|%s|%s|%s|%s\n", s.ID, s.Endpoint, s.P256dh, s.Auth, strings.Join(s.Events, ","))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// pushNotifiers builds one bus subscriber per subscription that wants
// anything.
func (r *Runtime) pushNotifiers(projectID string, pr *projectRuntime, list []store.PushSubscription) []integrations.Subscriber {
	if len(list) == 0 {
		return nil
	}
	sender, err := r.pushSender()
	if err != nil {
		r.log.Warn("push: notifications are off", "project", projectID, "err", err)
		return nil
	}
	db := pr.db
	var out []integrations.Subscriber
	for _, s := range list {
		events := map[integrations.Kind]bool{}
		for _, k := range s.Events {
			if push.ValidKind(k) {
				events[integrations.Kind(k)] = true
			}
		}
		if len(events) == 0 {
			continue
		}
		subID := s.ID
		out = append(out, &push.Notifier{
			SubscriptionID: subID,
			Subscription:   webpush.Subscription{Endpoint: s.Endpoint, P256dh: s.P256dh, Auth: s.Auth},
			Events:         events,
			Sender:         sender,
			Redact:         pr.redactor.RedactString,
			OnResult: func(ctx context.Context, errText string) {
				_ = db.RecordPushResult(ctx, subID, errText)
			},
			OnGone: func(context.Context) {
				// The push service says this browser unsubscribed (or was
				// reset). Removing it restarts the dispatcher, which must not
				// happen from inside the dispatcher's own delivery call.
				go r.removePushSubscription(projectID, subID)
			},
		})
	}
	return out
}

func (r *Runtime) removePushSubscription(projectID, subID string) {
	ctx := r.lifetimeContext()
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return
	}
	if err := pr.db.DeletePushSubscription(ctx, subID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		r.log.Warn("push: removing a dead subscription", "project", projectID, "subscription", subID, "err", err)
		return
	}
	r.log.Info("push: subscription gone at the push service; removed", "project", projectID, "subscription", subID)
	r.afterPushChange(ctx, projectID, pr)
}

// afterPushChange re-plans the project's bus subscribers and its start-up
// marker after a subscription was added, changed or removed.
func (r *Runtime) afterPushChange(ctx context.Context, projectID string, pr *projectRuntime) {
	list, err := pr.db.ListPushSubscriptions(ctx)
	if err == nil {
		v := ""
		if len(list) > 0 {
			v = "1"
		}
		_ = r.global.SetDaemonMeta(ctx, pushMetaKey(projectID), v)
	}
	r.configureIntegrations(projectID, pr)
}

// ---------- handlers ----------

// pushSubscriptionView is a subscription as the API shows it: never the
// endpoint path or the keys, which are what it takes to push to the browser.
type pushSubscriptionView struct {
	ID              string     `json:"id"`
	Label           string     `json:"label,omitempty"`
	EndpointHost    string     `json:"endpoint_host"`
	Events          []string   `json:"events"`
	CreatedAt       time.Time  `json:"created_at"`
	LastDeliveredAt *time.Time `json:"last_delivered_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
	// RenewalToken is set only in the responses that mint one -- subscribe
	// and renew -- and is never listed (handleRenewPushSubscription).
	RenewalToken string `json:"renewal_token,omitempty"`
}

func viewPush(s store.PushSubscription) pushSubscriptionView {
	return pushSubscriptionView{
		ID: s.ID, Label: s.Label, EndpointHost: webpush.EndpointHost(s.Endpoint), Events: s.Events,
		CreatedAt: s.CreatedAt, LastDeliveredAt: s.LastDeliveredAt, LastError: s.LastError,
	}
}

func pushKindNames() []string {
	out := make([]string, 0, len(push.Kinds))
	for _, k := range push.Kinds {
		out = append(out, string(k))
	}
	return out
}

func (s *Server) handlePushConfig(w http.ResponseWriter, r *http.Request) {
	sender, err := s.rt.pushSender()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"public_key": sender.Keys.PublicKey(), "event_kinds": pushKindNames()})
}

func (s *Server) handleListPushSubscriptions(w http.ResponseWriter, r *http.Request) {
	pr, err := s.rt.EnsureProject(r.Context(), r.PathValue("projectId"))
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	list, err := pr.db.ListPushSubscriptions(r.Context())
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]pushSubscriptionView, 0, len(list))
	for _, sub := range list {
		out = append(out, viewPush(sub))
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": out, "event_kinds": pushKindNames()})
}

// normalizeEvents validates requested kinds and sorts them into the
// canonical order; nil (field absent) means every kind.
func normalizeEvents(in []string, absentMeansAll bool) ([]string, error) {
	if in == nil && absentMeansAll {
		return pushKindNames(), nil
	}
	seen := map[string]bool{}
	for _, k := range in {
		if !push.ValidKind(k) {
			return nil, fmt.Errorf("unknown push event kind %q (use %s)", k, strings.Join(pushKindNames(), ", "))
		}
		seen[k] = true
	}
	out := []string{}
	for _, k := range pushKindNames() {
		if seen[k] {
			out = append(out, k)
		}
	}
	return out, nil
}

func decodePushBody(w http.ResponseWriter, r *http.Request, into any) bool {
	// An endpoint URL is a few hundred bytes; 16 KiB is generous.
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return false
	}
	return true
}

func (s *Server) handleCreatePushSubscription(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	pr, err := s.rt.EnsureProject(r.Context(), projectID)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
		Events []string `json:"events"`
		Label  string   `json:"label"`
	}
	if !decodePushBody(w, r, &body) {
		return
	}
	events, err := normalizeEvents(body.Events, true)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	sender, err := s.rt.pushSender()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	sub := webpush.Subscription{Endpoint: body.Endpoint, P256dh: body.Keys.P256dh, Auth: body.Keys.Auth}
	if err := sender.ValidateSubscription(sub); err != nil {
		writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "webpush: "))
		return
	}
	renewal, err := newRenewalToken()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	saved, created, err := pr.db.UpsertPushSubscription(r.Context(), store.PushSubscription{
		Endpoint: sub.Endpoint, P256dh: sub.P256dh, Auth: sub.Auth, Events: events, Label: truncateBytes(body.Label, 80),
		RenewalTokenHash: hashSecret(renewal),
	})
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.rt.afterPushChange(r.Context(), projectID, pr)
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	v := viewPush(saved)
	v.RenewalToken = renewal
	writeJSON(w, status, v)
}

// newRenewalToken is a subscription's renewal secret: 256 random bits,
// base64url. The daemon keeps only its SHA-256.
func newRenewalToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// handleRenewPushSubscription is the service worker's half of a push
// subscription change (web/public/sw-push.js).
//
// A push service may replace a browser's subscription at any time -- key
// rotation, expiry, a browser reset -- and tells only the service worker,
// in a `pushsubscriptionchange` event that may arrive with no page open. The
// worker cannot authenticate the usual ways: it never sees the API token
// (on loopback the page holds it; the worker shares no memory with the
// page), and the session cookie may have expired or never existed. A CSRF
// token would not help either -- it is derived from the cookie's session.
//
// So each subscription carries its own capability: a renewal token handed
// to the page when it subscribed (POST .../push/subscriptions) and kept in
// IndexedDB, where the worker can read it. It authorises exactly one act --
// re-pointing this subscription at a new endpoint and keys -- and is rotated
// by every use. It cannot read anything, change which events are sent, or
// touch another subscription; the endpoint it sets must pass the same
// validation as a subscribe (https, public address).
//
// What a stolen token buys, and why that is acceptable: someone who has it
// can redirect THIS subscription's notifications -- short, redacted lines --
// to a browser of their own until the owner notices them stop. Reading it
// needs the browser's own storage, where the session cookie also lives.
// docs/security-model.md says this in its Remote access section.
//
// The route is outside auth (server.go) and carries its own checks: a JSON
// body and an Origin, as login does, plus the token. Every refusal --
// unknown project, unknown subscription, wrong or spent token -- is the
// same 401, so the route reveals nothing about which ids exist.
func (s *Server) handleRenewPushSubscription(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || r.Header.Get("Origin") == "" {
		writeError(w, http.StatusForbidden, "renew from the idea-harness service worker")
		return
	}
	var body struct {
		RenewalToken string `json:"renewal_token"`
		Endpoint     string `json:"endpoint"`
		Keys         struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if !decodePushBody(w, r, &body) {
		return
	}
	const denied = "push subscription renewal refused: turn notifications on again from Settings"
	if body.RenewalToken == "" {
		writeError(w, http.StatusUnauthorized, denied)
		return
	}
	projectID, subID := r.PathValue("projectId"), r.PathValue("id")
	// Only a project the daemon already knows: an unauthenticated caller must
	// not make it open anything new.
	if _, err := s.rt.global.GetProject(r.Context(), projectID); err != nil {
		writeError(w, http.StatusUnauthorized, denied)
		return
	}
	pr, err := s.rt.EnsureProject(r.Context(), projectID)
	if err != nil {
		// A known project that will not open (a malformed harness.yaml) is
		// not a refusal: the worker must keep its token and try again, which
		// it does for anything but 401. The detail stays in the daemon log.
		var cfgErr *config.ParseError
		s.rt.log.Warn("push: renewal could not open the project", "project", projectID, "err", err)
		if errors.As(err, &cfgErr) {
			writeError(w, http.StatusUnprocessableEntity, "the project's harness.yaml does not parse; renewal will be retried")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	sender, err := s.rt.pushSender()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	sub := webpush.Subscription{Endpoint: body.Endpoint, P256dh: body.Keys.P256dh, Auth: body.Keys.Auth}
	if err := sender.ValidateSubscription(sub); err != nil {
		writeError(w, http.StatusBadRequest, strings.TrimPrefix(err.Error(), "webpush: "))
		return
	}
	next, err := newRenewalToken()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	saved, err := pr.db.RenewPushSubscription(r.Context(), subID, hashSecret(body.RenewalToken),
		store.PushSubscription{Endpoint: sub.Endpoint, P256dh: sub.P256dh, Auth: sub.Auth}, hashSecret(next))
	if errors.Is(err, store.ErrPushRenewalDenied) {
		writeError(w, http.StatusUnauthorized, denied)
		return
	}
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.rt.log.Info("push: subscription renewed by the browser", "project", projectID, "subscription", saved.ID, "endpoint_host", webpush.EndpointHost(saved.Endpoint))
	s.rt.afterPushChange(r.Context(), projectID, pr)
	v := viewPush(saved)
	v.RenewalToken = next
	writeJSON(w, http.StatusOK, v)
}

// isPushRenewPath reports whether path is the renewal route,
// /api/v1/projects/{p}/push/subscriptions/{id}/renew -- matched by exact
// shape, as isSSEEventsPath is, because it is let through without auth.
func isPushRenewPath(path string) bool {
	rest, ok := strings.CutPrefix(path, "/api/v1/projects/")
	if !ok {
		return false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 5 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	return parts[1] == "push" && parts[2] == "subscriptions" && parts[4] == "renew"
}

func (s *Server) handleUpdatePushSubscription(w http.ResponseWriter, r *http.Request) {
	projectID, subID := r.PathValue("projectId"), r.PathValue("id")
	pr, err := s.rt.EnsureProject(r.Context(), projectID)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	// The subscription is resolved before the body, so a stale id answers
	// 404 whatever was sent.
	if _, err := pr.db.GetPushSubscription(r.Context(), subID); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var body struct {
		Events *[]string `json:"events"`
	}
	if !decodePushBody(w, r, &body) {
		return
	}
	if body.Events == nil {
		writeError(w, http.StatusBadRequest, "events is required (an empty list pauses this browser's notifications)")
		return
	}
	events, err := normalizeEvents(*body.Events, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := pr.db.SetPushSubscriptionEvents(r.Context(), subID, events); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	got, err := pr.db.GetPushSubscription(r.Context(), subID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.rt.afterPushChange(r.Context(), projectID, pr)
	writeJSON(w, http.StatusOK, viewPush(got))
}

func (s *Server) handleDeletePushSubscription(w http.ResponseWriter, r *http.Request) {
	projectID, subID := r.PathValue("projectId"), r.PathValue("id")
	pr, err := s.rt.EnsureProject(r.Context(), projectID)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	if err := pr.db.DeletePushSubscription(r.Context(), subID); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.rt.afterPushChange(r.Context(), projectID, pr)
	w.WriteHeader(http.StatusNoContent)
}

// handleTestPushSubscription sends one notification now, outside the bus:
// the user just pressed a button and wants to see the result, not wait for a
// tick. A subscription the push service reports gone is removed.
func (s *Server) handleTestPushSubscription(w http.ResponseWriter, r *http.Request) {
	projectID, subID := r.PathValue("projectId"), r.PathValue("id")
	pr, err := s.rt.EnsureProject(r.Context(), projectID)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	sub, err := pr.db.GetPushSubscription(r.Context(), subID)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	sender, err := s.rt.pushSender()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	name := projectID
	if rec, err := s.rt.global.GetProject(r.Context(), projectID); err == nil && rec.Name != "" {
		name = rec.Name
	}
	payload, _ := json.Marshal(push.Payload{
		Title: "idea-harness", Body: "Notifications are on for " + name + ".",
		URL: "/#/p/" + projectID + "/queue", Tag: "test", Kind: "test",
	})
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	sendErr := sender.Send(ctx, webpush.Subscription{Endpoint: sub.Endpoint, P256dh: sub.P256dh, Auth: sub.Auth},
		webpush.Message{Payload: payload, Urgency: "high", TTL: time.Minute})
	resp := map[string]any{"ok": sendErr == nil}
	if sendErr != nil {
		resp["error"] = pr.redactor.RedactString(sendErr.Error())
		_ = pr.db.RecordPushResult(r.Context(), subID, sendErr.Error())
		if webpush.IsGone(sendErr) {
			resp["gone"] = true
			if err := pr.db.DeletePushSubscription(r.Context(), subID); err == nil {
				s.rt.afterPushChange(r.Context(), projectID, pr)
			}
		}
	} else {
		_ = pr.db.RecordPushResult(r.Context(), subID, "")
	}
	writeJSON(w, http.StatusOK, resp)
}
