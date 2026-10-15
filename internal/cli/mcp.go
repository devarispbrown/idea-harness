package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/devarispbrown/idea-harness/internal/buildinfo"
	"github.com/devarispbrown/idea-harness/internal/mcp"
)

// `idea mcp` serves idea-harness to MCP clients -- Claude Code, Cursor, Codex,
// anything that speaks the Model Context Protocol over stdio.
//
// It is a third surface over the same daemon API, not a second implementation
// of the workflow: every tool is a call through this package's HTTP client
// (connect() -> ensureDaemon(), so it auto-starts the daemon and carries the
// token like every other command) and reuses the CLI's own resolution helpers
// -- resolveInitiative, resolveQuestionRef, resolveDecision, queryTrace -- so a
// reference means the same thing typed at a shell or chosen by a model. It
// never opens state.db. Every human gate the daemon enforces (a change set
// whose adversarial review never completed cannot be approved; apply needs an
// approved change set) is enforced on these calls too, because they are the
// same calls.

// mcpTier is how much of the tool surface a server instance exposes.
type mcpTier int

const (
	// mcpTierRead: tools that only read.
	mcpTierRead mcpTier = iota
	// mcpTierJudgment: tools that record a judgment or start model work --
	// answer, resolve, review, verify, new initiative. None of them touches
	// the working tree.
	mcpTierJudgment
	// mcpTierApply: tools that change the user's working tree -- apply and
	// revert.
	mcpTierApply
)

// mcpStdin is the protocol input. A var so a test could drive cmdMCP itself;
// the protocol tests drive serveMCP over pipes directly.
var mcpStdin io.Reader = os.Stdin

// cmdMCP runs the stdio MCP server until stdin closes.
//
// The default tier is read + judgment, with apply/revert behind --allow-apply.
// The line is drawn at the working tree, not at "writes": answering a question
// or approving a change set records a judgment the daemon keeps, can be
// revisited (reopen-decision, a later review), and changes no file. Applying a
// change set writes the user's checkout, and a model driving an MCP client
// should only be able to do that when the human configuring the server opted
// in. --read-only is for a client that should look and never touch.
func cmdMCP(a cmdArgs, stdout, stderr io.Writer) error {
	if len(a.positional) > 0 {
		return fmt.Errorf("idea mcp takes no arguments, got %s (it speaks MCP on stdin/stdout; see docs/mcp.md)", strings.Join(a.positional, " "))
	}
	tier := mcpTierJudgment
	switch {
	case a.has("--read-only") && a.has("--allow-apply"):
		return fmt.Errorf("--read-only and --allow-apply contradict each other; pick one")
	case a.has("--read-only"):
		tier = mcpTierRead
	case a.has("--allow-apply"):
		tier = mcpTierApply
	}
	if dir := a.value("--dir"); dir != "" {
		// Some hosts launch MCP servers from their own working directory
		// rather than the project's, and every tool resolves "this project"
		// from the cwd, the same way every other command does.
		if err := os.Chdir(dir); err != nil {
			return fmt.Errorf("--dir: %w", err)
		}
	}
	// stdout is the protocol channel; a stray ANSI escape in a tool's text
	// would reach the model. Nothing here styles output, and this keeps it so.
	colorEnabled = false

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := newMCPServer(tier, stderr)
	fmt.Fprintf(stderr, "idea mcp: serving %d tools (%s) on stdio\n", len(srv.Tools), tier)
	return srv.Serve(ctx, mcpStdin, stdout)
}

func (t mcpTier) String() string {
	switch t {
	case mcpTierRead:
		return "read-only"
	case mcpTierApply:
		return "read + judgment + apply"
	default:
		return "read + judgment"
	}
}

// mcpInstructions is what a host shows its model about this server.
const mcpInstructions = `idea-harness is a local-first product-development control plane: an idea becomes a reviewed PRD, then an execution plan, then change sets a coding agent produced, each reviewed before it is applied to the repository.

Start with status (or next_step) to see where the current initiative stands and what it is waiting on. Initiative-scoped tools default to the most recently created initiative; pass "initiative" (an id from list_initiatives) to pick another.

The human is the reviewer of record. Prefer reading (list_findings, show_change_set, show_prd) before recording a judgment, and say what you are about to approve or resolve. Long-running work (new_initiative, review rerun, apply, revert) returns an operation_id at once; poll get_operation or status rather than calling again.`

// newMCPServer builds the server for a tier.
func newMCPServer(tier mcpTier, stderr io.Writer) *mcp.Server {
	all := mcpToolTable(tier)
	srv := &mcp.Server{
		Info:         mcp.Implementation{Name: "idea-harness", Title: "idea-harness", Version: mcpVersion()},
		Instructions: mcpInstructions,
		Withheld:     map[string]string{},
		ResourceTemplates: []mcp.ResourceTemplate{
			{URITemplate: "idea://initiative/{initiative_id}/prd", Name: "prd", Title: "PRD", Description: "The latest reviewed PRD of an initiative, as markdown.", MimeType: "text/markdown"},
			{URITemplate: "idea://initiative/{initiative_id}/execution_plan", Name: "execution_plan", Title: "Execution plan", Description: "The latest execution plan of an initiative, as markdown.", MimeType: "text/markdown"},
			{URITemplate: "idea://initiative/{initiative_id}/{kind}", Name: "artifact", Title: "Artifact", Description: "The latest artifact of any kind (technical_design, design_spec, verification_plan, threat_model, …) of an initiative, as markdown.", MimeType: "text/markdown"},
		},
		ListResources: mcpListResources,
		ReadResource:  mcpReadResource,
		Logf:          func(format string, args ...any) { fmt.Fprintf(stderr, format+"\n", args...) },
	}
	for _, t := range all {
		if t.tier <= tier {
			srv.Tools = append(srv.Tools, t.Tool)
			continue
		}
		if t.tier == mcpTierApply {
			srv.Withheld[t.Name] = "not enabled: it changes the working tree; restart the server as `idea mcp --allow-apply` to expose it"
		} else {
			srv.Withheld[t.Name] = "not enabled: this server was started with `idea mcp --read-only`"
		}
	}
	return srv
}

func mcpVersion() string {
	if v := buildinfo.Get().Version; v != "" {
		return v
	}
	return "dev"
}

type mcpToolDef struct {
	mcp.Tool
	tier mcpTier
}

// ---------- schema helpers ----------

func mcpObject(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func mcpString(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func mcpEnum(desc string, values ...string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": values}
}

var mcpInitiativeProp = map[string]any{
	"type":        "string",
	"description": "Initiative id, as list_initiatives returns it. Optional: defaults to the most recently created initiative, the same default every idea command uses.",
}

var mcpGraphTypes = []string{"requirement", "task", "decision", "artifact", "assertion", "context_doc", "objective", "metric", "change_set"}

func mcpGraphSchema() map[string]any {
	return mcpObject(map[string]any{
		"type":       mcpEnum("What kind of node the reference names.", mcpGraphTypes...),
		"ref":        mcpString("The node's code or id: REQ-1, TASK-2, DEC-3, an artifact kind such as prd, or a raw id."),
		"initiative": mcpInitiativeProp,
	}, "type", "ref")
}

var (
	annRead = mcp.Annotations{ReadOnlyHint: true, IdempotentHint: true}
	// annJudgment: writes daemon state, additive, nothing in the repository.
	annJudgment = mcp.Annotations{}
	// annDestructive: overwrites a recorded state or the working tree.
	annDestructive = mcp.Annotations{DestructiveHint: true}
)

// mcpArgs is the union of every tool's arguments. Schemas are validated before
// a handler runs (additionalProperties: false), so a field a tool does not
// declare never arrives here.
type mcpArgs struct {
	Initiative     string `json:"initiative"`
	Idea           string `json:"idea"`
	Kind           string `json:"kind"`
	Type           string `json:"type"`
	Ref            string `json:"ref"`
	Question       string `json:"question"`
	Answer         string `json:"answer"`
	Decision       string `json:"decision"`
	Resolution     string `json:"resolution"`
	ChangeSet      string `json:"change_set"`
	Verdict        string `json:"verdict"`
	AcceptFindings bool   `json:"accept_findings"`
	Requirement    string `json:"requirement"`
	OperationID    string `json:"operation_id"`
	Metric         string `json:"metric"`
	// Value is a pointer so record_metric can tell 0 from absent.
	Value *float64 `json:"value"`
	Note  string   `json:"note"`
}

// mcpHandler adapts a typed handler to mcp.Handler.
func mcpHandler(f func(args mcpArgs) (mcp.Result, error)) mcp.Handler {
	return func(_ context.Context, raw json.RawMessage) (mcp.Result, error) {
		var a mcpArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return mcp.Result{}, err
		}
		return f(a)
	}
}

// mcpInitiative connects and resolves the initiative a call is about.
func mcpInitiative(ref string) (*client, string, string, error) {
	c, projectID, err := connect()
	if err != nil {
		return nil, "", "", err
	}
	var args []string
	if ref != "" {
		args = []string{ref}
	}
	iniID, err := resolveInitiative(c, projectID, args)
	if err != nil {
		return nil, "", "", err
	}
	return c, projectID, iniID, nil
}

// nonNil keeps an empty list an empty list in structuredContent; a nil slice
// marshals to null, which an outputSchema-minded client reads as "absent".
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// ---------- the tool table ----------

// mcpToolTable is every tool, in the order tools/list reports them. The tier
// decides which ones a given server exposes; the handlers take it so a hint
// can name a tool only when this server actually offers it.
func mcpToolTable(tier mcpTier) []mcpToolDef {
	return []mcpToolDef{
		{tier: mcpTierRead, Tool: mcp.Tool{
			Name: "list_initiatives", Title: "List initiatives",
			Description: "List this project's initiatives (oldest first) with their status. The last one is the default every other tool uses when no initiative is given.",
			InputSchema: mcpObject(map[string]any{}), Annotations: annRead,
			Handler: mcpHandler(func(mcpArgs) (mcp.Result, error) { return mcpListInitiatives() }),
		}},
		{tier: mcpTierRead, Tool: mcp.Tool{
			Name: "status", Title: "Initiative status",
			Description: "One initiative's status, requirement/decision/question counts, the daemon's recommended next move and the next workflow action. Start here.",
			InputSchema: mcpObject(map[string]any{"initiative": mcpInitiativeProp}), Annotations: annRead,
			Handler: mcpHandler(mcpStatus),
		}},
		{tier: mcpTierRead, Tool: mcp.Tool{
			Name: "next_step", Title: "Next step",
			Description: "What the initiative is waiting on next (the same answer `idea next` gives), with the tool or command that moves it. Reads only; it never runs the step.",
			InputSchema: mcpObject(map[string]any{"initiative": mcpInitiativeProp}), Annotations: annRead,
			Handler: mcpHandler(func(a mcpArgs) (mcp.Result, error) { return mcpNextStep(a, tier) }),
		}},
		{tier: mcpTierRead, Tool: mcp.Tool{
			Name: "show_prd", Title: "Show PRD",
			Description: "The latest PRD as markdown -- or, with kind, another artifact: execution_plan, technical_design, design_spec, verification_plan, threat_model, idea_brief, research_brief.",
			InputSchema: mcpObject(map[string]any{
				"kind":       mcpString("Artifact kind. Defaults to prd."),
				"initiative": mcpInitiativeProp,
			}), Annotations: annRead,
			Handler: mcpHandler(mcpShowArtifact),
		}},
		{tier: mcpTierRead, Tool: mcp.Tool{
			Name: "list_tasks", Title: "List plan tasks",
			Description: "The execution plan's tasks in order, with priority, status, requirement links and dependencies.",
			InputSchema: mcpObject(map[string]any{"initiative": mcpInitiativeProp}), Annotations: annRead,
			Handler: mcpHandler(mcpListTasks),
		}},
		{tier: mcpTierRead, Tool: mcp.Tool{
			Name: "list_findings", Title: "List review findings",
			Description: "The adversarial reviewer's findings on the PRD, plan and change sets, with severity and recommendation.",
			InputSchema: mcpObject(map[string]any{"initiative": mcpInitiativeProp}), Annotations: annRead,
			Handler: mcpHandler(mcpListFindings),
		}},
		{tier: mcpTierRead, Tool: mcp.Tool{
			Name: "list_questions", Title: "List questions",
			Description: "The initiative's open and answered questions. The short ref shown is what answer_question accepts.",
			InputSchema: mcpObject(map[string]any{"initiative": mcpInitiativeProp}), Annotations: annRead,
			Handler: mcpHandler(mcpListQuestions),
		}},
		{tier: mcpTierRead, Tool: mcp.Tool{
			Name: "list_decisions", Title: "List decisions",
			Description: "The initiative's decisions with status, recommendation and numbered options. resolve_decision accepts the code and an option number.",
			InputSchema: mcpObject(map[string]any{"initiative": mcpInitiativeProp}), Annotations: annRead,
			Handler: mcpHandler(mcpListDecisions),
		}},
		{tier: mcpTierRead, Tool: mcp.Tool{
			Name: "why", Title: "Why (provenance)",
			Description: "The provenance chain of a requirement, task, decision, artifact, assertion or context doc: what it was derived from.",
			InputSchema: mcpGraphSchema(), Annotations: annRead,
			Handler: mcpHandler(func(a mcpArgs) (mcp.Result, error) { return mcpTrace("why", a) }),
		}},
		{tier: mcpTierRead, Tool: mcp.Tool{
			Name: "impact", Title: "Impact (dependents)",
			Description: "What depends on a node, and whether each dependent is stale relative to it.",
			InputSchema: mcpGraphSchema(), Annotations: annRead,
			Handler: mcpHandler(func(a mcpArgs) (mcp.Result, error) { return mcpTrace("impact", a) }),
		}},
		{tier: mcpTierRead, Tool: mcp.Tool{
			Name: "trace", Title: "Trace",
			Description: "Both directions at once: a node's provenance (why) and its dependents (impact).",
			InputSchema: mcpGraphSchema(), Annotations: annRead,
			Handler: mcpHandler(func(a mcpArgs) (mcp.Result, error) { return mcpTrace("trace", a) }),
		}},
		{tier: mcpTierRead, Tool: mcp.Tool{
			Name: "gaps", Title: "Product Graph gaps",
			Description: "What the Product Graph is missing or no longer holds, most severe first: requirements with no task or no applied change set, acceptance criteria with no evidence, failed or stale (code changed since) evidence, unvalidated assumptions the plan rests on, decisions whose basis moved, orphan tasks and change sets, artifacts older than what they were generated from. Deterministic, no model call. Each gap names its nodes, why, and the command that closes it.",
			InputSchema: mcpObject(map[string]any{
				"kind":       mcpString("Comma-separated gap kinds to keep, e.g. unverified_requirement,invalidated_decision. Defaults to every kind."),
				"initiative": mcpInitiativeProp,
			}), Annotations: annRead,
			Handler: mcpHandler(mcpGaps),
		}},
		{tier: mcpTierRead, Tool: mcp.Tool{
			Name: "changed", Title: "What changed",
			Description: "The initiative's timeline in plain language: what happened since it started, newest last.",
			InputSchema: mcpObject(map[string]any{"initiative": mcpInitiativeProp}), Annotations: annRead,
			Handler: mcpHandler(mcpChanged),
		}},
		{tier: mcpTierRead, Tool: mcp.Tool{
			Name: "list_change_sets", Title: "List change sets",
			Description: "The initiative's change sets (CHG-n) with their review/apply state.",
			InputSchema: mcpObject(map[string]any{"initiative": mcpInitiativeProp}), Annotations: annRead,
			Handler: mcpHandler(mcpListChangeSets),
		}},
		{tier: mcpTierRead, Tool: mcp.Tool{
			Name: "show_change_set", Title: "Show change set and diff",
			Description: "One change set's record and its diff (unified patch, secrets redacted; very large patches are truncated).",
			InputSchema: mcpObject(map[string]any{"change_set": mcpString("Change set id or human id, e.g. CHG-1.")}, "change_set"),
			Annotations: annRead,
			Handler:     mcpHandler(mcpShowChangeSet),
		}},
		{tier: mcpTierRead, Tool: mcp.Tool{
			Name: "get_operation", Title: "Get operation",
			Description: "The state of a background operation by the operation_id another tool returned: running, succeeded, failed or cancelled.",
			InputSchema: mcpObject(map[string]any{"operation_id": mcpString("Operation id.")}, "operation_id"),
			Annotations: annRead,
			Handler:     mcpHandler(mcpGetOperation),
		}},

		{tier: mcpTierRead, Tool: mcp.Tool{
			Name: "list_objectives", Title: "List objectives",
			Description: "The initiative's objectives (OBJ-n): the outcome each states, how many requirements serve it and how many metrics measure it.",
			InputSchema: mcpObject(map[string]any{"initiative": mcpInitiativeProp}), Annotations: annRead,
			Handler: mcpHandler(mcpListObjectives),
		}},
		{tier: mcpTierRead, Tool: mcp.Tool{
			Name: "list_metrics", Title: "List outcome metrics with drift",
			Description: "The initiative's outcome metrics (MET-n) with their deterministic drift (on_track, off_track, regressed, stale, no_data) and the reasons, plus the outcome summary: requirements that are verified but whose objective is failing, and objectives with no metric.",
			InputSchema: mcpObject(map[string]any{"initiative": mcpInitiativeProp}), Annotations: annRead,
			Handler: mcpHandler(mcpListMetrics),
		}},

		{tier: mcpTierJudgment, Tool: mcp.Tool{
			Name: "new_initiative", Title: "Start a new initiative",
			Description: "Create an initiative from an idea and start its workflow (framing, research, a reviewed PRD). Calls the configured model provider and spends its budget. Returns at once with an operation_id; poll get_operation or status.",
			InputSchema: mcpObject(map[string]any{"idea": mcpString("The idea, in a sentence or a paragraph.")}, "idea"),
			Annotations: mcp.Annotations{OpenWorldHint: true},
			Handler:     mcpHandler(mcpNewInitiative),
		}},
		{tier: mcpTierJudgment, Tool: mcp.Tool{
			Name: "answer_question", Title: "Answer a question",
			Description: "Record the answer to an open question. Answer as the human would, or ask them first; this is their judgment on record.",
			InputSchema: mcpObject(map[string]any{
				"question":   mcpString("Question id, or the unambiguous prefix list_questions shows."),
				"answer":     mcpString("The answer."),
				"initiative": mcpInitiativeProp,
			}, "question", "answer"),
			Annotations: annJudgment,
			Handler:     mcpHandler(mcpAnswerQuestion),
		}},
		{tier: mcpTierJudgment, Tool: mcp.Tool{
			Name: "resolve_decision", Title: "Resolve a decision",
			Description: "Resolve an open decision by option number (1-based, as list_decisions numbers them) or free text. It can be reopened later with `idea reopen-decision`.",
			InputSchema: mcpObject(map[string]any{
				"decision":   mcpString("Decision code (DEC-1) or id."),
				"resolution": mcpString("An option number, or the resolution text."),
				"initiative": mcpInitiativeProp,
			}, "decision", "resolution"),
			Annotations: annJudgment,
			Handler:     mcpHandler(mcpResolveDecision),
		}},
		{tier: mcpTierJudgment, Tool: mcp.Tool{
			Name: "review_change_set", Title: "Review a change set",
			Description: "Record a verdict on a change set: approve, reject, changes_requested, or rerun (re-run an adversarial review that never completed; returns an operation_id). approve is refused when the review never completed or blocking findings are open, unless accept_findings signs them off. Approving does not apply anything.",
			InputSchema: mcpObject(map[string]any{
				"change_set":      mcpString("Change set id or human id, e.g. CHG-1."),
				"verdict":         mcpEnum("The verdict.", "approve", "reject", "changes_requested", "rerun"),
				"accept_findings": map[string]any{"type": "boolean", "description": "With approve only: sign off the reviewer's open findings and approve. Deny-coded findings still refuse."},
			}, "change_set", "verdict"),
			Annotations: annDestructive,
			Handler:     mcpHandler(mcpReviewChangeSet),
		}},
		{tier: mcpTierJudgment, Tool: mcp.Tool{
			Name: "record_metric", Title: "Record a metric value",
			Description: "Record a measured value for an outcome metric (MET-n), as the human who measured it. Returns the metric's drift after it and how many change sets landed since the previous value. Only record a number you or the human actually observed; never estimate one.",
			InputSchema: mcpObject(map[string]any{
				"metric":     mcpString("Metric code (MET-1) or id."),
				"value":      map[string]any{"type": "number", "description": "The observed value, in the metric's unit."},
				"note":       mcpString("Optional: where the number came from."),
				"initiative": mcpInitiativeProp,
			}, "metric", "value"),
			Annotations: annJudgment,
			Handler:     mcpHandler(mcpRecordMetric),
		}},
		{tier: mcpTierJudgment, Tool: mcp.Tool{
			Name: "verify", Title: "Run the verification gate",
			Description: "Run the verification gate and report per-requirement coverage; with requirement, report that one. It records no evidence: signing a requirement off (`idea verify REQ-n --pass`) is recorded as a human's word and stays a human action.",
			InputSchema: mcpObject(map[string]any{
				"requirement": mcpString("Optional requirement code, e.g. REQ-1."),
				"initiative":  mcpInitiativeProp,
			}),
			Annotations: mcp.Annotations{IdempotentHint: true},
			Handler:     mcpHandler(mcpVerify),
		}},

		{tier: mcpTierApply, Tool: mcp.Tool{
			Name: "apply_change_set", Title: "Apply a change set",
			Description: "Apply an approved (or previously reverted) change set to the working tree. Writes files in the user's repository. Returns an operation_id; poll get_operation.",
			InputSchema: mcpObject(map[string]any{"change_set": mcpString("Change set id or human id, e.g. CHG-1.")}, "change_set"),
			Annotations: annDestructive,
			Handler:     mcpHandler(func(a mcpArgs) (mcp.Result, error) { return mcpChangeSetAction("apply", a) }),
		}},
		{tier: mcpTierApply, Tool: mcp.Tool{
			Name: "revert_change_set", Title: "Revert a change set",
			Description: "Roll an applied change set back out of the working tree. Writes files in the user's repository. Returns an operation_id; poll get_operation.",
			InputSchema: mcpObject(map[string]any{"change_set": mcpString("Change set id or human id, e.g. CHG-1.")}, "change_set"),
			Annotations: annDestructive,
			Handler:     mcpHandler(func(a mcpArgs) (mcp.Result, error) { return mcpChangeSetAction("revert", a) }),
		}},
	}
}

// ---------- read handlers ----------

func mcpListInitiatives() (mcp.Result, error) {
	c, projectID, err := connect()
	if err != nil {
		return mcp.Result{}, err
	}
	var inis []cliInitiative
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/initiatives", nil, &inis); err != nil {
		return mcp.Result{}, err
	}
	var b strings.Builder
	if len(inis) == 0 {
		b.WriteString("No initiatives yet. new_initiative starts one.")
	}
	for i, ini := range inis {
		fmt.Fprintf(&b, "%s  %s  [%s]", ini.ID, ini.Title, ini.Status)
		if i == len(inis)-1 {
			b.WriteString("  (newest; the default)")
		}
		if ini.FailureReason != "" {
			fmt.Fprintf(&b, "\n    failed: %s", ini.FailureReason)
		}
		b.WriteString("\n")
	}
	return mcp.Result{Text: strings.TrimRight(b.String(), "\n"), Structured: map[string]any{"initiatives": nonNil(inis)}}, nil
}

func mcpStatus(a mcpArgs) (mcp.Result, error) {
	c, projectID, iniID, err := mcpInitiative(a.Initiative)
	if err != nil {
		return mcp.Result{}, err
	}
	var ini cliInitiative
	if err := c.do("GET", "/api/v1/projects/"+url.PathEscape(projectID)+"/initiatives/"+url.PathEscape(iniID), nil, &ini); err != nil {
		return mcp.Result{}, err
	}
	var u cliUnderstanding
	if err := c.do("GET", initiativeResource(projectID, iniID, "understanding"), nil, &u); err != nil {
		return mcp.Result{}, err
	}
	step, err := nextAction(c, projectID, iniID)
	if err != nil {
		return mcp.Result{}, err
	}
	// Best-effort, as in `idea status`: guidance enriches the answer, and an
	// older daemon without it must not turn status into a failure.
	var guidance map[string]any
	_ = c.do("GET", initiativeResource(projectID, iniID, "guidance"), nil, &guidance)

	counts := map[string]int{"requirements": len(u.Requirements), "decisions": len(u.Decisions), "questions": len(u.Questions)}
	openDecisions, openQuestions := 0, 0
	for _, d := range u.Decisions {
		if d.Status == "open" {
			openDecisions++
		}
	}
	for _, q := range u.Questions {
		if q.Status == "open" {
			openQuestions++
		}
	}
	counts["open_decisions"], counts["open_questions"] = openDecisions, openQuestions

	var b strings.Builder
	fmt.Fprintf(&b, "%s  [%s]  (%s)\n", ini.Title, ini.Status, ini.ID)
	if ini.FailureReason != "" {
		fmt.Fprintf(&b, "failed: %s\n", ini.FailureReason)
	}
	fmt.Fprintf(&b, "requirements: %d · decisions: %d (%d open) · questions: %d (%d open)\n",
		counts["requirements"], counts["decisions"], openDecisions, counts["questions"], openQuestions)
	fmt.Fprintf(&b, "next action: %s", step.Action)
	if act, _ := guidance["action"].(string); act != "" {
		fmt.Fprintf(&b, "\nrecommended: %s", act)
		if why, _ := guidance["why"].(string); why != "" {
			fmt.Fprintf(&b, " — %s", why)
		}
	}
	return mcp.Result{Text: b.String(), Structured: map[string]any{
		"initiative": ini,
		"counts":     counts,
		"next":       step,
		"guidance":   guidance,
	}}, nil
}

// mcpNextHint names what moves an initiative out of the state `action`
// describes, preferring a tool this server offers and falling back to the CLI
// command otherwise. It mirrors renderNextAction/reportJudgment rather than
// calling them because those write terminal prose.
func mcpNextHint(action, failure string, tier mcpTier) string {
	judgment := tier >= mcpTierJudgment
	switch {
	case strings.HasPrefix(action, "review "):
		id := strings.TrimPrefix(action, "review ")
		if judgment {
			return fmt.Sprintf("change set %s awaits review: read it with show_change_set and list_findings, then review_change_set {change_set: %q, verdict: \"approve\"} (accept_findings: true accepts it findings and all).", id, id)
		}
		return fmt.Sprintf("change set %s awaits review: `idea review %s approve` in a terminal (this server is read-only).", id, id)
	case strings.HasPrefix(action, "apply "):
		id := strings.TrimPrefix(action, "apply ")
		if tier >= mcpTierApply {
			return fmt.Sprintf("change set %s is approved: apply_change_set {change_set: %q} writes it to the working tree.", id, id)
		}
		return fmt.Sprintf("change set %s is approved: `idea apply %s` in a terminal writes it to the working tree (this server was started without --allow-apply).", id, id)
	case strings.HasPrefix(action, "resolve"):
		switch strings.TrimSpace(strings.TrimPrefix(action, "resolve")) {
		case "prd":
			return "the PRD review did not converge: list_findings shows why; `idea approve` accepts the PRD as it stands, `idea revise` regenerates it."
		case "plan-dependencies":
			return "the execution plan has unresolved task dependencies: list_tasks shows the order; `idea revise-plan` regenerates it."
		case "plan":
			return "the plan review raised blocking findings: list_findings lists them; `idea revise-plan` regenerates the plan, `idea approve-plan` accepts it as it stands."
		case "verification":
			return "a requirement was failed after completion: a human records the evidence with `idea verify REQ-n --pass|--fail --note \"...\"`."
		}
		return "waiting on human judgment: list_decisions, list_questions and list_findings show what is open."
	case action == "advance" || action == "execute":
		return fmt.Sprintf("the next step (%s) is automatable and spends model budget: `idea next --until-human` in a terminal runs it.", action)
	case action == "verify":
		if judgment {
			return "the next step is the verification gate: verify runs it."
		}
		return "the next step is the verification gate: `idea verify` runs it."
	case action == "done":
		return "the initiative is complete."
	case action == "failed":
		if failure != "" {
			return "the initiative failed: " + failure + " — `idea retry` re-runs it."
		}
		return "the initiative failed and no reason was recorded — `idea logs` has the full text, `idea retry` re-runs it."
	default:
		return "the workflow is running: status (or get_operation with the operation id you were given) shows when it finishes."
	}
}

func mcpNextStep(a mcpArgs, tier mcpTier) (mcp.Result, error) {
	c, projectID, iniID, err := mcpInitiative(a.Initiative)
	if err != nil {
		return mcp.Result{}, err
	}
	step, err := nextAction(c, projectID, iniID)
	if err != nil {
		return mcp.Result{}, err
	}
	hint := mcpNextHint(step.Action, step.FailureReason, tier)
	return mcp.Result{
		Text: fmt.Sprintf("next: %s\n%s", step.Action, hint),
		Structured: map[string]any{
			"initiative_id":  iniID,
			"action":         step.Action,
			"failure_reason": step.FailureReason,
			"hint":           hint,
		},
	}, nil
}

func mcpShowArtifact(a mcpArgs) (mcp.Result, error) {
	kind := a.Kind
	if kind == "" {
		kind = "prd"
	}
	c, projectID, iniID, err := mcpInitiative(a.Initiative)
	if err != nil {
		return mcp.Result{}, err
	}
	art, body, err := mcpArtifactBody(c, projectID, iniID, kind)
	if err != nil {
		return mcp.Result{}, err
	}
	return mcp.Result{Text: body.Body, Structured: map[string]any{
		"initiative_id": iniID,
		"artifact_id":   art.ID,
		"kind":          art.Kind,
		"version":       art.Version,
		"status":        art.Status,
		"resource_uri":  mcpArtifactURI(iniID, art.Kind),
	}}, nil
}

func mcpArtifactBody(c *client, projectID, iniID, kind string) (*cliArtifact, cliArtifactBody, error) {
	art, err := resolveArtifact(c, projectID, iniID, kind)
	if err != nil {
		return nil, cliArtifactBody{}, err
	}
	var body cliArtifactBody
	if err := c.do("GET", "/api/v1/projects/"+url.PathEscape(projectID)+"/artifacts/"+url.PathEscape(art.ID)+"/body", nil, &body); err != nil {
		return nil, cliArtifactBody{}, err
	}
	return art, body, nil
}

func mcpListTasks(a mcpArgs) (mcp.Result, error) {
	c, projectID, iniID, err := mcpInitiative(a.Initiative)
	if err != nil {
		return mcp.Result{}, err
	}
	var tasks []cliTask
	if err := c.do("GET", initiativeResource(projectID, iniID, "tasks"), nil, &tasks); err != nil {
		return mcp.Result{}, err
	}
	var b strings.Builder
	if len(tasks) == 0 {
		b.WriteString("No tasks yet: the execution plan has not been drafted.")
	}
	for _, t := range tasks {
		fmt.Fprintf(&b, "%s [%s] %s", t.Code, t.Priority, t.Title)
		if t.Status != "" {
			fmt.Fprintf(&b, " (%s)", t.Status)
		}
		b.WriteString("\n")
	}
	return mcp.Result{Text: strings.TrimRight(b.String(), "\n"), Structured: map[string]any{"initiative_id": iniID, "tasks": nonNil(tasks)}}, nil
}

func mcpListFindings(a mcpArgs) (mcp.Result, error) {
	c, projectID, iniID, err := mcpInitiative(a.Initiative)
	if err != nil {
		return mcp.Result{}, err
	}
	var findings []cliFinding
	if err := c.do("GET", initiativeResource(projectID, iniID, "findings"), nil, &findings); err != nil {
		return mcp.Result{}, err
	}
	var b strings.Builder
	if len(findings) == 0 {
		b.WriteString("No open findings.")
	}
	for _, f := range findings {
		fmt.Fprintf(&b, "[%s] %s", f.Severity, f.Finding)
		if f.Disposition != "" {
			fmt.Fprintf(&b, " (%s)", f.Disposition)
		}
		if f.Recommendation != "" {
			fmt.Fprintf(&b, "\n    → %s", f.Recommendation)
		}
		b.WriteString("\n")
	}
	return mcp.Result{Text: strings.TrimRight(b.String(), "\n"), Structured: map[string]any{"initiative_id": iniID, "findings": nonNil(findings)}}, nil
}

func mcpListQuestions(a mcpArgs) (mcp.Result, error) {
	c, projectID, iniID, err := mcpInitiative(a.Initiative)
	if err != nil {
		return mcp.Result{}, err
	}
	var questions []cliQuestion
	if err := c.do("GET", initiativeResource(projectID, iniID, "questions"), nil, &questions); err != nil {
		return mcp.Result{}, err
	}
	type item struct {
		cliQuestion
		Ref string `json:"ref"`
	}
	items := make([]item, 0, len(questions))
	var b strings.Builder
	if len(questions) == 0 {
		b.WriteString("No questions yet.")
	}
	for _, q := range questions {
		items = append(items, item{cliQuestion: q, Ref: shortRef(q.ID)})
		fmt.Fprintf(&b, "%s [%s] %s\n", shortRef(q.ID), q.Status, q.Question)
	}
	return mcp.Result{Text: strings.TrimRight(b.String(), "\n"), Structured: map[string]any{"initiative_id": iniID, "questions": items}}, nil
}

func mcpListDecisions(a mcpArgs) (mcp.Result, error) {
	c, projectID, iniID, err := mcpInitiative(a.Initiative)
	if err != nil {
		return mcp.Result{}, err
	}
	var decisions []cliDecision
	if err := c.do("GET", initiativeResource(projectID, iniID, "decisions"), nil, &decisions); err != nil {
		return mcp.Result{}, err
	}
	var b strings.Builder
	if len(decisions) == 0 {
		b.WriteString("No decisions yet.")
	}
	for _, d := range decisions {
		fmt.Fprintf(&b, "%s [%s] %s\n", d.Code, d.Status, d.Question)
		for i, o := range d.Options {
			fmt.Fprintf(&b, "    %d. %s\n", i+1, o)
		}
		if d.Recommendation != "" {
			fmt.Fprintf(&b, "    recommendation: %s\n", d.Recommendation)
		}
	}
	return mcp.Result{Text: strings.TrimRight(b.String(), "\n"), Structured: map[string]any{"initiative_id": iniID, "decisions": nonNil(decisions)}}, nil
}

func mcpTrace(mode string, a mcpArgs) (mcp.Result, error) {
	c, projectID, iniID, err := mcpInitiative(a.Initiative)
	if err != nil {
		return mcp.Result{}, err
	}
	modes := []string{mode}
	if mode == "trace" {
		modes = []string{"why", "impact"}
	}
	var b strings.Builder
	out := map[string]any{"initiative_id": iniID, "type": a.Type, "ref": a.Ref}
	for _, m := range modes {
		items, err := queryTrace(c, projectID, iniID, m, a.Type, a.Ref)
		if err != nil {
			return mcp.Result{}, err
		}
		switch v := items.(type) {
		case []cliWhyItem:
			v = nonNil(v)
			out["why"] = v
			fmt.Fprintf(&b, "why %s:", a.Ref)
			if len(v) == 0 {
				b.WriteString(" no provenance edges")
			}
			for _, it := range v {
				fmt.Fprintf(&b, "\n  %s —%s→ %s", it.FromLabel, it.EdgeType, it.ToLabel)
			}
		case []cliImpactItem:
			v = nonNil(v)
			out["impact"] = v
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			fmt.Fprintf(&b, "impact of %s:", a.Ref)
			if len(v) == 0 {
				b.WriteString(" no dependents")
			}
			for _, it := range v {
				fmt.Fprintf(&b, "\n  %s [%s] via %s", it.FromLabel, it.Staleness, it.EdgeType)
			}
		}
	}
	return mcp.Result{Text: b.String(), Structured: out}, nil
}

func mcpGaps(a mcpArgs) (mcp.Result, error) {
	c, projectID, iniID, err := mcpInitiative(a.Initiative)
	if err != nil {
		return mcp.Result{}, err
	}
	path := initiativeResource(projectID, iniID, "gaps")
	if a.Kind != "" {
		path += "?kind=" + url.QueryEscape(a.Kind)
	}
	var report cliGapReport
	if err := c.do("GET", path, nil, &report); err != nil {
		return mcp.Result{}, err
	}
	report.Gaps = nonNil(report.Gaps)
	var b strings.Builder
	if len(report.Gaps) == 0 {
		b.WriteString("No gaps.")
	} else {
		fmt.Fprintf(&b, "%d gap(s): %s", report.Summary.Total, report.Summary.summaryLine())
	}
	for _, g := range report.Gaps {
		subject := ""
		if len(g.Nodes) > 0 {
			subject = g.Nodes[0].Code
		}
		fmt.Fprintf(&b, "\n[%s] %s %s: %s", g.Severity, g.Kind, subject, g.Why)
		if g.Next != "" {
			fmt.Fprintf(&b, "\n    → %s", g.Next)
		}
	}
	return mcp.Result{Text: b.String(), Structured: map[string]any{"initiative_id": iniID, "gaps": report.Gaps, "summary": report.Summary}}, nil
}

func mcpChanged(a mcpArgs) (mcp.Result, error) {
	c, projectID, iniID, err := mcpInitiative(a.Initiative)
	if err != nil {
		return mcp.Result{}, err
	}
	var entries []cliTimelineEntry
	if err := c.do("GET", initiativeResource(projectID, iniID, "timeline"), nil, &entries); err != nil {
		return mcp.Result{}, err
	}
	var b strings.Builder
	if len(entries) == 0 {
		b.WriteString("No changes yet.")
	}
	for _, e := range entries {
		fmt.Fprintf(&b, "%s  %s", e.At, e.Title)
		if e.Detail != "" {
			fmt.Fprintf(&b, " — %s", e.Detail)
		}
		b.WriteString("\n")
	}
	return mcp.Result{Text: strings.TrimRight(b.String(), "\n"), Structured: map[string]any{"initiative_id": iniID, "entries": nonNil(entries)}}, nil
}

func mcpListChangeSets(a mcpArgs) (mcp.Result, error) {
	c, projectID, iniID, err := mcpInitiative(a.Initiative)
	if err != nil {
		return mcp.Result{}, err
	}
	var changes []cliChangeSet
	if err := c.do("GET", initiativeResource(projectID, iniID, "change-sets"), nil, &changes); err != nil {
		return mcp.Result{}, err
	}
	type item struct {
		cliChangeSet
		// Label is the same human status `idea changes` prints; it is the
		// only place "awaiting approval" and "review never completed" -- both
		// status review_required -- are told apart.
		Label string `json:"label"`
	}
	items := make([]item, 0, len(changes))
	var b strings.Builder
	if len(changes) == 0 {
		b.WriteString("No change sets yet.")
	}
	for _, cs := range changes {
		label := changeSetReviewStatusLabel(cs)
		items = append(items, item{cliChangeSet: cs, Label: label})
		fmt.Fprintf(&b, "%s  %s\n", cs.HumanID, label)
	}
	return mcp.Result{Text: strings.TrimRight(b.String(), "\n"), Structured: map[string]any{"initiative_id": iniID, "change_sets": items}}, nil
}

// mcpMaxPatchBytes bounds the patch text a single tool result carries. A
// model's context is the scarce resource here; the daemon already caps the
// files per diff, and this caps the bytes.
const mcpMaxPatchBytes = 100 << 10

func mcpShowChangeSet(a mcpArgs) (mcp.Result, error) {
	c, projectID, err := connect()
	if err != nil {
		return mcp.Result{}, err
	}
	var cs cliChangeSet
	if err := c.do("GET", changeSetResource(projectID, a.ChangeSet, ""), nil, &cs); err != nil {
		return mcp.Result{}, err
	}
	var diff struct {
		Patch      string           `json:"patch"`
		Files      []map[string]any `json:"files"`
		TotalFiles int              `json:"total_files"`
		Truncated  bool             `json:"truncated"`
	}
	if err := c.do("GET", changeSetResource(projectID, a.ChangeSet, "diff"), nil, &diff); err != nil {
		return mcp.Result{}, err
	}
	patch, patchTruncated := diff.Patch, false
	if len(patch) > mcpMaxPatchBytes {
		patch, patchTruncated = patch[:mcpMaxPatchBytes], true
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s  (%d file(s))\n", cs.HumanID, changeSetReviewStatusLabel(cs), diff.TotalFiles)
	if diff.Truncated {
		fmt.Fprintf(&b, "diff shows the first %d of %d files\n", len(diff.Files), diff.TotalFiles)
	}
	b.WriteString(patch)
	if patchTruncated {
		fmt.Fprintf(&b, "\n[patch truncated at %d KiB; `idea change %s` and the web UI show it whole]", mcpMaxPatchBytes>>10, cs.HumanID)
	}
	return mcp.Result{Text: b.String(), Structured: map[string]any{
		"change_set":      cs,
		"label":           changeSetReviewStatusLabel(cs),
		"files":           nonNil(diff.Files),
		"total_files":     diff.TotalFiles,
		"truncated":       diff.Truncated,
		"patch_truncated": patchTruncated,
	}}, nil
}

func mcpGetOperation(a mcpArgs) (mcp.Result, error) {
	c, _, err := connect()
	if err != nil {
		return mcp.Result{}, err
	}
	var op map[string]any
	if err := c.do("GET", operationResource(a.OperationID, ""), nil, &op); err != nil {
		return mcp.Result{}, err
	}
	status, _ := op["status"].(string)
	typ, _ := op["type"].(string)
	text := fmt.Sprintf("operation %s (%s): %s", a.OperationID, typ, status)
	if stage, _ := op["stage"].(string); stage != "" && status == "running" {
		text += " — " + stage
	}
	if e, _ := op["error"].(string); e != "" {
		text += " — " + e
	}
	return mcp.Result{Text: text, Structured: op}, nil
}

func mcpListObjectives(a mcpArgs) (mcp.Result, error) {
	c, projectID, iniID, err := mcpInitiative(a.Initiative)
	if err != nil {
		return mcp.Result{}, err
	}
	var objs []cliObjective
	if err := c.do("GET", initiativeResource(projectID, iniID, "objectives"), nil, &objs); err != nil {
		return mcp.Result{}, err
	}
	var b strings.Builder
	if len(objs) == 0 {
		b.WriteString("No objectives yet.")
	}
	for _, o := range objs {
		fmt.Fprintf(&b, "%s [%s] %s (%d requirement(s), %d metric(s))\n", o.Code, o.Status, o.Statement, len(o.RequirementIDs), len(o.MetricIDs))
	}
	return mcp.Result{Text: strings.TrimRight(b.String(), "\n"), Structured: map[string]any{"initiative_id": iniID, "objectives": nonNil(objs)}}, nil
}

// mcpListMetrics reads the outcomes report (one request: metrics with
// drift, the works-but-fails signals, gaps and counts).
func mcpListMetrics(a mcpArgs) (mcp.Result, error) {
	c, projectID, iniID, err := mcpInitiative(a.Initiative)
	if err != nil {
		return mcp.Result{}, err
	}
	var rep struct {
		Metrics          []cliMetric `json:"metrics"`
		RequirementDrift []struct {
			Detail string `json:"detail"`
		} `json:"requirement_drift"`
		Gaps    json.RawMessage `json:"gaps"`
		Summary json.RawMessage `json:"summary"`
	}
	if err := c.do("GET", initiativeResource(projectID, iniID, "outcomes"), nil, &rep); err != nil {
		return mcp.Result{}, err
	}
	var b strings.Builder
	if len(rep.Metrics) == 0 {
		b.WriteString("No outcome metrics yet.")
	}
	for _, m := range rep.Metrics {
		st := cliMetricStatus{Drift: "no_data"}
		if m.Status != nil {
			st = *m.Status
		}
		fmt.Fprintf(&b, "%s %s (%s) [%s] %s\n", m.Code, m.Name, m.ObjectiveCode, st.Drift, describeCLIMetric(m, st))
		if st.Drift != "on_track" {
			for _, r := range st.Reasons {
				fmt.Fprintf(&b, "    %s\n", r)
			}
		}
	}
	for _, d := range rep.RequirementDrift {
		fmt.Fprintf(&b, "works, but fails its objective: %s\n", d.Detail)
	}
	return mcp.Result{Text: strings.TrimRight(b.String(), "\n"), Structured: map[string]any{
		"initiative_id":     iniID,
		"metrics":           nonNil(rep.Metrics),
		"requirement_drift": nonNil(rep.RequirementDrift),
		"gaps":              rep.Gaps,
		"summary":           rep.Summary,
	}}, nil
}

// ---------- judgment handlers ----------

func mcpRecordMetric(a mcpArgs) (mcp.Result, error) {
	if strings.TrimSpace(a.Metric) == "" || a.Value == nil {
		return mcp.Result{}, errors.New("metric and value are required")
	}
	c, projectID, iniID, err := mcpInitiative(a.Initiative)
	if err != nil {
		return mcp.Result{}, err
	}
	var res struct {
		Observation map[string]any  `json:"observation"`
		Status      cliMetricStatus `json:"status"`
	}
	body := map[string]any{"value": *a.Value, "note": a.Note}
	if err := c.do("POST", outcomeRef(projectID, iniID, "metrics", strings.TrimSpace(a.Metric), "observations"), body, &res); err != nil {
		return mcp.Result{}, err
	}
	text := fmt.Sprintf("recorded %s = %s; drift: %s", strings.ToUpper(strings.TrimSpace(a.Metric)), num(*a.Value), res.Status.Drift)
	if cs, _ := res.Observation["change_set_ids"].([]any); len(cs) > 0 {
		text += fmt.Sprintf(" (%d change set(s) landed since the previous value)", len(cs))
	}
	if res.Status.Drift != "on_track" && res.Status.Drift != "no_data" && len(res.Status.Reasons) > 0 {
		text += " — " + strings.Join(res.Status.Reasons, "; ")
	}
	return mcp.Result{Text: text, Structured: map[string]any{"initiative_id": iniID, "observation": res.Observation, "status": res.Status}}, nil
}

func mcpNewInitiative(a mcpArgs) (mcp.Result, error) {
	idea := strings.TrimSpace(a.Idea)
	if idea == "" {
		return mcp.Result{}, errors.New("idea must not be empty")
	}
	c, projectID, err := connect()
	if err != nil {
		return mcp.Result{}, err
	}
	// Title and description are both the idea, exactly as `idea new` sends.
	body := map[string]string{"title": idea, "description": idea}
	var out map[string]string
	if err := c.do("POST", "/api/v1/projects/"+url.PathEscape(projectID)+"/initiatives", body, &out); err != nil {
		return mcp.Result{}, err
	}
	text := fmt.Sprintf("initiative %s started", out["initiative_id"])
	if op := out["operation_id"]; op != "" {
		text += fmt.Sprintf("; operation %s is drafting and reviewing the PRD — poll get_operation or status", op)
	}
	return mcp.Result{Text: text, Structured: map[string]any{"initiative_id": out["initiative_id"], "operation_id": out["operation_id"]}}, nil
}

func mcpAnswerQuestion(a mcpArgs) (mcp.Result, error) {
	c, projectID, iniID, err := mcpInitiative(a.Initiative)
	if err != nil {
		return mcp.Result{}, err
	}
	qID, err := resolveQuestionRef(c, projectID, iniID, a.Question)
	if err != nil {
		return mcp.Result{}, err
	}
	if err := c.do("POST", questionResource(projectID, qID, "answer"), map[string]string{"answer": a.Answer}, nil); err != nil {
		return mcp.Result{}, err
	}
	return mcp.Result{
		Text:       fmt.Sprintf("answered %s", shortRef(qID)),
		Structured: map[string]any{"initiative_id": iniID, "question_id": qID, "answer": a.Answer},
	}, nil
}

func mcpResolveDecision(a mcpArgs) (mcp.Result, error) {
	c, projectID, iniID, err := mcpInitiative(a.Initiative)
	if err != nil {
		return mcp.Result{}, err
	}
	d, resolved, err := resolveDecision(c, projectID, iniID, a.Decision, a.Resolution)
	if err != nil {
		return mcp.Result{}, err
	}
	return mcp.Result{
		Text:       fmt.Sprintf("resolved %s → %s", d.Code, resolved),
		Structured: map[string]any{"initiative_id": iniID, "decision_id": d.ID, "code": d.Code, "resolution": resolved},
	}, nil
}

func mcpReviewChangeSet(a mcpArgs) (mcp.Result, error) {
	// The same verdict vocabulary as `idea review`, mapped the same way.
	verdict := map[string]string{"approve": "approved", "reject": "rejected", "changes_requested": "changes_requested", "rerun": "rerun"}[a.Verdict]
	if a.AcceptFindings && verdict != "approved" {
		return mcp.Result{}, errors.New("accept_findings only applies to verdict approve")
	}
	c, projectID, err := connect()
	if err != nil {
		return mcp.Result{}, err
	}
	body := map[string]any{"verdict": verdict}
	if a.AcceptFindings {
		body["accept_findings"] = true
	}
	var out map[string]string
	if err := c.do("POST", changeSetResource(projectID, a.ChangeSet, "review"), body, &out); err != nil {
		return mcp.Result{}, err
	}
	structured := map[string]any{"change_set": a.ChangeSet, "verdict": verdict, "accept_findings": a.AcceptFindings}
	if verdict == "rerun" {
		structured["operation_id"] = out["operation_id"]
		return mcp.Result{
			Text:       fmt.Sprintf("review rerun started for %s; operation %s — poll get_operation", a.ChangeSet, out["operation_id"]),
			Structured: structured,
		}, nil
	}
	text := fmt.Sprintf("review recorded: %s → %s", a.ChangeSet, verdict)
	if a.AcceptFindings {
		text += " (findings accepted)"
	}
	return mcp.Result{Text: text, Structured: structured}, nil
}

func mcpVerify(a mcpArgs) (mcp.Result, error) {
	c, projectID, iniID, err := mcpInitiative(a.Initiative)
	if err != nil {
		return mcp.Result{}, err
	}
	body := map[string]any{}
	if a.Requirement != "" {
		body["requirement"] = a.Requirement
	}
	var out struct {
		Status   string        `json:"status"`
		Coverage []cliCoverage `json:"coverage"`
	}
	if err := c.do("POST", initiativeResource(projectID, iniID, "verify"), body, &out); err != nil {
		return mcp.Result{}, err
	}
	// The same rule runVerify applies: a waiver satisfies the gate, a recorded
	// failure never does, otherwise every mapped check must pass.
	unmet := []string{}
	var b strings.Builder
	for _, cov := range out.Coverage {
		switch cov.Status {
		case "failed":
			unmet = append(unmet, cov.Code+" failed")
		case "waived":
		default:
			if cov.Pass < cov.Total {
				unmet = append(unmet, fmt.Sprintf("%s %d/%d verified", cov.Code, cov.Pass, cov.Total))
			}
		}
		fmt.Fprintf(&b, "\n  %s %d/%d verified", cov.Code, cov.Pass, cov.Total)
		if cov.Status == "waived" || cov.Status == "failed" {
			fmt.Fprintf(&b, " (%s)", cov.Status)
		} else if cov.NeedsManual > 0 {
			fmt.Fprintf(&b, " (%d need manual sign-off)", cov.NeedsManual)
		}
	}
	// An unmet gate is an answer, not a tool failure: the call worked and
	// says what is still missing. isError is for calls that did not work.
	head := "verified"
	if len(unmet) > 0 {
		head = "not verified: " + strings.Join(unmet, "; ")
	}
	return mcp.Result{
		Text: fmt.Sprintf("%s (status %s)%s", head, out.Status, b.String()),
		Structured: map[string]any{
			"initiative_id": iniID,
			"status":        out.Status,
			"verified":      len(unmet) == 0,
			"unmet":         unmet,
			"coverage":      nonNil(out.Coverage),
		},
	}, nil
}

// ---------- apply handlers ----------

func mcpChangeSetAction(action string, a mcpArgs) (mcp.Result, error) {
	c, projectID, err := connect()
	if err != nil {
		return mcp.Result{}, err
	}
	var out map[string]string
	if err := c.do("POST", changeSetResource(projectID, a.ChangeSet, action), nil, &out); err != nil {
		return mcp.Result{}, err
	}
	op := out["operation_id"]
	text := fmt.Sprintf("%s started for %s", action, a.ChangeSet)
	if op != "" {
		text += fmt.Sprintf("; operation %s — poll get_operation", op)
	}
	return mcp.Result{Text: text, Structured: map[string]any{"change_set": a.ChangeSet, "action": action, "operation_id": op}}, nil
}

// ---------- resources ----------

const mcpResourcePrefix = "idea://initiative/"

func mcpArtifactURI(iniID, kind string) string {
	return mcpResourcePrefix + url.PathEscape(iniID) + "/" + url.PathEscape(kind)
}

// mcpListResources lists the latest artifact of each kind for every
// initiative. One artifacts call per initiative; projects hold a handful.
func mcpListResources(_ context.Context) ([]mcp.Resource, error) {
	c, projectID, err := connect()
	if err != nil {
		return nil, err
	}
	var inis []cliInitiative
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/initiatives", nil, &inis); err != nil {
		return nil, err
	}
	var out []mcp.Resource
	for _, ini := range inis {
		var arts []cliArtifact
		if err := c.do("GET", initiativeResource(projectID, ini.ID, "artifacts"), nil, &arts); err != nil {
			return nil, err
		}
		// Latest wins, as in resolveArtifact; first-seen order is kept so
		// the list is deterministic.
		seen := map[string]int{}
		for _, art := range arts {
			r := mcp.Resource{
				URI:         mcpArtifactURI(ini.ID, art.Kind),
				Name:        ini.ID + "/" + art.Kind,
				Title:       fmt.Sprintf("%s — %s", art.Kind, ini.Title),
				Description: fmt.Sprintf("Latest %s (v%d) of initiative %q.", art.Kind, art.Version, ini.Title),
				MimeType:    "text/markdown",
			}
			if i, ok := seen[art.Kind]; ok {
				out[i] = r
				continue
			}
			seen[art.Kind] = len(out)
			out = append(out, r)
		}
	}
	return out, nil
}

// mcpReadResource serves idea://initiative/{id}/{kind}.
func mcpReadResource(_ context.Context, uri string) ([]mcp.ResourceContents, error) {
	rest, ok := strings.CutPrefix(uri, mcpResourcePrefix)
	if !ok {
		return nil, fmt.Errorf("%w: %s (resources here are idea://initiative/{id}/{kind})", mcp.ErrResourceNotFound, uri)
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return nil, fmt.Errorf("%w: %s (resources here are idea://initiative/{id}/{kind})", mcp.ErrResourceNotFound, uri)
	}
	iniID, err1 := url.PathUnescape(parts[0])
	kind, err2 := url.PathUnescape(parts[1])
	if err1 != nil || err2 != nil {
		return nil, fmt.Errorf("%w: %s", mcp.ErrResourceNotFound, uri)
	}
	c, projectID, err := connect()
	if err != nil {
		return nil, err
	}
	// Membership is checked here rather than read off resolveInitiative's and
	// resolveArtifact's error text: "not found" has to become -32602 and a
	// daemon failure -32603, and matching message prefixes would quietly turn
	// the second into the first the day a message is reworded.
	var inis []cliInitiative
	if err := c.do("GET", "/api/v1/projects/"+projectID+"/initiatives", nil, &inis); err != nil {
		return nil, err
	}
	known := false
	for _, ini := range inis {
		known = known || ini.ID == iniID
	}
	if !known {
		return nil, fmt.Errorf("%w: no initiative %q in this project", mcp.ErrResourceNotFound, iniID)
	}
	var arts []cliArtifact
	if err := c.do("GET", initiativeResource(projectID, iniID, "artifacts"), nil, &arts); err != nil {
		return nil, err
	}
	var target *cliArtifact
	for i := range arts {
		if arts[i].Kind == kind {
			target = &arts[i] // latest wins, as in resolveArtifact
		}
	}
	if target == nil {
		return nil, fmt.Errorf("%w: initiative %s has no %q artifact yet", mcp.ErrResourceNotFound, iniID, kind)
	}
	var body cliArtifactBody
	if err := c.do("GET", "/api/v1/projects/"+url.PathEscape(projectID)+"/artifacts/"+url.PathEscape(target.ID)+"/body", nil, &body); err != nil {
		return nil, err
	}
	return []mcp.ResourceContents{{URI: uri, MimeType: "text/markdown", Text: body.Body}}, nil
}
