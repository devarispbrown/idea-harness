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
  type MetricDirection,
  type Objective,
  type OutcomeMetric,
} from "./api";
import {
  driftClass,
  driftLabel,
  linkableRequirements,
  metricRange,
  metricsByObjective,
  parseNumber,
  requirementCodes,
  requirementsWithoutObjective,
  type RequirementLike,
} from "./outcomes";

interface Requirement extends RequirementLike {
  statement: string;
}

const emptyMetric = () => ({ name: "", direction: "increase" as MetricDirection, target: "", baseline: "", unit: "" });

// ObjectivesSection is Define's view of the outcome chain: each objective
// (OBJ-n), the requirements serving it, and the metrics (MET-n) measuring
// it, with the human edits the daemon accepts -- add an objective, link a
// requirement, add a metric, record a value.
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
  const [metricDraft, setMetricDraft] = useState(emptyMetric());
  const [values, setValues] = useState<Record<string, string>>({});

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
  // and a reload so the section reflects what the daemon now holds.
  const run = async (fn: () => Promise<unknown>): Promise<boolean> => {
    setError("");
    setBusy(true);
    try {
      await fn();
      await load();
      return true;
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      return false;
    } finally {
      setBusy(false);
    }
  };

  const addObjective = async () => {
    const statement = newObjective.statement.trim();
    if (!statement) return;
    const rationale = newObjective.rationale.trim();
    const ok = await run(() =>
      createObjective(projectId, initiativeId, rationale ? { statement, rationale } : { statement }),
    );
    if (ok) setNewObjective({ statement: "", rationale: "" });
  };

  const linkRequirement = async (o: Objective) => {
    const code = linkPick[o.id];
    if (!code) return;
    const ok = await run(() => linkObjectiveRequirement(projectId, initiativeId, o.code, code));
    if (ok) setLinkPick((p) => ({ ...p, [o.id]: "" }));
  };

  const addMetric = async (o: Objective) => {
    const name = metricDraft.name.trim();
    const target = parseNumber(metricDraft.target);
    if (!name || target === undefined) {
      setError("A metric needs a name and a numeric target.");
      return;
    }
    const baseline = parseNumber(metricDraft.baseline);
    if (metricDraft.baseline.trim() && baseline === undefined) {
      setError("Baseline must be a number (or blank).");
      return;
    }
    const unit = metricDraft.unit.trim();
    const ok = await run(() =>
      createMetric(projectId, initiativeId, {
        objective: o.code,
        name,
        direction: metricDraft.direction,
        target,
        ...(baseline !== undefined ? { baseline } : {}),
        ...(unit ? { unit } : {}),
      }),
    );
    if (ok) {
      setMetricFormFor(null);
      setMetricDraft(emptyMetric());
    }
  };

  const record = async (m: OutcomeMetric) => {
    const value = parseNumber(values[m.id] ?? "");
    if (value === undefined) {
      setError(`Enter a number to record for ${m.code}.`);
      return;
    }
    const ok = await run(() => recordObservation(projectId, initiativeId, m.code, value));
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
        <section className="health-card">
          {objectives.map((o) => {
            const served = requirementCodes(o.requirement_ids, requirements);
            const linkable = linkableRequirements(requirements, o.requirement_ids);
            const oms = byObjective[o.id] ?? [];
            return (
              <div key={o.id} className="health-row">
                <span className="idchip chip-accent">{o.code}</span>
                <div className="health-body">
                  <div className="health-title">
                    {o.statement} {o.status !== "active" && <span className="tag">{o.status}</span>}
                  </div>
                  {o.rationale && <div className="health-sub">{o.rationale}</div>}
                  <div className="health-sub">
                    Served by: {served.length ? served.join(", ") : <span className="muted">no requirements</span>}
                  </div>
                  {linkable.length > 0 && (
                    <div className="row" style={{ marginTop: 6, gap: 8 }}>
                      <select
                        className="text-input"
                        style={{ maxWidth: 220, padding: "4px 8px", fontSize: 12 }}
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
                  {oms.map((m) => (
                    <div key={m.id} className="row" style={{ marginTop: 6, gap: 8, flexWrap: "wrap" }}>
                      <span className="code">{m.code}</span>
                      <span>{m.name}</span>
                      <span className="muted">{metricRange(m)}</span>
                      <span className={`idchip ${driftClass(m.status?.drift ?? "no_data")}`}>
                        {driftLabel(m.status?.drift ?? "no_data")}
                      </span>
                      {m.status?.latest !== undefined && (
                        <span className="muted">latest {m.status.latest}</span>
                      )}
                      <input
                        className="text-input"
                        style={{ maxWidth: 90, padding: "4px 8px", fontSize: 12 }}
                        placeholder="value"
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
                    </div>
                  ))}
                  {metricFormFor === o.id ? (
                    <div className="row" style={{ marginTop: 6, gap: 8, flexWrap: "wrap" }}>
                      <input
                        className="text-input"
                        style={{ maxWidth: 200, padding: "4px 8px", fontSize: 12 }}
                        placeholder="Metric name"
                        value={metricDraft.name}
                        onChange={(e) => setMetricDraft((d) => ({ ...d, name: e.target.value }))}
                      />
                      <select
                        className="text-input"
                        style={{ maxWidth: 110, padding: "4px 8px", fontSize: 12 }}
                        value={metricDraft.direction}
                        onChange={(e) => setMetricDraft((d) => ({ ...d, direction: e.target.value as MetricDirection }))}
                      >
                        <option value="increase">increase</option>
                        <option value="decrease">decrease</option>
                        <option value="maintain">maintain</option>
                      </select>
                      <input
                        className="text-input"
                        style={{ maxWidth: 90, padding: "4px 8px", fontSize: 12 }}
                        placeholder="baseline"
                        inputMode="decimal"
                        value={metricDraft.baseline}
                        onChange={(e) => setMetricDraft((d) => ({ ...d, baseline: e.target.value }))}
                      />
                      <input
                        className="text-input"
                        style={{ maxWidth: 90, padding: "4px 8px", fontSize: 12 }}
                        placeholder="target"
                        inputMode="decimal"
                        value={metricDraft.target}
                        onChange={(e) => setMetricDraft((d) => ({ ...d, target: e.target.value }))}
                      />
                      <input
                        className="text-input"
                        style={{ maxWidth: 70, padding: "4px 8px", fontSize: 12 }}
                        placeholder="unit"
                        value={metricDraft.unit}
                        onChange={(e) => setMetricDraft((d) => ({ ...d, unit: e.target.value }))}
                      />
                      <button className="jc-primary" disabled={busy} onClick={() => addMetric(o)}>
                        Add metric
                      </button>
                      <button className="jc-secondary" onClick={() => setMetricFormFor(null)}>
                        Cancel
                      </button>
                    </div>
                  ) : (
                    <div className="row" style={{ marginTop: 6 }}>
                      <button
                        className="jc-secondary"
                        onClick={() => {
                          setMetricFormFor(o.id);
                          setMetricDraft(emptyMetric());
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
          value={newObjective.statement}
          onChange={(e) => setNewObjective((n) => ({ ...n, statement: e.target.value }))}
        />
        <input
          className="text-input"
          style={{ maxWidth: 280 }}
          placeholder="Rationale (optional)"
          value={newObjective.rationale}
          onChange={(e) => setNewObjective((n) => ({ ...n, rationale: e.target.value }))}
        />
        <button className="jc-primary" disabled={busy || !newObjective.statement.trim()} onClick={addObjective}>
          Add objective
        </button>
      </div>
      {error && <p className="error">{error}</p>}
    </>
  );
}
