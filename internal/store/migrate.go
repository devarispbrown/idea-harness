package store

import (
	"database/sql"
	"errors"
	"fmt"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// globalMigrations is the global.db schema (project registry + daemon meta).
var globalMigrations = []string{`
CREATE TABLE IF NOT EXISTS projects (
	id         TEXT PRIMARY KEY,
	name       TEXT NOT NULL,
	root_path  TEXT NOT NULL,
	project_id TEXT NOT NULL UNIQUE,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS daemon_meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
`}

// migrations is the ordered state.db schema history. Append-only: never edit an
// applied migration; add a new one (§19).
var migrations = []string{
	// 1: core product-graph entities (Milestone 1).
	`
CREATE TABLE IF NOT EXISTS initiatives (
	id           TEXT PRIMARY KEY,
	title        TEXT NOT NULL,
	description  TEXT NOT NULL,
	status       TEXT NOT NULL,
	created_at   INTEGER NOT NULL,
	updated_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS workflows (
	id                 TEXT PRIMARY KEY,
	name               TEXT NOT NULL,
	version            INTEGER NOT NULL,
	definition_digest  TEXT NOT NULL,
	created_at         INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS workflow_runs (
	id            TEXT PRIMARY KEY,
	initiative_id TEXT NOT NULL,
	workflow_id   TEXT NOT NULL,
	status        TEXT NOT NULL,
	created_at    INTEGER NOT NULL,
	finished_at   INTEGER
);

CREATE TABLE IF NOT EXISTS stage_runs (
	id              TEXT PRIMARY KEY,
	workflow_run_id TEXT NOT NULL,
	stage_key       TEXT NOT NULL,
	status          TEXT NOT NULL,
	attempt         INTEGER NOT NULL,
	started_at      INTEGER,
	finished_at     INTEGER
);

CREATE TABLE IF NOT EXISTS runs (
	id           TEXT PRIMARY KEY,
	kind         TEXT NOT NULL,
	stage_run_id TEXT,
	status       TEXT NOT NULL,
	started_at   INTEGER,
	finished_at  INTEGER
);

CREATE TABLE IF NOT EXISTS operations (
	id            TEXT PRIMARY KEY,
	project_id    TEXT NOT NULL,
	initiative_id TEXT,
	type          TEXT NOT NULL,
	status        TEXT NOT NULL,
	result_ref    TEXT,
	created_at    INTEGER NOT NULL,
	finished_at   INTEGER
);

CREATE TABLE IF NOT EXISTS events (
	seq            INTEGER PRIMARY KEY AUTOINCREMENT,
	id             TEXT NOT NULL UNIQUE,
	aggregate_type TEXT NOT NULL,
	aggregate_id   TEXT NOT NULL,
	actor          TEXT NOT NULL,
	correlation_id TEXT NOT NULL,
	causation_id   TEXT NOT NULL,
	operation      TEXT NOT NULL,
	event_type     TEXT NOT NULL,
	payload_digest TEXT NOT NULL,
	created_at     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS assertions (
	id                TEXT PRIMARY KEY,
	initiative_id     TEXT NOT NULL,
	type              TEXT NOT NULL,
	status            TEXT NOT NULL,
	statement         TEXT NOT NULL,
	evidence_digest   TEXT NOT NULL,
	confidence        TEXT NOT NULL,
	source            TEXT NOT NULL,
	provenance_json   TEXT NOT NULL,
	produced_by_run_id TEXT NOT NULL,
	superseded_by     TEXT NOT NULL,
	created_at        INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS requirements (
	id                     TEXT PRIMARY KEY,
	initiative_id          TEXT NOT NULL,
	code                   TEXT NOT NULL,
	statement              TEXT NOT NULL,
	acceptance_criteria_json TEXT NOT NULL,
	priority               TEXT NOT NULL,
	status                 TEXT NOT NULL,
	source_assertion_id    TEXT NOT NULL,
	provenance_json        TEXT NOT NULL,
	produced_by_run_id     TEXT NOT NULL,
	superseded_by          TEXT NOT NULL,
	created_at             INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS decisions (
	id               TEXT PRIMARY KEY,
	initiative_id    TEXT NOT NULL,
	code             TEXT NOT NULL,
	type             TEXT NOT NULL,
	question         TEXT NOT NULL,
	options_json     TEXT NOT NULL,
	recommendation   TEXT NOT NULL,
	status           TEXT NOT NULL,
	resolution       TEXT NOT NULL,
	resolved_by      TEXT NOT NULL,
	provenance_json  TEXT NOT NULL,
	superseded_by    TEXT NOT NULL,
	created_at       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS decision_dissents (
	id               TEXT PRIMARY KEY,
	decision_id      TEXT NOT NULL,
	proponent_role   TEXT NOT NULL,
	position         TEXT NOT NULL,
	reasoning_digest TEXT NOT NULL,
	created_at       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS questions (
	id            TEXT PRIMARY KEY,
	initiative_id TEXT NOT NULL,
	question      TEXT NOT NULL,
	status        TEXT NOT NULL,
	answer_digest TEXT NOT NULL,
	answered_by   TEXT NOT NULL,
	created_at    INTEGER NOT NULL,
	resolved_at   INTEGER
);

CREATE TABLE IF NOT EXISTS artifacts (
	id            TEXT PRIMARY KEY,
	initiative_id TEXT NOT NULL,
	kind          TEXT NOT NULL,
	created_at    INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS artifact_versions (
	id               TEXT PRIMARY KEY,
	artifact_id      TEXT NOT NULL,
	version          INTEGER NOT NULL,
	status           TEXT NOT NULL,
	body_digest      TEXT NOT NULL,
	produced_by_run_id TEXT NOT NULL,
	superseded_by    TEXT NOT NULL,
	created_at       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS findings (
	id                TEXT PRIMARY KEY,
	review_run_id     TEXT NOT NULL,
	target_type       TEXT NOT NULL,
	target_id         TEXT NOT NULL,
	severity          TEXT NOT NULL,
	confidence        TEXT NOT NULL,
	category          TEXT NOT NULL,
	finding           TEXT NOT NULL,
	evidence          TEXT NOT NULL,
	impact            TEXT NOT NULL,
	recommendation    TEXT NOT NULL,
	disposition       TEXT,
	disposition_actor TEXT NOT NULL,
	created_at        INTEGER NOT NULL,
	CHECK (disposition IS NULL OR disposition IN ('fixed','accepted_risk','not_applicable','duplicate','deferred','rejected'))
);

CREATE TABLE IF NOT EXISTS run_manifests (
	id                TEXT PRIMARY KEY,
	run_id            TEXT NOT NULL,
	snapshot_digest   TEXT NOT NULL,
	config_digest     TEXT NOT NULL,
	workflow_digest   TEXT NOT NULL,
	role_version      TEXT NOT NULL,
	requested_model   TEXT NOT NULL,
	actual_model      TEXT NOT NULL,
	context_digest    TEXT NOT NULL,
	skill_digests_json TEXT NOT NULL,
	tool_versions_json TEXT NOT NULL,
	usage_json        TEXT NOT NULL,
	result_digest     TEXT NOT NULL,
	started_at        INTEGER NOT NULL,
	finished_at       INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS context_bundles (
	id         TEXT PRIMARY KEY,
	run_id     TEXT NOT NULL,
	digest     TEXT NOT NULL,
	created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS context_entries (
	bundle_id        TEXT NOT NULL,
	priority         INTEGER NOT NULL,
	source           TEXT NOT NULL,
	digest           TEXT NOT NULL,
	trust            TEXT NOT NULL,
	inclusion_reason TEXT NOT NULL,
	selector         TEXT NOT NULL,
	transformation   TEXT NOT NULL,
	size             INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS read_cursors (
	scope           TEXT PRIMARY KEY,
	last_event_seq  INTEGER NOT NULL,
	updated_at      INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS blobs (
	digest     TEXT PRIMARY KEY,
	size       INTEGER NOT NULL,
	created_at INTEGER NOT NULL
);
`,
	// 2: denormalize initiative_id onto findings for initiative-scoped listing.
	`
ALTER TABLE findings ADD COLUMN initiative_id TEXT NOT NULL DEFAULT '';
`,
	// 3: persist the workflow failure reason for surfacing in the UI.
	`
ALTER TABLE initiatives ADD COLUMN failure_reason TEXT NOT NULL DEFAULT '';
`,
	// 4: allow a human free-text note ("discuss") on a review finding.
	`
ALTER TABLE findings ADD COLUMN note TEXT NOT NULL DEFAULT '';
`,
	// 5: decision discussion thread (human↔AI) for elaborating recommendations
	// and proposing alternatives.
	`
CREATE TABLE IF NOT EXISTS decision_discussions (
	id          TEXT PRIMARY KEY,
	decision_id TEXT NOT NULL,
	author      TEXT NOT NULL,
	message     TEXT NOT NULL,
	created_at  INTEGER NOT NULL
);
`,
	// 6: widen the findings disposition CHECK to include 'mitigate' (human asked
	// to fix/mitigate the finding and incorporate it into the PRD). SQLite can't
	// alter a CHECK in place, so rebuild the table.
	`
CREATE TABLE findings_new (
	id                TEXT PRIMARY KEY,
	initiative_id     TEXT NOT NULL DEFAULT '',
	review_run_id     TEXT NOT NULL,
	target_type       TEXT NOT NULL,
	target_id         TEXT NOT NULL,
	severity          TEXT NOT NULL,
	confidence        TEXT NOT NULL,
	category          TEXT NOT NULL,
	finding           TEXT NOT NULL,
	evidence          TEXT NOT NULL,
	impact            TEXT NOT NULL,
	recommendation    TEXT NOT NULL,
	disposition       TEXT,
	disposition_actor TEXT NOT NULL,
	note              TEXT NOT NULL DEFAULT '',
	created_at        INTEGER NOT NULL,
	CHECK (disposition IS NULL OR disposition IN ('fixed','accepted_risk','not_applicable','duplicate','deferred','rejected','mitigate'))
);
INSERT INTO findings_new (id, initiative_id, review_run_id, target_type, target_id, severity, confidence, category, finding, evidence, impact, recommendation, disposition, disposition_actor, note, created_at)
SELECT id, initiative_id, review_run_id, target_type, target_id, severity, confidence, category, finding, evidence, impact, recommendation, disposition, disposition_actor, note, created_at FROM findings;
DROP TABLE findings;
ALTER TABLE findings_new RENAME TO findings;
`,
	// 7: Product Graph edges + execution-plan tasks (M2).
	`
CREATE TABLE IF NOT EXISTS graph_edges (
	id            TEXT PRIMARY KEY,
	initiative_id TEXT NOT NULL,
	from_type     TEXT NOT NULL,
	from_id       TEXT NOT NULL,
	edge_type     TEXT NOT NULL,
	to_type       TEXT NOT NULL,
	to_id         TEXT NOT NULL,
	metadata_json TEXT NOT NULL DEFAULT '{}',
	created_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_graph_edges_from ON graph_edges(initiative_id, from_type, from_id);
CREATE INDEX IF NOT EXISTS idx_graph_edges_to ON graph_edges(initiative_id, to_type, to_id);

CREATE TABLE IF NOT EXISTS tasks (
	id              TEXT PRIMARY KEY,
	initiative_id   TEXT NOT NULL,
	code            TEXT NOT NULL,
	title           TEXT NOT NULL,
	statement       TEXT NOT NULL,
	status          TEXT NOT NULL,
	priority        TEXT NOT NULL,
	estimate        TEXT NOT NULL DEFAULT '',
	produced_by_run TEXT NOT NULL DEFAULT '',
	superseded_by   TEXT NOT NULL DEFAULT '',
	created_at      INTEGER NOT NULL
);

ALTER TABLE decisions ADD COLUMN converged_by TEXT NOT NULL DEFAULT '';
`,
	// 8: AI-suggested answers to open questions (async, persisted).
	`
CREATE TABLE IF NOT EXISTS suggested_answers (
	id            TEXT PRIMARY KEY,
	initiative_id TEXT NOT NULL,
	question_id   TEXT NOT NULL,
	question      TEXT NOT NULL,
	answer        TEXT NOT NULL,
	created_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_suggested_answers ON suggested_answers(initiative_id);
`,
	// 9: ChangeSets + apply journal + executors + risk (M3).
	`
CREATE TABLE IF NOT EXISTS change_sets (
	id                TEXT PRIMARY KEY,
	human_id          TEXT NOT NULL,
	initiative_id     TEXT NOT NULL,
	task_id           TEXT NOT NULL,
	base_snapshot_id  TEXT NOT NULL DEFAULT '',
	status            TEXT NOT NULL,
	patch_digest      TEXT NOT NULL,
	review_id         TEXT NOT NULL DEFAULT '',
	git_commit_sha    TEXT NOT NULL DEFAULT '',
	created_by_run_id TEXT NOT NULL DEFAULT '',
	created_at        INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_change_sets_initiative ON change_sets(initiative_id);

CREATE TABLE IF NOT EXISTS apply_journal (
	id                        TEXT PRIMARY KEY,
	change_set_id             TEXT NOT NULL,
	state                     TEXT NOT NULL,
	workspace_snapshot_digest TEXT NOT NULL DEFAULT '',
	created_at                INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_apply_journal_change_set ON apply_journal(change_set_id);

CREATE TABLE IF NOT EXISTS execution_attempts (
	id             TEXT PRIMARY KEY,
	change_set_id  TEXT NOT NULL DEFAULT '',
	executor       TEXT NOT NULL,
	status         TEXT NOT NULL,
	progress_json  TEXT NOT NULL DEFAULT '{}',
	result_digest  TEXT NOT NULL DEFAULT '',
	created_at     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS risk_profiles (
	id            TEXT PRIMARY KEY,
	initiative_id TEXT NOT NULL,
	tier          TEXT NOT NULL,
	signals_json  TEXT NOT NULL DEFAULT '[]',
	reason_json   TEXT NOT NULL DEFAULT '[]',
	created_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_risk_profiles_initiative ON risk_profiles(initiative_id);

CREATE TABLE IF NOT EXISTS snapshots (
	id            TEXT PRIMARY KEY,
	initiative_id TEXT NOT NULL,
	kind          TEXT NOT NULL,
	digest        TEXT NOT NULL,
	created_at    INTEGER NOT NULL
);
`,
	// 10: applied_at on the apply journal + unique ChangeSet HumanID per initiative.
	`
ALTER TABLE apply_journal ADD COLUMN applied_at INTEGER;
CREATE UNIQUE INDEX IF NOT EXISTS idx_change_sets_human_id ON change_sets(initiative_id, human_id);
`,
	// 11: verification evidence + requirement verified_at (§70, review B4/B10).
	`
ALTER TABLE requirements ADD COLUMN verified_at INTEGER;
CREATE TABLE IF NOT EXISTS verification_evidence (
	id              TEXT PRIMARY KEY,
	initiative_id   TEXT NOT NULL,
	requirement_id  TEXT NOT NULL,
	criterion_key   TEXT NOT NULL,
	kind            TEXT NOT NULL,
	status          TEXT NOT NULL,
	evidence_digest TEXT NOT NULL DEFAULT '',
	run_id          TEXT NOT NULL DEFAULT '',
	verified_by     TEXT NOT NULL DEFAULT '',
	note            TEXT NOT NULL DEFAULT '',
	created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_verification_evidence_req ON verification_evidence(requirement_id);
`,
	// 12: findings.code carries the machine-readable system-deny code (security
	// model), distinct from the free-text category.
	`
ALTER TABLE findings ADD COLUMN code TEXT NOT NULL DEFAULT '';
`,
	// 13: context documents — initiative-attached source material (PRD, design,
	// tech, market, etc.) referenced by content-addressed digest.
	`
CREATE TABLE IF NOT EXISTS context_docs (
	id            TEXT PRIMARY KEY,
	initiative_id TEXT NOT NULL,
	kind          TEXT NOT NULL,
	title         TEXT NOT NULL,
	digest        TEXT NOT NULL,
	preview       TEXT NOT NULL DEFAULT '',
	created_at    INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_context_docs_initiative ON context_docs(initiative_id);
`,
	// 14: context-doc format (text | image) + the digest of the vision-generated
	// text description for image docs (mockups/wireframes are described once and
	// the description feeds the text pipeline).
	`
ALTER TABLE context_docs ADD COLUMN format TEXT NOT NULL DEFAULT 'text';
ALTER TABLE context_docs ADD COLUMN description_digest TEXT NOT NULL DEFAULT '';
`,
	// 15: tasks.position — a stable ordinal so the execution plan can be
	// reordered (human Plan editing). Backfill preserves the existing
	// created_at ordering per initiative; new tasks get max+1.
	`
ALTER TABLE tasks ADD COLUMN position INTEGER NOT NULL DEFAULT 0;
UPDATE tasks SET position = (
  SELECT COUNT(*) FROM tasks t2
  WHERE t2.initiative_id = tasks.initiative_id
    AND (t2.created_at < tasks.created_at OR (t2.created_at = tasks.created_at AND t2.id < tasks.id))
);
`,
	// 16: task_counters — a monotonic per-initiative TASK code counter so codes
	// are never reused after a task is deleted (tasks are the one deletable
	// code-bearing entity). Seeded from the highest existing code.
	`
CREATE TABLE IF NOT EXISTS task_counters (
  initiative_id TEXT PRIMARY KEY,
  next_seq INTEGER NOT NULL
);
INSERT INTO task_counters (initiative_id, next_seq)
SELECT initiative_id, COALESCE(MAX(CAST(SUBSTR(code, 6) AS INTEGER)), 0) + 1
FROM tasks GROUP BY initiative_id;
`,
	// 17: doc_comments — human comments anchored to an artifact-version section,
	// promotable into an open Question.
	`
CREATE TABLE IF NOT EXISTS doc_comments (
  id                   TEXT PRIMARY KEY,
  initiative_id        TEXT NOT NULL,
  artifact_version_id  TEXT NOT NULL,
  section              TEXT NOT NULL DEFAULT '',
  text                 TEXT NOT NULL,
  author               TEXT NOT NULL,
  promoted_question_id TEXT NOT NULL DEFAULT '',
  created_at           INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_doc_comments_artifact ON doc_comments(artifact_version_id);
`,
	// 18: record WHY an initiative needs human judgment. Six code paths set
	// `needs_human_judgment` and the status alone cannot tell them apart, so
	// every client guessed -- the web UI from artifact presence, which is
	// accurate enough to light a phase pill and not accurate enough to name a
	// command.
	`
ALTER TABLE initiatives ADD COLUMN judgment_reason TEXT NOT NULL DEFAULT '';
`,
	// 19: record a failed operation's own reason (B2). Before this, a failed
	// execute left the initiative at plan_approved, the Build page's running
	// card vanished with no replacement, and the only record of why was a line
	// in daemon.log -- invisible to anyone not tailing it.
	`
ALTER TABLE operations ADD COLUMN error TEXT NOT NULL DEFAULT '';
`,
	// 20: record the actual model that produced a change set, so the UI can
	// label demo/placeholder output distinctly from a real agent's without a
	// second round trip to the run manifest (whose id space does not even
	// match change_sets.created_by_run_id -- that column holds the executor
	// attempt id, not the domain Run id CreateRunManifest keys on).
	`
ALTER TABLE change_sets ADD COLUMN model TEXT NOT NULL DEFAULT '';
`,
	// 21: record which executor produced a change set. The internal executor
	// writes a placeholder whatever the model is, so "was this real output"
	// is a property of the executor; the UI read the currently selected one,
	// and switching executors relabelled every earlier change set.
	`
ALTER TABLE change_sets ADD COLUMN executor TEXT NOT NULL DEFAULT '';
`,
	// 22: keep the coding agent's final output with the change set it
	// produced. The running card showed it live and disappeared with it when
	// the run finished, so nothing the agent printed could be read afterwards.
	`
ALTER TABLE change_sets ADD COLUMN agent_output TEXT NOT NULL DEFAULT '';
`,
	// 23: secret_sweep findings carry WHERE the credential was found (secret_
	// path/secret_kind) and a fingerprint of the value (never the value) so a
	// human's "not a secret" disposition can allowlist it (review changes #1,
	// ICP round). The disposition CHECK is widened to admit not_a_secret --
	// SQLite cannot ALTER a CHECK in place, so the table is rebuilt again, as
	// migration 6 did for `mitigate`.
	`
CREATE TABLE findings_new (
	id                TEXT PRIMARY KEY,
	initiative_id     TEXT NOT NULL DEFAULT '',
	review_run_id     TEXT NOT NULL,
	target_type       TEXT NOT NULL,
	target_id         TEXT NOT NULL,
	severity          TEXT NOT NULL,
	confidence        TEXT NOT NULL,
	category          TEXT NOT NULL,
	code              TEXT NOT NULL DEFAULT '',
	finding           TEXT NOT NULL,
	evidence          TEXT NOT NULL,
	impact            TEXT NOT NULL,
	recommendation    TEXT NOT NULL,
	disposition       TEXT,
	disposition_actor TEXT NOT NULL,
	note              TEXT NOT NULL DEFAULT '',
	secret_fingerprint TEXT NOT NULL DEFAULT '',
	secret_kind        TEXT NOT NULL DEFAULT '',
	secret_path        TEXT NOT NULL DEFAULT '',
	created_at        INTEGER NOT NULL,
	CHECK (disposition IS NULL OR disposition IN ('fixed','accepted_risk','not_applicable','duplicate','deferred','rejected','mitigate','not_a_secret'))
);
INSERT INTO findings_new (id, initiative_id, review_run_id, target_type, target_id, severity, confidence, category, code, finding, evidence, impact, recommendation, disposition, disposition_actor, note, created_at)
SELECT id, initiative_id, review_run_id, target_type, target_id, severity, confidence, category, code, finding, evidence, impact, recommendation, disposition, disposition_actor, note, created_at FROM findings;
DROP TABLE findings;
ALTER TABLE findings_new RENAME TO findings;
`,
	// 24: the project-scoped "not a secret" allowlist (review changes #1). One
	// row per human disposition, keyed by the fingerprint the sweep re-checks;
	// path/kind are kept as they were AT THE TIME, for the Settings list, not
	// as a live join back to the finding (which may since have been deleted
	// with its change set's initiative).
	`
CREATE TABLE IF NOT EXISTS secret_allowlist (
	id          TEXT PRIMARY KEY,
	fingerprint TEXT NOT NULL,
	kind        TEXT NOT NULL,
	path        TEXT NOT NULL,
	actor       TEXT NOT NULL,
	created_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_secret_allowlist_fingerprint ON secret_allowlist(fingerprint);
`,
	// 25: stale_marks -- the staleness primitive (review A4 #5). A decided
	// decision reopened (or re-resolved differently) after tasks/change sets
	// were built on it marks the requirements that cite it, their tasks, and
	// the applied change sets for those tasks, stale. `cleared_at IS NULL`
	// selects the active marks; a mark clears via re-planning or an explicit
	// human acknowledgement, never by deletion, so the record survives.
	`
CREATE TABLE IF NOT EXISTS stale_marks (
	id            TEXT PRIMARY KEY,
	initiative_id TEXT NOT NULL,
	target_type   TEXT NOT NULL,
	target_id     TEXT NOT NULL,
	reason        TEXT NOT NULL,
	decision_id   TEXT NOT NULL DEFAULT '',
	created_at    INTEGER NOT NULL,
	cleared_at    INTEGER,
	cleared_by    TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_stale_marks_initiative ON stale_marks(initiative_id, cleared_at);
CREATE INDEX IF NOT EXISTS idx_stale_marks_target ON stale_marks(initiative_id, target_type, target_id, cleared_at);
`,
	// 26: record why an applied change set was NOT committed even though
	// git_commit is on (detached HEAD, a rebase/merge/cherry-pick in progress,
	// or the repository's default branch without git.allow_default_branch --
	// review C4, adopted #2). A guard refusal must never fail the apply itself,
	// so the reason is recorded here instead, alongside git_commit_sha.
	`
ALTER TABLE change_sets ADD COLUMN git_commit_warning TEXT NOT NULL DEFAULT '';
`,
	// 27: the dedicated branch git.branch_per_initiative creates on an
	// initiative's first apply (review C4 piece 2), so every later change set
	// commits to the same branch rather than computing a fresh name each time.
	`
ALTER TABLE initiatives ADD COLUMN git_branch TEXT NOT NULL DEFAULT '';
`,
	// 28: the pull request `idea pr` opened for the initiative's branch
	// (review C4 piece 3), so a repeat `idea pr` is idempotent instead of
	// opening a second PR for the same branch.
	`
ALTER TABLE initiatives ADD COLUMN pr_url TEXT NOT NULL DEFAULT '';
`,
	// 29: change-set review findings carry an optional `path` and a reviewer
	// self-reported `in_diff` (review B2, adopted #5), and any finding can be
	// marked `advisory` -- non-blocking regardless of severity (review B1's
	// rigor cap note, and B2's "about code this change set did not touch").
	`
ALTER TABLE findings ADD COLUMN path TEXT NOT NULL DEFAULT '';
ALTER TABLE findings ADD COLUMN in_diff INTEGER NOT NULL DEFAULT 0;
ALTER TABLE findings ADD COLUMN advisory INTEGER NOT NULL DEFAULT 0;
`,
	// 30: secret-allowlist safety (review H3/M1).
	//
	//   - project_state is a small per-project key/value table (state.db's
	//     counterpart to global.db's daemon_meta) for the per-project HMAC key
	//     Fingerprint now uses instead of a bare sha256, so a secret's
	//     fingerprint is not the same, precomputable value across every
	//     installation that ever saw it.
	//   - findings.secret_overridable records whether a secret_sweep finding
	//     is even eligible for the not_a_secret override (false for a match
	//     drawn from the project's own configured secrets); defaults to 1
	//     (true) so a pre-existing pattern-match finding is not retroactively
	//     treated as a configured-secret match.
	//   - findings.secret_preview carries the masked preview
	//     (SecretPreview) of what a not_a_secret disposition would allowlist.
	//   - secret_allowlist gets a unique index on (fingerprint, path) instead
	//     of fingerprint alone (a value allowlisted in one file must still be
	//     denied in another), with any pre-existing duplicate rows for the
	//     same pair collapsed to one before the index is created.
	`
CREATE TABLE IF NOT EXISTS project_state (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
ALTER TABLE findings ADD COLUMN secret_overridable INTEGER NOT NULL DEFAULT 1;
ALTER TABLE findings ADD COLUMN secret_preview TEXT NOT NULL DEFAULT '';
DELETE FROM secret_allowlist
	WHERE rowid NOT IN (SELECT MIN(rowid) FROM secret_allowlist GROUP BY fingerprint, path);
DROP INDEX IF EXISTS idx_secret_allowlist_fingerprint;
CREATE UNIQUE INDEX IF NOT EXISTS idx_secret_allowlist_fingerprint_path ON secret_allowlist(fingerprint, path);
`,
	// 31: wave 3 (surfaces) -- C1 short titles, C3 cost/stage attribution
	// (ICP round).
	//
	//   - initiatives.short_title: a deterministic, generated short label
	//     (domain.GenerateShortTitle), backfilled lazily by GetInitiative/
	//     ListInitiatives for rows written before this column existed.
	//   - runs.initiative_id / runs.stage: attribute a model or agent run to
	//     the initiative and workflow stage that produced it, so per-initiative
	//     per-phase cost/time (GET .../costs) can be computed by joining
	//     run_manifests back to runs -- previously runs carried neither, and a
	//     run could not be attributed to an initiative at all.
	//   - operations.stage: the engine's current step ("Writing the PRD",
	//     "Agent running TASK-2", ...), set by a callback as the engine moves
	//     between steps, so the CLI's wait loop can print it instead of a bare
	//     "still working...".
	`
ALTER TABLE initiatives ADD COLUMN short_title TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN initiative_id TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN stage TEXT NOT NULL DEFAULT '';
ALTER TABLE runs ADD COLUMN note TEXT NOT NULL DEFAULT '';
ALTER TABLE operations ADD COLUMN stage TEXT NOT NULL DEFAULT '';
`,
	// 32: review low-priority item, migration 30's follow-up. Migration 30
	// switched Fingerprint from a bare sha256 to a per-project HMAC, and left
	// every pre-existing configured-secret finding's secret_overridable at
	// its column default (1/true). That default is harmless in practice --
	// a configured-secret disposition is refused server-side regardless of
	// this flag (see the store's not_a_secret guard), and the old
	// fingerprint no longer matches anything post-upgrade anyway -- but it
	// mis-describes the finding, and this sets the record straight rather
	// than leaving it to that other guard alone.
	//
	// This does NOT (and cannot) recompute HMAC fingerprints for old
	// sha256-fingerprinted secret_allowlist rows -- there is no key to
	// invert a hash with, and the whole point of the per-project HMAC is
	// that it cannot be recomputed from the old value without the project's
	// key. Any "not a secret" exemption granted before this project's HMAC
	// key existed is silently orphaned by migration 30 (its stored
	// fingerprint never matches a newly-computed one again) and must be
	// re-granted; see CHANGELOG.md.
	`
UPDATE findings SET secret_overridable = 0 WHERE secret_kind = 'configured secret value';
`,
	// 33: ICP re-run defect #1. review_completed_at records the one time the
	// adversarial reviewer actually FINISHED for a change set, as opposed to
	// the change set merely reaching review_required. Before this, a change
	// set whose Execute-time review call errored (the operation was
	// cancelled mid-review, the provider timed out, ...) after the
	// checks-running -> review_required transition already happened was
	// indistinguishable, in the stored state, from one that had genuinely
	// converged with zero adversarial findings: `idea review CHG-n approve`
	// happily approved it, and it was then appliable, with only mechanical
	// check observations (build/tests/patch-applies/workspace-copy) ever
	// having been recorded against it. NULL (unset) for every change set
	// created before this migration and for one whose review has not yet
	// completed; set once, in store.SetChangeSetReview, the only place a
	// completed review is ever recorded.
	`
ALTER TABLE change_sets ADD COLUMN review_completed_at INTEGER;
`,
	// 34: ICP re-run defect #2. no_op marks a change set that touched zero
	// files -- fsdiff.Diff found nothing between the before/after snapshots,
	// because the agent decided the task was already done or its model call
	// failed and it wrote nothing. Such a change set can still be reviewed,
	// approved, applied and reverted like any other (the existing "approve
	// the empty change set if the task was already done" escape hatch is
	// unchanged) -- but an APPLIED no_op change set must never be counted as
	// "this task is implemented": a real run produced exactly this (an empty
	// change set for one task, counted as implementing it), and reverting
	// the task's real, later change set left the task `done` and guidance
	// insisting "every task has an applied change set" while the actual
	// feature was gone from disk. 0 (false) for every change set created
	// before this migration, which is the correct, conservative default:
	// there is no way to recompute file counts for old rows without
	// reloading and diffing every historical patch, and treating them all as
	// "not no-op" preserves their existing, already-relied-upon behavior.
	`
ALTER TABLE change_sets ADD COLUMN no_op INTEGER NOT NULL DEFAULT 0;
`,
	// 35: review H5. Migration 31 added runs.initiative_id/stage so a model
	// or agent run could be attributed to the initiative/phase that produced
	// it, but left every PRE-EXISTING run row's initiative_id at the column
	// default ('') -- nothing backfilled it. ListModelPhaseCosts and
	// ListAgentRunStats (internal/projection/costs.go) both filter
	// `WHERE r.initiative_id = ?`, so an initiative that existed before this
	// upgrade permanently reports $0.00 / 0 calls from GET .../costs even
	// though the spend still sits in run_manifests -- a wrong number, not a
	// missing one, on money already burned.
	//
	// The initiative a run belongs to IS recoverable: every run this
	// happened to (one created before migration 31 shipped) is still wired
	// into the product graph by a produced_by edge from whatever it produced
	// (a requirement, an assertion, an artifact version, ...) back to the
	// run, and that edge's own initiative_id column names the initiative.
	// One run can in principle be produced_by-linked from more than one
	// aggregate (or, defensively, from more than one initiative, which
	// should never happen but costs nothing to guard); MIN(...) picks
	// deterministically rather than leaving the join's row order to define
	// the result. A run with no such edge at all (never wired, or the edge
	// itself predates the product graph) is left at '' exactly as before --
	// there is nothing to recover it from.
	`
UPDATE runs SET initiative_id = (
	SELECT MIN(e.initiative_id) FROM graph_edges e WHERE e.to_type = 'run' AND e.to_id = runs.id
)
WHERE initiative_id = '' AND EXISTS (
	SELECT 1 FROM graph_edges e WHERE e.to_type = 'run' AND e.to_id = runs.id
);
`,
	// 36: the adversarial-review truncation defect. Before this, a change set
	// whose reviewer call genuinely ran but could never complete -- every
	// attempt (including runModel's own reduced-ask retry) cut off at the
	// model's output-token ceiling, model.ErrTruncated -- was recorded
	// identically to one whose review was simply never attempted (the
	// operation cancelled mid-review): both left review_completed_at NULL,
	// and ReviewNeverCompletedCause's only answer was the generic "likely
	// cancelled mid-review". `idea execute` then refused to re-run the task
	// (the never-completed-review guard) AND `idea review <id> approve
	// --accept-findings` refused too (TestApproveAcceptFindingsAlsoRefuses
	// WhenReviewNeverCompleted deliberately pins that for the ordinary
	// "never attempted" case) -- a real diff too large for the model's own
	// ceiling had no way forward that did not mean hand-editing harness.yaml.
	//
	// review_unavailable_reason is set exactly once, by
	// store.SetChangeSetReviewUnavailable, ONLY when the reviewer was
	// genuinely attempted and explicitly could not finish -- never merely
	// guessed. Its presence is what reviewChangeSet's approve path now
	// checks to allow the ONE new, explicit, recorded escape:
	// --accept-findings on a change set whose review never completed BUT
	// carries this reason. A change set that was simply cancelled
	// (review_unavailable_reason still '') keeps refusing exactly as before
	// -- this migration narrows, not removes, the round-4 guarantee that an
	// unreviewed change set cannot be silently approved.
	`
ALTER TABLE change_sets ADD COLUMN review_unavailable_reason TEXT NOT NULL DEFAULT '';
`,
	// 37: executor provenance on the run manifest: which executor kind was
	// requested and which actually ran (an unset kind is "auto" and can fall
	// back to the placeholder executor -- the manifest has to say so), the
	// image, the fallback warning, and an ACP agent's permission requests with
	// the answers the harness gave on the operator's behalf.
	`
ALTER TABLE run_manifests ADD COLUMN executor_json TEXT NOT NULL DEFAULT '';
`,
	// 38: review convergence telemetry. review_json holds one review cycle's
	// domain.ReviewTelemetry on the adversarial reviewer's own run manifest:
	// findings raised/new/re-raised/resolved by severity, what the
	// convergence rules demoted and why, the open blocking count and the
	// outcome (converged, revise, stalled, max_cycles). '' for every manifest
	// that is not a review cycle's, and for every one written before this
	// migration -- there is nothing to backfill it from, since the per-cycle
	// classification it records was never computed before.
	`
ALTER TABLE run_manifests ADD COLUMN review_json TEXT NOT NULL DEFAULT '';
`,
	// 39: integrations (Slack, Linear). Three tables of delivery bookkeeping,
	// none of which is product state -- which is why none of their writes
	// appends to `events` (see integration.go): the integration bus TAILS the
	// event log, and a write that appended an event of its own would feed the
	// bus its own exhaust.
	//
	// integration_cursors is the per-subscriber high-water mark into events.seq,
	// so a restarted daemon resumes where it left off instead of replaying the
	// whole log into Slack. integration_outbox holds one row per (subscriber,
	// domain event); idempotency_key is UNIQUE, so re-ingesting the same event
	// after a crash between "row inserted" and "cursor advanced" is a no-op,
	// and a row already `delivered` is never sent again. integration_links maps
	// a local object (an initiative's Slack thread, a task's Linear issue) to
	// the remote one, so a retry finds the issue it already created rather
	// than creating a second.
	`
CREATE TABLE IF NOT EXISTS integration_cursors (
	subscriber TEXT PRIMARY KEY,
	last_seq   INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS integration_outbox (
	id              TEXT PRIMARY KEY,
	subscriber      TEXT NOT NULL,
	idempotency_key TEXT NOT NULL UNIQUE,
	event_seq       INTEGER NOT NULL,
	kind            TEXT NOT NULL,
	payload_json    TEXT NOT NULL,
	status          TEXT NOT NULL,
	attempts        INTEGER NOT NULL DEFAULT 0,
	next_attempt_at INTEGER NOT NULL,
	last_error      TEXT NOT NULL DEFAULT '',
	created_at      INTEGER NOT NULL,
	delivered_at    INTEGER
);
CREATE INDEX IF NOT EXISTS idx_integration_outbox_due ON integration_outbox(subscriber, status, event_seq);
CREATE TABLE IF NOT EXISTS integration_links (
	integration  TEXT NOT NULL,
	kind         TEXT NOT NULL,
	local_id     TEXT NOT NULL,
	remote_id    TEXT NOT NULL,
	remote_key   TEXT NOT NULL DEFAULT '',
	remote_url   TEXT NOT NULL DEFAULT '',
	remote_state TEXT NOT NULL DEFAULT '',
	note         TEXT NOT NULL DEFAULT '',
	created_at   INTEGER NOT NULL,
	updated_at   INTEGER NOT NULL,
	PRIMARY KEY (integration, kind, local_id)
);
CREATE INDEX IF NOT EXISTS idx_integration_links_remote ON integration_links(integration, kind, remote_id);
`,
	// 40: remote_runs: one row per agent run delegated to a remote execution
	// plane (executor kind openhands -- an OpenHands Agent Server). The run
	// outlives the Execute operation that started it: a daemon restart fails
	// that operation, but the conversation keeps going on the server, and
	// this row is how the next daemon re-attaches instead of starting a
	// second one. idempotency_key is UNIQUE and deterministic per (task,
	// attempt), so a retry after a crash between "row inserted" and
	// "conversation created" finds the row rather than double-dispatching.
	// last_event_id is the event-ingestion cursor; progress is the compact,
	// already-redacted event log the live view reads.
	`
CREATE TABLE IF NOT EXISTS remote_runs (
	id              TEXT PRIMARY KEY,
	executor        TEXT NOT NULL,
	idempotency_key TEXT NOT NULL UNIQUE,
	initiative_id   TEXT NOT NULL,
	task_id         TEXT NOT NULL,
	attempt         INTEGER NOT NULL,
	server_url      TEXT NOT NULL,
	conversation_id TEXT NOT NULL DEFAULT '',
	remote_dir      TEXT NOT NULL DEFAULT '',
	base_digest     TEXT NOT NULL DEFAULT '',
	status          TEXT NOT NULL,
	remote_status   TEXT NOT NULL DEFAULT '',
	last_event_id   TEXT NOT NULL DEFAULT '',
	event_count     INTEGER NOT NULL DEFAULT 0,
	progress        TEXT NOT NULL DEFAULT '',
	error           TEXT NOT NULL DEFAULT '',
	created_at      INTEGER NOT NULL,
	updated_at      INTEGER NOT NULL,
	finished_at     INTEGER
);
CREATE INDEX IF NOT EXISTS idx_remote_runs_task ON remote_runs(task_id, attempt);
CREATE INDEX IF NOT EXISTS idx_remote_runs_status ON remote_runs(status);
`,
	// 41: quality_results: verify's quality dimensions (internal/quality). One row
	// per dimension per measurement, append-only: the latest per dimension is
	// the scorecard and the one before it is the performance baseline.
	// checks_json is the []domain.QualityCheck with citations; metrics_json the
	// named numbers a budget measured; input_digest names the applied change
	// sets and verify policy measured, so a stale result reads as not
	// measured at the gate. Each row's `verifies` edges to requirements are
	// ordinary graph_edges rows (from_type 'quality_result').
	`
CREATE TABLE IF NOT EXISTS quality_results (
	id             TEXT PRIMARY KEY,
	initiative_id  TEXT NOT NULL,
	measurement_id TEXT NOT NULL,
	dimension      TEXT NOT NULL,
	mode           TEXT NOT NULL,
	status         TEXT NOT NULL,
	summary        TEXT NOT NULL DEFAULT '',
	checks_json    TEXT NOT NULL DEFAULT '[]',
	metrics_json   TEXT NOT NULL DEFAULT '{}',
	input_digest   TEXT NOT NULL DEFAULT '',
	created_at     INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_quality_results_dim ON quality_results(initiative_id, dimension, created_at);
`,
	// 42: objectives & outcomes: the product outcome end of the graph. An
	// objective (OBJ-n) is what an initiative exists to move; an outcome
	// metric (MET-n) is how anyone would know, with a direction, an optional
	// baseline and guardrail, a target, a measurement source and a cadence; a
	// metric observation is one measured value with its provenance.
	//
	// The links are graph edges, not columns, like task -> requirement:
	// requirement --serves--> objective is N:M, and a hypothesis assertion
	// --predicts--> metric and metric --attributed_to--> change_set are
	// edges so why/impact/trace reach them with no special case.
	//
	// baseline and guardrail are nullable REALs because "no baseline" is a
	// real state with its own drift rule (the first observation stands in);
	// 0 would be a baseline. change_set_ids_json on an observation is the
	// attribution as it was computed when the value was recorded -- which
	// change sets were applied since the previous observation -- kept even if
	// one is reverted later, because the observation happened under it.
	`
CREATE TABLE IF NOT EXISTS objectives (
	id                 TEXT PRIMARY KEY,
	initiative_id      TEXT NOT NULL,
	code               TEXT NOT NULL,
	statement          TEXT NOT NULL,
	rationale          TEXT NOT NULL DEFAULT '',
	status             TEXT NOT NULL,
	owner              TEXT NOT NULL DEFAULT '',
	produced_by_run_id TEXT NOT NULL DEFAULT '',
	superseded_by      TEXT NOT NULL DEFAULT '',
	created_at         INTEGER NOT NULL,
	updated_at         INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_objectives_code ON objectives(initiative_id, code);
CREATE TABLE IF NOT EXISTS outcome_metrics (
	id                 TEXT PRIMARY KEY,
	initiative_id      TEXT NOT NULL,
	objective_id       TEXT NOT NULL,
	code               TEXT NOT NULL,
	name               TEXT NOT NULL,
	definition         TEXT NOT NULL DEFAULT '',
	unit               TEXT NOT NULL DEFAULT '',
	direction          TEXT NOT NULL,
	baseline           REAL,
	target             REAL NOT NULL,
	guardrail          REAL,
	source_kind        TEXT NOT NULL DEFAULT 'manual',
	source_command     TEXT NOT NULL DEFAULT '',
	source_url         TEXT NOT NULL DEFAULT '',
	source_pointer     TEXT NOT NULL DEFAULT '',
	cadence            TEXT NOT NULL DEFAULT '',
	produced_by_run_id TEXT NOT NULL DEFAULT '',
	superseded_by      TEXT NOT NULL DEFAULT '',
	created_at         INTEGER NOT NULL,
	updated_at         INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_outcome_metrics_code ON outcome_metrics(initiative_id, code);
CREATE INDEX IF NOT EXISTS idx_outcome_metrics_objective ON outcome_metrics(objective_id);
CREATE TABLE IF NOT EXISTS metric_observations (
	id                  TEXT PRIMARY KEY,
	initiative_id       TEXT NOT NULL,
	metric_id           TEXT NOT NULL,
	value               REAL NOT NULL,
	observed_at         INTEGER NOT NULL,
	source              TEXT NOT NULL,
	note                TEXT NOT NULL DEFAULT '',
	recorded_by         TEXT NOT NULL,
	provenance_json     TEXT NOT NULL DEFAULT '[]',
	change_set_ids_json TEXT NOT NULL DEFAULT '[]',
	created_at          INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_metric_observations_metric ON metric_observations(metric_id, observed_at);
`,
	// 43: review_note -- what the human asked for when they requested changes
	// on (or rejected) a change set. "Request changes" used to record only the
	// verdict: the next run of the task got no word of what to change, so the
	// only way to steer it was to remember to type the same thing into a run
	// note. Execute now appends the newest such note to the implementer's
	// brief for the task's next run (see priorReviewNote in execute.go). '' for
	// every change set reviewed without one, and every one written before this.
	`
ALTER TABLE change_sets ADD COLUMN review_note TEXT NOT NULL DEFAULT '';
`,
	// 44: run_events: the normalized, redacted, bounded event stream of a coding
	// agent's run (domain.RunEvent), whichever executor ran it. A table of its
	// own rather than remote_runs.progress: that column is one capped text
	// blob per OpenHands run, with no per-event structure and no cursor a
	// client can resume from, and the hermes/omp/acp/internal kinds have no
	// remote_runs row at all. seq is monotonic per run (the client's resume
	// cursor); id is monotonic across the project, which is what the
	// project-wide SSE stream resumes from. body_json is the event itself.
	//
	// remote_runs.run_id ties an OpenHands run to the agent run whose events
	// it feeds: its watcher outlives the Execute that started it (and the
	// daemon), so it cannot be handed the run id in memory.
	`
CREATE TABLE IF NOT EXISTS run_events (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id        TEXT NOT NULL,
	seq           INTEGER NOT NULL,
	initiative_id TEXT NOT NULL DEFAULT '',
	kind          TEXT NOT NULL,
	at            INTEGER NOT NULL,
	body_json     TEXT NOT NULL,
	UNIQUE (run_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_run_events_initiative ON run_events(initiative_id, id);
ALTER TABLE remote_runs ADD COLUMN run_id TEXT NOT NULL DEFAULT '';
`,
	// 45: held_steering_notes -- a steering note sent to a run that could not
	// take it mid-run (an executor that cannot be steered, or an agent that was
	// already finishing) is held for the task's next run. They lived in the
	// engine's memory, so a daemon restart between the note and the next run
	// dropped them silently, though the UI had said "will be added to this
	// task's next run". consumed_at is set, in the same statement that reads
	// them, by the Execute whose brief carries them: each note rides exactly
	// one run. note is stored redacted.
	`
CREATE TABLE IF NOT EXISTS held_steering_notes (
	id               INTEGER PRIMARY KEY AUTOINCREMENT,
	initiative_id    TEXT NOT NULL,
	task_id          TEXT NOT NULL DEFAULT '',
	run_id           TEXT NOT NULL DEFAULT '',
	note             TEXT NOT NULL,
	created_at       INTEGER NOT NULL,
	consumed_at      INTEGER,
	consumed_task_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_held_steering_notes_open ON held_steering_notes(initiative_id, consumed_at);
`,
}

// migrate applies any pending migrations transactionally, recording each in
// schema_migrations.
//
// review MEDIUM: this exists-check used to run as a standalone SELECT
// outside any transaction, and only THEN would applyMigration open its own
// transaction to run the DDL and record the row. Two connections opening the
// same un-migrated database for the first time concurrently (a real
// scenario store.Open on its own does nothing to prevent -- EnsureProject's
// single-flight is a daemon-layer guard, not one this package can rely on
// every caller to provide) both read `exists=0` for version 1 before either
// had committed, so both proceeded to applyMigration: whichever's write
// transaction commits second then re-runs DDL SQLite had already applied
// (e.g. `ALTER TABLE … ADD COLUMN` a second time) and fails outright.
// Reproduced failing roughly 3 of 4 times under real concurrent opens.
//
// The fix is in applyMigration below: it no longer trusts a check made
// outside its own transaction. This loop's exists check stays as a cheap
// pre-filter (skipping an already-applied migration's transaction entirely
// in the common, uncontended case) but is no longer load-bearing for
// correctness.
func (db *DB) migrate(list []string) error {
	if _, err := db.sql.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("store: migrate bootstrap: %w", err)
	}

	for i, m := range list {
		version := i + 1
		var exists int
		err := db.sql.QueryRow(`SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, version).Scan(&exists)
		if err != nil {
			return fmt.Errorf("store: migrate check %d: %w", version, err)
		}
		if exists > 0 {
			continue
		}
		if err := db.applyMigration(version, m); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration records version as applied FIRST, inside the transaction,
// before running its DDL -- not after (review MEDIUM: concurrent first-open
// race). schema_migrations.version is its PRIMARY KEY, so that INSERT is
// itself the atomic claim: SQLite's own file locking serializes two
// connections' concurrent write transactions (the busy_timeout already
// configured on Open makes the second simply wait rather than error), and
// once the second's INSERT finally runs, it either succeeds (this
// connection won the race, or nobody has applied this version yet) or hits
// a primary-key constraint violation (the OTHER connection already
// committed this version while this one was waiting) -- reported here as
// "already applied", not as a failure, and the DDL below is correctly
// skipped rather than run a second time over a schema SQLite already
// changed.
func (db *DB) applyMigration(version int, sqlText string) error {
	tx, err := db.sql.Begin()
	if err != nil {
		return fmt.Errorf("store: migrate %d begin: %w", version, err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, nowMS()); err != nil {
		tx.Rollback()
		if isUniqueOrPKViolation(err) {
			// A concurrent first-open already applied this version while we
			// were waiting for the write lock. Nothing to do.
			return nil
		}
		return fmt.Errorf("store: migrate %d record: %w", version, err)
	}
	if _, err := tx.Exec(sqlText); err != nil {
		tx.Rollback()
		return fmt.Errorf("store: migrate %d: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: migrate %d commit: %w", version, err)
	}
	return nil
}

// isUniqueOrPKViolation reports whether err is a SQLite UNIQUE or PRIMARY KEY
// constraint violation (any SQLITE_CONSTRAINT_* extended code masks down to
// the base SQLITE_CONSTRAINT primary result code), as opposed to some other
// failure (disk full, corruption, a genuinely malformed statement) that must
// still surface as a real error.
func isUniqueOrPKViolation(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	return sqliteErr.Code()&0xff == sqlite3.SQLITE_CONSTRAINT
}

var _ = sql.ErrNoRows // keep database/sql import stable across edits
