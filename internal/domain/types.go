package domain

import (
	"strconv"
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

// Assertion is a structured Fact/Assumption/Hypothesis (§18).
type Assertion struct {
	ID             string           `json:"id"`
	InitiativeID   string           `json:"initiative_id"`
	Type           AssertionType    `json:"type"`
	Status         AssertionStatus  `json:"status"`
	Statement      string           `json:"statement"`
	EvidenceDigest string           `json:"evidence_digest,omitempty"`
	Confidence     Confidence       `json:"confidence"`
	Source         string           `json:"source"`
	Provenance     []ProvenanceRef  `json:"provenance"`
	ProducedByRun  string           `json:"produced_by_run,omitempty"`
	SupersededBy   string           `json:"superseded_by,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
}

// Requirement is a first-class structured requirement (§17).
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
	VerifiedAt         *time.Time            `json:"verified_at,omitempty"`
	CreatedAt          time.Time             `json:"created_at"`
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
	AnswerDigest string         `json:"answer_digest,omitempty"`
	AnsweredBy   string         `json:"answered_by,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	ResolvedAt   *time.Time     `json:"resolved_at,omitempty"`
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
	// secret_sweep finding raised from a per-file credential pattern match
	// (§ D2, review changes #1). SecretFingerprint is the sha256 of the
	// matched value's capture group, never the value itself; disposing the
	// finding not_a_secret allowlists it by this fingerprint.
	SecretFingerprint string    `json:"secret_fingerprint,omitempty"`
	SecretKind        string    `json:"secret_kind,omitempty"`
	SecretPath        string    `json:"secret_path,omitempty"`
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
	// severity: today, only "about code this change set did not touch"
	// (review B2) and a rigor cap's own dropped-count note (review B1) set
	// it. Kept as its own bool rather than folded into Severity or
	// Disposition -- review.EvaluateApproval and denyReviewOpenBlocking both
	// skip an advisory finding outright, and a UI backlog view is exactly the
	// findings with this set and no disposition yet.
	Advisory  bool      `json:"advisory,omitempty"`
	CreatedAt time.Time `json:"created_at"`
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
	ID            string           `json:"id"`
	ProjectID     string           `json:"project_id,omitempty"`
	Title         string           `json:"title"`
	Description   string           `json:"description"`
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
	Error      string     `json:"error,omitempty"`
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

// Task is one unit of the execution plan (§12, M2).
type Task struct {
	ID             string              `json:"id"`
	InitiativeID   string              `json:"initiative_id"`
	Code           string              `json:"code"`
	Title          string              `json:"title"`
	Statement      string              `json:"statement"`
	Status         TaskStatus          `json:"status"`
	Priority       RequirementPriority `json:"priority"`
	Estimate       string              `json:"estimate,omitempty"`
	ProducedByRun  string              `json:"produced_by_run,omitempty"`
	SupersededBy   string              `json:"superseded_by,omitempty"`
	CreatedAt      time.Time           `json:"created_at"`
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
	CreatedByRunID string          `json:"created_by_run_id,omitempty"`
	// Model is the actual model spec that produced this change set (e.g.
	// "openai/gpt-5"), or "demo" when no real provider was configured. The UI
	// keys model=="demo" to label the change "Shipped (demo)" / "Placeholder
	// change" rather than real agent output.
	Model     string    `json:"model,omitempty"`
	// Executor is the executor kind that produced the change set ("internal"
	// output is a placeholder whatever the model).
	Executor string `json:"executor,omitempty"`
	// AgentOutput is the end of the coding agent's own output for the run
	// that produced this change set: redacted, sanitized, at most 100 lines.
	AgentOutput string `json:"agent_output,omitempty"`
	CreatedAt time.Time `json:"created_at"`
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
