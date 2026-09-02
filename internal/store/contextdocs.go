package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/idea-harness/idea-harness/internal/domain"
	"github.com/idea-harness/idea-harness/internal/id"
)

// CreateContextDoc inserts a context document and appends its creation event.
func (db *DB) CreateContextDoc(ctx context.Context, doc *domain.ContextDoc) error {
	if doc.ID == "" {
		doc.ID = id.New()
	}
	if doc.CreatedAt.IsZero() {
		doc.CreatedAt = time.Now().UTC()
	}
	return db.Apply(ctx, Event{
		AggregateType: "context_doc",
		AggregateID:   doc.ID,
		Actor:         ActorOrchestrator,
		EventType:     "context_doc.created",
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO context_docs (id, initiative_id, kind, title, digest, preview, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			doc.ID, doc.InitiativeID, doc.Kind, doc.Title, doc.Digest, doc.Preview, doc.CreatedAt.UnixMilli())
		return err
	})
}

// ListContextDocs returns context documents for an initiative, oldest first.
func (db *DB) ListContextDocs(ctx context.Context, initiativeID string) ([]domain.ContextDoc, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, kind, title, digest, preview, created_at
		FROM context_docs WHERE initiative_id = ? ORDER BY created_at ASC`, initiativeID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.ContextDoc, 0)
	for rows.Next() {
		var d domain.ContextDoc
		var created int64
		if err := rows.Scan(&d.ID, &d.InitiativeID, &d.Kind, &d.Title, &d.Digest, &d.Preview, &created); err != nil {
			return nil, err
		}
		d.CreatedAt = time.UnixMilli(created)
		out = append(out, d)
	}
	return out, rows.Err()
}

// GetContextDoc returns a single context document by id.
func (db *DB) GetContextDoc(ctx context.Context, id string) (*domain.ContextDoc, error) {
	row := db.SQL().QueryRowContext(ctx, `
		SELECT id, initiative_id, kind, title, digest, preview, created_at
		FROM context_docs WHERE id = ?`, id)
	var d domain.ContextDoc
	var created int64
	if err := row.Scan(&d.ID, &d.InitiativeID, &d.Kind, &d.Title, &d.Digest, &d.Preview, &created); err != nil {
		return nil, err
	}
	d.CreatedAt = time.UnixMilli(created)
	return &d, nil
}
