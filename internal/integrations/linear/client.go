// Package linear is the two-way Linear integration: outbound, an approved
// execution plan becomes one Linear issue per task under a parent issue per
// initiative, kept in sync as tasks and change sets move; inbound, a poll for
// issues carrying a label turns each into an initiative.
//
// Polling rather than webhooks for the same reason Slack uses Socket Mode:
// the daemon is loopback-only and a webhook needs a public URL.
package linear

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/devarispbrown/idea-harness/internal/integrations"
)

// DefaultEndpoint is Linear's GraphQL API.
const DefaultEndpoint = "https://api.linear.app/graphql"

// Client is a minimal Linear GraphQL client over net/http: a handful of
// typed queries, no generated SDK.
type Client struct {
	Endpoint string
	APIKey   string
	HTTP     *http.Client
}

// gqlError is one entry of a GraphQL `errors` array.
type gqlError struct {
	Message    string `json:"message"`
	Extensions struct {
		Code string `json:"code"`
		Type string `json:"type"`
	} `json:"extensions"`
}

// defaultRateLimitWait is used when Linear signals a rate limit without
// saying when it resets.
const defaultRateLimitWait = time.Minute

// do runs one GraphQL operation and decodes `data` into out.
//
// Linear signals rate limiting two ways: HTTP 429, and HTTP 400 with an
// error whose extensions.code is RATELIMITED -- which also covers its
// complexity limit. Both become an integrations.RetryAfterError timed to the
// reset header when Linear sends one. Authentication and input errors are
// permanent; anything else is retried.
func (c *Client) do(ctx context.Context, query string, vars map[string]any, out any) error {
	if c.APIKey == "" {
		return integrations.Permanent(fmt.Errorf("linear: no API key configured"))
	}
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = DefaultEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	// A personal API key goes in Authorization as-is; "Bearer" is for OAuth
	// access tokens, and Linear rejects a personal key with it.
	req.Header.Set("Authorization", c.APIKey)
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("linear: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("linear: read: %w", err)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return &integrations.RetryAfterError{Err: fmt.Errorf("linear: rate limited (HTTP 429)"), After: resetAfter(resp.Header)}
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return integrations.Permanent(fmt.Errorf("linear: HTTP %d: check LINEAR_API_KEY", resp.StatusCode))
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("linear: HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []gqlError      `json:"errors"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("linear: HTTP %d: undecodable response", resp.StatusCode)
	}
	if len(envelope.Errors) > 0 {
		e := envelope.Errors[0]
		err := fmt.Errorf("linear: %s", e.Message)
		switch strings.ToUpper(e.Extensions.Code) {
		case "RATELIMITED":
			return &integrations.RetryAfterError{Err: err, After: resetAfter(resp.Header)}
		case "AUTHENTICATION_ERROR", "FORBIDDEN", "INPUT_ERROR", "GRAPHQL_VALIDATION_FAILED", "BAD_USER_INPUT", "INVALID_INPUT":
			return integrations.Permanent(err)
		}
		if resp.StatusCode == http.StatusBadRequest {
			return integrations.Permanent(err)
		}
		return err
	}
	if out != nil {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("linear: decode: %w", err)
		}
	}
	return nil
}

// resetAfter reads Linear's reset hints: Retry-After (seconds), else
// X-RateLimit-Requests-Reset / X-RateLimit-Complexity-Reset (epoch ms).
func resetAfter(h http.Header) time.Duration {
	if s, err := strconv.Atoi(h.Get("Retry-After")); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	for _, k := range []string{"X-RateLimit-Requests-Reset", "X-RateLimit-Complexity-Reset"} {
		if ms, err := strconv.ParseInt(h.Get(k), 10, 64); err == nil && ms > 0 {
			if d := time.Until(time.UnixMilli(ms)); d > 0 {
				return d
			}
		}
	}
	return defaultRateLimitWait
}

// State is a team workflow state.
type State struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Type is Linear's category: triage, backlog, unstarted, started,
	// completed, canceled.
	Type string `json:"type"`
}

// Team is a team and its workflow states.
type Team struct {
	ID     string  `json:"id"`
	Key    string  `json:"key"`
	Name   string  `json:"name"`
	States []State `json:"-"`
}

// Issue is the slice of a Linear issue this integration reads.
type Issue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	State       State  `json:"state"`
}

// Viewer returns the API key's user, which validates the key.
func (c *Client) Viewer(ctx context.Context) (string, error) {
	var out struct {
		Viewer struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"viewer"`
	}
	if err := c.do(ctx, `query { viewer { name email } }`, nil, &out); err != nil {
		return "", err
	}
	return out.Viewer.Name, nil
}

// TeamByKey resolves a team key (ENG) to the team and its states.
func (c *Client) TeamByKey(ctx context.Context, key string) (*Team, error) {
	var out struct {
		Teams struct {
			Nodes []struct {
				ID     string `json:"id"`
				Key    string `json:"key"`
				Name   string `json:"name"`
				States struct {
					Nodes []State `json:"nodes"`
				} `json:"states"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	err := c.do(ctx, `query Team($key: String!) {
  teams(filter: { key: { eq: $key } }) { nodes { id key name states { nodes { id name type } } } }
}`, map[string]any{"key": key}, &out)
	if err != nil {
		return nil, err
	}
	if len(out.Teams.Nodes) == 0 {
		return nil, integrations.Permanent(fmt.Errorf("linear: no team with key %q visible to this API key", key))
	}
	n := out.Teams.Nodes[0]
	return &Team{ID: n.ID, Key: n.Key, Name: n.Name, States: n.States.Nodes}, nil
}

// ProjectByName resolves a project name to its id.
func (c *Client) ProjectByName(ctx context.Context, name string) (string, error) {
	var out struct {
		Projects struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"projects"`
	}
	err := c.do(ctx, `query Project($name: String!) {
  projects(filter: { name: { eq: $name } }, first: 1) { nodes { id } }
}`, map[string]any{"name": name}, &out)
	if err != nil {
		return "", err
	}
	if len(out.Projects.Nodes) == 0 {
		return "", integrations.Permanent(fmt.Errorf("linear: no project named %q", name))
	}
	return out.Projects.Nodes[0].ID, nil
}

// LabelExists reports whether an issue label with this name exists.
func (c *Client) LabelExists(ctx context.Context, name string) (bool, error) {
	var out struct {
		IssueLabels struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"issueLabels"`
	}
	err := c.do(ctx, `query Label($name: String!) {
  issueLabels(filter: { name: { eqIgnoreCase: $name } }, first: 1) { nodes { id } }
}`, map[string]any{"name": name}, &out)
	if err != nil {
		return false, err
	}
	return len(out.IssueLabels.Nodes) > 0, nil
}

// IssueInput is issueCreate's input, trimmed to what is used.
type IssueInput struct {
	TeamID      string `json:"teamId"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	ParentID    string `json:"parentId,omitempty"`
	ProjectID   string `json:"projectId,omitempty"`
	StateID     string `json:"stateId,omitempty"`
}

// CreateIssue creates an issue.
func (c *Client) CreateIssue(ctx context.Context, in IssueInput) (*Issue, error) {
	var out struct {
		IssueCreate struct {
			Success bool  `json:"success"`
			Issue   Issue `json:"issue"`
		} `json:"issueCreate"`
	}
	err := c.do(ctx, `mutation CreateIssue($input: IssueCreateInput!) {
  issueCreate(input: $input) { success issue { id identifier url title state { id name type } } }
}`, map[string]any{"input": in}, &out)
	if err != nil {
		return nil, err
	}
	if !out.IssueCreate.Success || out.IssueCreate.Issue.ID == "" {
		return nil, fmt.Errorf("linear: issueCreate did not succeed")
	}
	return &out.IssueCreate.Issue, nil
}

// SetIssueState moves an issue to a workflow state.
func (c *Client) SetIssueState(ctx context.Context, issueID, stateID string) error {
	var out struct {
		IssueUpdate struct {
			Success bool `json:"success"`
		} `json:"issueUpdate"`
	}
	err := c.do(ctx, `mutation SetState($id: String!, $stateId: String!) {
  issueUpdate(id: $id, input: { stateId: $stateId }) { success }
}`, map[string]any{"id": issueID, "stateId": stateID}, &out)
	if err == nil && !out.IssueUpdate.Success {
		err = fmt.Errorf("linear: issueUpdate did not succeed")
	}
	return err
}

// Comment adds a comment to an issue.
func (c *Client) Comment(ctx context.Context, issueID, body string) error {
	var out struct {
		CommentCreate struct {
			Success bool `json:"success"`
		} `json:"commentCreate"`
	}
	err := c.do(ctx, `mutation Comment($issueId: String!, $body: String!) {
  commentCreate(input: { issueId: $issueId, body: $body }) { success }
}`, map[string]any{"issueId": issueID, "body": body}, &out)
	if err == nil && !out.CommentCreate.Success {
		err = fmt.Errorf("linear: commentCreate did not succeed")
	}
	return err
}

// LabeledIssues lists a team's issues carrying label (case-insensitive),
// oldest first, at most 50 per poll -- a backlog drains over a few polls
// instead of one expensive query.
func (c *Client) LabeledIssues(ctx context.Context, teamKey, label string) ([]Issue, error) {
	var out struct {
		Issues struct {
			Nodes []Issue `json:"nodes"`
		} `json:"issues"`
	}
	err := c.do(ctx, `query Labeled($team: String!, $label: String!) {
  issues(first: 50, orderBy: createdAt, filter: { team: { key: { eq: $team } }, labels: { name: { eqIgnoreCase: $label } } }) {
    nodes { id identifier title description url state { id name type } }
  }
}`, map[string]any{"team": teamKey, "label": label}, &out)
	if err != nil {
		return nil, err
	}
	return out.Issues.Nodes, nil
}

// IssuesByID fetches the current state of up to 50 issues.
func (c *Client) IssuesByID(ctx context.Context, ids []string) ([]Issue, error) {
	var out struct {
		Issues struct {
			Nodes []Issue `json:"nodes"`
		} `json:"issues"`
	}
	err := c.do(ctx, `query ByID($ids: [ID!]) {
  issues(first: 50, filter: { id: { in: $ids } }) { nodes { id identifier url state { id name type } } }
}`, map[string]any{"ids": ids}, &out)
	if err != nil {
		return nil, err
	}
	return out.Issues.Nodes, nil
}
