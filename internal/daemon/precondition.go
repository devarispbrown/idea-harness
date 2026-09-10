package daemon

import (
	"fmt"
	"net/http"

	"github.com/devarispbrown/idea-harness/internal/store"
)

// PreconditionError marks an error as the caller asking for something the
// current state does not allow -- "initiative is already running", "advance
// requires an approved PRD". It exists so writeErr can answer 409 without each
// handler remembering to.
//
// The audit found handleAdvance and handleRevise mapping every error to 500,
// including those two, while their four siblings -- retry, approve-plan,
// reopen-phase, revise-plan -- returned 409 for the identical class. Hand-mapping
// a sixth handler would have left the seventh to get it wrong; this makes the
// classification travel with the error instead.
//
// Deliberately narrow. Only Runtime.* precondition checks return this type,
// never internal/workflow. Handler call sites pass http.StatusBadRequest for
// engine errors; retyping an error those paths can surface would silently flip
// a 400 into a 409, so the type is applied to an enumerated set and
// TestPrecondition pins both halves.
//
// Runtime.RunVerificationGate joined that set when `idea verify` stopped being
// able to complete a failed initiative. Note what did and did not change:
// Engine.VerifyInitiative's own errors still reach writeErr as plain errors and
// still answer 400. What carries this type is the Runtime's own read of the
// status, and the translation of store.ErrInitiativeNotClaimable, both of which
// are preconditions on the request rather than failures inside the gate.
type PreconditionError struct {
	Reason string
}

func (e *PreconditionError) Error() string { return e.Reason }

// preconditionf builds a PreconditionError with a formatted reason.
func preconditionf(format string, args ...any) error {
	return &PreconditionError{Reason: fmt.Sprintf(format, args...)}
}

// writeReloadErr answers a failed project reload after an executor config write.
//
// This was the last raw err.Error() reaching a client in this package.
// ReloadProject wraps *fs.PathError from config.Load and blob.New, so on a
// permissions problem it printed an absolute state path at the user (DX audit).
//
// It cannot simply go through writeErr. store.IsStorageFailure uses errors.As,
// so wrapping the cause with %w keeps it detectable and writeErr would answer
// 500 "internal error (correlation id: …)" -- losing the one thing the caller
// actually needs to know, which is that their config file was written but the
// running daemon is still on the old configuration. So classify first, then
// respond, and keep the 409 in both branches.
func (s *Server) writeReloadErr(w http.ResponseWriter, err error) {
	const wrote = "config written but not applied (reload failed)"
	if store.IsStorageFailure(err) {
		ref := correlationID()
		s.rt.log.Error("executor config reload failed", "correlation_id", ref, "error", err)
		writeError(w, http.StatusConflict, wrote+"; the daemon kept the previous "+
			"configuration — see `idea logs` (correlation id: "+ref+")")
		return
	}
	// A domain error here is worth reading verbatim: ReloadProject's own is
	// "cannot reload project while N operation(s) are running".
	s.writeErr(w, http.StatusConflict, fmt.Errorf("%s: %w", wrote, err))
}
