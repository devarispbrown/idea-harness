import { useCallback, useEffect, useState } from "react";
import {
  createMetric,
  createObjective,
  get,
  linkObjectiveRequirement,
  listMetrics,
  listObjectives,
  recordObservation,
  resource,
  updateMetric,
  updateObjective,
  type MetricDirection,
  type Objective,
  type ObjectiveStatus,
  type OutcomeMetric,
} from "./api";
import {
  driftClass,
  driftLabel,
  linkableRequirements,
  metricDraft,
  metricEditBody,
  metricRange,
  metricsByObjective,
  objectiveDraft,
  objectiveEditBody,
  parseNumber,
  requirementCodes,
  requirementsWithoutObjective,
  type MetricDraft,
  type ObjectiveDraft,
  type RequirementLike,
} from "./outcomes";
import LineagePanel from "./LineagePanel";
import InlinePanel from "./InlinePanel";
import { lineageHref } from "./lineage";
import { announce } from "./announce";

interface Requirement extends RequirementLike {
  statement: string;
}

const emptyMetric = () => ({ name: "", direction: "increase" as MetricDirection, target: "", baseline: "", unit: "" });

const OBJECTIVE_STATUSES: ObjectiveStatus[] = ["active", "achieved", "abandoned"];

// ObjectivesSection is Define's view of the outcome chain: each objective
// (OBJ-n), the requirements serving it, and the metrics (MET-n) measuring
// it, with the human edits the daemon accepts -- add, edit an objective
// (statement, rationale, owner, status), link and unlink a requirement, add
// and edit a metric, record a value. The same edits as `idea objective
// add|edit|link [--unlink]` and `idea metric add|edit|record`.
export default function ObjectivesSection({
  projectId,
  initiativeId,
}: {
  projectId: string;
  initiativeId: string;
}) {
  const [objectives, setObjectives] = useState<Objective[]>([]);
  const [metrics, setMetrics] = useState<OutcomeMetric[]>([]);
  const [requirements, setRequirements] = useState<Requirement[]>([]);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [newObjective, setNewObjective] = useState({ statement: "", rationale: "" });
  // Per-objective / per-metric draft inputs, keyed by id.
  const [linkPick, setLinkPick] = useState<Record<string, string>>({});
  const [metricFormFor, setMetricFormFor] = useState<string | null>(null);
  const [metricDraftNew, setMetricDraftNew] = useState(emptyMetric());
  const [values, setValues] = useState<Record<string, string>>({});
  // At most one edit form open at a time: the objective or metric being
  // edited, with its draft.
  const [editingObjective, setEditingObjective] = useState<{ id: string; draft: ObjectiveDraft } | null>(null);
  const [editingMetric, setEditingMetric] = useState<{ id: string; draft: MetricDraft } | null>(null);

  const load = useCallback(async () => {
    const [o, m, r] = await Promise.all([
      listObjectives(projectId, initiativeId).catch(() => []),
      listMetrics(projectId, initiativeId).catch(() => []),
      get<Requirement[]>(resource(projectId, initiativeId, "requirements")).catch(() => []),
    ]);
    setObjectives(o ?? []);
    setMetrics(m ?? []);
    setRequirements(r ?? []);
  }, [projectId, initiativeId]);

  useEffect(() => {
    load();
    const id = setInterval(load, 5000);
    return () => clearInterval(id);
  }, [load]);

  // run wraps a mutation: one in flight at a time, the error shown inline,
  // and a reload so the section reflects what the daemon now holds. done is
  // said in the app's live region when it lands.
  const run = async (fn: () => Promise<unknown>, done?: string): Promise<boolean> => {
    setError("");
    setBusy(true);
    try {
      await fn();
      await load();
      if (done) announce(done);
      return true;
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e);
      setError(msg);
      announce(msg);
      return false;
    } finally {
      setBusy(false);
    }
  };

  const addObjective = async () => {
    const statement = newObjective.statement.trim();
    if (!statement) return;
    const rationale = newObjective.rationale.trim();
    const ok = await run(
      () => createObjective(projectId, initiativeId, rationale ? { statement, rationale } : { statement }),
      "Objective added.",
    );
    if (ok) setNewObjective({ statement: "", rationale: "" });
  };

  const saveObjective = async (o: Objective) => {
    if (!editingObjective) return;
    const res = objectiveEditBody(o, editingObjective.draft);
    if ("error" in res) {
      setError(res.error);
      return;
    }
    if (Object.keys(res.body).length === 0) {
      setEditingObjective(null);
      return;
    }
    const ok = await run(() => updateObjective(projectId, initiativeId, o.code, res.body), `${o.code} saved.`);
    if (ok) setEditingObjective(null);
  };

  const linkRequirement = async (o: Objective) => {
    const code = linkPick[o.id];
    if (!code) return;
    const ok = await run(
      () => linkObjectiveRequirement(projectId, initiativeId, o.code, code),
      `${code} now serves ${o.code}.`,
    );
    if (ok) setLinkPick((p) => ({ ...p, [o.id]: "" }));
  };

  const unlinkRequirement = (o: Objective, code: string) =>
    run(
      () => linkObjectiveRequirement(projectId, initiativeId, o.code, code, true),
      `${code} no longer serves ${o.code}.`,
    );

  const addMetric = async (o: Objective) => {
    const name = metricDraftNew.name.trim();
    const target = parseNumber(metricDraftNew.target);
    if (!name || target === undefined) {
      setError("A metric needs a name and a numeric target.");
      return;
    }
    const baseline = parseNumber(metricDraftNew.baseline);
    if (metricDraftNew.baseline.trim() && baseline === undefined) {
      setError("Baseline must be a number (or blank).");
      return;
    }
    const unit = metricDraftNew.unit.trim();
    const ok = await run(
      () =>
        createMetric(projectId, initiativeId, {
          objective: o.code,
          name,
          direction: metricDraftNew.direction,
          target,
          ...(baseline !== undefined ? { baseline } : {}),
          ...(unit ? { unit } : {}),
        }),
      `Metric added to ${o.code}.`,
    );
    if (ok) {
      setMetricFormFor(null);
      setMetricDraftNew(emptyMetric());
    }
  };

  const saveMetric = async (m: OutcomeMetric) => {
    if (!editingMetric) return;
    const res = metricEditBody(m, editingMetric.draft);
    if ("error" in res) {
      setError(res.error);
      return;
    }
    if (Object.keys(res.body).length === 0) {
      setEditingMetric(null);
      return;
    }
    const ok = await run(() => updateMetric(projectId, initiativeId, m.code, res.body), `${m.code} saved.`);
    if (ok) setEditingMetric(null);
  };

  const record = async (m: OutcomeMetric) => {
    const value = parseNumber(values[m.id] ?? "");
    if (value === undefined) {
      setError(`Enter a number to record for ${m.code}.`);
      return;
    }
    const ok = await run(() => recordObservation(projectId, initiativeId, m.code, value), `Recorded ${value} for ${m.code}.`);
    if (ok) setValues((v) => ({ ...v, [m.id]: "" }));
  };

  const byObjective = metricsByObjective(metrics);
  const unserved = requirementsWithoutObjective(requirements);

  return (
    <>
      <div className="judgment-head">
        <h2 className="section-title">Objectives</h2>
        <span className="muted">
          {objectives.length === 0
            ? "none yet — what outcome should this initiative move?"
            : `${objectives.length} objective${objectives.length === 1 ? "" : "s"} · ${metrics.length} metric${metrics.length === 1 ? "" : "s"}`}
        </span>
      </div>
      {objectives.length > 0 && (
        <section className="health-card" aria-label="Objectives">
          {objectives.map((o) => {
            const served = requirementCodes(o.requirement_ids, requirements);
            const linkable = linkableRequirements(requirements, o.requirement_ids);
            const oms = byObjective[o.id] ?? [];
            const editing = editingObjective?.id === o.id ? editingObjective : null;
            return (
              <div key={o.id} className="health-row">
                <span className="idchip chip-accent">{o.code}</span>
                <div className="health-body">
                  {editing ? (
                    <InlinePanel
                      className="obj-edit"
                      label={`Edit ${o.code}`}
                      onClose={() => setEditingObjective(null)}
                    >
                      <ObjectiveEditForm
                        code={o.code}
                        draft={editing.draft}
                        busy={busy}
                        onChange={(draft) => setEditingObjective({ id: o.id, draft })}
                        onSave={() => saveObjective(o)}
                        onCancel={() => setEditingObjective(null)}
                      />
                    </InlinePanel>
                  ) : (
                    <>
                      <div className="health-title">
                        {o.statement} {o.status !== "active" && <span className="tag">{o.status}</span>}
                      </div>
                      {o.rationale && <div className="health-sub">{o.rationale}</div>}
                      {o.owner && <div className="health-sub">Owner: {o.owner}</div>}
                      <div className="row obj-actions">
                        <button
                          className="jc-secondary"
                          disabled={busy}
                          aria-label={`Edit ${o.code}`}
                          onClick={() => {
                            setEditingMetric(null);
                            setEditingObjective({ id: o.id, draft: objectiveDraft(o) });
                          }}
                        >
                          Edit
                        </button>
                      </div>
                    </>
                  )}
                  <div className="health-sub">
                    Served by:{" "}
                    {served.length ? (
                      <span className="obj-served">
                        {served.map((code) => (
                          <span key={code} className="obj-served-req">
                            <a className="code" href={lineageHref(initiativeId, "requirement", code)}>
                              {code}
                            </a>
                            <button
                              className="obj-unlink"
                              disabled={busy}
                              aria-label={`Unlink ${code} from ${o.code}`}
                              title={`Unlink ${code} from ${o.code}`}
                              onClick={() => unlinkRequirement(o, code)}
                            >
                              ×
                            </button>
                          </span>
                        ))}
                      </span>
                    ) : (
                      <span className="muted">no requirements</span>
                    )}
                  </div>
                  <LineagePanel projectId={projectId} initiativeId={initiativeId} type="objective" id={o.id} code={o.code} />
                  {linkable.length > 0 && (
                    <div className="row" style={{ marginTop: 6, gap: 8 }}>
                      <select
                        className="text-input"
                        style={{ maxWidth: 220, padding: "4px 8px", fontSize: 12 }}
                        aria-label={`Requirement to link to ${o.code}`}
                        value={linkPick[o.id] ?? ""}
                        onChange={(e) => setLinkPick((p) => ({ ...p, [o.id]: e.target.value }))}
                      >
                        <option value="">Link a requirement…</option>
                        {linkable.map((r) => (
                          <option key={r.id} value={r.code}>
                            {r.code}
                            {r.statement ? ` — ${r.statement.slice(0, 60)}` : ""}
                          </option>
                        ))}
                      </select>
                      <button
                        className="jc-secondary"
                        disabled={busy || !linkPick[o.id]}
                        onClick={() => linkRequirement(o)}
                      >
                        Link
                      </button>
                    </div>
                  )}
                  {oms.map((m) =>
                    editingMetric?.id === m.id ? (
                      <InlinePanel
                        key={m.id}
                        className="row obj-edit"
                        label={`Edit ${m.code}`}
                        onClose={() => setEditingMetric(null)}
                      >
                        <MetricFields
                          prefix={`${m.code}`}
                          draft={editingMetric.draft}
                          onChange={(draft) => setEditingMetric({ id: m.id, draft })}
                        />
                        <button className="jc-primary" disabled={busy} onClick={() => saveMetric(m)}>
                          Save {m.code}
                        </button>
                        <button className="jc-secondary" onClick={() => setEditingMetric(null)}>
                          Cancel
                        </button>
                      </InlinePanel>
                    ) : (
                      <div key={m.id} className="row" style={{ marginTop: 6, gap: 8, flexWrap: "wrap" }}>
                        <a className="code" href={lineageHref(initiativeId, "metric", m.code)}>{m.code}</a>
                        <span>{m.name}</span>
                        <span className="muted">{metricRange(m)}</span>
                        <span className={`idchip ${driftClass(m.status?.drift ?? "no_data")}`}>
                          {driftLabel(m.status?.drift ?? "no_data")}
                        </span>
                        {m.status?.latest !== undefined && <span className="muted">latest {m.status.latest}</span>}
                        <input
                          className="text-input"
                          style={{ maxWidth: 90, padding: "4px 8px", fontSize: 12 }}
                          placeholder="value"
                          aria-label={`Value to record for ${m.code}`}
                          inputMode="decimal"
                          value={values[m.id] ?? ""}
                          onChange={(e) => setValues((v) => ({ ...v, [m.id]: e.target.value }))}
                          onKeyDown={(e) => {
                            if (e.key === "Enter") record(m);
                          }}
                        />
                        <button className="jc-secondary" disabled={busy || !(values[m.id] ?? "").trim()} onClick={() => record(m)}>
                          Record
                        </button>
                        <button
                          className="jc-secondary"
                          disabled={busy}
                          aria-label={`Edit ${m.code}`}
                          onClick={() => {
                            setEditingObjective(null);
                            setEditingMetric({ id: m.id, draft: metricDraft(m) });
                          }}
                        >
                          Edit
                        </button>
                      </div>
                    ),
                  )}
                  {metricFormFor === o.id ? (
                    <InlinePanel
                      className="row obj-edit"
                      label={`New metric for ${o.code}`}
                      onClose={() => setMetricFormFor(null)}
                    >
                      <MetricFields prefix={`New ${o.code} metric`} draft={metricDraftNew} onChange={setMetricDraftNew} />
                      <button className="jc-primary" disabled={busy} onClick={() => addMetric(o)}>
                        Add metric
                      </button>
                      <button className="jc-secondary" onClick={() => setMetricFormFor(null)}>
                        Cancel
                      </button>
                    </InlinePanel>
                  ) : (
                    <div className="row" style={{ marginTop: 6 }}>
                      <button
                        className="jc-secondary"
                        aria-label={`Add a metric to ${o.code}`}
                        onClick={() => {
                          setMetricFormFor(o.id);
                          setMetricDraftNew(emptyMetric());
                        }}
                      >
                        + Metric
                      </button>
                    </div>
                  )}
                </div>
              </div>
            );
          })}
        </section>
      )}
      {unserved.length > 0 && (
        <p className="muted" style={{ marginTop: 8 }}>
          {unserved.map((r) => r.code).join(", ")} serve{unserved.length === 1 ? "s" : ""} no objective.
        </p>
      )}
      <div className="row" style={{ gap: 8, flexWrap: "wrap" }}>
        <input
          className="text-input"
          style={{ maxWidth: 340 }}
          placeholder="New objective — the outcome to move"
          aria-label="New objective statement"
          value={newObjective.statement}
          onChange={(e) => setNewObjective((n) => ({ ...n, statement: e.target.value }))}
        />
        <input
          className="text-input"
          style={{ maxWidth: 280 }}
          placeholder="Rationale (optional)"
          aria-label="New objective rationale (optional)"
          value={newObjective.rationale}
          onChange={(e) => setNewObjective((n) => ({ ...n, rationale: e.target.value }))}
        />
        <button className="jc-primary" disabled={busy || !newObjective.statement.trim()} onClick={addObjective}>
          Add objective
        </button>
      </div>
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
    </>
  );
}

// ObjectiveEditForm is `idea objective edit` as a form: statement,
// rationale, owner and status, each labelled.
function ObjectiveEditForm({
  code,
  draft,
  busy,
  onChange,
  onSave,
  onCancel,
}: {
  code: string;
  draft: ObjectiveDraft;
  busy: boolean;
  onChange: (d: ObjectiveDraft) => void;
  onSave: () => void;
  onCancel: () => void;
}) {
  const id = (f: string) => `obj-${code}-${f}`;
  return (
    <div className="obj-edit-grid">
      <label htmlFor={id("statement")}>Statement</label>
      <input
        id={id("statement")}
        className="text-input"
        value={draft.statement}
        onChange={(e) => onChange({ ...draft, statement: e.target.value })}
        onKeyDown={(e) => {
          if (e.key === "Enter") onSave();
        }}
      />
      <label htmlFor={id("rationale")}>Rationale</label>
      <input
        id={id("rationale")}
        className="text-input"
        value={draft.rationale}
        onChange={(e) => onChange({ ...draft, rationale: e.target.value })}
      />
      <label htmlFor={id("owner")}>Owner</label>
      <input
        id={id("owner")}
        className="text-input"
        value={draft.owner}
        onChange={(e) => onChange({ ...draft, owner: e.target.value })}
      />
      <label htmlFor={id("status")}>Status</label>
      <select
        id={id("status")}
        className="text-input"
        value={draft.status}
        onChange={(e) => onChange({ ...draft, status: e.target.value as ObjectiveStatus })}
      >
        {OBJECTIVE_STATUSES.map((s) => (
          <option key={s} value={s}>
            {s}
          </option>
        ))}
      </select>
      <div className="row obj-edit-actions">
        <button className="jc-primary" disabled={busy} onClick={onSave}>
          Save {code}
        </button>
        <button className="jc-secondary" onClick={onCancel}>
          Cancel
        </button>
      </div>
    </div>
  );
}

// MetricFields are a metric's editable fields, shared by add and edit; every
// input is labelled (visually by its placeholder, for a screen reader by
// aria-label).
function MetricFields({
  prefix,
  draft,
  onChange,
}: {
  prefix: string;
  draft: MetricDraft;
  onChange: (d: MetricDraft) => void;
}) {
  const small = { padding: "4px 8px", fontSize: 12 };
  return (
    <>
      <input
        className="text-input"
        style={{ maxWidth: 200, ...small }}
        placeholder="Metric name"
        aria-label={`${prefix} name`}
        value={draft.name}
        onChange={(e) => onChange({ ...draft, name: e.target.value })}
      />
      <select
        className="text-input"
        style={{ maxWidth: 110, ...small }}
        aria-label={`${prefix} direction`}
        value={draft.direction}
        onChange={(e) => onChange({ ...draft, direction: e.target.value as MetricDirection })}
      >
        <option value="increase">increase</option>
        <option value="decrease">decrease</option>
        <option value="maintain">maintain</option>
      </select>
      <input
        className="text-input"
        style={{ maxWidth: 90, ...small }}
        placeholder="baseline"
        aria-label={`${prefix} baseline (blank for none)`}
        inputMode="decimal"
        value={draft.baseline}
        onChange={(e) => onChange({ ...draft, baseline: e.target.value })}
      />
      <input
        className="text-input"
        style={{ maxWidth: 90, ...small }}
        placeholder="target"
        aria-label={`${prefix} target`}
        inputMode="decimal"
        value={draft.target}
        onChange={(e) => onChange({ ...draft, target: e.target.value })}
      />
      <input
        className="text-input"
        style={{ maxWidth: 70, ...small }}
        placeholder="unit"
        aria-label={`${prefix} unit`}
        value={draft.unit}
        onChange={(e) => onChange({ ...draft, unit: e.target.value })}
      />
    </>
  );
}
