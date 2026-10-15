// Package integrations is the daemon's integration event bus: it turns the
// project's append-only event log -- the same source the SSE endpoint streams
// -- into a small vocabulary of typed domain events, and delivers them to
// subscribers (Slack, Linear) through a persistent outbox.
//
// The engine never calls into this package. Every state change already
// appends an event in the same SQLite transaction as the change itself
// (store.Apply), so the bus only has to TAIL that log: a slow or down Slack
// can delay a notification but can never hold a workflow transaction open,
// fail an apply, or lose an event the engine committed. Delivery is
// at-least-once per (subscriber, event), made effectively-once by a unique
// idempotency key on the outbox row; see Dispatcher.
package integrations

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Kind is one domain event type the bus publishes. The vocabulary is pinned
// to config.IntegrationEventKinds (what harness.yaml may name) by a test.
type Kind string

const (
	KindInitiativeCreated Kind = "initiative.created"
	KindNeedsJudgment     Kind = "initiative.needs_judgment"
	KindPRDApproved       Kind = "review.prd_approved"
	KindPlanApproved      Kind = "review.plan_approved"
	KindChangeSetCreated  Kind = "change_set.created"
	KindChangeSetReady    Kind = "change_set.ready_for_review"
	KindChangeSetApproved Kind = "change_set.approved"
	KindChangeSetRejected Kind = "change_set.rejected"
	KindChangeSetApplied  Kind = "change_set.applied"
	KindChangeSetReverted Kind = "change_set.reverted"
	KindVerifyPassed      Kind = "verify.passed"
	KindVerifyFailed      Kind = "verify.failed"
	KindRunFailed         Kind = "run.failed"
	KindTaskStatusChanged Kind = "task.status_changed"
	// KindOutcomeDrifted: an observation moved an outcome metric into
	// off_track or regressed (Status carries which).
	KindOutcomeDrifted Kind = "outcome.drifted"
	// KindBudgetExceeded: an initiative reached its spending cap and the
	// engine stopped before the next model or agent call (Reason carries
	// "$spent of $cap (before <call>)"). It is published instead of the
	// needs_judgment event the same stop produces, not as well.
	KindBudgetExceeded Kind = "initiative.budget_exceeded"
	// KindEvalRegressed: a scheduled `idea eval` run regressed against its
	// baseline (Reason summarizes the regressions). Project-level: no
	// initiative.
	KindEvalRegressed Kind = "eval.regressed"
)

// AllKinds lists every Kind, in the order config.IntegrationEventKinds does.
var AllKinds = []Kind{
	KindInitiativeCreated, KindNeedsJudgment, KindPRDApproved, KindPlanApproved,
	KindChangeSetCreated, KindChangeSetReady, KindChangeSetApproved, KindChangeSetRejected,
	KindChangeSetApplied, KindChangeSetReverted, KindVerifyPassed, KindVerifyFailed,
	KindRunFailed, KindTaskStatusChanged, KindOutcomeDrifted, KindBudgetExceeded, KindEvalRegressed,
}

// Event is one typed domain event. It is a SNAPSHOT taken when the bus
// ingested the underlying log row and is persisted as the outbox payload, so
// a delivery retried an hour later still describes the moment it happened
// rather than whatever the initiative looks like now.
type Event struct {
	// Seq and SourceEventID identify the event-log row this was derived from.
	Seq           int64     `json:"seq"`
	SourceEventID string    `json:"source_event_id"`
	Kind          Kind      `json:"kind"`
	ProjectID     string    `json:"project_id"`
	Actor         string    `json:"actor,omitempty"`
	At            time.Time `json:"at"`

	InitiativeID    string `json:"initiative_id,omitempty"`
	InitiativeTitle string `json:"initiative_title,omitempty"`
	// Reason is the judgment reason (needs_judgment), the failure reason
	// (run.failed), or the evidence note (verify.failed).
	Reason string `json:"reason,omitempty"`

	ChangeSetID  string `json:"change_set_id,omitempty"`
	ChangeSetRef string `json:"change_set_ref,omitempty"` // CHG-n

	TaskID    string `json:"task_id,omitempty"`
	TaskCode  string `json:"task_code,omitempty"`
	TaskTitle string `json:"task_title,omitempty"`
	// Status is the new task status (task.status_changed), or the metric's
	// new drift status (outcome.drifted).
	Status string `json:"status,omitempty"`

	RequirementCode string `json:"requirement_code,omitempty"`

	// MetricCode/MetricName/ObjectiveCode name the drifting metric
	// (outcome.drifted); Status is its new drift status.
	MetricCode    string `json:"metric_code,omitempty"`
	MetricName    string `json:"metric_name,omitempty"`
	ObjectiveCode string `json:"objective_code,omitempty"`
}

// IdempotencyKey is the outbox key for delivering ev to subscriber. The
// source event id is a ULID unique across the log, and one log row can yield
// more than one Kind, so both are part of the key.
func (ev Event) IdempotencyKey(subscriber string) string {
	return fmt.Sprintf("%s:%s:%s", subscriber, ev.Kind, ev.SourceEventID)
}

// Subscriber receives domain events. Deliver is called from the dispatcher's
// own goroutine, one event at a time per subscriber, in event-log order.
type Subscriber interface {
	// Name is stable across restarts: it keys the cursor and the outbox.
	Name() string
	// Wants filters at ingest time; an unwanted event is never queued.
	Wants(Event) bool
	// Deliver sends one event. A nil error marks it delivered. A
	// PermanentError dead-letters it at once; anything else is retried with
	// backoff, honouring a RetryAfter hint when the error carries one.
	Deliver(ctx context.Context, ev Event) error
}

// PermanentError marks a delivery failure retrying cannot fix (a revoked
// token, a channel the bot is not in, a malformed request).
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent wraps err as a PermanentError (nil stays nil).
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// IsPermanent reports whether err is, or wraps, a PermanentError.
func IsPermanent(err error) bool {
	var p *PermanentError
	return errors.As(err, &p)
}

// RetryAfterError carries a server's own backoff hint (an HTTP 429's
// Retry-After, Linear's rate-limit reset).
type RetryAfterError struct {
	Err   error
	After time.Duration
}

func (e *RetryAfterError) Error() string { return e.Err.Error() }
func (e *RetryAfterError) Unwrap() error { return e.Err }

// RetryAfter returns the hint carried by err, if any.
func RetryAfter(err error) (time.Duration, bool) {
	var r *RetryAfterError
	if errors.As(err, &r) && r.After > 0 {
		return r.After, true
	}
	return 0, false
}
