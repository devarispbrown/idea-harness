package daemon

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/idea-harness/idea-harness/internal/store"
)

// writeErr renders an error for an API client, replacing the pattern
// `writeError(w, status, err.Error())` that let the storage layer's own strings
// become the product's error messages (DX audit, gate UNEXPLAINED_ERROR):
//
//	$ idea apply CHG-99
//	idea: sql: no rows in result set        (exit 1)
//
// Three cases, in order:
//
//   - A store miss is the caller's mistake: 404 with the store's message, which
//     names the kind and the reference asked for.
//   - An error from below the domain boundary is the daemon's problem, not the
//     caller's: 500 with a generic message plus a correlation id, and the full
//     error logged server-side under that id. SQL text and absolute state paths
//     never reach the client.
//   - Everything else is a domain error whose message was written to be read —
//     "initiative is already running", "no initiatives in project". Those keep
//     their message and the status the handler chose. Masking them would have
//     turned working errors into "internal error", which is why the status stays
//     a parameter instead of being inferred here.
func (s *Server) writeErr(w http.ResponseWriter, status int, err error) {
	var nf *store.NotFoundError
	if errors.As(err, &nf) {
		writeError(w, http.StatusNotFound, nf.Error())
		return
	}
	if looksInternal(err) {
		ref := correlationID()
		s.rt.log.Error("request failed", "correlation_id", ref, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error (correlation id: "+ref+")")
		return
	}
	writeError(w, status, err.Error())
}

// looksInternal reports whether an error came from below the domain boundary
// and so must not be shown to a client verbatim. Storage and driver errors are
// the ones that leak schema and filesystem detail.
func looksInternal(err error) bool {
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}
	msg := err.Error()
	for _, marker := range []string{"sql:", "SQL logic error", "no such table", "no such column", "database is locked", "unable to open database"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// correlationID returns a short random id tying a 500 response to its log line.
func correlationID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(b[:])
}
