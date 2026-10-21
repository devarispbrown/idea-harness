package domain

import (
	"strconv"
	"strings"
	"time"
)

// AcceptanceCriterion is a structurally verifiable acceptance criterion: a
// measurable predicate with a metric, a target, and a condition (§11, §18).
type AcceptanceCriterion struct {
	Metric    string `json:"metric"`
	Target    string `json:"target"`
	Condition string `json:"condition"`
}

// Verifiable reports whether the criterion has a quantified target, not a bare
// evaluative adjective. This is the deterministic structural check (§11).
func (c AcceptanceCriterion) Verifiable() bool {
	return c.Metric != "" && c.Target != "" && c.Condition != ""
}

// ProvenanceRef is a structured reference to the object that produced or
// supports a statement (§5, §6). It is a typed reference, never free text.
type ProvenanceRef struct {
	Kind   string     `json:"kind"`
	ID     string     `json:"id"`
	Digest string     `json:"digest,omitempty"`
	Trust  TrustLevel `json:"trust"`
}

// Assertion is a structured Fact/Assumption/Hypothesis (§18). Code is its
// initiative-scoped human code (ASM-n): what a PRD cites in a requirement's or
// decision's based_on, and what `idea assumption` accepts.
type Assertion struct {
	ID             string          `json:"id"`
	InitiativeID   string          `json:"initiative_id"`
	Code           string          `json:"code"`
	Type           AssertionType   `json:"type"`
	Status         AssertionStatus `json:"status"`
	Statement      string          `json:"statement"`
	EvidenceDigest string          `json:"evidence_digest,omitempty"`
	Confidence     Confidence      `json:"confidence"`
	Source         string          `json:"source"`
	Provenance     []ProvenanceRef `json:"provenance"`
	ProducedByRun  string          `json:"produced_by_run,omitempty"`
	SupersededBy   string          `json:"superseded_by,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

// Requirement is a first-class structured requirement (§17).
// RequirementChangeKind is the derived delta a requirement represents relative
// to a prior revision: added (new), modified (revised in place), or removed
// (superseded/removed). It is computed at read time from the row's own
// lifecycle, never stored, so it cannot drift from status/SupersededBy.
type RequirementChangeKind string

const (
	RequirementChangeAdded    RequirementChangeKind = "added"
	RequirementChangeModified RequirementChangeKind = "modified"
	RequirementChangeRemoved  RequirementChangeKind = "removed"
)

type Requirement struct {
	ID                 string                `json:"id"`
	InitiativeID       string                `json:"initiative_id"`
	Code               string                `json:"code"`
	Statement          string                `json:"statement"`
	AcceptanceCriteria []AcceptanceCriterion `json:"acceptance_criteria"`
	Priority           RequirementPriority   `json:"priority"`
	Status             RequirementStatus     `json:"status"`
	SourceAssertionID  string                `json:"source_assertion_id,omitempty"`
	Provenance         []ProvenanceRef       `json:"provenance"`
	ProducedByRun      string                `json:"produced_by_run,omitempty"`
	SupersededBy       string                `json:"superseded_by,omitempty"`
	// RevisedAt is set (once) when a revision changes the requirement's
	// statement or acceptance criteria in place (store.UpdateRequirement).
	RevisedAt *time.Time `json:"revised_at,omitempty"`
	// ChangeKind is the derived delta (added/modified/removed), populated at
	// read time by DeriveChangeKind — never a stored column.
	ChangeKind RequirementChangeKind `json:"change_kind,omitempty"`
	VerifiedAt *time.Time            `json:"verified_at,omitempty"`
	// EvidenceTests is the test-run evidence mapped to this requirement (C2,
	// wave 3 contract): every test the verification plan (or a name match on
	// this requirement's code) associates with it, and whether it last passed
	// or failed. Computed at read time by internal/projection from the most
	// recent "tests" check output per change set -- never persisted, so it is
	// always as fresh as the last check run. Nil/empty means no test is known
	// to cover this requirement yet, not that it failed.
	EvidenceTests []EvidenceTest `json:"evidence_tests,omitempty"`
	// ObjectiveIDs are the live objectives this requirement serves
	// (requirement --serves--> objective, N:M). Computed at read time from
	// graph edges by store.ListRequirements -- never nil there, so a
	// requirement serving nothing is `[]`, which is exactly the gap the
	// PRD's deterministic outcome check reports.
	ObjectiveIDs []string  `json:"objective_ids"`
	CreatedAt    time.Time `json:"created_at"`
}

// DeriveChangeKind reports the requirement's delta relative to a prior
// revision, derived from its own lifecycle fields: removed wins, then modified
// (revised in place), else added.
func (r Requirement) DeriveChangeKind() RequirementChangeKind {
	if r.Status == RequirementRemoved || r.SupersededBy != "" {
		return RequirementChangeRemoved
	}
	if r.RevisedAt != nil {
		return RequirementChangeModified
	}
	return RequirementChangeAdded
}

// EvidenceTest is one test-run result mapped to a requirement (C2, wave 3
// contract). Status is the test's own outcome ("passed"|"failed"), not a
// domain.VerificationStatus -- it is evidence a human still has to confirm,
// not a verification judgment.
type EvidenceTest struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	ChangeSet string `json:"change_set"`
}

// Decision is a structured decision with a recommendation (§8, §19).
type Decision struct {
	ID             string          `json:"id"`
	InitiativeID   string          `json:"initiative_id"`
	Code           string          `json:"code"`
	Type           DecisionType    `json:"type"`
	Question       string          `json:"question"`
	Options        []string        `json:"options"`
	Recommendation string          `json:"recommendation"`
	Status         DecisionStatus  `json:"status"`
	Resolution     string          `json:"resolution,omitempty"`
	ResolvedBy     string          `json:"resolved_by,omitempty"`
	Provenance     []ProvenanceRef `json:"provenance"`
	SupersededBy   string          `json:"superseded_by,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

// DecisionDissent preserves a dissenting view so it is never erased (§8).
type DecisionDissent struct {
	ID              string    `json:"id"`
	DecisionID      string    `json:"decision_id"`
	ProponentRole   string    `json:"proponent_role"`
	Position        string    `json:"position"`
	ReasoningDigest string    `json:"reasoning_digest,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// DecisionDiscussion is one message in a human↔AI discussion thread attached
// to a decision (§8): the human asks for context or proposes an alternative,
// and the model replies.
type DecisionDiscussion struct {
	ID         string    `json:"id"`
	DecisionID string    `json:"decision_id"`
	Author     string    `json:"author"`
	Message    string    `json:"message"`
	CreatedAt  time.Time `json:"created_at"`
}

// Question is an open question (§4, §9).
type Question struct {
	ID           string         `json:"id"`
	InitiativeID string         `json:"initiative_id"`
	Question     string         `json:"question"`
	Status       QuestionStatus `json:"status"`
	// NeedsClarification marks a question the model flagged [NEEDS
	// CLARIFICATION] rather than guessing at a missing input. Approving the PRD
	// refuses while any such question is still open, so an unresolved unknown
	// cannot silently become an invented requirement.
	NeedsClarification bool           `json:"needs_clarification,omitempty"`
	AnswerDigest       string         `json:"answer_digest,omitempty"`
	// Answer is the answer's text, filled at read time by the questions list
	// (never stored on the row: the text lives in the blob store under
	// AnswerDigest). The UI's Resolved section shows it; without it an
	// answered question simply vanished from Define.
	Answer     string     `json:"answer,omitempty"`
	AnsweredBy string     `json:"answered_by,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

// ProjectMemory is one accumulated, cross-initiative entry in a project's
// standing memory: a human-resolved decision (DEC-x: question → resolution)
// that later initiatives read as authoritative context. The per-project DB is
// the project, so no initiative_id is stored.
type ProjectMemory struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// DocComment is a human comment anchored to a section of an artifact version.
// It can be promoted into an open Question for the initiative's judgment flow.
type DocComment struct {
	ID                 string    `json:"id"`
	InitiativeID       string    `json:"initiative_id"`
	ArtifactVersionID  string    `json:"artifact_version_id"`
	Section            string    `json:"section,omitempty"`
	Text               string    `json:"text"`
	Author             string    `json:"author"`
	PromotedQuestionID string    `json:"promoted_question_id,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
}

// Artifact is an immutable-versioned artifact family (§20).
type Artifact struct {
	ID           string       `json:"id"`
	InitiativeID string       `json:"initiative_id"`
	Kind         ArtifactKind `json:"kind"`
	CreatedAt    time.Time    `json:"created_at"`
}

// ContextDoc is one source document attached to an initiative: a PRD, design
// note, technical spec, market research, a wireframe/mockup image, or other
// free-form labeled context. The body is stored as a content-addressed blob;
// this row records its digest plus a short redacted text preview. Image docs
// (Format=="image") carry the raw image in Digest and, once described by a
// vision model, the description text digest in DescriptionDigest.
type ContextDoc struct {
	ID                string    `json:"id"`
	InitiativeID      string    `json:"initiative_id"`
	Kind              string    `json:"kind"`
	Title             string    `json:"title"`
	Digest            string    `json:"digest"`
	Preview           string    `json:"preview"`
	Format            string    `json:"format"`
	DescriptionDigest string    `json:"description_digest,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

// SuggestedAnswer is one AI-proposed answer to an open question.
type SuggestedAnswer struct {
	QuestionID string `json:"question_id"`
	Question   string `json:"question"`
	Answer     string `json:"answer"`
}

// ArtifactVersion is one immutable version of an Artifact.
type ArtifactVersion struct {
	ID            string    `json:"id"`
	ArtifactID    string    `json:"artifact_id"`
	Version       int       `json:"version"`
	Status        string    `json:"status"`
	BodyDigest    string    `json:"body_digest"`
	ProducedByRun string    `json:"produced_by_run,omitempty"`
	SupersededBy  string    `json:"superseded_by,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// Finding is a structured review finding (§15).
type Finding struct {
	ID               string      `json:"id"`
	InitiativeID     string      `json:"initiative_id,omitempty"`
	ReviewRunID      string      `json:"review_run_id"`
	TargetType       string      `json:"target_type"`
	TargetID         string      `json:"target_id"`
	Severity         Severity    `json:"severity"`
	Confidence       Confidence  `json:"confidence"`
	Category         string      `json:"category"`
	Code             DenyCode    `json:"code,omitempty"`
	Finding          string      `json:"finding"`
	Evidence         string      `json:"evidence"`
	Impact           string      `json:"impact"`
	Recommendation   string      `json:"recommendation"`
	Disposition      Disposition `json:"disposition,omitempty"`
	DispositionActor string      `json:"disposition_actor,omitempty"`
	Note             string      `json:"note,omitempty"`
	// SecretFingerprint, SecretKind and SecretPath are set only on a
	// secret_sweep finding raised from a per-file credential match (§ D2,
	// review changes #1, review H3). SecretFingerprint is a per-project HMAC
	// of the matched value's capture group, never the value itself, and never
	// a bare sha256 (which anyone could precompute and correlate across
	// installations); disposing the finding not_a_secret allowlists it by
	// this fingerprint scoped to SecretPath.
	SecretFingerprint string `json:"secret_fingerprint,omitempty"`
	SecretKind        string `json:"secret_kind,omitempty"`
	SecretPath        string `json:"secret_path,omitempty"`
	// SecretOverridable reports whether a human may disposition this finding
	// not_a_secret at all (review H3): true for an ordinary pattern-shaped
	// match (might be a false positive), false for a match drawn from the
	// project's own configured secret values -- a real credential by
	// construction, never eligible for the override regardless of who asks.
	// Defaults to true (the zero value of the underlying column) so older
	// rows written before this field existed are not retroactively treated
	// as configured-secret matches.
	SecretOverridable bool `json:"secret_overridable"`
	// SecretPreview is a masked preview of the matched value (review LOW):
	// nothing at all for a value under 16 characters (masked entirely), at
	// most 1 character on each side for a 16-23 character value, and at most
	// the first/last 4 characters (e.g. "sk-p…Xy9z") only from 24 characters
	// up -- shown so a human dispositioning this finding can see enough to
	// recognize what they are allowlisting without the raw value ever being
	// persisted or returned in full. See security.maskValue.
	SecretPreview string `json:"secret_preview,omitempty"`
	// Path is the file the finding is about, self-reported by the reviewer
	// (change-set review only, review B2, adopted #5). Optional: a finding
	// with no specific file leaves it empty.
	Path string `json:"path,omitempty"`
	// InDiff is the reviewer's own claim that Path (or, if Path is empty, the
	// finding as a whole) is about code the change set actually touches
	// (review B2, adopted #5). Only consulted when Path is empty -- when Path
	// is set, whether it is one of the change set's files is checked
	// deterministically instead of trusting this self-report.
	InDiff bool `json:"in_diff,omitempty"`
	// Advisory marks a finding that does not block approval regardless of
	// severity: "about code this change set did not touch" (review B2), a
	// rigor cap's own dropped-count note (review B1), and a generic
	// "credential assignment" secret-sweep match found in a test/fixture path
	// (ICP re-run defect #1) all set it. Kept as its own bool rather than
	// folded into Severity or Disposition -- review.EvaluateApproval and
	// denyReviewOpenBlocking both skip an advisory finding outright, and a UI
	// backlog view is exactly the findings with this set and no disposition
	// yet.
	Advisory  bool      `json:"advisory,omitempty"`
	CreatedAt time.Time `json:"created_at"`

	// Cites, ReRaises and Justification are the adversarial reviewer's own
	// convergence fields (schema.Review: "requirement", "reraises",
	// "justification"), read by review.ParseFindings and consumed by the PRD
	// review loop's convergence rules before the finding is persisted. They
	// are parse-time only -- no column stores them, so a finding read back
	// from the store always has them empty, and json:"-" keeps the API from
	// advertising fields it could never fill. What they decided is persisted
	// where a human can see it: the cited requirement as TargetType/TargetID,
	// and the justification (or the reason a finding was made advisory) in
	// Note.
	Cites         string `json:"-"`
	ReRaises      string `json:"-"`
	Justification string `json:"-"`
}

// ReviewTelemetry is one review cycle's convergence record, persisted on the
// adversarial reviewer's run manifest (RunManifest.Review). It exists because
// the PRD review loop used to be a black box: "did not converge" was the only
// output, and nothing distinguished a reviewer that kept finding genuinely new
// problems from one that kept rephrasing the same three -- which is the
// difference between a loop that needs a human and one that needs a fix.
//
// Counts are per severity (critical/high/medium/low/observation). Raised is
// every adversarial finding this cycle produced; New and ReRaised partition it
// against the previous cycle's gating findings; Resolved counts previous-cycle
// gating findings this cycle did NOT re-raise. Demoted counts findings made
// advisory by a convergence rule, keyed by the rule.
type ReviewTelemetry struct {
	// Review is which review loop this is: "prd" or "execution_plan".
	Review string `json:"review"`
	Cycle  int    `json:"cycle"`
	// Mode is review.convergence.mode as it ran: "guarded" or "legacy".
	Mode  string `json:"mode"`
	Rigor string `json:"rigor,omitempty"`

	Raised   map[Severity]int `json:"raised,omitempty"`
	New      map[Severity]int `json:"new,omitempty"`
	ReRaised map[Severity]int `json:"reraised,omitempty"`
	Resolved map[Severity]int `json:"resolved,omitempty"`
	Demoted  map[string]int   `json:"demoted,omitempty"`

	// Deterministic is the number of gating deterministic-check findings
	// (duplicate code, missing/vague criterion, plan structure) this cycle.
	Deterministic int `json:"deterministic"`
	// OpenBlocking is every finding still gating approval after this cycle's
	// rules ran, deterministic ones included. PrevOpenBlocking is the same
	// figure for the previous cycle of the same loop, -1 on a loop's first
	// cycle -- the pair is what the stall detector compares.
	OpenBlocking     int `json:"open_blocking"`
	PrevOpenBlocking int `json:"prev_open_blocking"`
	// Outcome is what the loop did next: "converged", "revise", "stalled"
	// (the open blocking count stopped decreasing), "max_cycles", or
	// "blocked" (a single-pass review, e.g. the plan's, that parked).
	Outcome string `json:"outcome"`
}

// ResolvesBlocking reports whether f's current disposition clears it from the
// approval bar (§11, §15) -- review.EvaluateApproval and
// denyReviewOpenBlocking both call this instead of comparing Disposition
// directly, so the two stay in lockstep (ICP re-run defect #2).
//
// fixed and accepted_risk always resolve a blocking finding. rejected also
// does, but ONLY when a HUMAN set it (DispositionActor == "human" --
// store.HumanActor's value; domain cannot import store, which imports
// domain) on a finding that carries NO system deny code (Code == ""): a
// human dispositioning the adversarial reviewer's own judgment call
// "rejected" is "the human judged this finding wrong," semantically no
// different from overruling it with accepted_risk, and the ICP re-run found
// this permanently deadlocked approval instead (rejected recorded 200,
// AllDispositioned true, but nothing ever cleared the block; the error even
// recommended --accept-findings, which had no effect on an ALREADY
// dispositioned finding).
//
// A SYSTEM-set rejected must keep blocking regardless of who looks at it
// afterward ("system deny beats human approve"): a failing build/test check
// (Code == "", Disposition == rejected, set by the check runner itself, never
// a human), a secret_sweep/host_exec/etc. finding (Code != "", self-rejected
// at creation), and a rejected change set's own findings closed via
// Disposition = rejected (review M5) all fail the Code == "" and/or
// DispositionActor == "human" tests above and so are excluded here exactly
// as before this method existed.
func (f Finding) ResolvesBlocking() bool {
	if f.Disposition == DispositionFixed || f.Disposition == DispositionAcceptedRisk {
		return true
	}
	return f.Disposition == DispositionRejected && f.Code == "" && f.DispositionActor == "human"
}

// SecretAllowlistEntry is a human "not a secret" disposition on a detected
// credential pattern match, scoped to one project (review changes #1). The
// fingerprint is the sha256 of the matched value's capture group -- never the
// raw value -- so the sweep can recognise the same false positive again
// without ever having stored the secret it decided wasn't one.
type SecretAllowlistEntry struct {
	ID          string    `json:"id"`
	Fingerprint string    `json:"fingerprint"`
	Kind        string    `json:"kind"`
	Path        string    `json:"path"`
	Actor       string    `json:"actor"`
	CreatedAt   time.Time `json:"created_at"`
}

// Initiative is the user-facing idea under development.
type Initiative struct {
	ID          string `json:"id"`
	ProjectID   string `json:"project_id,omitempty"`
	Title       string `json:"title"`
	Description string `json:"description"`
	// ShortTitle is a deterministically generated short label (<=60 chars,
	// C1 wave 3 contract): the idea's first sentence or clause, trimmed at a
	// word boundary, with a common lead-in ("I want to", "Build") dropped.
	// Title/Description keep the full prompt; the UI shows ShortTitle||Title.
	// Generated on create, and backfilled lazily for older initiatives by
	// GetInitiative/ListInitiatives when empty.
	ShortTitle    string           `json:"short_title,omitempty"`
	Status        InitiativeStatus `json:"status"`
	FailureReason string           `json:"failure_reason,omitempty"`
	// JudgmentReason is set only while Status is needs_human_judgment. It is
	// not cleared when the status moves on -- nothing reads it in any other
	// status, and clearing it on every transition would mean touching the claim
	// path, which failure_reason already showed to be the wrong place.
	JudgmentReason JudgmentReason `json:"judgment_reason,omitempty"`
	// GitBranch is the dedicated branch (`idea/<slug>`) this initiative's
	// change sets commit to when git.branch_per_initiative is on. Set on the
	// initiative's first apply and reused by every later one, so all of its
	// change sets land on the same branch. Empty when the setting is off, or
	// before a first apply has created it.
	GitBranch string `json:"git_branch,omitempty"`
	// PRURL is the pull request `idea pr` opened for GitBranch (review C4
	// piece 3). Empty until a PR exists; a repeat `idea pr` is idempotent and
	// returns this URL rather than opening a second PR.
	PRURL     string    `json:"pr_url,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Event is an append-only event record (§32).
type Event struct {
	Seq           int64     `json:"seq"`
	AggregateType string    `json:"aggregate_type"`
	AggregateID   string    `json:"aggregate_id"`
	Actor         string    `json:"actor"`
	CorrelationID string    `json:"correlation_id"`
	CausationID   string    `json:"causation_id"`
	Operation     string    `json:"operation"`
	EventType     string    `json:"event_type"`
	PayloadDigest string    `json:"payload_digest"`
	CreatedAt     time.Time `json:"created_at"`
}

// Operation is a durable long-running operation (§34).
type Operation struct {
	ID           string          `json:"id"`
	ProjectID    string          `json:"project_id"`
	InitiativeID string          `json:"initiative_id,omitempty"`
	Type         string          `json:"type"`
	Status       OperationStatus `json:"status"`
	ResultRef    string          `json:"result_ref,omitempty"`
	// Error is the reason a failed or cancelled operation ended that way,
	// redacted and length-capped before it is persisted (B2). Before this
	// field existed, a failed execute left the initiative at plan_approved,
	// the Build page's running card simply vanished, and the only record of
	// why was a line in daemon.log — invisible to anyone not tailing it.
	Error string `json:"error,omitempty"`
	// Stage is the current step the engine reports while this operation runs
	// (C3, wave 3 contract), e.g. "Writing the PRD", "Reviewing the plan
	// (cycle 2)", "Agent running TASK-2", "Running checks". Set by a small
	// callback the engine calls as it moves between steps and cleared by
	// nothing -- it simply stops changing once the operation reaches a
	// terminal status. The CLI's wait loop prints it in place of a bare
	// "still working…".
	Stage      string     `json:"stage,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

// RunManifest records immutable provenance for a Run (§36).
type RunManifest struct {
	ID             string            `json:"id"`
	RunID          string            `json:"run_id"`
	SnapshotDigest string            `json:"snapshot_digest,omitempty"`
	ConfigDigest   string            `json:"config_digest,omitempty"`
	WorkflowDigest string            `json:"workflow_digest,omitempty"`
	RoleVersion    string            `json:"role_version,omitempty"`
	RequestedModel string            `json:"requested_model"`
	ActualModel    string            `json:"actual_model"`
	ContextDigest  string            `json:"context_digest,omitempty"`
	SkillDigests   []string          `json:"skill_digests,omitempty"`
	ToolVersions   map[string]string `json:"tool_versions,omitempty"`
	Usage          RunUsage          `json:"usage"`
	ResultDigest   string            `json:"result_digest,omitempty"`
	StartedAt      time.Time         `json:"started_at"`
	FinishedAt     time.Time         `json:"finished_at"`
	// Executor is set on coding-agent (implementer) runs only.
	Executor *ExecutorProvenance `json:"executor,omitempty"`
	// Review is the convergence record of the review cycle this run was the
	// adversarial reviewer for (nil on every other run). Written after the
	// manifest itself, once the cycle's rules have run -- see
	// store.SetRunManifestReview.
	Review *ReviewTelemetry `json:"review,omitempty"`
}

// RunUsage records token/cost/duration for a run (§17, §36).
type RunUsage struct {
	InputTokens  int64   `json:"input_tokens,omitempty"`
	OutputTokens int64   `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	DurationMS   int64   `json:"duration_ms,omitempty"`
}

// ContextEntry is one compiled context entry (§37).
type ContextEntry struct {
	Priority        int        `json:"priority"`
	Source          string     `json:"source"`
	Digest          string     `json:"digest,omitempty"`
	Trust           TrustLevel `json:"trust"`
	InclusionReason string     `json:"inclusion_reason"`
	Selector        string     `json:"selector"`
	Transformation  string     `json:"transformation"`
	Size            int64      `json:"size"`
}

// GraphEdge is a Product Graph edge linking two typed nodes (§25).
type GraphEdge struct {
	ID           string         `json:"id"`
	InitiativeID string         `json:"initiative_id,omitempty"`
	FromType     string         `json:"from_type"`
	FromID       string         `json:"from_id"`
	EdgeType     EdgeType       `json:"edge_type"`
	ToType       string         `json:"to_type"`
	ToID         string         `json:"to_id"`
	Metadata     map[string]any `json:"metadata,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
}

// EnvVar is one environment variable a task's code reads from the process
// environment (process.env.X / os.Getenv), declared by the planner so the
// harness can prompt the human to set it — with how to obtain it and where —
// instead of leaving them to discover the name from a failing build.
type EnvVar struct {
	Name string `json:"name"`
	How  string `json:"how"`
	URL  string `json:"url,omitempty"`
}

// Task is one unit of the execution plan (§12, M2).
type Task struct {
	ID            string              `json:"id"`
	InitiativeID  string              `json:"initiative_id"`
	Code          string              `json:"code"`
	Title         string              `json:"title"`
	Statement     string              `json:"statement"`
	Status        TaskStatus          `json:"status"`
	Priority      RequirementPriority `json:"priority"`
	Estimate      string              `json:"estimate,omitempty"`
	ProducedByRun string              `json:"produced_by_run,omitempty"`
	SupersededBy  string              `json:"superseded_by,omitempty"`
	// RequirementIDs is the requirement graph edges this task implements
	// (N:M, review A4 #3/#4) and DependsOn the task ids it depends on.
	// Computed at read time from graph_edges, not a stored column -- always
	// populated by the handler before the response is written, never nil
	// (an empty task has both as `[]`, not `null`).
	RequirementIDs []string  `json:"requirement_ids"`
	DependsOn      []string  `json:"depends_on"`
	EnvVars        []EnvVar  `json:"env_vars,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
}

// FileChangeKind classifies a single FileChange for display (add/modify/delete).
type FileChangeKind string

const (
	FileAdded    FileChangeKind = "add"
	FileModified FileChangeKind = "modify"
	FileDeleted  FileChangeKind = "delete"
)

// FileChange is one file-level change in a source diff (§36). Path is relative
// to the workspace root. A non-empty Digest is the SHA-256 of the file's
// post-image content; an empty Digest denotes deletion. Mode is "file" for the
// M4 thin slice (the internal implementer produces only regular files).
// ChangeType records whether the file was added, modified, or deleted (computed
// by the orchestrator's Diff); it is empty for patches persisted before the
// field existed, in which case callers fall back to IsDelete.
type FileChange struct {
	Path       string         `json:"path"`
	Digest     string         `json:"digest"`
	Mode       string         `json:"mode,omitempty"`
	ChangeType FileChangeKind `json:"change_type,omitempty"`
	// BaseDigest is the digest of the file this change was made against, for a
	// modified or deleted file. Apply refuses when the workspace file no longer
	// matches it, rather than overwriting an edit made after the agent ran.
	BaseDigest string `json:"base_digest,omitempty"`
	// RedactedDigest is the digest of this file's post-image content with every
	// detected secret span masked, persisted INSTEAD of the raw bytes when the
	// secret sweep denied the change set (Digest above still names the real
	// content's hash, but that hash is never resolvable in the blob store for a
	// denied change set -- only the masked bytes were ever written, so the raw
	// value never reaches the raw store). Empty whenever the raw content was
	// persisted normally. Review: secret-sweep diff visibility.
	RedactedDigest string `json:"redacted_digest,omitempty"`
}

// IsDelete reports whether this change removes the file at Path.
func (f FileChange) IsDelete() bool { return f.Digest == "" }

// ChangeSet is the source mutation primitive (§36).
type ChangeSet struct {
	ID             string          `json:"id"`
	HumanID        string          `json:"human_id"`
	InitiativeID   string          `json:"initiative_id"`
	TaskID         string          `json:"task_id"`
	BaseSnapshotID string          `json:"base_snapshot_id,omitempty"`
	Status         ChangeSetStatus `json:"status"`
	PatchDigest    string          `json:"patch_digest"`
	ReviewID       string          `json:"review_id,omitempty"`
	GitCommitSHA   string          `json:"git_commit_sha,omitempty"`
	// GitCommitWarning explains why an applied change set was NOT committed
	// even though git_commit is on: a detached HEAD, a rebase/merge/
	// cherry-pick in progress, or the repository's default branch without
	// git.allow_default_branch (review C4, adopted #2). Empty whenever
	// git_commit is off, the change set was committed (GitCommitSHA is set),
	// or the workspace is not a git repository.
	GitCommitWarning string `json:"git_commit_warning,omitempty"`
	CreatedByRunID   string `json:"created_by_run_id,omitempty"`
	// Model is the actual model spec that produced this change set (e.g.
	// "openai/gpt-5"), or "demo" when no real provider was configured. The UI
	// keys model=="demo" to label the change "Shipped (demo)" / "Placeholder
	// change" rather than real agent output.
	Model string `json:"model,omitempty"`
	// Executor is the executor kind that produced the change set ("internal"
	// output is a placeholder whatever the model).
	Executor string `json:"executor,omitempty"`
	// AgentOutput is the end of the coding agent's own output for the run
	// that produced this change set: redacted, sanitized, at most 100 lines.
	AgentOutput string    `json:"agent_output,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	// NoOp is true when the change set touched zero files (fsdiff.Diff found
	// nothing to persist) — the agent decided the task was already done, or
	// its model call failed and it wrote nothing (ICP re-run defect #2). An
	// applied NoOp change set must never count as "this task is implemented"
	// anywhere: task completion, evidence eligibility, staleness propagation,
	// or the "every task has an applied change set" guidance — see
	// ImplementsTask. It can still be approved/applied/reverted like any
	// other change set; only what it is COUNTED as changes.
	NoOp bool `json:"no_op,omitempty"`
	// ReviewCompletedAt is set the one time the adversarial reviewer actually
	// finishes for this change set (store.SetChangeSetReview), never merely
	// attempted (ICP re-run defect #1). A change set can reach
	// review_required with this nil when Execute's call to the reviewer
	// itself errored (e.g. the operation was cancelled mid-review) after the
	// checks-running -> review_required transition already happened: it then
	// carries only mechanical-check observations, no adversarial findings,
	// and reviewChangeSet's "approved" verdict must refuse it rather than
	// let it through as if a real review had converged.
	ReviewCompletedAt *time.Time `json:"review_completed_at,omitempty"`
	// PredatesReviewTracking is true when cs was created before migration 33
	// added review_completed_at at all (review MEDIUM #2: its store, not the
	// wire, is the source of truth -- computed by comparing CreatedAt against
	// that migration's own applied_at, per store.applyPredatesReviewTracking).
	// It is only meaningful when ReviewCompletedAt is nil, and it exists
	// purely to keep the four "never finished its adversarial review"
	// messages (Execute's re-pick refusal, the approve refusal, guidance, and
	// applyStateMessage) from calling a pre-upgrade row "cancelled": before
	// the upgrade, a change set's review ran (or was skipped) at approve
	// time and nothing ever recorded whether it "completed" -- NULL is the
	// expected, permanent state for that row, not evidence of a cancellation.
	// Never serialized: it is a message-selection detail, not API surface.
	PredatesReviewTracking bool `json:"-"`
	// ReviewUnavailableReason records why the adversarial reviewer was
	// genuinely ATTEMPTED for cs but could not complete -- e.g. every
	// attempt (including runModel's own reduced-ask retry) was cut off at
	// the model's output-token ceiling (model.ErrTruncated) on a change set
	// too large for it. Set once, by store.SetChangeSetReviewUnavailable,
	// from Execute's own review call or RerunChangeSetReview -- never
	// guessed. Empty means either the review completed (ReviewCompleted()
	// is true) or it was never attempted at all (see
	// ReviewNeverCompletedCause, which falls back to "likely cancelled
	// mid-review" in that case). See ReviewUnavailable.
	ReviewUnavailableReason string `json:"review_unavailable_reason,omitempty"`
	// ReviewNote is what the human asked for when they requested changes on,
	// or rejected, this change set (`idea review <id> changes_requested
	// --note`, POST .../review {"note"}). The task's next run carries it in the
	// implementer's brief, so "request changes" says what to change. Empty for
	// a verdict given without one.
	ReviewNote string `json:"review_note,omitempty"`
}

// ReviewCompleted reports whether the adversarial reviewer actually finished
// for this change set at least once (ICP re-run defect #1). False for a
// change set still in review_required after Execute's own review call
// errored (e.g. cancelled mid-review) — it holds only mechanical-check
// observations, never a real adversarial pass.
func (cs ChangeSet) ReviewCompleted() bool {
	return cs.ReviewCompletedAt != nil
}

// ReviewNeverCompletedCause names, in a short clause meant to be embedded in
// a sentence, why cs has no completed adversarial review (ICP re-run defect
// #1) -- the shared wording for every surface that explains this to a human:
// workflow.Execute's re-pick refusal, reviewChangeSet's approve refusal,
// projection.BuildGuidance, and daemon.applyStateMessage (review MEDIUM #2).
// Only meaningful when !cs.ReviewCompleted().
func (cs ChangeSet) ReviewNeverCompletedCause() string {
	if cs.ReviewUnavailableReason != "" {
		return cs.ReviewUnavailableReason
	}
	if cs.PredatesReviewTracking {
		return "it predates review-completion tracking from before this project's daemon was upgraded, not because anything was cancelled"
	}
	return "likely cancelled mid-review"
}

// ReviewUnavailable reports whether the adversarial reviewer was genuinely
// ATTEMPTED for cs but explicitly could not complete (as opposed to never
// having run at all -- ordinary cancellation, or predating review tracking).
// Only meaningful when !cs.ReviewCompleted().
//
// This is the one case reviewChangeSet's "approved" verdict may let through
// with --accept-findings even though ReviewCompleted() is false: the human
// asked for a review, one genuinely ran, and it explicitly could not finish
// (recorded by store.SetChangeSetReviewUnavailable, never guessed) -- as
// opposed to a change set that was simply cancelled mid-review and has no
// review attempt to point to at all, which must still refuse either way (the
// round-4 guarantee: an unreviewed change set cannot be silently approved).
func (cs ChangeSet) ReviewUnavailable() bool {
	return cs.ReviewUnavailableReason != ""
}

// ImplementsTask reports whether this change set is applied AND actually
// changed something. An applied change set with zero file changes (NoOp)
// must never count as "the task is implemented" (ICP re-run defect #2): not
// for task completion, not for verification-evidence eligibility, not for
// staleness propagation, and not for "every task has an applied change set"
// guidance.
func (cs ChangeSet) ImplementsTask() bool {
	return cs.Status == ChangeSetApplied && !cs.NoOp
}

// NewerChangeSetInScanOrder reports whether cs should replace cur as "the
// latest" when a caller reduces a slice of change sets to one per key (per
// task, or across an initiative) by scanning it in ascending CreatedAt order
// -- store.ListChangeSets' own order, with no secondary sort key.
//
// MEDIUM fix: two change sets created in the same millisecond tie on
// CreatedAt alone, and this codebase had two different answers for who wins
// that tie -- evidence eligibility and /queue used "not before" (the later
// one in scan order, i.e. the actual later attempt, wins), while
// /guidance's LatestChangeSetsPerActiveTask and PR's
// mostRecentGitCommitWarning used strict "after" (the FIRST one scanned
// wins instead). Two change sets a millisecond apart could make /queue and
// /guidance name different "latest" change sets for the same task. Every
// such reducer must call this one function instead of comparing CreatedAt
// itself, so they can never disagree again.
func NewerChangeSetInScanOrder(cs, cur ChangeSet) bool {
	return !cs.CreatedAt.Before(cur.CreatedAt)
}

// ApplyJournalEntry is one durable apply-journal record (§39).
type ApplyJournalEntry struct {
	ID                      string     `json:"id"`
	ChangeSetID             string     `json:"change_set_id"`
	State                   ApplyState `json:"state"`
	WorkspaceSnapshotDigest string     `json:"workspace_snapshot_digest,omitempty"`
	AppliedAt               *time.Time `json:"applied_at,omitempty"`
	CreatedAt               time.Time  `json:"created_at"`
}

// RiskSignal is one deterministic security signal (§31).
type RiskSignal struct {
	Name     string `json:"name"`
	Detected bool   `json:"detected"`
	Detail   string `json:"detail,omitempty"`
}

// RiskProfile is a coarse risk tiering for an initiative (§31).
type RiskProfile struct {
	ID           string       `json:"id"`
	InitiativeID string       `json:"initiative_id"`
	Tier         RiskTier     `json:"tier"`
	Signals      []RiskSignal `json:"signals"`
	Reason       []string     `json:"reason,omitempty"`
	CreatedAt    time.Time    `json:"created_at"`
}

// VerificationEvidence is one piece of evidence for an acceptance criterion
// (§70). CriterionKey is a stable per-criterion key (see CriterionKey). A
// requirement is verified only when every mandatory criterion has a pass
// evidence — never merely because tests exist.
type VerificationEvidence struct {
	ID             string             `json:"id"`
	InitiativeID   string             `json:"initiative_id"`
	RequirementID  string             `json:"requirement_id"`
	CriterionKey   string             `json:"criterion_key"`
	Kind           VerificationKind   `json:"kind"`
	Status         VerificationStatus `json:"status"`
	EvidenceDigest string             `json:"evidence_digest,omitempty"`
	RunID          string             `json:"run_id,omitempty"`
	VerifiedBy     string             `json:"verified_by,omitempty"` // role name or "human:<actor>"
	Note           string             `json:"note,omitempty"`
	CreatedAt      time.Time          `json:"created_at"`
}

// StaleItem marks a graph node whose validity is in question because a
// decided decision it (transitively) depends on stopped being decided after
// the node was produced or applied (§26, review A4 #5: "impact is true").
// TargetType is one of "requirement", "task" or "change_set". Cleared either
// by re-planning (which re-derives the plan from the live decision state) or
// by an explicit human acknowledgement.
type StaleItem struct {
	ID           string     `json:"id"`
	InitiativeID string     `json:"initiative_id"`
	TargetType   string     `json:"target_type"`
	TargetID     string     `json:"target_id"`
	Reason       string     `json:"reason"`
	DecisionID   string     `json:"decision_id,omitempty"`
	CreatedAt    time.Time  `json:"created_at"`
	ClearedAt    *time.Time `json:"cleared_at,omitempty"`
	ClearedBy    string     `json:"cleared_by,omitempty"`
}

// CriterionKey returns the stable key for the i-th acceptance criterion of a
// requirement. The thin slice treats every criterion as mandatory and keys
// evidence by requirement + index (§70, review B4).
func CriterionKey(requirementID string, i int) string {
	return requirementID + ":" + strconv.Itoa(i)
}

// TimelineRef points a TimelineEntry at the entity it is about, so a client
// can link to it without parsing the human title.
type TimelineRef struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// TimelineEntry is one human-language line of an initiative's "what changed"
// timeline (C1, wave 3 contract), newest first. Kind is one of
// change_set_applied, change_set_rejected, change_set_denied,
// change_set_reverted, run_failed, run_stopped, decision_changed, stale,
// phase_completed.
type TimelineEntry struct {
	At     time.Time   `json:"at"`
	Kind   string      `json:"kind"`
	Title  string      `json:"title"`
	Detail string      `json:"detail,omitempty"`
	Ref    TimelineRef `json:"ref"`
}

// PhaseCost is one phase's spend and time (C3, wave 3 contract): a "phase" is
// a workflow stage that made a model call (PRD, review, design, plan, ...).
// Agent (coding) runs are reported separately under InitiativeCosts.Agent,
// since their spend is not always measurable the same way.
type PhaseCost struct {
	Phase   string  `json:"phase"`
	USD     float64 `json:"usd"`
	Seconds float64 `json:"seconds"`
	Calls   int     `json:"calls"`
}

// AgentCost is the coding-agent run summary (C3, wave 3 contract). USD is nil
// ("agent spend not measured") unless every counted run's cost could be parsed
// from the agent's own output.
type AgentCost struct {
	Runs    int      `json:"runs"`
	Seconds float64  `json:"seconds"`
	USD     *float64 `json:"usd"`
	Note    string   `json:"note,omitempty"`
}

// InitiativeCosts is the GET .../costs response body (C3, wave 3 contract).
type InitiativeCosts struct {
	TotalUSD float64     `json:"total_usd"`
	ByPhase  []PhaseCost `json:"by_phase"`
	Agent    AgentCost   `json:"agent"`
}

// QueueItem is one cross-initiative "needs you" row (D-b, wave 3 contract).
// Rank is a sort key, ascending: lower ranks more urgent, blocking items first
// and then by age within a category.
type QueueItem struct {
	InitiativeID string `json:"initiative_id"`
	ShortTitle   string `json:"short_title"`
	Kind         string `json:"kind"`
	Label        string `json:"label"`
	Rank         int64  `json:"rank"`
	Href         string `json:"href"`
}

// leadIns are common prefixes GenerateShortTitle drops before taking the
// idea's first clause (C1, wave 3 contract): "build a rate limiter" and "a
// rate limiter" should produce the same short title.
var leadIns = []string{
	"i would like to ", "i'd like to ", "i want to ", "i need to ",
	"we would like to ", "we want to ", "we need to ",
	"please build ", "please add ", "please create ", "please implement ",
	"let's build ", "lets build ",
	"build ", "add ", "create ", "implement ", "make ",
}

// GenerateShortTitle deterministically derives a short, human title (<=60
// characters) from an idea's full text (C1, wave 3 contract): the first
// sentence or clause, with a common lead-in phrase dropped, trimmed at a word
// boundary. Title/Description keep the full text; this is only ever a display
// label. Same input always produces the same output, so it can be generated
// once on create and backfilled identically for older initiatives.
func GenerateShortTitle(idea string) string {
	s := strings.TrimSpace(idea)
	if s == "" {
		return ""
	}
	withoutLeadIn := dropLeadIn(s)
	clause := firstClause(withoutLeadIn)
	if clause == "" {
		clause = firstClause(s)
	}
	if clause == "" {
		clause = s
	}
	return TrimToWordBoundary(clause, 60)
}

// dropLeadIn removes one leading phrase from leadIns, case-insensitively, if s
// starts with one.
func dropLeadIn(s string) string {
	lower := strings.ToLower(s)
	for _, lead := range leadIns {
		if strings.HasPrefix(lower, lead) {
			return strings.TrimSpace(s[len(lead):])
		}
	}
	return s
}

// firstClauseSeparators are checked in the order given; the earliest match in
// s wins regardless of which separator it is.
var firstClauseSeparators = []string{".", "!", "?", ";", ","}

// firstClause returns the text up to (excluding) the first sentence or clause
// boundary, or the whole string when none is found.
func firstClause(s string) string {
	cut := len(s)
	for _, sep := range firstClauseSeparators {
		if i := strings.Index(s, sep); i >= 0 && i < cut {
			cut = i
		}
	}
	return strings.TrimSpace(s[:cut])
}

// TrimToWordBoundary trims s to at most n runes without splitting a word,
// falling back to a hard cut only when no earlier space exists. Exported
// (review LOW #11) so it is the one place this algorithm lives: projection's
// truncateWords used to carry an independent, near-identical copy that had
// already drifted on when it measures length (before vs. after trimming
// surrounding whitespace) before either callers noticed -- two
// implementations of the same word-boundary cut, each free to diverge
// further the next time one of them was touched.
func TrimToWordBoundary(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	cut := n
	for cut > 0 && r[cut] != ' ' {
		cut--
	}
	if cut == 0 {
		cut = n
	}
	return strings.TrimSpace(string(r[:cut]))
}

// QualityCitation points a quality check at the evidence behind it: a file
// and line in the workspace, and/or the requirement it bears on.
type QualityCitation struct {
	Path        string `json:"path,omitempty"`
	Line        int    `json:"line,omitempty"`
	Requirement string `json:"requirement,omitempty"`
}

// QualityCheck is one check inside a quality dimension: what was checked, how
// (deterministic or model-reviewed), the outcome, and where the evidence is.
type QualityCheck struct {
	Name      string            `json:"name"`
	Status    QualityStatus     `json:"status"`
	Method    QualityMethod     `json:"method"`
	Detail    string            `json:"detail,omitempty"`
	Citations []QualityCitation `json:"citations,omitempty"`
}

// QualityResult is one dimension's measured outcome for an initiative's
// applied work (verify.dimensions). Results are append-only: every
// measurement writes a new row per dimension, the scorecard is the latest of
// each, and the previous one is the performance baseline. Each result is
// linked to the requirements it bears on with a `verifies` edge, so
// `idea why quality_result <id>` and `idea impact requirement REQ-n` show it.
type QualityResult struct {
	ID            string           `json:"id"`
	InitiativeID  string           `json:"initiative_id"`
	MeasurementID string           `json:"measurement_id"`
	Dimension     QualityDimension `json:"dimension"`
	Mode          QualityMode      `json:"mode"`
	Status        QualityStatus    `json:"status"`
	Summary       string           `json:"summary"`
	Checks        []QualityCheck   `json:"checks"`
	// Metrics are named numeric measurements (performance budgets record
	// theirs here; they are the next measurement's baseline).
	Metrics map[string]float64 `json:"metrics,omitempty"`
	// InputDigest names what was measured: the applied change sets and the
	// verify policy. A result whose digest no longer matches the initiative's
	// current one is stale and counts as not measured at the gate.
	InputDigest string `json:"input_digest"`
	// Requirements are the codes of the requirements this result is linked
	// to in the Product Graph.
	Requirements []string  `json:"requirements"`
	CreatedAt    time.Time `json:"created_at"`
}
