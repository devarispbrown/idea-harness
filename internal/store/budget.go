package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/id"
)

// InitiativeBudget is one initiative's own spending-cap state.
type InitiativeBudget struct {
	// CapUSD is the initiative's own cap; nil inherits the project default.
	CapUSD *float64
	// ParkedFrom is the status ("plan_approved", "needs_human_judgment:prd_review",
	// ...) the initiative held before a budget stop parked it; "" otherwise.
	ParkedFrom string
}

// BudgetExceededEvent is the event type appended when a spending cap stops a
// run. The integration bus publishes it as initiative.budget_exceeded.
const BudgetExceededEvent = "initiative.budget_exceeded"

// EvalRegressedEvent is the event type appended when a scheduled eval run
// regresses against its baseline (aggregate: the automation). The
// integration bus publishes it as eval.regressed.
const EvalRegressedEvent = "automation.eval_regressed"

// GetInitiativeBudget returns the initiative's cap state (zero value when it
// has never had one).
func (db *DB) GetInitiativeBudget(ctx context.Context, initiativeID string) (InitiativeBudget, error) {
	var out InitiativeBudget
	var capUSD sql.NullFloat64
	err := db.sql.QueryRowContext(ctx, `SELECT cap_usd, parked_from FROM initiative_budgets WHERE initiative_id = ?`, initiativeID).
		Scan(&capUSD, &out.ParkedFrom)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, fmt.Errorf("store: initiative budget: %w", err)
	}
	if capUSD.Valid {
		v := capUSD.Float64
		out.CapUSD = &v
	}
	return out, nil
}

// SetInitiativeBudgetCap sets (or, with nil, clears) an initiative's own cap.
// It does not unpark a budget-stopped initiative; ResumeFromBudget does, and
// the caller decides whether the new cap allows it.
func (db *DB) SetInitiativeBudgetCap(ctx context.Context, initiativeID string, capUSD *float64, actor string) error {
	if capUSD != nil && *capUSD < 0 {
		return fmt.Errorf("store: a budget cap must be >= 0")
	}
	if actor == "" {
		actor = HumanActor
	}
	op := "inherit"
	var v any
	if capUSD != nil {
		op = fmt.Sprintf("%.4f", *capUSD)
		v = *capUSD
	}
	return db.Apply(ctx, Event{
		AggregateType: "initiative", AggregateID: initiativeID, Actor: actor,
		EventType: "initiative.budget_set", Operation: op,
	}, func(tx *sql.Tx) error {
		if err := initiativeExists(tx, initiativeID); err != nil {
			return err
		}
		_, err := tx.Exec(`
			INSERT INTO initiative_budgets (initiative_id, cap_usd, parked_from, updated_at) VALUES (?, ?, '', ?)
			ON CONFLICT(initiative_id) DO UPDATE SET cap_usd = excluded.cap_usd, updated_at = excluded.updated_at`,
			initiativeID, v, nowMS())
		return err
	})
}

func initiativeExists(tx *sql.Tx, initiativeID string) error {
	var n int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM initiatives WHERE id = ?`, initiativeID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return notFound("initiative", initiativeID)
	}
	return nil
}

// ParkForBudget stops an initiative at its spending cap: needs_human_judgment
// with reason budget_exceeded, remembering parkedFrom (the status to return
// to once the cap is raised). Two events are appended in the one
// transaction: the status change every client already follows, and
// initiative.budget_exceeded, whose operation carries detail ("$1.2000 of
// $1.0000"), for the integration bus.
//
// A complete initiative is never parked: nothing is left to spend on, and a
// completion that landed while the stopped call unwound must stand.
func (db *DB) ParkForBudget(ctx context.Context, initiativeID, parkedFrom, detail string) error {
	return db.InTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.Exec(
			`UPDATE initiatives SET status = ?, failure_reason = '', judgment_reason = ?, updated_at = ? WHERE id = ? AND status != ?`,
			domain.InitiativeNeedsHumanJudgment, domain.JudgmentBudget, nowMS(), initiativeID, domain.InitiativeComplete)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil
		}
		if _, err := tx.Exec(`
			INSERT INTO initiative_budgets (initiative_id, cap_usd, parked_from, updated_at) VALUES (?, NULL, ?, ?)
			ON CONFLICT(initiative_id) DO UPDATE SET parked_from = excluded.parked_from, updated_at = excluded.updated_at`,
			initiativeID, parkedFrom, nowMS()); err != nil {
			return err
		}
		if err := appendEvent(tx, Event{ID: id.New(), AggregateType: "initiative", AggregateID: initiativeID, Actor: ActorOrchestrator,
			EventType: "initiative.status_changed", Operation: string(domain.InitiativeNeedsHumanJudgment) + ":" + string(domain.JudgmentBudget), PayloadDigest: "-"}); err != nil {
			return err
		}
		return appendEvent(tx, Event{ID: id.New(), AggregateType: "initiative", AggregateID: initiativeID, Actor: ActorOrchestrator,
			EventType: BudgetExceededEvent, Operation: detail, PayloadDigest: "-"})
	})
}

// ResumeFromBudget returns a budget-stopped initiative to the status it was
// parked from, and reports whether it did. A no-op (false, nil) unless the
// initiative is parked on budget_exceeded.
//
// Where it returns to:
//   - plan_approved, approved, or needs_human_judgment with its old reason:
//     there, exactly, so the step that stopped can simply be run again;
//   - anything else (a stop mid-PRD from a fresh initiative, or from a retry
//     of a failed one): failed, with a reason naming `idea retry`, which is
//     how a design-phase run is continued.
func (db *DB) ResumeFromBudget(ctx context.Context, initiativeID, actor string) (bool, error) {
	if actor == "" {
		actor = HumanActor
	}
	resumed := false
	err := db.InTx(ctx, func(tx *sql.Tx) error {
		var status, reason string
		if err := tx.QueryRow(`SELECT status, judgment_reason FROM initiatives WHERE id = ?`, initiativeID).Scan(&status, &reason); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return notFound("initiative", initiativeID)
			}
			return err
		}
		if status != string(domain.InitiativeNeedsHumanJudgment) || reason != string(domain.JudgmentBudget) {
			return nil
		}
		var parkedFrom string
		if err := tx.QueryRow(`SELECT parked_from FROM initiative_budgets WHERE initiative_id = ?`, initiativeID).Scan(&parkedFrom); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		to, toReason, _ := strings.Cut(parkedFrom, ":")
		var evType, op string
		switch domain.InitiativeStatus(to) {
		case domain.InitiativePlanApproved, domain.InitiativeApproved:
			if _, err := tx.Exec(`UPDATE initiatives SET status = ?, judgment_reason = '', updated_at = ? WHERE id = ?`, to, nowMS(), initiativeID); err != nil {
				return err
			}
			evType, op = "initiative.status_changed", to
		case domain.InitiativeNeedsHumanJudgment:
			if !domain.JudgmentReason(toReason).Valid() || toReason == string(domain.JudgmentBudget) {
				toReason = ""
			}
			if _, err := tx.Exec(`UPDATE initiatives SET judgment_reason = ?, updated_at = ? WHERE id = ?`, toReason, nowMS(), initiativeID); err != nil {
				return err
			}
			evType, op = "initiative.status_changed", string(domain.InitiativeNeedsHumanJudgment)
			if toReason != "" {
				op += ":" + toReason
			}
		default:
			why := "stopped at its budget cap before this step finished; the cap was raised -- run `idea retry` to continue"
			if _, err := tx.Exec(`UPDATE initiatives SET status = ?, failure_reason = ?, judgment_reason = '', updated_at = ? WHERE id = ?`,
				domain.InitiativeFailed, why, nowMS(), initiativeID); err != nil {
				return err
			}
			evType, op = "initiative.failed", why
		}
		if _, err := tx.Exec(`UPDATE initiative_budgets SET parked_from = '', updated_at = ? WHERE initiative_id = ?`, nowMS(), initiativeID); err != nil {
			return err
		}
		resumed = true
		return appendEvent(tx, Event{ID: id.New(), AggregateType: "initiative", AggregateID: initiativeID, Actor: actor,
			EventType: evType, Operation: op, PayloadDigest: "-"})
	})
	return resumed, err
}

// ---------- automation run history ----------

// AutomationRun is one run of a scheduled background job.
type AutomationRun struct {
	ID         string   `json:"id"`
	Automation string   `json:"automation"`
	Target     string   `json:"target,omitempty"`
	Trigger    string   `json:"trigger"`
	Status     string   `json:"status"`
	StartedAt  int64    `json:"started_at_ms"`
	FinishedAt int64    `json:"finished_at_ms,omitempty"`
	CostUSD    *float64 `json:"cost_usd,omitempty"`
	Summary    string   `json:"summary,omitempty"`
	Error      string   `json:"error,omitempty"`
	ReportPath string   `json:"report_path,omitempty"`
}

// Automation run statuses.
const (
	AutomationRunning   = "running"
	AutomationSucceeded = "succeeded"
	AutomationFailed    = "failed"
	AutomationSkipped   = "skipped"
)

// StartAutomationRun records a run as running and returns it.
func (db *DB) StartAutomationRun(ctx context.Context, automation, target, trigger string, startedAtMS int64) (AutomationRun, error) {
	r := AutomationRun{ID: id.New(), Automation: automation, Target: target, Trigger: trigger, Status: AutomationRunning, StartedAt: startedAtMS}
	_, err := db.sql.ExecContext(ctx, `
		INSERT INTO automation_runs (id, automation, target, trigger, status, started_at) VALUES (?, ?, ?, ?, ?, ?)`,
		r.ID, r.Automation, r.Target, r.Trigger, r.Status, r.StartedAt)
	if err != nil {
		return r, fmt.Errorf("store: start automation run: %w", err)
	}
	return r, nil
}

// FinishAutomationRun records a run's outcome.
func (db *DB) FinishAutomationRun(ctx context.Context, r AutomationRun) error {
	var cost any
	if r.CostUSD != nil {
		cost = *r.CostUSD
	}
	_, err := db.sql.ExecContext(ctx, `
		UPDATE automation_runs SET status = ?, finished_at = ?, cost_usd = ?, summary = ?, error = ?, report_path = ? WHERE id = ?`,
		r.Status, r.FinishedAt, cost, r.Summary, r.Error, r.ReportPath, r.ID)
	if err != nil {
		return fmt.Errorf("store: finish automation run: %w", err)
	}
	return nil
}

// ListAutomationRuns returns an automation's runs, newest first. target ""
// lists every target.
func (db *DB) ListAutomationRuns(ctx context.Context, automation, target string, limit int) ([]AutomationRun, error) {
	if limit <= 0 {
		limit = 20
	}
	q := `SELECT id, automation, target, trigger, status, started_at, finished_at, cost_usd, summary, error, report_path
		FROM automation_runs WHERE automation = ?`
	args := []any{automation}
	if target != "" {
		q += ` AND target = ?`
		args = append(args, target)
	}
	q += ` ORDER BY started_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.sql.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list automation runs: %w", err)
	}
	defer rows.Close()
	out := make([]AutomationRun, 0)
	for rows.Next() {
		var r AutomationRun
		var cost sql.NullFloat64
		if err := rows.Scan(&r.ID, &r.Automation, &r.Target, &r.Trigger, &r.Status, &r.StartedAt, &r.FinishedAt, &cost, &r.Summary, &r.Error, &r.ReportPath); err != nil {
			return nil, err
		}
		if cost.Valid {
			v := cost.Float64
			r.CostUSD = &v
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// FailInterruptedAutomationRuns marks every run still `running` as failed --
// a daemon that stopped mid-run cannot finish it. Called at project open.
func (db *DB) FailInterruptedAutomationRuns(ctx context.Context) error {
	_, err := db.sql.ExecContext(ctx, `
		UPDATE automation_runs SET status = ?, finished_at = ?, error = 'interrupted: the daemon stopped before this run finished'
		WHERE status = ?`, AutomationFailed, nowMS(), AutomationRunning)
	return err
}

// AppendAutomationEvent appends a project-level automation event (e.g.
// automation.eval_regressed) for the integration bus and the activity feed.
func (db *DB) AppendAutomationEvent(ctx context.Context, automation, eventType, operation string) error {
	return db.ApplyNoop(ctx, Event{AggregateType: "automation", AggregateID: automation, Actor: ActorOrchestrator, EventType: eventType, Operation: operation})
}
