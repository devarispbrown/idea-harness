package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/idea-harness/idea-harness/internal/domain"
	"github.com/idea-harness/idea-harness/internal/id"
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

// MarkRequirementVerified transitions a requirement approved→verified and stamps
// verified_at. It fails if the requirement is not in the approved state.
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
		Operation:     "approved->verified",
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE requirements SET status = ?, verified_at = ? WHERE id = ? AND status = ?`,
			domain.RequirementVerified, now.UnixMilli(), requirementID, domain.RequirementApproved)
		if err != nil {
			return err
		}
		return requireOneRow(res, "requirement %q not in state approved", requirementID)
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
	return nil, fmt.Errorf("store: requirement %q not found", ref)
}
