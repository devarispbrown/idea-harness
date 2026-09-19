package daemon

import (
	"net/http"
	"time"
)

// fingerprintPrefixLen is how much of a secret allowlist entry's fingerprint
// the listing shows: enough to tell entries apart, short of publishing the
// whole sha256 digest for no operational reason.
const fingerprintPrefixLen = 12

// secretAllowlistEntryView is the Settings-page projection of
// domain.SecretAllowlistEntry (review changes #1): fingerprint prefix, kind,
// path at the time, actor, created_at.
type secretAllowlistEntryView struct {
	ID                string    `json:"id"`
	FingerprintPrefix string    `json:"fingerprint_prefix"`
	Kind              string    `json:"kind"`
	Path              string    `json:"path"`
	Actor             string    `json:"actor"`
	CreatedAt         time.Time `json:"created_at"`
}

// handleListSecretAllowlist lists a project's "not a secret" overrides, for
// the wave-3 Settings page.
func (s *Server) handleListSecretAllowlist(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	entries, err := pr.db.ListSecretAllowlist(r.Context())
	if err != nil {
		s.writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]secretAllowlistEntryView, 0, len(entries))
	for _, e := range entries {
		prefix := e.Fingerprint
		if len(prefix) > fingerprintPrefixLen {
			prefix = prefix[:fingerprintPrefixLen]
		}
		out = append(out, secretAllowlistEntryView{
			ID:                e.ID,
			FingerprintPrefix: prefix,
			Kind:              e.Kind,
			Path:              e.Path,
			Actor:             e.Actor,
			CreatedAt:         e.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleRevokeSecretAllowlist removes one override, so the sweep denies that
// fingerprint again from the next execute or apply.
func (s *Server) handleRevokeSecretAllowlist(w http.ResponseWriter, r *http.Request) {
	pr, err := s.project(r)
	if err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	if err := pr.db.RevokeSecretAllowlist(r.Context(), r.PathValue("id")); err != nil {
		s.writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}
