package workflow

import (
	"context"
	"fmt"
	"strings"

	"github.com/devarispbrown/idea-harness/internal/domain"
)

// PromoteDocComment turns a document comment into an open Question for the
// initiative's judgment flow, and links the comment to the new question (so it
// is not promotable twice). The section, when present, is prefixed to the
// question so the question names where in the document it came from.
func (e *Engine) PromoteDocComment(ctx context.Context, commentID string) (*domain.Question, error) {
	c, err := e.db.GetDocComment(ctx, commentID)
	if err != nil {
		return nil, err
	}
	if c.PromotedQuestionID != "" {
		return nil, fmt.Errorf("workflow: comment %q was already promoted to a question", commentID)
	}
	text := strings.TrimSpace(c.Text)
	if text == "" {
		return nil, fmt.Errorf("workflow: comment %q has no text to promote", commentID)
	}
	if c.Section != "" {
		text = "[" + c.Section + "] " + text
	}
	q := &domain.Question{InitiativeID: c.InitiativeID, Question: e.redact(text)}
	if err := e.db.CreateQuestion(ctx, q); err != nil {
		return nil, err
	}
	if err := e.db.MarkDocCommentPromoted(ctx, commentID, q.ID); err != nil {
		return nil, err
	}
	return q, nil
}
