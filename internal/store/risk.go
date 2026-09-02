package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// SaveRiskProfile persists a deterministic risk profile for an initiative. A
// profile is append-only history: every computed profile is retained so the tier
// floor can be re-derived from past scans.
func (db *DB) SaveRiskProfile(ctx context.Context, p *domain.RiskProfile) error {
	if p.ID == "" {
		return fmt.Errorf("store: risk profile id is required")
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	signalsJSON, err := json.Marshal(p.Signals)
	if err != nil {
		return fmt.Errorf("store: marshal risk signals: %w", err)
	}
	reasonJSON, err := json.Marshal(p.Reason)
	if err != nil {
		return fmt.Errorf("store: marshal risk reason: %w", err)
	}
	return db.Apply(ctx, Event{
		AggregateType: "risk_profile",
		AggregateID:   p.ID,
		Actor:         ActorOrchestrator,
		EventType:     "risk_profile.saved",
		Operation:     string(p.Tier),
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO risk_profiles (id, initiative_id, tier, signals_json, reason_json, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			p.ID, p.InitiativeID, p.Tier, string(signalsJSON), string(reasonJSON), p.CreatedAt.UnixMilli())
		return err
	})
}
