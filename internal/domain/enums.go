// Package domain defines the typed entities and controlled vocabularies of the
// idea-harness product graph (Milestone 1 subset). It contains no I/O and no LLM
// logic: it is the single source of truth for enums and invariants that other
// packages enforce.
package domain

// AssertionType discriminates Facts from Assumptions from Hypotheses (constitution §18).
type AssertionType string

const (
	AssertionFact       AssertionType = "fact"
	AssertionAssumption AssertionType = "assumption"
	AssertionHypothesis AssertionType = "hypothesis"
)

func (t AssertionType) Valid() bool {
	switch t {
	case AssertionFact, AssertionAssumption, AssertionHypothesis:
		return true
	}
	return false
}

// AssertionStatus is the lifecycle state of an Assertion (§18).
type AssertionStatus string

const (
	AssertionCandidate  AssertionStatus = "candidate"
	AssertionAccepted   AssertionStatus = "accepted"
	AssertionDisputed   AssertionStatus = "disputed"
	AssertionRetracted  AssertionStatus = "retracted"
	AssertionSuperseded AssertionStatus = "superseded"
)

func (s AssertionStatus) Valid() bool {
	switch s {
	case AssertionCandidate, AssertionAccepted, AssertionDisputed, AssertionRetracted, AssertionSuperseded:
		return true
	}
	return false
}

// RunKind discriminates the generic Run model (§35).
type RunKind string

const (
	RunModel  RunKind = "model"
	RunReview RunKind = "review"
	RunCheck  RunKind = "check"
	RunTool   RunKind = "tool"
	RunAgent  RunKind = "agent"
)

func (k RunKind) Valid() bool {
	switch k {
	case RunModel, RunReview, RunCheck, RunTool, RunAgent:
		return true
	}
	return false
}

// RunStatus is the lifecycle of a Run.
type RunStatus string

const (
	RunPending   RunStatus = "pending"
	RunRunning   RunStatus = "running"
	RunSucceeded RunStatus = "succeeded"
	RunFailed    RunStatus = "failed"
)

func (s RunStatus) Valid() bool {
	switch s {
	case RunPending, RunRunning, RunSucceeded, RunFailed:
		return true
	}
	return false
}

// Severity is the review-finding severity (§15).
type Severity string

const (
	SeverityCritical    Severity = "critical"
	SeverityHigh        Severity = "high"
	SeverityMedium      Severity = "medium"
	SeverityLow         Severity = "low"
	SeverityObservation Severity = "observation"
)

func (s Severity) Valid() bool {
	switch s {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow, SeverityObservation:
		return true
	}
	return false
}

// Blocking reports whether an open finding of this severity blocks approval (§11).
func (s Severity) Blocking() bool {
	return s == SeverityCritical || s == SeverityHigh
}

// Confidence is the review-finding confidence (§15).
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

func (c Confidence) Valid() bool {
	switch c {
	case ConfidenceHigh, ConfidenceMedium, ConfidenceLow:
		return true
	}
	return false
}

// JudgmentReason says why an initiative is in `needs_human_judgment`.
//
// The status has six producers and they need different answers from the user:
// a stalled PRD review is approved or revised, a plan whose dependency graph
// did not resolve can only be regenerated, and a requirement failed after
// completion is re-verified. Recording the reason is what lets `idea next` name
// a command instead of guessing. It was guessed before, from the presence of
// the execution-plan artifact, which is written *before* the unresolved-
// dependency check that produces two of the six -- so the guess named
// `approve-plan` for a plan known to be incomplete.
type JudgmentReason string

const (
	// JudgmentPRDReview: the adversarial PRD review did not converge within
	// max_cycles. Resolve by accepting the PRD or regenerating it.
	JudgmentPRDReview JudgmentReason = "prd_review"
	// JudgmentPlanDependencies: the execution plan has tasks whose dependencies
	// were never satisfied. Only regenerating the plan resolves this -- approving
	// it would set plan_approved over a known-incomplete graph and leave an
	// undisposed blocking finding that wedges completion.
	JudgmentPlanDependencies JudgmentReason = "plan_dependencies"
	// JudgmentPlanReview: the adversarial plan review raised blocking findings.
	JudgmentPlanReview JudgmentReason = "plan_review"
	// JudgmentVerification: a requirement was failed after the initiative had
	// already completed, so it dropped back out of `complete`. Resolve by
	// recording verification evidence, not by touching the plan.
	JudgmentVerification JudgmentReason = "verification"
)

// Valid reports whether r is a known judgment reason. The empty string is valid
// and means "not recorded": initiatives written before this was introduced, and
// any producer that has not been taught to set it.
func (r JudgmentReason) Valid() bool {
	switch r {
	case "", JudgmentPRDReview, JudgmentPlanDependencies, JudgmentPlanReview, JudgmentVerification:
		return true
	}
	return false
}

// Disposition is the review-finding disposition (§15 seven-value enum, plus
// not_a_secret -- review changes #1 -- which is valid only on a secret_sweep
// finding; store.SetFindingDisposition enforces that scoping, not this type).
type Disposition string

const (
	DispositionFixed         Disposition = "fixed"
	DispositionAcceptedRisk  Disposition = "accepted_risk"
	DispositionNotApplicable Disposition = "not_applicable"
	DispositionDuplicate     Disposition = "duplicate"
	DispositionDeferred      Disposition = "deferred"
	DispositionRejected      Disposition = "rejected"
	DispositionMitigate      Disposition = "mitigate"
	// DispositionNotASecret marks a secret_sweep finding's detected value as a
	// false positive. Recording it also allowlists the value's fingerprint
	// (never the value) for the project, so future sweeps ignore it (§ D2).
	DispositionNotASecret Disposition = "not_a_secret"
)

func (d Disposition) Valid() bool {
	switch d {
	case DispositionFixed, DispositionAcceptedRisk, DispositionNotApplicable,
		DispositionDuplicate, DispositionDeferred, DispositionRejected, DispositionMitigate,
		DispositionNotASecret:
		return true
	}
	return false
}

// DecisionStatus is the lifecycle of a Decision (§19).
type DecisionStatus string

const (
	DecisionOpen       DecisionStatus = "open"
	DecisionDecided    DecisionStatus = "decided"
	DecisionSuperseded DecisionStatus = "superseded"
)

func (s DecisionStatus) Valid() bool {
	switch s {
	case DecisionOpen, DecisionDecided, DecisionSuperseded:
		return true
	}
	return false
}

// DecisionType categorizes a Decision (§19).
type DecisionType string

const (
	DecisionProduct      DecisionType = "product"
	DecisionArchitecture DecisionType = "architecture"
	DecisionDesign       DecisionType = "design"
	DecisionSecurity     DecisionType = "security"
	DecisionOperations   DecisionType = "operations"
)

func (t DecisionType) Valid() bool {
	switch t {
	case DecisionProduct, DecisionArchitecture, DecisionDesign, DecisionSecurity, DecisionOperations:
		return true
	}
	return false
}

// TrustLevel is the context trust level (§38). It is assigned by deterministic
// code, never by model output.
type TrustLevel string

const (
	TrustSystem                TrustLevel = "system"
	TrustPolicy                TrustLevel = "policy"
	TrustHumanApproved         TrustLevel = "human_approved"
	TrustReviewedAgentOutput   TrustLevel = "reviewed_agent_output"
	TrustUnreviewedAgentOutput TrustLevel = "unreviewed_agent_output"
	TrustExternalUntrusted     TrustLevel = "external_untrusted"
)

func (t TrustLevel) Valid() bool {
	switch t {
	case TrustSystem, TrustPolicy, TrustHumanApproved, TrustReviewedAgentOutput,
		TrustUnreviewedAgentOutput, TrustExternalUntrusted:
		return true
	}
	return false
}

// InitiativeStatus is the initiative/workflow lifecycle (§11).
type InitiativeStatus string

const (
	InitiativeRunning            InitiativeStatus = "running"
	InitiativeNeedsHumanJudgment InitiativeStatus = "needs_human_judgment"
	InitiativeApproved           InitiativeStatus = "approved"
	InitiativePlanApproved       InitiativeStatus = "plan_approved"
	InitiativeFailed             InitiativeStatus = "failed"
	InitiativeComplete           InitiativeStatus = "complete"
)

func (s InitiativeStatus) Valid() bool {
	switch s {
	case InitiativeRunning, InitiativeNeedsHumanJudgment, InitiativeApproved, InitiativePlanApproved, InitiativeFailed, InitiativeComplete:
		return true
	}
	return false
}

// QuestionStatus is the lifecycle of an Open Question.
type QuestionStatus string

const (
	QuestionOpen     QuestionStatus = "open"
	QuestionResolved QuestionStatus = "resolved"
)

func (s QuestionStatus) Valid() bool {
	return s == QuestionOpen || s == QuestionResolved
}

// RequirementStatus is the lifecycle of a Requirement (§17).
type RequirementStatus string

const (
	RequirementDraft    RequirementStatus = "draft"
	RequirementApproved RequirementStatus = "approved"
	RequirementVerified RequirementStatus = "verified"
	// RequirementFailed records a human "fail" judgment: the requirement stays in
	// the active set and blocks completion until re-verified (a human pass).
	RequirementFailed RequirementStatus = "failed"
	// RequirementWaived records a human "waive" judgment: the requirement is out
	// of scope and is excluded from the completion gate (like removed), but is
	// retained as a visible, recorded judgment rather than deleted.
	RequirementWaived  RequirementStatus = "waived"
	RequirementRemoved RequirementStatus = "removed"
)

func (s RequirementStatus) Valid() bool {
	switch s {
	case RequirementDraft, RequirementApproved, RequirementVerified, RequirementFailed, RequirementWaived, RequirementRemoved:
		return true
	}
	return false
}

// RequirementPriority is the requirement priority level.
type RequirementPriority string

const (
	PriorityMust   RequirementPriority = "must"
	PriorityShould RequirementPriority = "should"
	PriorityCould  RequirementPriority = "could"
)

func (p RequirementPriority) Valid() bool {
	switch p {
	case PriorityMust, PriorityShould, PriorityCould:
		return true
	}
	return false
}

// VerificationKind is the evidence category for a verification (§70).
type VerificationKind string

const (
	VerificationAcceptance VerificationKind = "acceptance"
	VerificationSecurity   VerificationKind = "security"
	VerificationUX         VerificationKind = "ux"
)

func (k VerificationKind) Valid() bool {
	switch k {
	case VerificationAcceptance, VerificationSecurity, VerificationUX:
		return true
	}
	return false
}

// VerificationStatus is the outcome of one piece of evidence (§70).
type VerificationStatus string

const (
	VerificationPass        VerificationStatus = "pass"
	VerificationFail        VerificationStatus = "fail"
	VerificationBlocked     VerificationStatus = "blocked"
	VerificationNeedsManual VerificationStatus = "needs_manual"
	// VerificationWaived records a human waiver judgment at evidence level (the
	// corresponding requirement is marked waived, not verified).
	VerificationWaived VerificationStatus = "waived"
)

func (s VerificationStatus) Valid() bool {
	switch s {
	case VerificationPass, VerificationFail, VerificationBlocked, VerificationNeedsManual, VerificationWaived:
		return true
	}
	return false
}

// EdgeType is a Product Graph edge kind (§25). M2 wires the five traceability
// edges; the rest are declared now and written in later milestones.
type EdgeType string

const (
	EdgeSupports    EdgeType = "supports"
	EdgeContradicts EdgeType = "contradicts"
	EdgeDerivedFrom EdgeType = "derived_from"
	EdgeDependsOn   EdgeType = "depends_on"
	EdgeImplements  EdgeType = "implements"
	EdgeAddresses   EdgeType = "addresses"
	EdgeTests       EdgeType = "tests"
	EdgeVerifies    EdgeType = "verifies"
	EdgeReviews     EdgeType = "reviews"
	EdgeSupersedes  EdgeType = "supersedes"
	EdgeInvalidates EdgeType = "invalidates"
	EdgeProducedBy  EdgeType = "produced_by"
	EdgeConsumes    EdgeType = "consumes"
)

func (e EdgeType) Valid() bool {
	switch e {
	case EdgeSupports, EdgeContradicts, EdgeDerivedFrom, EdgeDependsOn, EdgeImplements,
		EdgeAddresses, EdgeTests, EdgeVerifies, EdgeReviews, EdgeSupersedes,
		EdgeInvalidates, EdgeProducedBy, EdgeConsumes:
		return true
	}
	return false
}

// TaskStatus is the lifecycle of an execution-plan Task.
type TaskStatus string

const (
	TaskProposed  TaskStatus = "proposed"
	TaskReady     TaskStatus = "ready"
	TaskBlocked   TaskStatus = "blocked"
	TaskDone      TaskStatus = "done"
	TaskCancelled TaskStatus = "cancelled"
)

func (s TaskStatus) Valid() bool {
	switch s {
	case TaskProposed, TaskReady, TaskBlocked, TaskDone, TaskCancelled:
		return true
	}
	return false
}

// ArtifactKind enumerates the core artifacts (§20).
type ArtifactKind string

const (
	ArtifactIdeaBrief            ArtifactKind = "idea_brief"
	ArtifactResearchBrief        ArtifactKind = "research_brief"
	ArtifactPRD                  ArtifactKind = "prd"
	ArtifactTechnicalDesign      ArtifactKind = "technical_design"
	ArtifactDesignSpec           ArtifactKind = "design_spec"
	ArtifactVerificationPlan     ArtifactKind = "verification_plan"
	ArtifactThreatModel          ArtifactKind = "threat_model"
	ArtifactExecutionPlan        ArtifactKind = "execution_plan"
	ArtifactOperationalReadiness ArtifactKind = "operational_readiness"
	ArtifactVerificationReport   ArtifactKind = "verification_report"
)

func (k ArtifactKind) Valid() bool {
	switch k {
	case ArtifactIdeaBrief, ArtifactResearchBrief, ArtifactPRD, ArtifactTechnicalDesign,
		ArtifactDesignSpec, ArtifactVerificationPlan, ArtifactThreatModel,
		ArtifactExecutionPlan, ArtifactOperationalReadiness, ArtifactVerificationReport:
		return true
	}
	return false
}

// OperationStatus is the lifecycle of a durable Operation (§34).
type OperationStatus string

const (
	OperationPending   OperationStatus = "pending"
	OperationRunning   OperationStatus = "running"
	OperationSucceeded OperationStatus = "succeeded"
	OperationFailed    OperationStatus = "failed"
	OperationCancelled OperationStatus = "cancelled"
)

func (s OperationStatus) Valid() bool {
	switch s {
	case OperationPending, OperationRunning, OperationSucceeded, OperationFailed, OperationCancelled:
		return true
	}
	return false
}

// DenyCode is a machine-readable system-deny reason (security model, §system
// deny). It is set on a Finding.Code when a deterministic, non-overridable
// boundary rejects or blocks an action — independent of executor trust or human
// approval ("system deny beats human approve").
type DenyCode string

const (
	DenyCodeSecretSweep        DenyCode = "secret_sweep"
	DenyCodePII                DenyCode = "pii"
	DenyCodePathEscape         DenyCode = "path_escape"
	DenyCodeHostExec           DenyCode = "host_exec"
	DenyCodeBlobDigestMismatch DenyCode = "blob_digest_mismatch"
	DenyCodeTrustDeny          DenyCode = "trust_deny"
	DenyCodeReviewOpenBlocking DenyCode = "review_open_blocking"
)

func (d DenyCode) Valid() bool {
	switch d {
	case DenyCodeSecretSweep, DenyCodePII, DenyCodePathEscape, DenyCodeHostExec,
		DenyCodeBlobDigestMismatch, DenyCodeTrustDeny, DenyCodeReviewOpenBlocking:
		return true
	}
	return false
}

// ChangeSetStatus is the lifecycle of a ChangeSet (§38).
type ChangeSetStatus string

const (
	ChangeSetDraft            ChangeSetStatus = "draft"
	ChangeSetChecksRunning    ChangeSetStatus = "checks_running"
	ChangeSetReviewRequired   ChangeSetStatus = "review_required"
	ChangeSetRejected         ChangeSetStatus = "rejected"
	ChangeSetChangesRequested ChangeSetStatus = "changes_requested"
	ChangeSetSuperseded       ChangeSetStatus = "superseded"
	ChangeSetApproved         ChangeSetStatus = "approved"
	ChangeSetApplying         ChangeSetStatus = "applying"
	ChangeSetApplied          ChangeSetStatus = "applied"
	ChangeSetReverted         ChangeSetStatus = "reverted"
	ChangeSetConflicted       ChangeSetStatus = "conflicted"
)

// HoldsWork reports whether a change set in this state is work a plan-level
// command must not disturb.
//
// `rejected` and `superseded` are absorbing states minted on ordinary paths --
// every credential-sweep reject leaves one behind and reopens the task, and a
// re-execution supersedes its predecessor -- so a plan carrying only those has
// no work in flight and is legitimately revisable. Counting them made
// `revise-plan`, `approve-plan` and `reopen-phase` refuse forever, and the
// refusal named `idea review` and `idea apply`, both of which are closed for a
// rejected change set.
//
// This is the same distinction that got audit 5's A2 rejected: it proposed
// gating completion on `{applied, reverted}` and review pointed out that the
// absorbing states are minted on ordinary paths, so the predicate would wedge
// any initiative that ever tripped the sweep. Here it is stated once and read
// by every site rather than re-derived at each.
func (s ChangeSetStatus) HoldsWork() bool {
	switch s {
	case ChangeSetRejected, ChangeSetSuperseded:
		return false
	}
	return true
}

func (s ChangeSetStatus) Valid() bool {
	switch s {
	case ChangeSetDraft, ChangeSetChecksRunning, ChangeSetReviewRequired, ChangeSetRejected,
		ChangeSetChangesRequested, ChangeSetSuperseded, ChangeSetApproved,
		ChangeSetApplying, ChangeSetApplied, ChangeSetReverted, ChangeSetConflicted:
		return true
	}
	return false
}

// ApplyState is the durable apply-journal state (§39).
type ApplyState string

const (
	ApplyPrepared         ApplyState = "prepared"
	ApplyWorkspaceMutated ApplyState = "workspace_mutated"
	ApplyCommitted        ApplyState = "committed"
	ApplyRolledBack       ApplyState = "rolled_back"
	ApplyRecoveryRequired ApplyState = "recovery_required"
)

func (s ApplyState) Valid() bool {
	switch s {
	case ApplyPrepared, ApplyWorkspaceMutated, ApplyCommitted, ApplyRolledBack, ApplyRecoveryRequired:
		return true
	}
	return false
}

// RiskTier is a coarse risk tier (§31); no numeric scores.
type RiskTier string

const (
	RiskLow      RiskTier = "low"
	RiskMedium   RiskTier = "medium"
	RiskHigh     RiskTier = "high"
	RiskCritical RiskTier = "critical"
)

func (t RiskTier) Valid() bool {
	switch t {
	case RiskLow, RiskMedium, RiskHigh, RiskCritical:
		return true
	}
	return false
}

// ExecutorTrust is the executor trust tier (§49).
type ExecutorTrust string

const (
	ExecutorInternal       ExecutorTrust = "internal"
	ExecutorLocalExternal  ExecutorTrust = "local_external"
	ExecutorRemoteExternal ExecutorTrust = "remote_external"
)

func (t ExecutorTrust) Valid() bool {
	switch t {
	case ExecutorInternal, ExecutorLocalExternal, ExecutorRemoteExternal:
		return true
	}
	return false
}

// SandboxMode is the OS-level sandbox mode (§52).
type SandboxMode string

const (
	SandboxContainer       SandboxMode = "container"
	SandboxLocalRestricted SandboxMode = "local_restricted"
	SandboxHostApproved    SandboxMode = "host_approved"
)

func (m SandboxMode) Valid() bool {
	switch m {
	case SandboxContainer, SandboxLocalRestricted, SandboxHostApproved:
		return true
	}
	return false
}

// QualityDimension is one quality dimension `idea verify` measures beyond the
// acceptance tests (verify.dimensions in harness.yaml). Each produces a
// QualityResult with pass/warn/fail -- or an explicit not_measured -- and
// file/requirement citations, recorded as evidence in the Product Graph.
type QualityDimension string

const (
	QualitySetupReproducibility QualityDimension = "setup_reproducibility"
	QualityDocsCompleteness     QualityDimension = "docs_completeness"
	QualityAPICompatibility     QualityDimension = "api_compatibility"
	QualityAPIErgonomics        QualityDimension = "api_ergonomics"
	QualitySecurity             QualityDimension = "security"
	QualityPerformance          QualityDimension = "performance"
	QualityOperationalReadiness QualityDimension = "operational_readiness"
)

// QualityDimensions is every dimension, in scorecard order. The order is the
// order a reader meets them in -- "does it build from a clean checkout" first,
// the judgment calls last -- not alphabetical.
var QualityDimensions = []QualityDimension{
	QualitySetupReproducibility,
	QualityDocsCompleteness,
	QualityAPICompatibility,
	QualityAPIErgonomics,
	QualitySecurity,
	QualityPerformance,
	QualityOperationalReadiness,
}

func (d QualityDimension) Valid() bool {
	for _, v := range QualityDimensions {
		if v == d {
			return true
		}
	}
	return false
}

// QualityStatus is a dimension's (or one of its checks') outcome.
//
// not_measured is not a softer pass: it is what a dimension reports when the
// harness could not look -- no Docker, no toolchain image, a vulnerability
// database that needs the network, a demo model that cannot review -- and a
// gated dimension that was not measured does not satisfy the gate.
// not_applicable is the opposite case: the harness looked and there was
// nothing for the dimension to judge (no OpenAPI spec and no Go package
// changed, say), which does satisfy it.
type QualityStatus string

const (
	QualityPass          QualityStatus = "pass"
	QualityWarn          QualityStatus = "warn"
	QualityFail          QualityStatus = "fail"
	QualityNotMeasured   QualityStatus = "not_measured"
	QualityNotApplicable QualityStatus = "not_applicable"
)

func (s QualityStatus) Valid() bool {
	switch s {
	case QualityPass, QualityWarn, QualityFail, QualityNotMeasured, QualityNotApplicable:
		return true
	}
	return false
}

// SatisfiesGate reports whether a gated dimension with this status lets the
// verification gate pass. warn does (it is a note, not a failure); fail and
// not_measured do not.
func (s QualityStatus) SatisfiesGate() bool {
	switch s {
	case QualityPass, QualityWarn, QualityNotApplicable:
		return true
	}
	return false
}

// QualityMode is a dimension's policy: off (not measured at all), advisory
// (measured and reported, never blocks) or gate (a fail or not_measured
// result keeps the verification gate from passing).
type QualityMode string

const (
	QualityModeOff      QualityMode = "off"
	QualityModeAdvisory QualityMode = "advisory"
	QualityModeGate     QualityMode = "gate"
)

func (m QualityMode) Valid() bool {
	switch m {
	case QualityModeOff, QualityModeAdvisory, QualityModeGate:
		return true
	}
	return false
}

// QualityMethod says how a quality check reached its result: a deterministic
// check (a parser, a heuristic, a command run in the sandbox) or a model
// review (the verification_engineer role, whose findings must cite lines).
type QualityMethod string

const (
	QualityMethodDeterministic QualityMethod = "deterministic"
	QualityMethodModel         QualityMethod = "model"
)

func (m QualityMethod) Valid() bool {
	switch m {
	case QualityMethodDeterministic, QualityMethodModel:
		return true
	}
	return false
}
