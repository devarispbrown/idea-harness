package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/id"
)

// CreateVerificationEvidence inserts one evidence row (§70, review B4/B10).
func (db *DB) CreateVerificationEvidence(ctx context.Context, e *domain.VerificationEvidence) error {
	if e.ID == "" {
		e.ID = id.New()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	if !e.Kind.Valid() {
		return fmt.Errorf("store: invalid verification kind %q", e.Kind)
	}
	if !e.Status.Valid() {
		return fmt.Errorf("store: invalid verification status %q", e.Status)
	}
	return db.Apply(ctx, Event{
		AggregateType: "verification_evidence",
		AggregateID:   e.ID,
		Actor:         ActorOrchestrator,
		EventType:     "verification_evidence.created",
		Operation:     string(e.Status),
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO verification_evidence
				(id, initiative_id, requirement_id, criterion_key, kind, status, evidence_digest, run_id, verified_by, note, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			e.ID, e.InitiativeID, e.RequirementID, e.CriterionKey, e.Kind, e.Status,
			e.EvidenceDigest, e.RunID, e.VerifiedBy, e.Note, e.CreatedAt.UnixMilli())
		return err
	})
}

// ListEvidenceByRequirement returns evidence for a requirement, oldest first.
func (db *DB) ListEvidenceByRequirement(ctx context.Context, requirementID string) ([]domain.VerificationEvidence, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, requirement_id, criterion_key, kind, status, evidence_digest, run_id, verified_by, note, created_at
		FROM verification_evidence WHERE requirement_id = ? ORDER BY created_at ASC`, requirementID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.VerificationEvidence, 0)
	for rows.Next() {
		var e domain.VerificationEvidence
		var created int64
		if err := rows.Scan(&e.ID, &e.InitiativeID, &e.RequirementID, &e.CriterionKey, &e.Kind, &e.Status,
			&e.EvidenceDigest, &e.RunID, &e.VerifiedBy, &e.Note, &created); err != nil {
			return nil, err
		}
		e.CreatedAt = time.UnixMilli(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

// PassedCriterionKeys returns the set of criterion keys that have at least one
// pass evidence for a requirement.
func (db *DB) PassedCriterionKeys(ctx context.Context, requirementID string) (map[string]bool, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT criterion_key FROM verification_evidence
		WHERE requirement_id = ? AND status = ?`, requirementID, domain.VerificationPass)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// MarkRequirementVerified transitions a requirement approved/failed/waived→verified
// and stamps verified_at. A human re-pass clears a prior fail or waiver and
// returns the requirement to the verified set. It fails if the requirement is
// not in one of those three source states.
func (db *DB) MarkRequirementVerified(ctx context.Context, requirementID, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	now := time.Now().UTC()
	return db.Apply(ctx, Event{
		AggregateType: "requirement",
		AggregateID:   requirementID,
		Actor:         actor,
		EventType:     "requirement.verified",
		Operation:     "->verified",
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE requirements SET status = ?, verified_at = ? WHERE id = ? AND status IN (?, ?, ?)`,
			domain.RequirementVerified, now.UnixMilli(), requirementID,
			domain.RequirementApproved, domain.RequirementFailed, domain.RequirementWaived)
		if err != nil {
			return err
		}
		return requireOneRow(res, "requirement %q not in an approvable state", requirementID)
	})
}

// MarkRequirementFailed transitions a requirement to failed from any approvable
// state (approved/verified/waived/failed — re-issue is idempotent). A failed
// requirement stays in the active set and blocks completion until a human
// re-records a pass.
func (db *DB) MarkRequirementFailed(ctx context.Context, requirementID, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	return db.Apply(ctx, Event{
		AggregateType: "requirement",
		AggregateID:   requirementID,
		Actor:         actor,
		EventType:     "requirement.failed",
		Operation:     "->failed",
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE requirements SET status = ?, verified_at = NULL WHERE id = ? AND status IN (?, ?, ?, ?)`,
			domain.RequirementFailed, requirementID,
			domain.RequirementApproved, domain.RequirementVerified, domain.RequirementWaived, domain.RequirementFailed)
		if err != nil {
			return err
		}
		return requireOneRow(res, "requirement %q not in an approvable state", requirementID)
	})
}

// MarkRequirementWaived transitions a requirement to waived from any approvable
// state (approved/verified/failed/waived — re-issue is idempotent). A waived
// requirement is excluded from the completion gate (like removed) but remains
// visible and recorded as a human judgment, not a deletion.
func (db *DB) MarkRequirementWaived(ctx context.Context, requirementID, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	return db.Apply(ctx, Event{
		AggregateType: "requirement",
		AggregateID:   requirementID,
		Actor:         actor,
		EventType:     "requirement.waived",
		Operation:     "->waived",
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE requirements SET status = ?, verified_at = NULL WHERE id = ? AND status IN (?, ?, ?, ?)`,
			domain.RequirementWaived, requirementID,
			domain.RequirementApproved, domain.RequirementVerified, domain.RequirementFailed, domain.RequirementWaived)
		if err != nil {
			return err
		}
		return requireOneRow(res, "requirement %q not in an approvable state", requirementID)
	})
}

// ClearVerificationEvidence deletes every evidence row for a requirement. A new
// human judgment (pass/fail/waived) supersedes all prior evidence for the
// requirement's criteria — otherwise a stale pass row would resurrect a
// requirement that a human has since failed or waived (evidence is otherwise
// append-only and PassedCriterionKeys counts every pass regardless of newer rows).
func (db *DB) ClearVerificationEvidence(ctx context.Context, requirementID, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	return db.Apply(ctx, Event{
		AggregateType: "verification_evidence",
		AggregateID:   requirementID,
		Actor:         actor,
		EventType:     "verification_evidence.cleared",
		Operation:     "human judgment supersedes prior evidence",
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM verification_evidence WHERE requirement_id = ?`, requirementID)
		return err
	})
}

// RecordHumanEvidence atomically supersedes a requirement's prior
// verification evidence with a fresh human judgment across every one of its
// acceptanceCriteria acceptance criteria, plus the resulting requirement
// status transition (fail/waived only -- a pass leaves status alone;
// VerifyInitiative promotes it separately once every criterion is covered),
// all in ONE transaction (review MEDIUM: RecordHumanEvidence atomicity).
//
// Before this, workflow.Engine.RecordHumanEvidence ran the equivalent
// sequence -- ClearVerificationEvidence, one CreateVerificationEvidence per
// criterion, then an optional MarkRequirementFailed/MarkRequirementWaived --
// as N+2 separate transactions. A crash (or a Stop) between any two of them
// destroyed the prior judgment without recording the new one: zero evidence
// rows left standing under whatever status the requirement had before the
// call, most visibly `verified` surviving over no evidence at all. One
// transaction makes the whole judgment atomic: either the old evidence is
// gone AND the new evidence AND status transition are all in place, or
// nothing here changed.
//
// Matches the merge/split precedent (store/merge.go, store/split.go) for a
// compound write: one event for the whole operation, not one per row.
func (db *DB) RecordHumanEvidence(ctx context.Context, initiativeID, requirementID string, acceptanceCriteria int, status domain.VerificationStatus, note, actor string) error {
	if actor == "" {
		actor = HumanActor
	}
	switch status {
	case domain.VerificationPass, domain.VerificationFail, domain.VerificationWaived:
	default:
		return fmt.Errorf("store: invalid human verification status %q", status)
	}
	if acceptanceCriteria <= 0 {
		return fmt.Errorf("store: requirement %q has no acceptance criteria to verify", requirementID)
	}
	return db.Apply(ctx, Event{
		AggregateType: "requirement",
		AggregateID:   requirementID,
		Actor:         actor,
		EventType:     "requirement.human_evidence_recorded",
		Operation:     string(status),
	}, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM verification_evidence WHERE requirement_id = ?`, requirementID); err != nil {
			return err
		}
		now := time.Now().UTC().UnixMilli()
		for i := 0; i < acceptanceCriteria; i++ {
			if _, err := tx.Exec(`
				INSERT INTO verification_evidence
					(id, initiative_id, requirement_id, criterion_key, kind, status, evidence_digest, run_id, verified_by, note, created_at)
				VALUES (?, ?, ?, ?, ?, ?, '', '', ?, ?, ?)`,
				id.New(), initiativeID, requirementID, domain.CriterionKey(requirementID, i),
				domain.VerificationAcceptance, status, actor, note, now); err != nil {
				return err
			}
		}
		switch status {
		case domain.VerificationFail:
			res, err := tx.Exec(`UPDATE requirements SET status = ?, verified_at = NULL WHERE id = ? AND status IN (?, ?, ?, ?)`,
				domain.RequirementFailed, requirementID,
				domain.RequirementApproved, domain.RequirementVerified, domain.RequirementWaived, domain.RequirementFailed)
			if err != nil {
				return err
			}
			return requireOneRow(res, "requirement %q not in an approvable state", requirementID)
		case domain.VerificationWaived:
			res, err := tx.Exec(`UPDATE requirements SET status = ?, verified_at = NULL WHERE id = ? AND status IN (?, ?, ?, ?)`,
				domain.RequirementWaived, requirementID,
				domain.RequirementApproved, domain.RequirementVerified, domain.RequirementFailed, domain.RequirementWaived)
			if err != nil {
				return err
			}
			return requireOneRow(res, "requirement %q not in an approvable state", requirementID)
		}
		return nil
	})
}

// UnverifyRequirement resets a verified requirement back to approved and clears
// verified_at (e.g. when the change set that verified it is reverted).
func (db *DB) UnverifyRequirement(ctx context.Context, requirementID, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	return db.Apply(ctx, Event{
		AggregateType: "requirement",
		AggregateID:   requirementID,
		Actor:         actor,
		EventType:     "requirement.unverified",
		Operation:     "verified->approved",
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE requirements SET status = ?, verified_at = NULL WHERE id = ? AND status = ?`,
			domain.RequirementApproved, requirementID, domain.RequirementVerified)
		if err != nil {
			return err
		}
		return requireOneRow(res, "requirement %q not in state verified", requirementID)
	})
}

// ApproveRequirements transitions all draft requirements of an initiative to
// approved (§17). Called when the PRD review passes or a human approves.
func (db *DB) ApproveRequirements(ctx context.Context, initiativeID, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	return db.Apply(ctx, Event{
		AggregateType: "requirement",
		AggregateID:   initiativeID,
		Actor:         actor,
		EventType:     "requirements.approved",
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE requirements SET status = ? WHERE initiative_id = ? AND status = ?`,
			domain.RequirementApproved, initiativeID, domain.RequirementDraft)
		return err
	})
}

// GetRequirementByRef resolves a requirement by its UUID or its Code (REQ-N)
// within an initiative.
func (db *DB) GetRequirementByRef(ctx context.Context, initiativeID, ref string) (*domain.Requirement, error) {
	reqs, err := db.ListRequirements(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	for i := range reqs {
		if reqs[i].ID == ref || reqs[i].Code == ref {
			return &reqs[i], nil
		}
	}
	// notFound, not a plain error: writeErr upgrades a *NotFoundError to 404
	// wherever it surfaces. Both callers reached here with the same text and the
	// handlers guessed different statuses -- /verify said 400, verification-task
	// said 404, byte-identical message. The store is the thing that knows a row
	// is missing, so it says so once.
	return nil, notFound("requirement", ref)
}
