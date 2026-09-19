package store

import (
	"database/sql"
	"fmt"
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
}

// migrate applies any pending migrations transactionally, recording each in
// schema_migrations.
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

func (db *DB) applyMigration(version int, sqlText string) error {
	tx, err := db.sql.Begin()
	if err != nil {
		return fmt.Errorf("store: migrate %d begin: %w", version, err)
	}
	if _, err := tx.Exec(sqlText); err != nil {
		tx.Rollback()
		return fmt.Errorf("store: migrate %d: %w", version, err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, version, nowMS()); err != nil {
		tx.Rollback()
		return fmt.Errorf("store: migrate %d record: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: migrate %d commit: %w", version, err)
	}
	return nil
}

var _ = sql.ErrNoRows // keep database/sql import stable across edits
