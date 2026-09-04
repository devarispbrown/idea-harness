package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/id"
)

// ActorOrchestrator is the default event actor for deterministic workflow steps.
const ActorOrchestrator = "orchestrator"

// HumanActor is the actor identity for human actions (§11, §15).
const HumanActor = "human"

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		// Marshal of these simple types never fails; fail loudly otherwise.
		panic(fmt.Sprintf("store: marshal: %v", err))
	}
	return string(b)
}

// ---------- Initiative ----------

// CreateInitiative inserts an initiative and appends its creation event.
func (db *DB) CreateInitiative(ctx context.Context, ini *domain.Initiative) error {
	if ini.ID == "" {
		ini.ID = id.New()
	}
	now := time.Now().UTC()
	ini.CreatedAt = now
	ini.UpdatedAt = now
	if !ini.Status.Valid() {
		ini.Status = domain.InitiativeRunning
	}
	return db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   ini.ID,
		Actor:         ActorOrchestrator,
		EventType:     "initiative.created",
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO initiatives (id, title, description, status, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			ini.ID, ini.Title, ini.Description, ini.Status, ini.CreatedAt.UnixMilli(), ini.UpdatedAt.UnixMilli())
		return err
	})
}

// GetInitiative returns one initiative by id.
func (db *DB) GetInitiative(ctx context.Context, initiativeID string) (domain.Initiative, error) {
	var ini domain.Initiative
	var created, updated int64
	err := db.SQL().QueryRowContext(ctx, `
		SELECT id, title, description, status, failure_reason, created_at, updated_at
		FROM initiatives WHERE id = ?`, initiativeID).
		Scan(&ini.ID, &ini.Title, &ini.Description, &ini.Status, &ini.FailureReason, &created, &updated)
	if err != nil {
		return ini, notFoundIfNoRows(err, "initiative", initiativeID)
	}
	ini.CreatedAt = time.UnixMilli(created)
	ini.UpdatedAt = time.UnixMilli(updated)
	return ini, nil
}

// ListInitiatives returns all initiatives ordered by creation.
func (db *DB) ListInitiatives(ctx context.Context) ([]domain.Initiative, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, title, description, status, failure_reason, created_at, updated_at
		FROM initiatives ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Initiative, 0)
	for rows.Next() {
		var ini domain.Initiative
		var created, updated int64
		if err := rows.Scan(&ini.ID, &ini.Title, &ini.Description, &ini.Status, &ini.FailureReason, &created, &updated); err != nil {
			return nil, err
		}
		ini.CreatedAt = time.UnixMilli(created)
		ini.UpdatedAt = time.UnixMilli(updated)
		out = append(out, ini)
	}
	return out, rows.Err()
}

// FailInitiative marks an initiative failed with a reason.
func (db *DB) FailInitiative(ctx context.Context, initiativeID, reason string) error {
	return db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   initiativeID,
		Actor:         ActorOrchestrator,
		EventType:     "initiative.failed",
		Operation:     reason,
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE initiatives SET status = ?, failure_reason = ?, updated_at = ? WHERE id = ?`,
			domain.InitiativeFailed, reason, time.Now().UTC().UnixMilli(), initiativeID)
		return err
	})
}

// ClaimInitiativeRunning marks an initiative running, but only from one of the
// states given. It reports ErrInitiativeNotClaimable when no row matched.
//
// This is the atomic half of "is it already running?". Seven Runtime entry
// points asked that question and four of them never set the flag, so two
// concurrent requests both passed the check -- a double-clicked "Revise plan"
// let one goroutine delete and recreate the task set while the other wrote
// dependency edges to task ids that no longer existed, after which every later
// revise was refused.
//
// A Go-level GetInitiative-then-set cannot fix that: the read and the write are
// not atomic with each other, which is the same check-then-act the finding is
// about. SetInitiativeStatus is an unconditional UPDATE and cannot be used for a
// claim. TransitionChangeSet has been the compare-and-swap precedent in this
// package all along, and this follows it.
func (db *DB) ClaimInitiativeRunning(ctx context.Context, initiativeID string, from ...domain.InitiativeStatus) error {
	if len(from) == 0 {
		return fmt.Errorf("store: ClaimInitiativeRunning needs at least one source status")
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(from)), ",")
	args := []any{domain.InitiativeRunning, time.Now().UTC().UnixMilli(), initiativeID}
	for _, f := range from {
		args = append(args, f)
	}
	return db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   initiativeID,
		Actor:         ActorOrchestrator,
		EventType:     "initiative.status_changed",
		Operation:     string(domain.InitiativeRunning),
	}, func(tx *sql.Tx) error {
		// failure_reason is deliberately untouched. Apply and revert claim from
		// every non-running state -- `failed` included -- and release back to
		// where they found it, so clearing the reason here erased it before the
		// release could restore it: an initiative reported as failed with no
		// reason at all. The reason is cleared by the transition that actually
		// resolves the failure (SetInitiativeStatus, below), not by taking the
		// claim.
		res, err := tx.Exec(
			`UPDATE initiatives SET status = ?, updated_at = ?
			 WHERE id = ? AND status IN (`+placeholders+`)`, args...)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrInitiativeNotClaimable
		}
		return nil
	})
}

// ErrInitiativeNotClaimable reports that an initiative could not be claimed:
// another request holds it, or it is not in a state the operation runs from.
var ErrInitiativeNotClaimable = errors.New("store: initiative is not in a claimable state")

func (db *DB) SetInitiativeStatus(ctx context.Context, initiativeID string, status domain.InitiativeStatus, actor string) error {
	if !status.Valid() {
		return fmt.Errorf("store: invalid initiative status %q", status)
	}
	if actor == "" {
		actor = ActorOrchestrator
	}
	return db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   initiativeID,
		Actor:         actor,
		EventType:     "initiative.status_changed",
		Operation:     string(status),
	}, func(tx *sql.Tx) error {
		// Any transition out of "failed" clears the stale failure reason (the
		// only writer of failure_reason is FailInitiative, which sets status
		// failed in the same row update).
		//
		// Except a transition *to* failed, which is how apply and revert release
		// a claim they took from a failed initiative. Clearing there replaced a
		// real reason with an empty one and left `idea status` reporting a
		// failure with no explanation. `running` is excluded for the same
		// reason: it is the claim's own status, and the release that follows it
		// is what decides whether the reason still applies.
		if status == domain.InitiativeFailed || status == domain.InitiativeRunning {
			_, err := tx.Exec(`UPDATE initiatives SET status = ?, updated_at = ? WHERE id = ?`,
				status, time.Now().UTC().UnixMilli(), initiativeID)
			return err
		}
		_, err := tx.Exec(`UPDATE initiatives SET status = ?, failure_reason = '', updated_at = ? WHERE id = ?`,
			status, time.Now().UTC().UnixMilli(), initiativeID)
		return err
	})
}

// ---------- Assertion ----------

// CreateAssertion inserts an assertion and appends its event.
func (db *DB) CreateAssertion(ctx context.Context, a *domain.Assertion) error {
	if a.ID == "" {
		a.ID = id.New()
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	if !a.Type.Valid() {
		return fmt.Errorf("store: invalid assertion type %q", a.Type)
	}
	if !a.Status.Valid() {
		a.Status = domain.AssertionCandidate
	}
	if a.Provenance == nil {
		a.Provenance = []domain.ProvenanceRef{}
	}
	return db.Apply(ctx, Event{
		AggregateType: "assertion",
		AggregateID:   a.ID,
		Actor:         ActorOrchestrator,
		EventType:     "assertion.created",
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO assertions
				(id, initiative_id, type, status, statement, evidence_digest, confidence, source, provenance_json, produced_by_run_id, superseded_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, a.InitiativeID, a.Type, a.Status, a.Statement, a.EvidenceDigest, a.Confidence,
			a.Source, mustJSON(a.Provenance), a.ProducedByRun, a.SupersededBy, a.CreatedAt.UnixMilli())
		if err != nil {
			return err
		}
		var edges []EdgeToWrite
		if a.ProducedByRun != "" {
			edges = append(edges, EdgeToWrite{FromType: "assertion", FromID: a.ID, EdgeType: domain.EdgeProducedBy, ToType: "run", ToID: a.ProducedByRun})
		}
		return db.wireEdges(tx, a.InitiativeID, edges)
	})
}

// SupersedeAssertions marks every active assertion of an initiative as
// superseded, mirroring SupersedeRequirements, so a full workflow retry does not
// accumulate duplicate framing/research assumptions.
func (db *DB) SupersedeAssertions(ctx context.Context, initiativeID, supersededBy string) error {
	return db.Apply(ctx, Event{
		AggregateType: "assertion",
		AggregateID:   initiativeID,
		Actor:         ActorOrchestrator,
		EventType:     "assertions.superseded",
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE assertions SET status = ?, superseded_by = ? WHERE initiative_id = ? AND status != ?`,
			domain.AssertionSuperseded, supersededBy, initiativeID, domain.AssertionSuperseded)
		return err
	})
}

// ListAssertions returns assertions for an initiative.
func (db *DB) ListAssertions(ctx context.Context, initiativeID string) ([]domain.Assertion, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, type, status, statement, evidence_digest, confidence, source, provenance_json, produced_by_run_id, superseded_by, created_at
		FROM assertions WHERE initiative_id = ? ORDER BY created_at ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Assertion, 0)
	for rows.Next() {
		var a domain.Assertion
		var prov string
		var created int64
		if err := rows.Scan(&a.ID, &a.InitiativeID, &a.Type, &a.Status, &a.Statement, &a.EvidenceDigest,
			&a.Confidence, &a.Source, &prov, &a.ProducedByRun, &a.SupersededBy, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = time.UnixMilli(created)
		json.Unmarshal([]byte(prov), &a.Provenance)
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---------- Requirement ----------

// CreateRequirement inserts a requirement with a project-scoped code (REQ-xx).
func (db *DB) CreateRequirement(ctx context.Context, r *domain.Requirement) error {
	if r.ID == "" {
		r.ID = id.New()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	if !r.Status.Valid() {
		r.Status = domain.RequirementDraft
	}
	if r.Provenance == nil {
		r.Provenance = []domain.ProvenanceRef{}
	}
	return db.Apply(ctx, Event{
		AggregateType: "requirement",
		AggregateID:   r.ID,
		Actor:         ActorOrchestrator,
		EventType:     "requirement.created",
	}, func(tx *sql.Tx) error {
		if r.Code == "" {
			code, err := nextCode(tx, "requirements", "code", "REQ", r.InitiativeID)
			if err != nil {
				return err
			}
			r.Code = code
		}
		_, err := tx.Exec(`
			INSERT INTO requirements
				(id, initiative_id, code, statement, acceptance_criteria_json, priority, status, source_assertion_id, provenance_json, produced_by_run_id, superseded_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.ID, r.InitiativeID, r.Code, r.Statement, mustJSON(r.AcceptanceCriteria), r.Priority, r.Status,
			r.SourceAssertionID, mustJSON(r.Provenance), r.ProducedByRun, r.SupersededBy, r.CreatedAt.UnixMilli())
		if err != nil {
			return err
		}
		var edges []EdgeToWrite
		if r.ProducedByRun != "" {
			edges = append(edges, EdgeToWrite{FromType: "requirement", FromID: r.ID, EdgeType: domain.EdgeProducedBy, ToType: "run", ToID: r.ProducedByRun})
		}
		if r.SourceAssertionID != "" {
			edges = append(edges, EdgeToWrite{FromType: "requirement", FromID: r.ID, EdgeType: domain.EdgeDerivedFrom, ToType: "assertion", ToID: r.SourceAssertionID})
		}
		return db.wireEdges(tx, r.InitiativeID, edges)
	})
}

// ListRequirements returns requirements for an initiative.
func (db *DB) ListRequirements(ctx context.Context, initiativeID string) ([]domain.Requirement, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, code, statement, acceptance_criteria_json, priority, status, source_assertion_id, provenance_json, produced_by_run_id, superseded_by, verified_at, created_at
		FROM requirements WHERE initiative_id = ? ORDER BY created_at ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Requirement, 0)
	for rows.Next() {
		var r domain.Requirement
		var ac, prov string
		var verified sql.NullInt64
		var created int64
		if err := rows.Scan(&r.ID, &r.InitiativeID, &r.Code, &r.Statement, &ac, &r.Priority, &r.Status,
			&r.SourceAssertionID, &prov, &r.ProducedByRun, &r.SupersededBy, &verified, &created); err != nil {
			return nil, err
		}
		r.CreatedAt = time.UnixMilli(created)
		if verified.Valid {
			t := time.UnixMilli(verified.Int64)
			r.VerifiedAt = &t
		}
		json.Unmarshal([]byte(ac), &r.AcceptanceCriteria)
		json.Unmarshal([]byte(prov), &r.Provenance)
		out = append(out, r)
	}
	return out, rows.Err()
}

// SupersedeRequirements marks every active requirement of an initiative as
// removed and records the superseding run. Used by the revise step (§11).
func (db *DB) SupersedeRequirements(ctx context.Context, initiativeID, supersededBy string) error {
	return db.Apply(ctx, Event{
		AggregateType: "requirement",
		AggregateID:   initiativeID,
		Actor:         ActorOrchestrator,
		EventType:     "requirements.superseded",
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE requirements SET status = ?, superseded_by = ? WHERE initiative_id = ? AND status != ?`,
			domain.RequirementRemoved, supersededBy, initiativeID, domain.RequirementRemoved)
		return err
	})
}

// nextCode computes the next monotonic code (e.g. REQ-17) within a transaction.
// It derives from MAX(numeric suffix) so codes are never reused after a row is
// deleted (a COUNT(*)-based scheme would regress, e.g. TASK-5 → TASK-5 again).
// column is the code column name ("code" or, for change sets, "human_id").
func nextCode(tx *sql.Tx, table, column, prefix, initiativeID string) (string, error) {
	offset := len(prefix) + 2 // "PREFIX-N" → numeric part starts at len(prefix)+2
	var n int
	err := tx.QueryRow(`SELECT COALESCE(MAX(CAST(SUBSTR(`+column+`, ?) AS INTEGER)), 0) FROM `+table+` WHERE initiative_id = ?`, offset, initiativeID).Scan(&n)
	if err != nil {
		return "", err
	}
	return id.Code(prefix, n+1), nil
}

// ---------- Decision ----------

// CreateDecision inserts a decision with code DEC-xx.
func (db *DB) CreateDecision(ctx context.Context, d *domain.Decision) error {
	if d.ID == "" {
		d.ID = id.New()
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	if !d.Status.Valid() {
		d.Status = domain.DecisionOpen
	}
	if d.Provenance == nil {
		d.Provenance = []domain.ProvenanceRef{}
	}
	return db.Apply(ctx, Event{
		AggregateType: "decision",
		AggregateID:   d.ID,
		Actor:         ActorOrchestrator,
		EventType:     "decision.created",
	}, func(tx *sql.Tx) error {
		if d.Code == "" {
			code, err := nextCode(tx, "decisions", "code", "DEC", d.InitiativeID)
			if err != nil {
				return err
			}
			d.Code = code
		}
		_, err := tx.Exec(`
			INSERT INTO decisions
				(id, initiative_id, code, type, question, options_json, recommendation, status, resolution, resolved_by, provenance_json, superseded_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			d.ID, d.InitiativeID, d.Code, d.Type, d.Question, mustJSON(d.Options), d.Recommendation,
			d.Status, d.Resolution, d.ResolvedBy, mustJSON(d.Provenance), d.SupersededBy, d.CreatedAt.UnixMilli())
		return err
	})
}

// ListDecisions returns decisions for an initiative.
func (db *DB) ListDecisions(ctx context.Context, initiativeID string) ([]domain.Decision, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, code, type, question, options_json, recommendation, status, resolution, resolved_by, provenance_json, superseded_by, created_at
		FROM decisions WHERE initiative_id = ? ORDER BY created_at ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Decision, 0)
	for rows.Next() {
		var d domain.Decision
		var opts, prov string
		var created int64
		if err := rows.Scan(&d.ID, &d.InitiativeID, &d.Code, &d.Type, &d.Question, &opts, &d.Recommendation,
			&d.Status, &d.Resolution, &d.ResolvedBy, &prov, &d.SupersededBy, &created); err != nil {
			return nil, err
		}
		d.CreatedAt = time.UnixMilli(created)
		json.Unmarshal([]byte(opts), &d.Options)
		json.Unmarshal([]byte(prov), &d.Provenance)
		out = append(out, d)
	}
	return out, rows.Err()
}

// WireDecisionDerivedFromQuestion links a decision to the open question it
// resolves (decision derived_from question), so the decision's provenance is
// traceable through why/impact.
func (db *DB) WireDecisionDerivedFromQuestion(ctx context.Context, initiativeID, decisionID, questionID string) error {
	return db.Apply(ctx, Event{
		AggregateType: "decision",
		AggregateID:   decisionID,
		Actor:         ActorOrchestrator,
		EventType:     "decision.derived_from_question",
		Operation:     questionID,
	}, func(tx *sql.Tx) error {
		return db.wireEdges(tx, initiativeID, []EdgeToWrite{{
			FromType: "decision", FromID: decisionID, EdgeType: domain.EdgeDerivedFrom, ToType: "question", ToID: questionID,
		}})
	})
}

// WireRequirementDerivedFromDecision links a requirement to the decision that
// shaped it (requirement derived_from decision), so an open decision's impact
// ("blocks N") is the set of requirements waiting on its resolution.
func (db *DB) WireRequirementDerivedFromDecision(ctx context.Context, initiativeID, requirementID, decisionID string) error {
	return db.Apply(ctx, Event{
		AggregateType: "requirement",
		AggregateID:   requirementID,
		Actor:         ActorOrchestrator,
		EventType:     "requirement.derived_from_decision",
		Operation:     decisionID,
	}, func(tx *sql.Tx) error {
		return db.wireEdges(tx, initiativeID, []EdgeToWrite{{
			FromType: "requirement", FromID: requirementID, EdgeType: domain.EdgeDerivedFrom, ToType: "decision", ToID: decisionID,
		}})
	})
}

// GetDecision returns a single decision by id.
func (db *DB) GetDecision(ctx context.Context, decisionID string) (*domain.Decision, error) {
	row := db.SQL().QueryRowContext(ctx, `
		SELECT id, initiative_id, code, type, question, options_json, recommendation, status, resolution, resolved_by, provenance_json, superseded_by, created_at
		FROM decisions WHERE id = ?`, decisionID)
	var d domain.Decision
	var opts, prov string
	var created int64
	if err := row.Scan(&d.ID, &d.InitiativeID, &d.Code, &d.Type, &d.Question, &opts, &d.Recommendation,
		&d.Status, &d.Resolution, &d.ResolvedBy, &prov, &d.SupersededBy, &created); err != nil {
		return nil, err
	}
	d.CreatedAt = time.UnixMilli(created)
	json.Unmarshal([]byte(opts), &d.Options)
	json.Unmarshal([]byte(prov), &d.Provenance)
	return &d, nil
}

// CreateDecisionDiscussion appends one message to a decision discussion thread.
func (db *DB) CreateDecisionDiscussion(ctx context.Context, m *domain.DecisionDiscussion) error {
	if m.ID == "" {
		m.ID = id.New()
	}
	if m.CreatedAt.IsZero() {
		m.CreatedAt = time.Now().UTC()
	}
	return db.Apply(ctx, Event{
		AggregateType: "decision",
		AggregateID:   m.DecisionID,
		Actor:         m.Author,
		EventType:     "decision.discussed",
		Operation:     m.Author,
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO decision_discussions (id, decision_id, author, message, created_at)
			VALUES (?, ?, ?, ?, ?)`,
			m.ID, m.DecisionID, m.Author, m.Message, m.CreatedAt.UnixMilli())
		return err
	})
}

// ListDecisionDiscussions returns the discussion thread for a decision.
func (db *DB) ListDecisionDiscussions(ctx context.Context, decisionID string) ([]domain.DecisionDiscussion, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, decision_id, author, message, created_at
		FROM decision_discussions WHERE decision_id = ? ORDER BY created_at ASC`, decisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.DecisionDiscussion, 0)
	for rows.Next() {
		var m domain.DecisionDiscussion
		var created int64
		if err := rows.Scan(&m.ID, &m.DecisionID, &m.Author, &m.Message, &created); err != nil {
			return nil, err
		}
		m.CreatedAt = time.UnixMilli(created)
		out = append(out, m)
	}
	return out, rows.Err()
}

// ResolveDecision records a human decision (immutable once resolved).
func (db *DB) ResolveDecision(ctx context.Context, decisionID, resolution, actor string) error {
	return db.Apply(ctx, Event{
		AggregateType: "decision",
		AggregateID:   decisionID,
		Actor:         actor,
		EventType:     "decision.resolved",
		Operation:     resolution,
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE decisions SET status = ?, resolution = ?, resolved_by = ? WHERE id = ? AND status = 'open'`,
			domain.DecisionDecided, resolution, actor, decisionID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("store: decision %s is not open", decisionID)
		}
		return nil
	})
}

// ReopenDecision returns a decided decision to open and clears its resolution,
// so the human can choose again. Dissent is preserved (never erased, §8).
func (db *DB) ReopenDecision(ctx context.Context, decisionID, actor string) error {
	return db.Apply(ctx, Event{
		AggregateType: "decision",
		AggregateID:   decisionID,
		Actor:         actor,
		EventType:     "decision.reopened",
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE decisions SET status = ?, resolution = '', resolved_by = '' WHERE id = ? AND status = ?`,
			domain.DecisionOpen, decisionID, domain.DecisionDecided)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("store: decision %s is not decided", decisionID)
		}
		return nil
	})
}

// SetDecisionRecommendation overwrites a decision's recommendation with a
// synthesized value (the orchestrator convergence, §M2 tail) and appends a
// decision event recording the synthesis.
func (db *DB) SetDecisionRecommendation(ctx context.Context, decisionID, rec, convergedBy, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	return db.Apply(ctx, Event{
		AggregateType: "decision",
		AggregateID:   decisionID,
		Actor:         actor,
		EventType:     "decision.recommendation_synthesized",
		Operation:     rec,
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE decisions SET recommendation = ?, converged_by = ? WHERE id = ? AND status = 'open'`, rec, convergedBy, decisionID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("store: decision %s is not open", decisionID)
		}
		return nil
	})
}

// HasDissent reports whether a decision has any preserved dissent (§8).
func (db *DB) HasDissent(ctx context.Context, decisionID string) (bool, error) {
	var n int
	if err := db.SQL().QueryRowContext(ctx, `SELECT COUNT(*) FROM decision_dissents WHERE decision_id = ?`, decisionID).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// CreateDissent preserves a dissenting view (§8).
func (db *DB) CreateDissent(ctx context.Context, d *domain.DecisionDissent) error {
	if d.ID == "" {
		d.ID = id.New()
	}
	if d.CreatedAt.IsZero() {
		d.CreatedAt = time.Now().UTC()
	}
	return db.Apply(ctx, Event{
		AggregateType: "decision",
		AggregateID:   d.DecisionID,
		Actor:         d.ProponentRole,
		EventType:     "decision.dissent",
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO decision_dissents (id, decision_id, proponent_role, position, reasoning_digest, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			d.ID, d.DecisionID, d.ProponentRole, d.Position, d.ReasoningDigest, d.CreatedAt.UnixMilli())
		return err
	})
}

// ListDissents returns the dissenting views for a decision.
func (db *DB) ListDissents(ctx context.Context, decisionID string) ([]domain.DecisionDissent, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, decision_id, proponent_role, position, reasoning_digest, created_at
		FROM decision_dissents WHERE decision_id = ? ORDER BY created_at ASC`, decisionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.DecisionDissent, 0)
	for rows.Next() {
		var d domain.DecisionDissent
		var created int64
		if err := rows.Scan(&d.ID, &d.DecisionID, &d.ProponentRole, &d.Position, &d.ReasoningDigest, &created); err != nil {
			return nil, err
		}
		d.CreatedAt = time.UnixMilli(created)
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---------- Question ----------

// CreateQuestion inserts an open question.
func (db *DB) CreateQuestion(ctx context.Context, q *domain.Question) error {
	if q.ID == "" {
		q.ID = id.New()
	}
	if q.CreatedAt.IsZero() {
		q.CreatedAt = time.Now().UTC()
	}
	if !q.Status.Valid() {
		q.Status = domain.QuestionOpen
	}
	return db.Apply(ctx, Event{
		AggregateType: "question",
		AggregateID:   q.ID,
		Actor:         ActorOrchestrator,
		EventType:     "question.created",
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO questions (id, initiative_id, question, status, answer_digest, answered_by, created_at, resolved_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			q.ID, q.InitiativeID, q.Question, q.Status, q.AnswerDigest, q.AnsweredBy, q.CreatedAt.UnixMilli(), nil)
		return err
	})
}

// ListQuestions returns questions for an initiative.
func (db *DB) ListQuestions(ctx context.Context, initiativeID string) ([]domain.Question, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, question, status, answer_digest, answered_by, created_at, resolved_at
		FROM questions WHERE initiative_id = ? ORDER BY created_at ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Question, 0)
	for rows.Next() {
		var q domain.Question
		var created int64
		var resolved sql.NullInt64
		if err := rows.Scan(&q.ID, &q.InitiativeID, &q.Question, &q.Status, &q.AnswerDigest,
			&q.AnsweredBy, &created, &resolved); err != nil {
			return nil, err
		}
		q.CreatedAt = time.UnixMilli(created)
		if resolved.Valid {
			t := time.UnixMilli(resolved.Int64)
			q.ResolvedAt = &t
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// AnswerQuestion records a human answer (human_approved) and resolves the question.
func (db *DB) AnswerQuestion(ctx context.Context, questionID, answerDigest, actor string) error {
	return db.Apply(ctx, Event{
		AggregateType: "question",
		AggregateID:   questionID,
		Actor:         actor,
		EventType:     "question.answered",
		PayloadDigest: answerDigest,
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE questions SET status = ?, answer_digest = ?, answered_by = ?, resolved_at = ? WHERE id = ? AND status = 'open'`,
			domain.QuestionResolved, answerDigest, actor, time.Now().UTC().UnixMilli(), questionID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("store: question %s is not open", questionID)
		}
		return nil
	})
}

// ---------- Artifact ----------

// CreateArtifact inserts an artifact family and its first version.
func (db *DB) CreateArtifact(ctx context.Context, a *domain.Artifact, v *domain.ArtifactVersion) error {
	if a.ID == "" {
		a.ID = id.New()
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	if v.ID == "" {
		v.ID = id.New()
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	if v.Version == 0 {
		v.Version = 1
	}
	if v.Status == "" {
		v.Status = "draft"
	}
	return db.Apply(ctx, Event{
		AggregateType: "artifact",
		AggregateID:   a.ID,
		Actor:         ActorOrchestrator,
		EventType:     "artifact.created",
	}, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO artifacts (id, initiative_id, kind, created_at) VALUES (?, ?, ?, ?)`,
			a.ID, a.InitiativeID, a.Kind, a.CreatedAt.UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.Exec(`
			INSERT INTO artifact_versions (id, artifact_id, version, status, body_digest, produced_by_run_id, superseded_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			v.ID, a.ID, v.Version, v.Status, v.BodyDigest, v.ProducedByRun, v.SupersededBy, v.CreatedAt.UnixMilli()); err != nil {
			return err
		}
		var edges []EdgeToWrite
		if v.ProducedByRun != "" {
			edges = append(edges, EdgeToWrite{FromType: "artifact", FromID: a.ID, EdgeType: domain.EdgeProducedBy, ToType: "run", ToID: v.ProducedByRun})
		}
		return db.wireEdges(tx, a.InitiativeID, edges)
	})
}

// ListArtifactVersions returns versions for an initiative's artifacts.
func (db *DB) ListArtifactVersions(ctx context.Context, initiativeID string) ([]domain.ArtifactVersion, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT av.id, av.artifact_id, av.version, av.status, av.body_digest, av.produced_by_run_id, av.superseded_by, av.created_at
		FROM artifact_versions av JOIN artifacts a ON a.id = av.artifact_id
		WHERE a.initiative_id = ? ORDER BY av.created_at ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.ArtifactVersion, 0)
	for rows.Next() {
		var v domain.ArtifactVersion
		var created int64
		if err := rows.Scan(&v.ID, &v.ArtifactID, &v.Version, &v.Status, &v.BodyDigest,
			&v.ProducedByRun, &v.SupersededBy, &created); err != nil {
			return nil, err
		}
		v.CreatedAt = time.UnixMilli(created)
		out = append(out, v)
	}
	return out, rows.Err()
}

// ---------- Finding ----------

// CreateFinding inserts a review finding.
func (db *DB) CreateFinding(ctx context.Context, f *domain.Finding) error {
	if f.ID == "" {
		f.ID = id.New()
	}
	if f.CreatedAt.IsZero() {
		f.CreatedAt = time.Now().UTC()
	}
	if !f.Severity.Valid() {
		return fmt.Errorf("store: invalid severity %q", f.Severity)
	}
	if f.Disposition != "" && !f.Disposition.Valid() {
		return fmt.Errorf("store: invalid disposition %q", f.Disposition)
	}
	if f.Code != "" && !f.Code.Valid() {
		return fmt.Errorf("store: invalid deny code %q", f.Code)
	}
	if f.DispositionActor == "" {
		f.DispositionActor = ActorOrchestrator
	}
	return db.Apply(ctx, Event{
		AggregateType: "finding",
		AggregateID:   f.ID,
		Actor:         ActorOrchestrator,
		EventType:     "finding.created",
	}, func(tx *sql.Tx) error {
		var disp sql.NullString
		if f.Disposition != "" {
			disp = sql.NullString{String: string(f.Disposition), Valid: true}
		}
		_, err := tx.Exec(`
			INSERT INTO findings (id, initiative_id, review_run_id, target_type, target_id, severity, confidence, category, code, finding, evidence, impact, recommendation, disposition, disposition_actor, note, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			f.ID, f.InitiativeID, f.ReviewRunID, f.TargetType, f.TargetID, f.Severity, f.Confidence, f.Category, f.Code,
			f.Finding, f.Evidence, f.Impact, f.Recommendation, disp, f.DispositionActor, f.Note, f.CreatedAt.UnixMilli())
		return err
	})
}

// ListFindings returns findings for a review run.
func (db *DB) ListFindings(ctx context.Context, reviewRunID string) ([]domain.Finding, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, review_run_id, target_type, target_id, severity, confidence, category, code, finding, evidence, impact, recommendation, disposition, disposition_actor, note, created_at
		FROM findings WHERE review_run_id = ? ORDER BY created_at ASC`, reviewRunID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFindings(rows)
}

// ListFindingsByTarget returns all findings targeting a specific node (e.g. a
// change set), used by the review_open_blocking deny gate at approve time.
func (db *DB) ListFindingsByTarget(ctx context.Context, targetType, targetID string) ([]domain.Finding, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, review_run_id, target_type, target_id, severity, confidence, category, code, finding, evidence, impact, recommendation, disposition, disposition_actor, note, created_at
		FROM findings WHERE target_type = ? AND target_id = ? ORDER BY created_at ASC`, targetType, targetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFindings(rows)
}

// ListFindingsByInitiative returns all findings for an initiative.
func (db *DB) ListFindingsByInitiative(ctx context.Context, initiativeID string) ([]domain.Finding, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, review_run_id, target_type, target_id, severity, confidence, category, code, finding, evidence, impact, recommendation, disposition, disposition_actor, note, created_at
		FROM findings WHERE initiative_id = ? ORDER BY created_at ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFindings(rows)
}

func scanFindings(rows *sql.Rows) ([]domain.Finding, error) {
	out := make([]domain.Finding, 0)
	for rows.Next() {
		var f domain.Finding
		var created int64
		var disp sql.NullString
		if err := rows.Scan(&f.ID, &f.InitiativeID, &f.ReviewRunID, &f.TargetType, &f.TargetID, &f.Severity, &f.Confidence,
			&f.Category, &f.Code, &f.Finding, &f.Evidence, &f.Impact, &f.Recommendation, &disp,
			&f.DispositionActor, &f.Note, &created); err != nil {
			return nil, err
		}
		f.CreatedAt = time.UnixMilli(created)
		if disp.Valid {
			f.Disposition = domain.Disposition(disp.String)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// DispositionOpenFindings sets the disposition of every still-open finding for
// an initiative in one atomic step. Used when a review cycle passes so prior
// cycles' blocking findings cannot wedge the verify gate (full-platform review).
func (db *DB) DispositionOpenFindings(ctx context.Context, initiativeID string, disp domain.Disposition, actor string) error {
	if !disp.Valid() {
		return fmt.Errorf("store: invalid disposition %q", disp)
	}
	return db.Apply(ctx, Event{
		AggregateType: "finding",
		AggregateID:   initiativeID,
		Actor:         actor,
		EventType:     "findings.dispositioned",
		Operation:     string(disp),
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE findings SET disposition = ?, disposition_actor = ? WHERE initiative_id = ? AND (disposition IS NULL OR disposition = '')`,
			disp, actor, initiativeID)
		return err
	})
}

// SetFindingDisposition records a disposition from a distinct actor.
func (db *DB) SetFindingDisposition(ctx context.Context, findingID string, disp domain.Disposition, actor string) error {
	if !disp.Valid() {
		return fmt.Errorf("store: invalid disposition %q", disp)
	}
	if disp == domain.DispositionAcceptedRisk && actor != HumanActor {
		return fmt.Errorf("store: accepted_risk requires a human actor")
	}
	return db.Apply(ctx, Event{
		AggregateType: "finding",
		AggregateID:   findingID,
		Actor:         actor,
		EventType:     "finding.dispositioned",
		Operation:     string(disp),
	}, func(tx *sql.Tx) error {
		// RowsAffected, because db.Apply commits the finding.dispositioned event
		// in this same transaction. Without the check, a stale or wrong id
		// returned 200 and durably recorded that a human accepted a risk on a
		// finding that does not exist -- in the event log this product sells as
		// provable provenance.
		res, err := tx.Exec(`UPDATE findings SET disposition = ?, disposition_actor = ? WHERE id = ?`,
			disp, actor, findingID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return &NotFoundError{Kind: "finding", Ref: findingID}
		}
		return nil
	})
}

// SetFindingNote records (or clears) a human free-text note on a finding. The
// note is fed back to the model on the next revise so the human can "discuss"
// a finding rather than only disposition it.
func (db *DB) SetFindingNote(ctx context.Context, findingID, note, actor string) error {
	return db.Apply(ctx, Event{
		AggregateType: "finding",
		AggregateID:   findingID,
		Actor:         actor,
		EventType:     "finding.noted",
	}, func(tx *sql.Tx) error {
		// Same reason as SetFindingDisposition: the event is committed with the
		// row update, so a miss must fail rather than record a note nobody wrote.
		res, err := tx.Exec(`UPDATE findings SET note = ? WHERE id = ?`, note, findingID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return &NotFoundError{Kind: "finding", Ref: findingID}
		}
		return nil
	})
}
