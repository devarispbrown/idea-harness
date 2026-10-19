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
	// Short titles are generated deterministically on create (C1, wave 3
	// contract), from the description (the full idea text -- Title carries the
	// same text today, see cmdNew). A caller that already supplied one (e.g. a
	// test fixture) is left alone.
	if ini.ShortTitle == "" {
		ini.ShortTitle = domain.GenerateShortTitle(ini.Description)
	}
	return db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   ini.ID,
		Actor:         ActorOrchestrator,
		EventType:     "initiative.created",
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO initiatives (id, title, description, status, short_title, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			ini.ID, ini.Title, ini.Description, ini.Status, ini.ShortTitle, ini.CreatedAt.UnixMilli(), ini.UpdatedAt.UnixMilli())
		return err
	})
}

// backfillShortTitle returns ini.ShortTitle, generating and persisting one
// first if the row predates the short_title column (C1, wave 3 contract). The
// write is best-effort: a read path should never fail because the backfill
// could not be saved, so the generated value is still returned to the caller
// even if persisting it errors (it will simply be regenerated -- identically,
// since generation is deterministic -- next time).
func (db *DB) backfillShortTitle(ctx context.Context, initiativeID, description, existing string) string {
	if existing != "" {
		return existing
	}
	generated := domain.GenerateShortTitle(description)
	if generated == "" {
		return generated
	}
	if _, err := db.SQL().ExecContext(ctx, `UPDATE initiatives SET short_title = ? WHERE id = ? AND short_title = ''`, generated, initiativeID); err != nil {
		// Best-effort: see doc comment.
		_ = err
	}
	return generated
}

// GetInitiative returns one initiative by id.
func (db *DB) GetInitiative(ctx context.Context, initiativeID string) (domain.Initiative, error) {
	var ini domain.Initiative
	var created, updated int64
	err := db.SQL().QueryRowContext(ctx, `
		SELECT id, title, description, status, failure_reason, judgment_reason, git_branch, pr_url, short_title, created_at, updated_at
		FROM initiatives WHERE id = ?`, initiativeID).
		Scan(&ini.ID, &ini.Title, &ini.Description, &ini.Status, &ini.FailureReason, &ini.JudgmentReason, &ini.GitBranch, &ini.PRURL, &ini.ShortTitle, &created, &updated)
	if err != nil {
		return ini, notFoundIfNoRows(err, "initiative", initiativeID)
	}
	ini.CreatedAt = time.UnixMilli(created)
	ini.UpdatedAt = time.UnixMilli(updated)
	ini.ShortTitle = db.backfillShortTitle(ctx, ini.ID, ini.Description, ini.ShortTitle)
	return ini, nil
}

// ListInitiatives returns all initiatives ordered by creation.
func (db *DB) ListInitiatives(ctx context.Context) ([]domain.Initiative, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, title, description, status, failure_reason, judgment_reason, git_branch, pr_url, short_title, created_at, updated_at
		FROM initiatives ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Initiative, 0)
	for rows.Next() {
		var ini domain.Initiative
		var created, updated int64
		if err := rows.Scan(&ini.ID, &ini.Title, &ini.Description, &ini.Status, &ini.FailureReason, &ini.JudgmentReason, &ini.GitBranch, &ini.PRURL, &ini.ShortTitle, &created, &updated); err != nil {
			return nil, err
		}
		ini.CreatedAt = time.UnixMilli(created)
		ini.UpdatedAt = time.UnixMilli(updated)
		out = append(out, ini)
	}
	rows.Close()
	for i := range out {
		out[i].ShortTitle = db.backfillShortTitle(ctx, out[i].ID, out[i].Description, out[i].ShortTitle)
	}
	return out, rows.Err()
}

// SetInitiativeGitBranch records the dedicated branch git.branch_per_initiative
// created for this initiative on its first apply (review C4 piece 2), so every
// later change set commits to the same branch instead of computing (and
// possibly colliding on) a new name each time.
func (db *DB) SetInitiativeGitBranch(ctx context.Context, initiativeID, branch, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	return db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   initiativeID,
		Actor:         actor,
		EventType:     "initiative.git_branch_set",
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE initiatives SET git_branch = ? WHERE id = ?`, branch, initiativeID)
		if err != nil {
			return err
		}
		return requireOneRow(res, "initiative %q not found", initiativeID)
	})
}

// SetInitiativePRURL records the pull request `idea pr` opened for the
// initiative's dedicated branch (review C4 piece 3).
func (db *DB) SetInitiativePRURL(ctx context.Context, initiativeID, prURL, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	return db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   initiativeID,
		Actor:         actor,
		EventType:     "initiative.pr_opened",
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE initiatives SET pr_url = ? WHERE id = ?`, prURL, initiativeID)
		if err != nil {
			return err
		}
		return requireOneRow(res, "initiative %q not found", initiativeID)
	})
}

// FailInitiative marks an initiative failed with a reason.
//
// review H7: this is a general-purpose "mark failed" primitive, not only the
// release half of a live ClaimInitiativeRunning claim -- tests (and,
// plausibly, a future administrative path) call it directly on an
// initiative that is not currently `running` and expect it to take effect,
// so it deliberately stays unconditional here rather than gaining
// ClaimInitiativeRunning's CAS. The actual hazard this review item is about
// -- failOperationAfterPanic calling this unconditionally for an operation
// that never claimed the initiative in the first place
// (SuggestAnswersInitiative, notably, has no ClaimInitiativeRunning call at
// all), which could overwrite a DIFFERENT, unrelated live claim with
// `failed` -- is closed at that call site instead, by skipping this call
// entirely for operation types known not to claim
// (operationClaimsInitiative in runtime.go). A blanket CAS here would have
// been the wrong layer: it cannot distinguish "no claim to release" from "a
// live claim that happens to belong to someone else", which is exactly the
// case that matters.
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

// errInitiativeNotRunning is ReleaseInitiativeClaim's internal no-op signal
// (review H7): the initiative was not `running`, so there was no live claim
// to release. Never returned to ReleaseInitiativeClaim's own callers, which
// translate it to a plain nil.
var errInitiativeNotRunning = errors.New("store: initiative was not running; nothing to release")

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

// NeedsHumanJudgment parks an initiative for a human and records why, in one
// statement. The two must move together: a status without a reason is what left
// `idea next` guessing from artifact presence, and a reason without the status
// would be read by nothing.
func (db *DB) NeedsHumanJudgment(ctx context.Context, initiativeID string, reason domain.JudgmentReason, actor string) error {
	if !reason.Valid() {
		return fmt.Errorf("store: invalid judgment reason %q", reason)
	}
	if actor == "" {
		actor = ActorOrchestrator
	}
	return db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   initiativeID,
		Actor:         actor,
		EventType:     "initiative.status_changed",
		Operation:     string(domain.InitiativeNeedsHumanJudgment) + ":" + string(reason),
	}, func(tx *sql.Tx) error {
		// failure_reason is cleared: parking for a human is not failing, and the
		// path SetInitiativeStatus took here used to clear it. A retry that
		// succeeds into a parked state would otherwise carry the reason of the
		// failure it just recovered from.
		_, err := tx.Exec(
			`UPDATE initiatives SET status = ?, failure_reason = '', judgment_reason = ?, updated_at = ? WHERE id = ?`,
			domain.InitiativeNeedsHumanJudgment, reason, time.Now().UTC().UnixMilli(), initiativeID)
		return err
	})
}

// TransitionInitiative is the compare-and-swap for a status change that is not
// a claim.
//
// SetInitiativeStatus is an unconditional UPDATE, so guarding it with a
// GetInitiative first is the check-then-act this package already rejected for
// ClaimInitiativeRunning: the read and the write are not atomic with each other.
// `Approve` did exactly that -- it had no check at all and simply overwrote the
// status, releasing a claim a running advance was holding, so two advances ran
// concurrently on one initiative. A guard that reads first would have narrowed
// that window without closing it.
func (db *DB) TransitionInitiative(ctx context.Context, initiativeID string, to domain.InitiativeStatus, actor string, from ...domain.InitiativeStatus) error {
	if len(from) == 0 {
		return fmt.Errorf("store: TransitionInitiative needs at least one source status")
	}
	if actor == "" {
		actor = ActorOrchestrator
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(from)), ",")
	args := []any{to, time.Now().UTC().UnixMilli(), initiativeID}
	for _, f := range from {
		args = append(args, f)
	}
	return db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   initiativeID,
		Actor:         actor,
		EventType:     "initiative.status_changed",
		Operation:     string(to),
	}, func(tx *sql.Tx) error {
		// judgment_reason is cleared: every target of this call is a status that
		// is not needs_human_judgment, and a stale reason left behind would be
		// read the next time the initiative parks. failure_reason follows the
		// same rule SetInitiativeStatus uses.
		res, err := tx.Exec(
			`UPDATE initiatives SET status = ?, failure_reason = '', judgment_reason = '', updated_at = ?
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

// CompleteInitiative writes `complete`, but never over a failed initiative.
//
// This is a guard on the write, not an enumeration of the states it accepts.
// InitiativeStatus has six values, so listing the other five would be the exact
// complement of the one refusal -- and a seventh status added later would be
// silently excluded, turning a legitimate completion into a refusal with no
// test failing. The invariant is "not failed", so that is what the SQL says.
//
// The defect: VerifyInitiative concluded `complete` for an initiative with no
// requirements at all (the gate loop never runs, so allVerified stays true) and
// wrote it with SetInitiativeStatus, unconditionally. DX audit 7 reproduced a
// bare `idea verify` on an initiative that failed at framing with a 401 --
// zero requirements, zero tasks, zero change sets -- printing
// `✓ verified status: complete`, exiting 0, and discarding the failure_reason.
//
// Runtime.RunVerificationGate refuses the request synchronously; this refuses
// the write, which covers any future caller that does not go through the
// Runtime and the window where a concurrent FailInitiative lands.
//
// The predicate is failure_reason, not just status, and that is load-bearing.
// Five entry points -- apply, revert, revise, retry, reopen-phase -- claim
// `running` FROM `failed` by design, and ClaimInitiativeRunning deliberately
// leaves failure_reason intact so the release can restore it. During any of
// them the row is a failed initiative wearing a running status: a status-only
// guard passes, this write lands, and it clears the reason PERMANENTLY, since
// the release only writes the status back. A commit review caught it by
// reproducing exactly that against the real handler. `failed` is a transient
// display of the status; the recorded reason is the durable fact.
//
// review H7: `status != failed` alone also passes while status == running
// with an empty failure_reason -- the documented apply-then-verify sequence
// (`idea apply` does not wait for its operation) runs exactly there, and
// TestVerifyIsAllowedWhileAnApplyIsInFlight asserts that must keep working,
// so `running` is deliberately NOT excluded here. What closes the actual
// hazard (verify's CompleteInitiative racing apply/execute/revert while they
// are still actively mutating files, not merely while the row still reads
// `running` after they finished) is VerifyInitiative now taking the same
// engine mutex Execute/ApplyChangeSet/RevertChangeSet hold for their whole
// duration (see verify.go) -- verify cannot even reach this write until
// their real work is over, which is the actual invariant this needs, not a
// DB-status proxy for it that would also refuse the legitimate case above.
// The residual sliver -- apply's own release() running a moment after
// ApplyChangeSet returns (mutex released) but this write lands first -- is
// closed by making that release a CAS too (ReleaseInitiativeClaim).
func (db *DB) CompleteInitiative(ctx context.Context, initiativeID, actor string) error {
	if actor == "" {
		actor = ActorOrchestrator
	}
	return db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   initiativeID,
		Actor:         actor,
		EventType:     "initiative.status_changed",
		Operation:     string(domain.InitiativeComplete),
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`UPDATE initiatives SET status = ?, failure_reason = '', judgment_reason = '', updated_at = ?
			 WHERE id = ? AND status != ? AND failure_reason = ''`,
			domain.InitiativeComplete, time.Now().UTC().UnixMilli(), initiativeID, domain.InitiativeFailed)
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

// ReleaseInitiativeClaim transitions an initiative FROM `running` back to
// `to` -- the release half of ClaimInitiativeRunning -- but only while it is
// STILL `running` (review H7). Every apply/execute/revert-style entry point
// claims running, does its real work, and then hands the initiative back to
// wherever it found it (or a related status); before this, that handback
// (runtime.go's `release()` closures) used the unconditional
// SetInitiativeStatus, so it could run moments after the claiming
// operation's real work finished (its engine mutex already released) but
// AFTER some other, legitimate write had already moved the initiative
// somewhere else -- proven with verify's CompleteInitiative landing in that
// exact gap and being silently overwritten back to the pre-apply status,
// discarding a real completion. CAS-ing this on `running` makes the handback
// a no-op whenever that has happened, instead of a stomp.
func (db *DB) ReleaseInitiativeClaim(ctx context.Context, initiativeID string, to domain.InitiativeStatus, actor string) error {
	if !to.Valid() {
		return fmt.Errorf("store: invalid initiative status %q", to)
	}
	if actor == "" {
		actor = ActorOrchestrator
	}
	err := db.Apply(ctx, Event{
		AggregateType: "initiative",
		AggregateID:   initiativeID,
		Actor:         actor,
		EventType:     "initiative.status_changed",
		Operation:     string(to),
	}, func(tx *sql.Tx) error {
		// Mirrors SetInitiativeStatus's own failure_reason handling: a
		// release TO failed or running or needs_human_judgment must not
		// clear a reason a real resolution (TransitionInitiative) has not
		// yet acted on.
		var res sql.Result
		var err error
		if to == domain.InitiativeFailed || to == domain.InitiativeRunning ||
			to == domain.InitiativeNeedsHumanJudgment {
			res, err = tx.Exec(
				`UPDATE initiatives SET status = ?, updated_at = ? WHERE id = ? AND status = ?`,
				to, time.Now().UTC().UnixMilli(), initiativeID, domain.InitiativeRunning)
		} else {
			res, err = tx.Exec(
				`UPDATE initiatives SET status = ?, failure_reason = '', judgment_reason = '', updated_at = ?
				 WHERE id = ? AND status = ?`,
				to, time.Now().UTC().UnixMilli(), initiativeID, domain.InitiativeRunning)
		}
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return errInitiativeNotRunning
		}
		return nil
	})
	if errors.Is(err, errInitiativeNotRunning) {
		return nil
	}
	return err
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
		// needs_human_judgment joins them, for exactly the same reason one step
		// removed: execute releases its claim with SetInitiativeStatus back to
		// the status it found (runtime.go, priorStatus), and that status is
		// often needs_human_judgment. Clearing the reason there wiped it on the
		// first claim/release cycle, so `idea next` and `idea status` fell back
		// to the generic wording one command after the reason was recorded.
		// A real resolution goes through TransitionInitiative, which clears it
		// explicitly.
		if status == domain.InitiativeFailed || status == domain.InitiativeRunning ||
			status == domain.InitiativeNeedsHumanJudgment {
			_, err := tx.Exec(`UPDATE initiatives SET status = ?, updated_at = ? WHERE id = ?`,
				status, time.Now().UTC().UnixMilli(), initiativeID)
			return err
		}
		_, err := tx.Exec(`UPDATE initiatives SET status = ?, failure_reason = '', judgment_reason = '', updated_at = ? WHERE id = ?`,
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
		if a.Code == "" {
			code, err := nextCode(tx, "assertions", "code", "ASM", a.InitiativeID)
			if err != nil {
				return err
			}
			a.Code = code
		}
		_, err := tx.Exec(`
			INSERT INTO assertions
				(id, initiative_id, code, type, status, statement, evidence_digest, confidence, source, provenance_json, produced_by_run_id, superseded_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, a.InitiativeID, a.Code, a.Type, a.Status, a.Statement, a.EvidenceDigest, a.Confidence,
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
		SELECT id, initiative_id, code, type, status, statement, evidence_digest, confidence, source, provenance_json, produced_by_run_id, superseded_by, created_at
		FROM assertions WHERE initiative_id = ? ORDER BY created_at ASC, rowid ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Assertion, 0)
	for rows.Next() {
		var a domain.Assertion
		var prov string
		var created int64
		if err := rows.Scan(&a.ID, &a.InitiativeID, &a.Code, &a.Type, &a.Status, &a.Statement, &a.EvidenceDigest,
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
		SELECT id, initiative_id, code, statement, acceptance_criteria_json, priority, status, source_assertion_id, provenance_json, produced_by_run_id, superseded_by, verified_at, created_at, revised_at
		FROM requirements WHERE initiative_id = ? ORDER BY created_at ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.Requirement, 0)
	for rows.Next() {
		var r domain.Requirement
		var ac, prov string
		var verified, revised sql.NullInt64
		var created int64
		if err := rows.Scan(&r.ID, &r.InitiativeID, &r.Code, &r.Statement, &ac, &r.Priority, &r.Status,
			&r.SourceAssertionID, &prov, &r.ProducedByRun, &r.SupersededBy, &verified, &created, &revised); err != nil {
			return nil, err
		}
		r.CreatedAt = time.UnixMilli(created)
		if verified.Valid {
			t := time.UnixMilli(verified.Int64)
			r.VerifiedAt = &t
		}
		if revised.Valid {
			t := time.UnixMilli(revised.Int64)
			r.RevisedAt = &t
		}
		json.Unmarshal([]byte(ac), &r.AcceptanceCriteria)
		json.Unmarshal([]byte(prov), &r.Provenance)
		r.ObjectiveIDs = []string{}
		r.ChangeKind = r.DeriveChangeKind()
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	serves, err := db.requirementObjectives(ctx, initiativeID)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if ids := serves[out[i].ID]; len(ids) > 0 {
			out[i].ObjectiveIDs = ids
		}
	}
	return out, nil
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
		if _, err := tx.Exec(`UPDATE requirements SET status = ?, superseded_by = ? WHERE initiative_id = ? AND status != ?`,
			domain.RequirementRemoved, supersededBy, initiativeID, domain.RequirementRemoved); err != nil {
			return err
		}
		// review MEDIUM: stale-mark garbage collection -- a superseded
		// requirement's stale mark is dead weight, not an outstanding
		// acknowledgement (see clearStaleMarksForRemovedRequirementsTx).
		return clearStaleMarksForRemovedRequirementsTx(tx, initiativeID, ActorOrchestrator)
	})
}

// SupersedeRequirementsExcept marks every active requirement of an
// initiative as removed EXCEPT those in keepIDs, recording the superseding
// reason. Used by a PRD revision that updates some requirements in place
// (UpdateRequirement) rather than recreating them (review ICP re-run #1):
// only the requirements this cycle's response actually dropped are
// superseded, so the code and every graph edge of a requirement carried
// forward stays stable across the revision. An empty keepIDs behaves exactly
// like SupersedeRequirements -- backward compatible, additive API.
func (db *DB) SupersedeRequirementsExcept(ctx context.Context, initiativeID, supersededBy string, keepIDs []string) error {
	if len(keepIDs) == 0 {
		return db.SupersedeRequirements(ctx, initiativeID, supersededBy)
	}
	return db.Apply(ctx, Event{
		AggregateType: "requirement",
		AggregateID:   initiativeID,
		Actor:         ActorOrchestrator,
		EventType:     "requirements.superseded",
	}, func(tx *sql.Tx) error {
		placeholders := make([]string, len(keepIDs))
		args := make([]any, 0, len(keepIDs)+3)
		args = append(args, domain.RequirementRemoved, supersededBy, initiativeID)
		for i, id := range keepIDs {
			placeholders[i] = "?"
			args = append(args, id)
		}
		args = append(args, domain.RequirementRemoved)
		query := `UPDATE requirements SET status = ?, superseded_by = ? WHERE initiative_id = ? AND id NOT IN (` +
			strings.Join(placeholders, ",") + `) AND status != ?`
		if _, err := tx.Exec(query, args...); err != nil {
			return err
		}
		// review MEDIUM: stale-mark garbage collection -- see
		// SupersedeRequirements's identical call.
		return clearStaleMarksForRemovedRequirementsTx(tx, initiativeID, ActorOrchestrator)
	})
}

// UpdateRequirement updates an existing requirement's statement, priority
// and acceptance criteria in place -- same id, same code -- and records the
// run that produced the revision. Used when a PRD revision continues a
// requirement's SUBJECT rather than replacing it (review ICP re-run #1):
// keeping the same id preserves every graph edge already wired to it
// (derived_from a decision, implements from a task, produced_by an earlier
// run) instead of orphaning them the way supersede-and-recreate did on every
// revise cycle. r.ID must already be set to the existing requirement's id.
//
// When the revision actually changes the statement or acceptance criteria of
// a requirement that has already left draft (review C5) -- approved,
// verified, failed or waived -- its status is reset to approved, verified_at
// is cleared, and every verification_evidence row for it is deleted, all in
// THIS SAME transaction. Without this, a requirement could report verified,
// with surviving human evidence, for a statement that no longer exists: the
// evidence was recorded against the OLD subject, and status/verified_at are
// otherwise untouched by a code-matched revision even though the thing being
// verified has changed underneath it. A revision that leaves the statement
// and acceptance criteria byte-for-byte unchanged (e.g. only priority moved)
// is not treated as re-opening verification. A still-draft requirement (an
// in-progress PRD review cycle revising it before it has ever been approved)
// is left in draft -- resetting it to approved here would let a mid-review
// revision jump the requirement past the review gate it hasn't passed yet.
func (db *DB) UpdateRequirement(ctx context.Context, r *domain.Requirement) error {
	return db.Apply(ctx, Event{
		AggregateType: "requirement",
		AggregateID:   r.ID,
		Actor:         ActorOrchestrator,
		EventType:     "requirement.revised",
	}, func(tx *sql.Tx) error {
		var curStatement, curCriteriaJSON string
		var curStatus domain.RequirementStatus
		if err := tx.QueryRow(`SELECT statement, acceptance_criteria_json, status FROM requirements WHERE id = ?`, r.ID).
			Scan(&curStatement, &curCriteriaJSON, &curStatus); err != nil {
			return err
		}
		newCriteriaJSON := mustJSON(r.AcceptanceCriteria)
		subjectChanged := curStatement != r.Statement || curCriteriaJSON != newCriteriaJSON
		reopenVerification := subjectChanged && curStatus != domain.RequirementDraft && curStatus != domain.RequirementRemoved
		var revisedAt *int64
		if subjectChanged {
			now := time.Now().UTC().UnixMilli()
			revisedAt = &now
		}

		if reopenVerification {
			_, err := tx.Exec(`
				UPDATE requirements
				SET statement = ?, acceptance_criteria_json = ?, priority = ?, produced_by_run_id = ?,
				    status = ?, verified_at = NULL, revised_at = ?
				WHERE id = ?`,
				r.Statement, newCriteriaJSON, r.Priority, r.ProducedByRun, domain.RequirementApproved, revisedAt, r.ID)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`DELETE FROM verification_evidence WHERE requirement_id = ?`, r.ID); err != nil {
				return err
			}
		} else {
			_, err := tx.Exec(`
				UPDATE requirements
				SET statement = ?, acceptance_criteria_json = ?, priority = ?, produced_by_run_id = ?, revised_at = ?
				WHERE id = ?`,
				r.Statement, newCriteriaJSON, r.Priority, r.ProducedByRun, revisedAt, r.ID)
			if err != nil {
				return err
			}
		}
		if r.ProducedByRun == "" {
			return nil
		}
		// A new produced_by edge per revision (not a replace) is deliberate:
		// it keeps every run that ever shaped this requirement in the graph,
		// which is exactly the traceability an update-in-place must not lose
		// relative to the old supersede-and-recreate path.
		return db.wireEdges(tx, r.InitiativeID, []EdgeToWrite{
			{FromType: "requirement", FromID: r.ID, EdgeType: domain.EdgeProducedBy, ToType: "run", ToID: r.ProducedByRun},
		})
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

// SetAssertionBasis records the assertions a requirement or decision rests on
// (its PRD `based_on`): one fromType --derived_from--> assertion edge per id.
// derived_from, not a new edge kind: it is the edge a requirement's source
// assertion already uses, the one `idea why` walks toward the evidence and
// `idea impact` walks away from it, and the one gap analysis reads both for an
// assertion's dependents and for a decision's basis_refuted.
//
// replace is for a node the same PRD cycle restates -- a requirement revised in
// place: its earlier derived_from->assertion edges that ids no longer names are
// removed first, so the graph says what the requirement rests on now, not the
// union of every revision. The requirement's own source_assertion_id edge is
// never removed here; it is a fact about where the row came from, not a claim
// the model restates each cycle. A decision is never replaced (callers pass
// false): a re-listed decision merges into the existing row, and an earlier
// cycle's basis for a choice already made is still its basis.
//
// Ids must belong to the initiative; an unknown one is refused rather than
// written as a dangling edge.
func (db *DB) SetAssertionBasis(ctx context.Context, initiativeID, fromType, fromID string, assertionIDs []string, replace bool) error {
	if fromType != "requirement" && fromType != "decision" {
		return fmt.Errorf("store: assertion basis is recorded for a requirement or decision, not %q", fromType)
	}
	return db.Apply(ctx, Event{
		AggregateType: fromType,
		AggregateID:   fromID,
		Actor:         ActorOrchestrator,
		EventType:     fromType + ".basis_set",
		Operation:     strings.Join(assertionIDs, ","),
	}, func(tx *sql.Tx) error {
		if replace {
			keep := ""
			if fromType == "requirement" {
				if err := tx.QueryRow(`SELECT source_assertion_id FROM requirements WHERE id = ?`, fromID).Scan(&keep); err != nil && err != sql.ErrNoRows {
					return err
				}
			}
			if _, err := tx.Exec(`
				DELETE FROM graph_edges
				WHERE initiative_id = ? AND from_type = ? AND from_id = ? AND edge_type = ? AND to_type = 'assertion' AND to_id != ?`,
				initiativeID, fromType, fromID, string(domain.EdgeDerivedFrom), keep); err != nil {
				return err
			}
		}
		var edges []EdgeToWrite
		seen := map[string]bool{}
		for _, aid := range assertionIDs {
			if aid == "" || seen[aid] {
				continue
			}
			seen[aid] = true
			if err := requireOwned(tx, "assertions", initiativeID, aid, "assertion"); err != nil {
				return err
			}
			// graph_edges has no uniqueness constraint, and a decision re-cited
			// by a later cycle must not count its basis twice.
			var have int
			if err := tx.QueryRow(`SELECT COUNT(*) FROM graph_edges WHERE initiative_id = ? AND from_type = ? AND from_id = ? AND edge_type = ? AND to_type = 'assertion' AND to_id = ?`,
				initiativeID, fromType, fromID, string(domain.EdgeDerivedFrom), aid).Scan(&have); err != nil {
				return err
			}
			if have > 0 {
				continue
			}
			edges = append(edges, EdgeToWrite{FromType: fromType, FromID: fromID, EdgeType: domain.EdgeDerivedFrom, ToType: "assertion", ToID: aid})
		}
		if err := db.wireEdges(tx, initiativeID, edges); err != nil {
			return err
		}
		// A node now resting on a retracted assertion is invalidated by it;
		// one whose revision dropped the citation no longer is.
		return db.syncInvalidatesTx(tx, initiativeID, ActorOrchestrator)
	})
}

// RelateAssertions records that one assertion corroborates (supports) or
// contradicts another -- a research finding judging a framing assumption. Both
// must belong to the initiative. supports makes the target one of the
// source's dependents in gap analysis (it is what the finding props up);
// contradicts is recorded for `idea why`/`impact` and the reader, and is not
// itself a gap trigger between two assertions.
func (db *DB) RelateAssertions(ctx context.Context, initiativeID, fromID string, edge domain.EdgeType, toID string) error {
	if edge != domain.EdgeSupports && edge != domain.EdgeContradicts {
		return fmt.Errorf("store: assertions relate by supports or contradicts, not %q", edge)
	}
	if fromID == toID {
		return fmt.Errorf("store: an assertion cannot %s itself", edge)
	}
	return db.Apply(ctx, Event{
		AggregateType: "assertion",
		AggregateID:   fromID,
		Actor:         ActorOrchestrator,
		EventType:     "assertion." + string(edge),
		Operation:     toID,
	}, func(tx *sql.Tx) error {
		for _, aid := range []string{fromID, toID} {
			if err := requireOwned(tx, "assertions", initiativeID, aid, "assertion"); err != nil {
				return err
			}
		}
		return db.wireEdges(tx, initiativeID, []EdgeToWrite{{FromType: "assertion", FromID: fromID, EdgeType: edge, ToType: "assertion", ToID: toID}})
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
		// Unwrapped, this reached writeErr as a raw sql.ErrNoRows and rendered as
		// a bare "not found" -- the least informative message in the API.
		return nil, notFoundIfNoRows(err, "decision", decisionID)
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
//
// ICP re-run defect H (third clause): quick rigor can auto-approve a plan (and
// tasks can execute) while a decision is still open, so "resolving a decision
// for the first time" is no longer guaranteed to happen before anything was
// built on it -- unlike the ordinary flow, where a decision is expected to
// resolve before the plan it feeds even exists. When something already
// depends on this decision (a live requirement derived from it, that
// requirement's tasks, their applied change sets), resolving it now is
// exactly as consequential as ReopenDecision's "changed my mind" case, and
// must mark the same dependents stale the same way -- otherwise a human's
// answer given after plan approval (e.g. "keys in an env var") never
// surfaces as "N items may be invalid" anywhere, and the tasks/change sets
// built without it are never flagged for a second look.
func (db *DB) ResolveDecision(ctx context.Context, decisionID, resolution, actor string) error {
	dec, err := db.GetDecision(ctx, decisionID)
	if err != nil {
		return err
	}
	// Discovery reads BEFORE the transaction starts -- see decisionDependents'
	// own doc comment (db.sql is a single-connection handle; a pool read from
	// inside an open transaction deadlocks against the transaction's own
	// connection). Read against the PRE-resolve state; resolving is itself
	// what makes these dependents newly worth flagging, whether or not any
	// exist yet.
	reqIDs, taskIDs, csIDs, err := db.decisionDependents(ctx, dec.InitiativeID, dec.ID)
	if err != nil {
		return err
	}
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
			return notFoundOrState(tx, "decisions", decisionID, "decision",
				fmt.Errorf("decision %s is not open", decisionID))
		}
		// review M5's reasoning applies here too: marked in the SAME
		// transaction as the resolve, so a crash between the two can never
		// leave the decision resolved with nothing marked stale. A decision
		// resolved before anything depends on it (the ordinary case) marks
		// nothing -- markDependentsStaleTx no-ops on three empty lists.
		if err := markDependentsStaleTx(tx, dec.InitiativeID, dec.ID, dec.Code, reqIDs, taskIDs, csIDs); err != nil {
			return err
		}
		// Append the resolution to project memory in the same transaction, so a
		// decided fact survives beyond its initiative and later initiatives read
		// it as authoritative context. Bounded so a long question/resolution
		// cannot bloat the memory.
		body := truncateOperationError(fmt.Sprintf("%s: %s → %s", dec.Code, dec.Question, resolution), 400)
		_, err = tx.Exec(`INSERT INTO project_memory (id, kind, body, created_at) VALUES (?, ?, ?, ?)`,
			id.New(), "decision", body, time.Now().UTC().UnixMilli())
		return err
	})
}

// ListProjectMemory returns the most recent project-memory entries, newest
// first. The per-project DB is the project, so no scope argument is needed.
func (db *DB) ListProjectMemory(ctx context.Context, limit int) ([]domain.ProjectMemory, error) {
	rows, err := db.SQL().QueryContext(ctx,
		`SELECT id, kind, body, created_at FROM project_memory ORDER BY created_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.ProjectMemory, 0)
	for rows.Next() {
		var m domain.ProjectMemory
		var created int64
		if err := rows.Scan(&m.ID, &m.Kind, &m.Body, &created); err != nil {
			return nil, err
		}
		m.CreatedAt = time.UnixMilli(created)
		out = append(out, m)
	}
	return out, rows.Err()
}

// ReopenDecision returns a decided decision to open and clears its resolution,
// so the human can choose again. Dissent is preserved (never erased, §8).
//
// Review A4 #5 ("impact is true"): a decided decision is authoritative only
// while it stays decided. The moment it is reopened, whatever was built on it
// -- the requirements that cite it, their tasks, and the applied change sets
// for those tasks -- is marked stale, so `impact decision DEC-n` and
// `/guidance` can say so instead of "no dependents found". This covers both
// "reopened" and "re-resolved with a different answer", since the latter is
// only reachable through this reopen in the current API.
func (db *DB) ReopenDecision(ctx context.Context, decisionID, actor string) error {
	dec, err := db.GetDecision(ctx, decisionID)
	if err != nil {
		return err
	}
	// Discovery reads BEFORE the transaction starts (see decisionDependents'
	// doc comment: db.sql is a single-connection handle, so a pool read from
	// inside an open transaction deadlocks against the transaction's own
	// connection).
	reqIDs, taskIDs, csIDs, err := db.decisionDependents(ctx, dec.InitiativeID, dec.ID)
	if err != nil {
		return err
	}
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
			return notFoundOrState(tx, "decisions", decisionID, "decision",
				fmt.Errorf("decision %s is not decided", decisionID))
		}
		// review M5: marked in the SAME transaction as the reopen above, so a
		// crash between the two can never leave the decision reopened with
		// nothing marked stale.
		return markDependentsStaleTx(tx, dec.InitiativeID, dec.ID, dec.Code, reqIDs, taskIDs, csIDs)
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
			return notFoundOrState(tx, "decisions", decisionID, "decision",
				fmt.Errorf("decision %s is not open", decisionID))
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
			INSERT INTO questions (id, initiative_id, question, status, answer_digest, answered_by, created_at, resolved_at, needs_clarification)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			q.ID, q.InitiativeID, q.Question, q.Status, q.AnswerDigest, q.AnsweredBy, q.CreatedAt.UnixMilli(), nil, q.NeedsClarification)
		return err
	})
}

// ListQuestions returns questions for an initiative.
func (db *DB) ListQuestions(ctx context.Context, initiativeID string) ([]domain.Question, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, question, status, answer_digest, answered_by, created_at, resolved_at, needs_clarification
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
			&q.AnsweredBy, &created, &resolved, &q.NeedsClarification); err != nil {
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
			return notFoundOrState(tx, "questions", questionID, "question",
				fmt.Errorf("question %s is not open", questionID))
		}
		return nil
	})
}

// ---------- Artifact ----------

// RecordArtifactVersion records a regenerated body as the next version of the
// initiative's existing artifact of that kind, creating the family (at v1)
// only when there is none yet. It returns whether a version was written.
//
// Every regeneration used to go through CreateArtifact, which always starts a
// new family at v1: a revised plan was a second "execution plan v1", the UI
// labelled the comparison "Diff v1 → v1", and the diff endpoint then refused
// it because the two versions belonged to different artifacts. Appending to
// the family is what makes "v2" mean the second version of the same document.
//
// skipIdentical makes a regeneration whose body is byte-identical to the
// latest version a no-op (false, nil): a revision that changed nothing must
// not mint a version that differs from its predecessor only in its number.
func (db *DB) RecordArtifactVersion(ctx context.Context, initiativeID string, kind domain.ArtifactKind, v *domain.ArtifactVersion, skipIdentical bool) (bool, error) {
	var latestID, artifactID, latestDigest string
	var latestVersion int
	err := db.SQL().QueryRowContext(ctx, `
		SELECT av.id, av.artifact_id, av.version, av.body_digest
		FROM artifact_versions av JOIN artifacts a ON a.id = av.artifact_id
		WHERE a.initiative_id = ? AND a.kind = ?
		ORDER BY av.created_at DESC, av.version DESC LIMIT 1`, initiativeID, kind).
		Scan(&latestID, &artifactID, &latestVersion, &latestDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return true, db.CreateArtifact(ctx, &domain.Artifact{InitiativeID: initiativeID, Kind: kind}, v)
	}
	if err != nil {
		return false, err
	}
	if skipIdentical && latestDigest == v.BodyDigest {
		return false, nil
	}
	if v.ID == "" {
		v.ID = id.New()
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	if v.Status == "" {
		v.Status = "draft"
	}
	v.ArtifactID = artifactID
	v.Version = latestVersion + 1
	return true, db.Apply(ctx, Event{
		AggregateType: "artifact",
		AggregateID:   artifactID,
		Actor:         ActorOrchestrator,
		EventType:     "artifact.version_created",
	}, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`
			INSERT INTO artifact_versions (id, artifact_id, version, status, body_digest, produced_by_run_id, superseded_by, created_at)
			VALUES (?, ?, ?, ?, ?, ?, '', ?)`,
			v.ID, artifactID, v.Version, v.Status, v.BodyDigest, v.ProducedByRun, v.CreatedAt.UnixMilli()); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE artifact_versions SET superseded_by = ? WHERE id = ?`, v.ID, latestID); err != nil {
			return err
		}
		if v.ProducedByRun == "" {
			return nil
		}
		return db.wireEdges(tx, initiativeID, []EdgeToWrite{{FromType: "artifact", FromID: artifactID, EdgeType: domain.EdgeProducedBy, ToType: "run", ToID: v.ProducedByRun}})
	})
}

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
			INSERT INTO findings (id, initiative_id, review_run_id, target_type, target_id, severity, confidence, category, code, finding, evidence, impact, recommendation, disposition, disposition_actor, note, secret_fingerprint, secret_kind, secret_path, secret_overridable, secret_preview, path, in_diff, advisory, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			f.ID, f.InitiativeID, f.ReviewRunID, f.TargetType, f.TargetID, f.Severity, f.Confidence, f.Category, f.Code,
			f.Finding, f.Evidence, f.Impact, f.Recommendation, disp, f.DispositionActor, f.Note,
			f.SecretFingerprint, f.SecretKind, f.SecretPath, f.SecretOverridable, f.SecretPreview, f.Path, f.InDiff, f.Advisory, f.CreatedAt.UnixMilli())
		return err
	})
}

// ListFindings returns findings for a review run.
func (db *DB) ListFindings(ctx context.Context, reviewRunID string) ([]domain.Finding, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, review_run_id, target_type, target_id, severity, confidence, category, code, finding, evidence, impact, recommendation, disposition, disposition_actor, note, secret_fingerprint, secret_kind, secret_path, secret_overridable, secret_preview, path, in_diff, advisory, created_at
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
		SELECT id, initiative_id, review_run_id, target_type, target_id, severity, confidence, category, code, finding, evidence, impact, recommendation, disposition, disposition_actor, note, secret_fingerprint, secret_kind, secret_path, secret_overridable, secret_preview, path, in_diff, advisory, created_at
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
		SELECT id, initiative_id, review_run_id, target_type, target_id, severity, confidence, category, code, finding, evidence, impact, recommendation, disposition, disposition_actor, note, secret_fingerprint, secret_kind, secret_path, secret_overridable, secret_preview, path, in_diff, advisory, created_at
		FROM findings WHERE initiative_id = ? ORDER BY created_at ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFindings(rows)
}

// GetFinding returns one finding by id (ICP re-run defect #2: used to warn a
// human dispositioning a finding "rejected" when it carries a system deny
// code and so will NOT clear the approval bar, per Finding.ResolvesBlocking).
func (db *DB) GetFinding(ctx context.Context, id string) (*domain.Finding, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, review_run_id, target_type, target_id, severity, confidence, category, code, finding, evidence, impact, recommendation, disposition, disposition_actor, note, secret_fingerprint, secret_kind, secret_path, secret_overridable, secret_preview, path, in_diff, advisory, created_at
		FROM findings WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	found, err := scanFindings(rows)
	if err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, &NotFoundError{Kind: "finding", Ref: id}
	}
	return &found[0], nil
}

func scanFindings(rows *sql.Rows) ([]domain.Finding, error) {
	out := make([]domain.Finding, 0)
	for rows.Next() {
		var f domain.Finding
		var created int64
		var disp sql.NullString
		if err := rows.Scan(&f.ID, &f.InitiativeID, &f.ReviewRunID, &f.TargetType, &f.TargetID, &f.Severity, &f.Confidence,
			&f.Category, &f.Code, &f.Finding, &f.Evidence, &f.Impact, &f.Recommendation, &disp,
			&f.DispositionActor, &f.Note, &f.SecretFingerprint, &f.SecretKind, &f.SecretPath,
			&f.SecretOverridable, &f.SecretPreview, &f.Path, &f.InDiff, &f.Advisory, &created); err != nil {
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

// DispositionUnclaimedFindingsByTarget dispositions the undisposed findings
// targeting one node that carry NO deny code, in a single event.
//
// Three constraints, each of which a simpler call gets wrong:
//
//   - Target-scoped. DispositionOpenFindings is `WHERE initiative_id = ?`, so
//     using it to accept a change set's findings would also sign off every open
//     finding on the initiative -- including the PRD review's.
//   - `code = ”`. Every producer that raises a system deny stamps a DenyCode:
//     secret_sweep, pii, host_exec, blob_digest_mismatch. Only the adversarial
//     reviewer leaves it empty (review.ParseFindings never sets it). So this
//     cannot sign off a credential detection, whatever the caller intends, and
//     "system deny beats human approve" survives.
//   - accepted_risk requires a human. SetFindingDisposition enforces that and
//     DispositionOpenFindings does not; a bulk path that skipped it would let an
//     automated actor forge the one disposition the reviewer is forbidden to
//     set.
//
// One event rather than N: a loop of SetFindingDisposition is not atomic, and a
// partial write here leaves a change set half-accepted.
func (db *DB) DispositionUnclaimedFindingsByTarget(ctx context.Context, initiativeID, targetType, targetID string, disp domain.Disposition, actor string) error {
	if !disp.Valid() {
		return fmt.Errorf("store: invalid disposition %q", disp)
	}
	if disp == domain.DispositionAcceptedRisk && actor != HumanActor {
		return fmt.Errorf("store: accepted_risk requires a human actor")
	}
	return db.Apply(ctx, Event{
		AggregateType: "finding",
		AggregateID:   targetID,
		Actor:         actor,
		EventType:     "findings.dispositioned",
		Operation:     string(disp),
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE findings SET disposition = ?, disposition_actor = ?
			 WHERE initiative_id = ? AND target_type = ? AND target_id = ?
			   AND (disposition IS NULL OR disposition = '')
			   AND (code IS NULL OR code = '')`,
			disp, actor, initiativeID, targetType, targetID)
		return err
	})
}

// SetFindingDisposition records a disposition from a distinct actor.
//
// not_a_secret (review changes #1, review H3) is scoped to secret_sweep
// findings only: every other category refuses it, so the one disposition
// that also writes a project-scoped allowlist entry cannot be forged onto an
// unrelated finding. A configured secret value's own finding
// (secret_overridable = false) refuses it too, unconditionally: that match
// is a real credential by construction, not a shape that might be a false
// positive, and no human override makes it eligible. The scoping checks and
// the allowlist write happen in the same transaction as the disposition
// update, so a crash between them cannot leave one without the other.
//
// The allowlist entry is scoped to (fingerprint, path) -- not fingerprint
// alone -- via upsert (ON CONFLICT), so the same value allowlisted in one
// file (e.g. a test fixture) still blocks in another. Changing a finding's
// disposition AWAY FROM not_a_secret removes the matching allowlist entry,
// so an allowlist decision can be reversed by re-dispositioning the finding
// that created it.
// No "store: " prefix on any message below (same reasoning as ErrSelfMerge/
// ErrMergeCycle/ErrDependsOnCycle and ReorderTasks's two messages -- see
// merge.go/dependson.go/graph.go): handleFindingDisposition surfaces this
// function's Error() text verbatim in a 400, and `idea disposition`
// (internal/cli/tasks.go) is the first CLI-reachable caller of this path.
func (db *DB) SetFindingDisposition(ctx context.Context, findingID string, disp domain.Disposition, actor string) error {
	if !disp.Valid() {
		return fmt.Errorf("invalid disposition %q", disp)
	}
	if disp == domain.DispositionAcceptedRisk && actor != HumanActor {
		return fmt.Errorf("accepted_risk requires a human actor")
	}
	if disp == domain.DispositionNotASecret && actor != HumanActor {
		return fmt.Errorf("not_a_secret requires a human actor")
	}
	return db.Apply(ctx, Event{
		AggregateType: "finding",
		AggregateID:   findingID,
		Actor:         actor,
		EventType:     "finding.dispositioned",
		Operation:     string(disp),
	}, func(tx *sql.Tx) error {
		var category, secretFingerprint, secretKind, secretPath string
		var secretOverridable bool
		var priorDisp sql.NullString
		if err := tx.QueryRow(`SELECT category, secret_fingerprint, secret_kind, secret_path, secret_overridable, disposition FROM findings WHERE id = ?`, findingID).
			Scan(&category, &secretFingerprint, &secretKind, &secretPath, &secretOverridable, &priorDisp); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return &NotFoundError{Kind: "finding", Ref: findingID}
			}
			return err
		}
		if disp == domain.DispositionNotASecret {
			if category != "secret_sweep" {
				return fmt.Errorf("not_a_secret is only valid on a secret_sweep finding (this finding is %q)", category)
			}
			if secretFingerprint == "" {
				return fmt.Errorf("this finding has no recorded secret fingerprint to allowlist")
			}
			if !secretOverridable {
				return fmt.Errorf("not_a_secret is refused for %q: this value was drawn from the project's own configured secrets, not a pattern match, and is never eligible for the not_a_secret override", secretKind)
			}
		}
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
		wasNotASecret := priorDisp.Valid && domain.Disposition(priorDisp.String) == domain.DispositionNotASecret
		switch {
		case disp == domain.DispositionNotASecret:
			if _, err := tx.Exec(`
				INSERT INTO secret_allowlist (id, fingerprint, kind, path, actor, created_at) VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT(fingerprint, path) DO UPDATE SET kind = excluded.kind, actor = excluded.actor, created_at = excluded.created_at`,
				id.New(), secretFingerprint, secretKind, secretPath, actor, nowMS()); err != nil {
				return err
			}
		case wasNotASecret && secretFingerprint != "":
			// The disposition moved away from not_a_secret: the allowlist
			// entry it created must go with it, or the sweep would keep
			// silently passing a value the human just un-allowlisted.
			if _, err := tx.Exec(`DELETE FROM secret_allowlist WHERE fingerprint = ? AND path = ?`, secretFingerprint, secretPath); err != nil {
				return err
			}
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
