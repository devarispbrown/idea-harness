package store

import (
	"context"
	"database/sql"
	"time"

	"github.com/devarispbrown/idea-harness/internal/domain"
	"github.com/devarispbrown/idea-harness/internal/id"
)

// CreateDocComment inserts one human comment anchored to an artifact version.
func (db *DB) CreateDocComment(ctx context.Context, c *domain.DocComment) error {
	if c.ID == "" {
		c.ID = id.New()
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	if c.Author == "" {
		c.Author = HumanActor
	}
	return db.Apply(ctx, Event{
		AggregateType: "doc_comment",
		AggregateID:   c.ID,
		Actor:         c.Author,
		EventType:     "doc_comment.created",
	}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			INSERT INTO doc_comments
				(id, initiative_id, artifact_version_id, section, text, author, promoted_question_id, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ID, c.InitiativeID, c.ArtifactVersionID, c.Section, c.Text, c.Author, c.PromotedQuestionID, c.CreatedAt.UnixMilli())
		return err
	})
}

// ListDocComments returns comments for an artifact version, oldest first.
func (db *DB) ListDocComments(ctx context.Context, artifactVersionID string) ([]domain.DocComment, error) {
	rows, err := db.SQL().QueryContext(ctx, `
		SELECT id, initiative_id, artifact_version_id, section, text, author, promoted_question_id, created_at
		FROM doc_comments WHERE artifact_version_id = ? ORDER BY created_at ASC`, artifactVersionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]domain.DocComment, 0)
	for rows.Next() {
		var c domain.DocComment
		var created int64
		if err := rows.Scan(&c.ID, &c.InitiativeID, &c.ArtifactVersionID, &c.Section, &c.Text, &c.Author, &c.PromotedQuestionID, &created); err != nil {
			return nil, err
		}
		c.CreatedAt = time.UnixMilli(created)
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetDocComment returns one comment by id.
func (db *DB) GetDocComment(ctx context.Context, commentID string) (*domain.DocComment, error) {
	var c domain.DocComment
	var created int64
	err := db.SQL().QueryRowContext(ctx, `
		SELECT id, initiative_id, artifact_version_id, section, text, author, promoted_question_id, created_at
		FROM doc_comments WHERE id = ?`, commentID).
		Scan(&c.ID, &c.InitiativeID, &c.ArtifactVersionID, &c.Section, &c.Text, &c.Author, &c.PromotedQuestionID, &created)
	if err != nil {
		return nil, notFoundIfNoRows(err, "doc comment", commentID)
	}
	c.CreatedAt = time.UnixMilli(created)
	return &c, nil
}

// MarkDocCommentPromoted records the question a comment was promoted into. The
// update is conditional on the comment being unpromoted, so a concurrent or
// duplicate promote affects zero rows and fails rather than overwriting the
// first link (last-writer-wins).
func (db *DB) MarkDocCommentPromoted(ctx context.Context, commentID, questionID string) error {
	return db.Apply(ctx, Event{
		AggregateType: "doc_comment",
		AggregateID:   commentID,
		Actor:         ActorOrchestrator,
		EventType:     "doc_comment.promoted",
		Operation:     questionID,
	}, func(tx *sql.Tx) error {
		res, err := tx.Exec(`UPDATE doc_comments SET promoted_question_id = ? WHERE id = ? AND promoted_question_id = ''`, questionID, commentID)
		if err != nil {
			return err
		}
		return requireOneRow(res, "doc comment %q not found or already promoted", commentID)
	})
}
