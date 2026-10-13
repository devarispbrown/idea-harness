package integrations

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/store"
)

// Lookup is the read access the translator needs to enrich a bare log row.
// *store.DB satisfies it.
type Lookup interface {
	GetInitiative(ctx context.Context, initiativeID string) (domain.Initiative, error)
	GetChangeSet(ctx context.Context, changeSetID string) (*domain.ChangeSet, error)
	GetTask(ctx context.Context, taskID string) (*domain.Task, error)
	PreviousInitiativeStatus(ctx context.Context, initiativeID string, beforeSeq int64, skipRunning bool) (string, error)
	GetVerificationEvidenceRef(ctx context.Context, evidenceID string) (*store.VerificationEvidenceRef, error)
}

// Translate maps one event-log row to zero or more typed domain events.
//
// It is deliberately a pure function of the log plus point lookups: the log
// row carries the fact (its Operation is the new status, written in the same
// transaction as the change), and the lookups only add display context
// (titles, CHG-n). A lookup that finds nothing -- the row was deleted since,
// e.g. a task removed by a plan revision -- yields no event rather than an
// error, because there is nothing left to describe and a hard error would
// wedge the cursor on that row forever.
func Translate(ctx context.Context, lk Lookup, projectID string, row store.EventRow) ([]Event, error) {
	base := Event{
		Seq: row.Seq, SourceEventID: row.ID, ProjectID: projectID,
		Actor: row.Actor, At: row.CreatedAt().UTC(),
	}
	evs, err := translate(ctx, lk, base, row)
	if isNotFound(err) {
		return nil, nil
	}
	return evs, err
}

func translate(ctx context.Context, lk Lookup, ev Event, row store.EventRow) ([]Event, error) {
	switch row.EventType {
	case "initiative.created":
		if err := withInitiative(ctx, lk, &ev, row.AggregateID); err != nil {
			return nil, err
		}
		ev.Kind = KindInitiativeCreated
		return []Event{ev}, nil

	case "initiative.failed":
		return initiativeStatus(ctx, lk, ev, row, string(domain.InitiativeFailed), row.Operation)

	case "initiative.status_changed":
		status, reason, _ := strings.Cut(row.Operation, ":")
		return initiativeStatus(ctx, lk, ev, row, status, reason)

	case "change_set.created":
		return changeSet(ctx, lk, ev, row.AggregateID, KindChangeSetCreated)

	case "change_set.status_changed":
		_, to, ok := strings.Cut(row.Operation, "->")
		if !ok {
			return nil, nil
		}
		switch domain.ChangeSetStatus(to) {
		case domain.ChangeSetReviewRequired:
			return changeSet(ctx, lk, ev, row.AggregateID, KindChangeSetReady)
		case domain.ChangeSetApproved:
			return changeSet(ctx, lk, ev, row.AggregateID, KindChangeSetApproved)
		case domain.ChangeSetRejected:
			return changeSet(ctx, lk, ev, row.AggregateID, KindChangeSetRejected)
		}
		return nil, nil

	case "change_set.applied":
		return changeSet(ctx, lk, ev, row.AggregateID, KindChangeSetApplied)

	case "change_set.reverted":
		return changeSet(ctx, lk, ev, row.AggregateID, KindChangeSetReverted)

	case "verification_evidence.created":
		if row.Operation != string(domain.VerificationFail) {
			return nil, nil
		}
		ref, err := lk.GetVerificationEvidenceRef(ctx, row.AggregateID)
		if err != nil {
			return nil, err
		}
		if err := withInitiative(ctx, lk, &ev, ref.InitiativeID); err != nil {
			return nil, err
		}
		ev.Kind, ev.RequirementCode, ev.Reason = KindVerifyFailed, ref.RequirementCode, ref.Note
		return []Event{ev}, nil

	case "task.status_changed":
		t, err := lk.GetTask(ctx, row.AggregateID)
		if err != nil {
			return nil, err
		}
		if err := withInitiative(ctx, lk, &ev, t.InitiativeID); err != nil {
			return nil, err
		}
		ev.Kind, ev.TaskID, ev.TaskCode, ev.TaskTitle, ev.Status = KindTaskStatusChanged, t.ID, t.Code, t.Title, row.Operation
		return []Event{ev}, nil
	}
	return nil, nil
}

// initiativeStatus handles the two shapes of an initiative status event.
//
// Milestones a claim release returns TO (approved, plan_approved, complete)
// are compared with the previous NON-running status: plan_approved -> running
// -> plan_approved is an apply's claim and release, not a second approval.
// failed and needs_human_judgment are compared with the immediately previous
// status, running included: retry -> running -> failed again IS news, but
// FailInitiative's own event followed by a release to `failed` is one failure.
func initiativeStatus(ctx context.Context, lk Lookup, ev Event, row store.EventRow, status, reason string) ([]Event, error) {
	var kind Kind
	skipRunning := true
	switch domain.InitiativeStatus(status) {
	case domain.InitiativeApproved:
		kind = KindPRDApproved
	case domain.InitiativePlanApproved:
		kind = KindPlanApproved
	case domain.InitiativeComplete:
		kind = KindVerifyPassed
	case domain.InitiativeFailed:
		kind, skipRunning = KindRunFailed, false
	case domain.InitiativeNeedsHumanJudgment:
		kind, skipRunning = KindNeedsJudgment, false
	default:
		return nil, nil
	}
	prev, err := lk.PreviousInitiativeStatus(ctx, row.AggregateID, row.Seq, skipRunning)
	if err != nil {
		return nil, err
	}
	prevStatus, _, _ := strings.Cut(prev, ":")
	if prevStatus == status {
		return nil, nil
	}
	ini, err := lk.GetInitiative(ctx, row.AggregateID)
	if err != nil {
		return nil, err
	}
	setInitiative(&ev, ini)
	ev.Kind = kind
	switch kind {
	case KindNeedsJudgment:
		ev.Reason = reason
		if ev.Reason == "" {
			ev.Reason = string(ini.JudgmentReason)
		}
	case KindRunFailed:
		ev.Reason = reason
		if ev.Reason == "" || ev.Reason == string(domain.InitiativeFailed) {
			ev.Reason = ini.FailureReason
		}
	}
	return []Event{ev}, nil
}

func changeSet(ctx context.Context, lk Lookup, ev Event, changeSetID string, kind Kind) ([]Event, error) {
	cs, err := lk.GetChangeSet(ctx, changeSetID)
	if err != nil {
		return nil, err
	}
	if err := withInitiative(ctx, lk, &ev, cs.InitiativeID); err != nil {
		return nil, err
	}
	ev.Kind, ev.ChangeSetID, ev.ChangeSetRef = kind, cs.ID, cs.HumanID
	if cs.TaskID != "" {
		if t, err := lk.GetTask(ctx, cs.TaskID); err == nil {
			ev.TaskID, ev.TaskCode, ev.TaskTitle = t.ID, t.Code, t.Title
		} else if !isNotFound(err) {
			return nil, err
		} else {
			ev.TaskID = cs.TaskID
		}
	}
	return []Event{ev}, nil
}

func withInitiative(ctx context.Context, lk Lookup, ev *Event, initiativeID string) error {
	ini, err := lk.GetInitiative(ctx, initiativeID)
	if err != nil {
		return err
	}
	setInitiative(ev, ini)
	return nil
}

func setInitiative(ev *Event, ini domain.Initiative) {
	ev.InitiativeID = ini.ID
	ev.InitiativeTitle = ini.ShortTitle
	if ev.InitiativeTitle == "" {
		ev.InitiativeTitle = ini.Title
	}
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, sql.ErrNoRows)
}
