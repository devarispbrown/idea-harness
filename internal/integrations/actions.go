package integrations

import (
	"context"
	"fmt"
)

// Actions is everything an integration may DO to a project, as opposed to
// observe. The daemon implements it by sending each call through its own
// authenticated HTTP handler (internal/daemon/integrations.go) -- the same
// handler, preconditions and gates `idea review`, `idea answer`, `idea new`
// and `idea accept-recommendations` reach -- so an integration has no path to
// a state change the CLI does not, and no way around a gate the CLI cannot
// get around. Approving a change set with open blocking findings from Slack
// is refused for exactly the reason, and with exactly the message, it is
// refused at the terminal.
type Actions interface {
	ReviewChangeSet(ctx context.Context, projectID, changeSetID, verdict string) error
	AcceptRecommendations(ctx context.Context, projectID, initiativeID string) (resolved []string, err error)
	AnswerQuestion(ctx context.Context, projectID, questionID, answer string) error
	CreateInitiative(ctx context.Context, projectID string, in NewInitiative) (initiativeID string, err error)
	ListInitiatives(ctx context.Context, projectID string) ([]InitiativeSummary, error)
	// Operate actions (slash commands): the same gates as the CLI.
	ApproveInitiative(ctx context.Context, projectID, initiativeID string) error
	AdvanceInitiative(ctx context.Context, projectID, initiativeID string) error
	ExecuteInitiative(ctx context.Context, projectID, initiativeID string) error
	ApplyChangeSet(ctx context.Context, projectID, changeSetID string) error
}

// NewInitiative is a request to start an initiative.
type NewInitiative struct {
	Title       string
	Description string
	Docs        []Doc
}

// Doc is a context document attached at creation. Context documents enter
// every model prompt at the external_untrusted trust level, framed as data
// (internal/context): that, and not the initiative's own description, is
// where text written by someone outside this machine belongs.
type Doc struct {
	Title   string
	Kind    string
	Content string
}

// InitiativeSummary is one row of a project's initiative list.
type InitiativeSummary struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	ShortTitle     string `json:"short_title,omitempty"`
	Status         string `json:"status"`
	JudgmentReason string `json:"judgment_reason,omitempty"`
}

// ActionError is a refusal from the daemon's handler: the HTTP status and
// the handler's own message, which is what an integration shows its user.
type ActionError struct {
	Status  int
	Message string
}

func (e *ActionError) Error() string { return fmt.Sprintf("%d: %s", e.Status, e.Message) }
