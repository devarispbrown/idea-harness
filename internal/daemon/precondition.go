package daemon

import "fmt"

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
// Deliberately narrow. Only Runtime.*Initiative precondition checks return this
// type, never internal/workflow. Fourteen handler call sites pass
// http.StatusBadRequest for engine errors (ReviewChangeSet and VerifyInitiative
// among them); retyping an error those paths can surface would silently flip a
// 400 into a 409, so the type is applied to an enumerated set and TestPrecondition
// pins both halves.
type PreconditionError struct {
	Reason string
}

func (e *PreconditionError) Error() string { return e.Reason }

// preconditionf builds a PreconditionError with a formatted reason.
func preconditionf(format string, args ...any) error {
	return &PreconditionError{Reason: fmt.Sprintf(format, args...)}
}
