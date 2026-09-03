package daemon

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"

	"github.com/devarispbrown/idea-harness/internal/store"
)

// writeErr renders an error for an API client, replacing the pattern
// `writeError(w, status, err.Error())` that let the storage layer's own strings
// become the product's error messages (DX audit, gate UNEXPLAINED_ERROR):
//
//	$ idea apply CHG-99
//	idea: sql: no rows in result set        (exit 1)
//
// Four cases, in order:
//
//   - A store miss is the caller's mistake: 404 with the store's message, which
//     names the kind and the reference asked for.
//   - A driver or filesystem failure (store.IsStorageFailure, a type check on
//     *sqlite.Error and *fs.PathError) is the daemon's problem, not the
//     caller's: 500 with a generic message plus a correlation id, and the full
//     error logged server-side under that id. That keeps constraint text,
//     schema names and absolute state paths out of the response.
//   - A *PreconditionError is the caller asking for something the current state
//     does not allow: 409, whatever status the handler passed. Added when advance
//     and revise were found returning 500 for the condition four sibling
//     handlers returned 409 for.
//   - Everything else is a domain error whose message was written to be read —
//     "answer is required", "each node must be type:id". Those keep
//     their message and the status the handler chose. Masking them would have
//     turned working errors into "internal error", which is why the status stays
//     a parameter instead of being inferred here.
func (s *Server) writeErr(w http.ResponseWriter, status int, err error) {
	var nf *store.NotFoundError
	if errors.As(err, &nf) {
		writeError(w, http.StatusNotFound, nf.Error())
		return
	}
	// A lookup that reached here without being wrapped by the store is still a
	// miss, not a server fault: 404, and a generic message rather than the
	// driver's. (Wrapping every lookup is the real fix; this keeps an unwrapped
	// one from being reported as a 500.)
	if errors.Is(err, sql.ErrNoRows) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	// A precondition failure is the caller asking for something the current
	// state does not allow: 409, whatever status the handler guessed. Checked
	// after the store cases so a not-found stays a 404, and before the storage
	// case so a genuine driver fault still masks itself.
	var pre *PreconditionError
	if errors.As(err, &pre) {
		writeError(w, http.StatusConflict, pre.Error())
		return
	}
	if store.IsStorageFailure(err) {
		ref := correlationID()
		s.rt.log.Error("request failed", "correlation_id", ref, "error", err)
		writeError(w, http.StatusInternalServerError, "internal error (correlation id: "+ref+")")
		return
	}
	writeError(w, status, err.Error())
}

// correlationID returns a short random id tying a 500 response to its log line.
func correlationID() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "unavailable"
	}
	return hex.EncodeToString(b[:])
}
