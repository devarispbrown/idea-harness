package daemon

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// newCaptureToken is a capture token: 256 random bits, base64url. The daemon
// keeps only its SHA-256 (same shape as the push renewal token).
func newCaptureToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// MintCaptureToken mints a capture token for a project: generates the secret,
// stores its SHA-256 in global.db, and returns the secret exactly once. The
// caller is the one who shows it (CLI/Settings), never a read path.
func (r *Runtime) MintCaptureToken(ctx context.Context, projectID, label string) (string, error) {
	if _, err := r.global.GetProject(ctx, projectID); err != nil {
		return "", err
	}
	token, err := newCaptureToken()
	if err != nil {
		return "", err
	}
	if _, err := r.global.CreateCaptureToken(ctx, projectID, hashSecret(token), label); err != nil {
		return "", err
	}
	return token, nil
}

// ListCaptureTokens returns a project's capture tokens as metadata (id, label,
// created/last-used) — never the secret or its hash.
func (r *Runtime) ListCaptureTokens(ctx context.Context, projectID string) ([]store.CaptureToken, error) {
	return r.global.ListCaptureTokens(ctx, projectID)
}

// CaptureInitiative creates a draft initiative from a brief, authorised by the
// capture token alone (the unauthenticated route). The pipeline does NOT start:
// the draft waits for a human to open it and press Start.
func (r *Runtime) CaptureInitiative(ctx context.Context, token, brief string) (string, error) {
	projectID, err := r.projectForCaptureToken(ctx, token)
	if err != nil {
		return "", err
	}
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	brief = strings.TrimSpace(brief)
	if brief == "" {
		return "", ingestError{http.StatusBadRequest, "brief is required"}
	}
	ini := &domain.Initiative{
		Title:       truncateRunes(pr.redactor.RedactString(captureTitle(brief)), 200),
		Description: pr.redactor.RedactString(brief),
		Status:      domain.InitiativeDraft,
	}
	if err := pr.db.CreateInitiative(ctx, ini); err != nil {
		return "", err
	}
	return ini.ID, nil
}

// projectForCaptureToken resolves a token to the project it authorizes. One
// refusal for every failure mode — unknown token, or a token whose project the
// daemon does not serve — so the unauthenticated route reveals nothing.
func (r *Runtime) projectForCaptureToken(ctx context.Context, token string) (string, error) {
	if token == "" {
		return "", store.ErrCaptureTokenUnknown
	}
	projectID, err := r.global.LookupCaptureToken(ctx, hashSecret(token))
	if err != nil {
		return "", store.ErrCaptureTokenUnknown
	}
	if _, err := r.global.GetProject(ctx, projectID); err != nil {
		return "", store.ErrCaptureTokenUnknown
	}
	return projectID, nil
}

// StartDraftInitiative runs the pipeline on a captured draft: it claims the
// initiative running from `draft` and launches the M1 workflow, exactly as
// `idea new` does for a freshly created initiative. A non-draft status is a
// conflict.
func (r *Runtime) StartDraftInitiative(ctx context.Context, projectID, initiativeID string) (string, error) {
	pr, err := r.EnsureProject(ctx, projectID)
	if err != nil {
		return "", err
	}
	ini, err := pr.db.GetInitiative(ctx, initiativeID)
	if err != nil {
		return "", err
	}
	if ini.Status != domain.InitiativeDraft {
		return "", preconditionf("only a draft can be started; it is %s", ini.Status)
	}
	if err := pr.db.ClaimInitiativeRunning(ctx, initiativeID, domain.InitiativeDraft); err != nil {
		return "", err
	}
	opID, err := pr.db.CreateOperation(ctx, projectID, initiativeID, "run_initiative")
	if err != nil {
		r.failOperation(opID, initiativeID, "could not create operation", context.WithoutCancel(ctx), pr, err, true)
		return "", err
	}
	r.launchOperation(opID, func(bg context.Context) {
		status, runErr := pr.engine.Run(bg, initiativeID, ini.Description)
		if runErr != nil {
			r.failOperation(opID, initiativeID, "initiative run failed", bg, pr, runErr, true)
			return
		}
		pr.db.SetOperationStatus(context.WithoutCancel(bg), opID, domain.OperationSucceeded, initiativeID)
		r.log.Info("initiative run complete", "initiative", initiativeID, "status", status)
	})
	return opID, nil
}

// captureTitle is the first non-empty line of a captured brief, the same rule
// `idea new` uses for a title.
func captureTitle(brief string) string {
	for _, line := range strings.Split(brief, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return brief
}

// handleCapture is the unauthenticated capture route: POST /api/v1/capture with
// {"token","brief"}. Outside auth like push renewal; every refusal is the same
// 401 so the route reveals nothing about which tokens or projects exist.
func (s *Server) handleCapture(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusForbidden, "capture requires a JSON body")
		return
	}
	var body struct {
		Token string `json:"token"`
		Brief string `json:"brief"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDocBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	iniID, err := s.rt.CaptureInitiative(r.Context(), body.Token, body.Brief)
	if err != nil {
		if errors.Is(err, store.ErrCaptureTokenUnknown) {
			writeError(w, http.StatusUnauthorized, "capture refused")
			return
		}
		var ie ingestError
		if errors.As(err, &ie) {
			writeError(w, ie.status, ie.msg)
			return
		}
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.rt.log.Info("capture created a draft", "initiative", iniID)
	writeJSON(w, http.StatusCreated, map[string]string{"initiative_id": iniID})
}

// handleMintCaptureToken mints a capture token and returns it exactly once.
func (s *Server) handleMintCaptureToken(w http.ResponseWriter, r *http.Request) {
	if _, err := s.project(r); err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	var body struct {
		Label string `json:"label"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxDocBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	token, err := s.rt.MintCaptureToken(r.Context(), r.PathValue("projectId"), body.Label)
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"token": token})
}

// handleListCaptureTokens lists a project's capture tokens as metadata, never
// the secret or its hash.
func (s *Server) handleListCaptureTokens(w http.ResponseWriter, r *http.Request) {
	if _, err := s.project(r); err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	tokens, err := s.rt.ListCaptureTokens(r.Context(), r.PathValue("projectId"))
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, tokens)
}

// handleStartDraftInitiative runs a captured draft's pipeline.
func (s *Server) handleStartDraftInitiative(w http.ResponseWriter, r *http.Request) {
	if _, err := s.project(r); err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	opID, err := s.rt.StartDraftInitiative(r.Context(), r.PathValue("projectId"), r.PathValue("id"))
	if err != nil {
		s.writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"operation_id": opID})
}
