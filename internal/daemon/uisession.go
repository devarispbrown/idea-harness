package daemon

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/devarispbrown/idea-harness/internal/id"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// Browser sessions for remote use of the web UI (docs/security-model.md
// "Remote access").
//
// Why a pairing code rather than typing the API token into the phone:
//
//   - the token is the daemon's master credential -- the CLI, `idea mcp`
//     configs and every script hold it -- so it cannot be rotated cheaply, and
//     a browser that has seen it once has it for good;
//   - a pairing code is minted by someone who already holds the token
//     (`idea ui pair`, on the machine), is good for one sign-in within ten
//     minutes, and buys a session that can be listed and revoked on its own;
//   - the code never travels in a URL: it is typed into the sign-in page and
//     POSTed, so it is in no history, log or Referer.
//
// The session secret lives only in an HttpOnly, SameSite=Strict cookie
// (Secure, with the __Host- prefix, whenever the browser is on HTTPS); the
// database keeps its SHA-256 alone. State-changing requests authenticated by
// the cookie must also carry a CSRF token -- an HMAC of the session secret,
// which page script receives from GET /api/v1/ui/session but a cross-site
// page can never read -- and an allowed Origin. SameSite=Strict already keeps
// a cross-site form from sending the cookie at all; the CSRF token is the
// second lock for browsers and proxies where that does not hold.

const (
	sessionCookieSecure = "__Host-idea_session"
	sessionCookiePlain  = "idea_session"
	csrfHeader          = "X-Idea-CSRF"

	pairingCodeTTL = 10 * time.Minute
	// sessionIdleTTL ends a session nobody has used for a week, whatever its
	// absolute expiry: a phone left in a drawer should not stay signed in for
	// the full thirty days.
	sessionIdleTTL = 7 * 24 * time.Hour
	// touchEvery throttles last_seen_at writes: a UI polls every few seconds,
	// and a write per poll is a write per poll.
	touchEvery = time.Minute
)

// The sign-in failure budgets, and the code-space arithmetic behind them, are
// in loginlimit.go.

// pairingAlphabet is Crockford-style base32 without I, L, O and U: nothing a
// person reading a terminal and typing on a phone can confuse.
const pairingAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// uiAuth is the daemon's in-memory sign-in state: the login rate limiter and
// the last-seen throttle. Sessions themselves are in global.db.
type uiAuth struct {
	mu        sync.Mutex
	logins    loginLimiter
	lastTouch map[string]time.Time
	now       func() time.Time
}

func newUIAuth() *uiAuth { return &uiAuth{lastTouch: map[string]time.Time{}, now: time.Now} }

func (r *Runtime) uiAuth() *uiAuth {
	r.uiAuthOnce.Do(func() {
		if r.uiAuthState == nil {
			r.uiAuthState = newUIAuth()
		}
	})
	return r.uiAuthState
}

// newPairingCode returns a 10-character code (50 bits), formatted XXXXX-XXXXX.
func newPairingCode() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 0, 11)
	for i, c := range b {
		if i == 5 {
			out = append(out, '-')
		}
		out = append(out, pairingAlphabet[int(c)%len(pairingAlphabet)])
	}
	return string(out), nil
}

// normalizePairingCode makes a typed code comparable: case, spaces and dashes
// do not matter, and the letters people misread are read as the digits.
func normalizePairingCode(s string) string {
	var b strings.Builder
	for _, c := range strings.ToUpper(s) {
		switch c {
		case ' ', '-', '\t':
			continue
		case 'O':
			c = '0'
		case 'I', 'L':
			c = '1'
		}
		b.WriteRune(c)
	}
	return b.String()
}

func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// csrfFor derives a session's CSRF token from its secret: nothing to store,
// and nothing that leaks the secret (HMAC is one-way).
func csrfFor(secret string) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte("idea-harness csrf v1"))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// ---------- auth context ----------

type authKind string

const (
	authToken   authKind = "token"
	authSession authKind = "session"
)

type authInfo struct {
	kind      authKind
	sessionID string
	csrf      string
}

type authCtxKey struct{}

func withAuth(r *http.Request, a authInfo) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authCtxKey{}, a))
}

func authFrom(r *http.Request) authInfo {
	a, _ := r.Context().Value(authCtxKey{}).(authInfo)
	return a
}

func isUnsafeMethod(m string) bool {
	switch m {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// sessionSecret returns the session cookie's value, whichever name it has.
func sessionSecret(r *http.Request) string {
	for _, name := range []string{sessionCookieSecure, sessionCookiePlain} {
		if c, err := r.Cookie(name); err == nil && c.Value != "" {
			return c.Value
		}
	}
	return ""
}

// sessionFromRequest resolves the request's session cookie to a live session.
func (s *Server) sessionFromRequest(r *http.Request) (store.UISession, string, bool) {
	secret := sessionSecret(r)
	if secret == "" {
		return store.UISession{}, "", false
	}
	ua := s.rt.uiAuth()
	now := ua.now()
	sess, ok, err := s.rt.global.LookupUISession(r.Context(), hashSecret(secret), now)
	if err != nil || !ok {
		return store.UISession{}, "", false
	}
	if now.Sub(sess.LastSeenAt) > sessionIdleTTL {
		return store.UISession{}, "", false
	}
	ua.mu.Lock()
	last := ua.lastTouch[sess.ID]
	touch := now.Sub(last) >= touchEvery
	if touch {
		ua.lastTouch[sess.ID] = now
	}
	ua.mu.Unlock()
	if touch {
		_ = s.rt.global.TouchUISession(r.Context(), sess.ID, now)
	}
	return sess, secret, true
}

// checkSessionCSRF is the second lock on a cookie-authenticated write: the
// CSRF token must match, and the Origin must be present (hostOriginGuard has
// already refused one that is present and foreign).
func checkSessionCSRF(r *http.Request, secret string) error {
	if r.Header.Get("Origin") == "" {
		return errors.New("a signed-in browser request that changes state must carry an Origin header")
	}
	got := r.Header.Get(csrfHeader)
	if got == "" || !hmac.Equal([]byte(got), []byte(csrfFor(secret))) {
		return errors.New("missing or wrong " + csrfHeader + " header (reload the page)")
	}
	return nil
}

// ---------- handlers ----------

// requireToken refuses a request authenticated by a session cookie. Minting
// pairing codes is token-only: a stolen session must not be able to mint the
// next one and outlive its own revocation.
func requireToken(w http.ResponseWriter, r *http.Request) bool {
	if authFrom(r).kind != authToken {
		writeError(w, http.StatusForbidden, "this needs the API token (run it from the CLI on the daemon's machine: idea ui pair)")
		return false
	}
	return true
}

type pairingCodeResponse struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
	// Remote reports whether this daemon can be reached from another device
	// at all: a remote listener, or allowed_hosts for a proxy. Without it a
	// code is useless and the CLI says so.
	Remote bool     `json:"remote"`
	URLs   []string `json:"urls"`
}

func (s *Server) handleCreatePairingCode(w http.ResponseWriter, r *http.Request) {
	if !requireToken(w, r) {
		return
	}
	var body struct {
		Label string `json:"label"`
	}
	if !decodeOptionalJSON(w, r, &body) {
		return
	}
	code, err := newPairingCode()
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	now := s.rt.uiAuth().now()
	exp := now.Add(pairingCodeTTL)
	if err := s.rt.global.CreatePairingCode(r.Context(), hashSecret(normalizePairingCode(code)), truncateBytes(body.Label, 80), now, exp); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	urls := s.rt.remoteURLs()
	writeJSON(w, http.StatusCreated, pairingCodeResponse{Code: code, ExpiresAt: exp.UTC(), Remote: len(urls) > 0, URLs: urls})
}

type sessionView struct {
	Auth      string           `json:"auth"`
	CSRFToken string           `json:"csrf_token,omitempty"`
	Session   *store.UISession `json:"session,omitempty"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	// Unauthenticated by design, so it carries its own checks: a JSON body (a
	// cross-site HTML form cannot send one without a CORS preflight this
	// daemon never answers) and an Origin the guard has already vetted.
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") || r.Header.Get("Origin") == "" {
		writeError(w, http.StatusForbidden, "sign in from the idea-harness page")
		return
	}
	ua := s.rt.uiAuth()
	now := ua.now()
	client := loginClientKey(r, s.rt.hosts)
	ua.mu.Lock()
	lock, wait := ua.logins.check(client, now)
	ua.mu.Unlock()
	if lock != loginOpen {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Round(time.Second)/time.Second)))
		mins := int((wait + time.Minute - 1) / time.Minute)
		if lock == loginLockedGlobal {
			writeError(w, http.StatusTooManyRequests, fmt.Sprintf("too many failed sign-ins from too many places: sign-in is paused for everyone and every pairing code has been cancelled. Wait %d minute(s), then run `idea ui pair` for a new one", mins))
		} else {
			writeError(w, http.StatusTooManyRequests, fmt.Sprintf("too many failed sign-ins from this device: wait %d minute(s) before trying again (your pairing code still works until it expires)", mins))
		}
		return
	}
	var body struct {
		Code  string `json:"code"`
		Label string `json:"label"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	secretBytes := make([]byte, 32)
	if _, err := rand.Read(secretBytes); err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	secret := base64.RawURLEncoding.EncodeToString(secretBytes)
	ttl := s.rt.remote.sessionTTL()
	label := truncateBytes(body.Label, 80)
	if label == "" {
		// Only a trusted proxy's word names the person (trustedProxyClient).
		if who, ok := trustedProxyClient(r, s.rt.hosts); ok {
			if login, isTS := strings.CutPrefix(who, "tailscale:"); isTS {
				label = truncateBytes(login, 80)
			}
		}
	}
	sess := store.UISession{
		ID: id.New(), Label: label, UserAgent: truncateBytes(r.UserAgent(), 200), RemoteAddr: s.clientAddr(r),
		CreatedAt: now.UTC(), LastSeenAt: now.UTC(), ExpiresAt: now.Add(ttl).UTC(),
	}
	sess, err := s.rt.global.RedeemPairingCode(r.Context(), hashSecret(normalizePairingCode(body.Code)), sess, hashSecret(secret), now)
	if errors.Is(err, store.ErrPairingCodeInvalid) {
		ua.mu.Lock()
		spent := ua.logins.fail(client, now)
		ua.mu.Unlock()
		if spent {
			n, _ := s.rt.global.InvalidatePairingCodes(r.Context(), now)
			s.rt.log.Warn("ui: global sign-in failure ceiling reached; sign-in paused and outstanding pairing codes cancelled", "cancelled", n, "remote", s.clientAddr(r))
		}
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	ua.mu.Lock()
	ua.logins.succeed(client)
	ua.mu.Unlock()
	s.rt.log.Info("ui: browser signed in", "session", sess.ID, "label", sess.Label, "remote", sess.RemoteAddr)
	setSessionCookie(w, r, secret, ttl)
	writeJSON(w, http.StatusOK, sessionView{Auth: string(authSession), CSRFToken: csrfFor(secret), Session: &sess})
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, secret string, ttl time.Duration) {
	c := &http.Cookie{
		Name: sessionCookiePlain, Value: secret, Path: "/", HttpOnly: true,
		SameSite: http.SameSiteStrictMode, MaxAge: int(ttl.Seconds()),
	}
	if requestIsHTTPS(r) {
		c.Name, c.Secure = sessionCookieSecure, true
	}
	http.SetCookie(w, c)
}

func clearSessionCookies(w http.ResponseWriter) {
	for _, name := range []string{sessionCookieSecure, sessionCookiePlain} {
		http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1, Secure: name == sessionCookieSecure})
	}
}

func (s *Server) handleGetUISession(w http.ResponseWriter, r *http.Request) {
	a := authFrom(r)
	if a.kind != authSession {
		writeJSON(w, http.StatusOK, sessionView{Auth: string(a.kind)})
		return
	}
	sess, _, ok := s.sessionFromRequest(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	writeJSON(w, http.StatusOK, sessionView{Auth: string(authSession), CSRFToken: a.csrf, Session: &sess})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	a := authFrom(r)
	if a.kind == authSession {
		if _, err := s.rt.global.RevokeUISession(r.Context(), a.sessionID, time.Now()); err != nil {
			s.writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	clearSessionCookies(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListUISessions(w http.ResponseWriter, r *http.Request) {
	list, err := s.rt.global.ListUISessions(r.Context())
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sessions": list, "current": authFrom(r).sessionID})
}

func (s *Server) handleRevokeUISession(w http.ResponseWriter, r *http.Request) {
	ok, err := s.rt.global.RevokeUISession(r.Context(), r.PathValue("id"), time.Now())
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, fmt.Sprintf("no session matching %q", r.PathValue("id")))
		return
	}
	if authFrom(r).sessionID == r.PathValue("id") {
		clearSessionCookies(w)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRevokeAllUISessions(w http.ResponseWriter, r *http.Request) {
	n, err := s.rt.global.RevokeAllUISessions(r.Context(), time.Now())
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if authFrom(r).kind == authSession {
		clearSessionCookies(w)
	}
	writeJSON(w, http.StatusOK, map[string]int64{"revoked": n})
}

// clientAddr is the address to show for a session: the trusted proxy's
// client when the request came through one (trustedProxyClient), the peer
// otherwise. It used to believe X-Forwarded-For on any loopback connection,
// which let a client write its own entry in the session list. Display only.
func (s *Server) clientAddr(r *http.Request) string {
	if _, ok := trustedProxyClient(r, s.rt.hosts); ok {
		if ip := lastForwardedFor(r); ip != "" {
			return ip
		}
	}
	return peerHost(r)
}

func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// decodeJSON reads a small JSON body; on failure it answers 400 and reports
// false. The sign-in bodies are a code and a label, so 4 KiB is generous.
func decodeJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(into); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return false
	}
	return true
}

// decodeOptionalJSON is decodeJSON for a body that may be absent.
func decodeOptionalJSON(w http.ResponseWriter, r *http.Request, into any) bool {
	if r.Body == nil || r.ContentLength == 0 {
		return true
	}
	err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(into)
	if err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid body")
		return false
	}
	return true
}
