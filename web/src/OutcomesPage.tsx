import { useCallback, useEffect, useState, type FormEvent } from "react";
import {
  get,
  getOutcomes,
  post,
  recordObservation,
  resource,
  type MetricObservation,
  type OutcomeMetric,
  type OutcomeReport,
} from "./api";
import PhaseHeader from "./PhaseHeader";
import LineagePanel from "./LineagePanel";
import { NodeLink } from "./GraphBits";
import { driftClass, driftLabel, metricRange, metricsByObjective, outcomeGapRows, parseNumber } from "./outcomes";
import {
  formatValue,
  isMeasurable,
  metricProgress,
  observationRows,
  sourceLabel,
  sparkline,
  byObservedAt,
} from "./outcomesView";
import { lineageHref } from "./lineage";

interface ChangeSetRow {
  id: string;
  human_id: string;
}

interface MeasureResult {
  metric_code: string;
  skipped?: string;
  error?: string;
  observation?: MetricObservation;
}

// OutcomesPage is #/i/<id>/outcomes: the outcome end of the Product Graph.
// Each objective with its metrics: baseline → target progress and the latest
// value, the drift badge with the daemon's reasons, the observation history
// as a sparkline and a table, which change sets landed between observations
// (each linked to its lineage), and the "works but fails its objective"
// requirements. Values are recorded here (manual metrics) or measured
// (command/url sources) -- the same writes as `idea metric record|measure`.
export default function OutcomesPage({ projectId, initiativeId }: { projectId: string; initiativeId: string }) {
  const [report, setReport] = useState<OutcomeReport | null>(null);
  const [obs, setObs] = useState<Record<string, MetricObservation[]>>({});
  const [csCodes, setCsCodes] = useState<Record<string, string>>({});
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState("");

  const load = useCallback(async () => {
    try {
      const [rep, cs] = await Promise.all([
        getOutcomes(projectId, initiativeId),
        get<ChangeSetRow[]>(resource(projectId, initiativeId, "change-sets")).catch(() => []),
      ]);
      setReport(rep);
      setCsCodes(Object.fromEntries((cs ?? []).map((c) => [c.id, c.human_id])));
      const entries = await Promise.all(
        (rep.metrics ?? []).map(async (m) => {
          const list = await get<MetricObservation[]>(
            resource(projectId, initiativeId, `metrics/${encodeURIComponent(m.code)}/observations`),
          ).catch(() => []);
          return [m.id, list ?? []] as const;
        }),
      );
      setObs(Object.fromEntries(entries));
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [projectId, initiativeId]);

  useEffect(() => {
    load();
  }, [load]);

  const record = async (m: OutcomeMetric, value: number, note: string) => {
    setBusy(m.id);
    setNotice("");
    try {
      const res = await recordObservation(projectId, initiativeId, m.code, value, note || undefined);
      setNotice(`Recorded ${m.code} = ${formatValue(value, m.unit)} — ${driftLabel(res.status.drift)}.`);
      await load();
      return true;
    } catch (e) {
      setNotice(e instanceof Error ? e.message : String(e));
      return false;
    } finally {
      setBusy("");
    }
  };

  const measure = async (m: OutcomeMetric) => {
    setBusy(m.id);
    setNotice("");
    try {
      const res = await post<{ results: MeasureResult[] }>(resource(projectId, initiativeId, "metrics/measure"), {
        metric: m.code,
      });
      const r = res.results?.[0];
      setNotice(
        r?.error
          ? `${m.code}: ${r.error}`
          : r?.skipped
            ? `${m.code} skipped: ${r.skipped}`
            : `Measured ${m.code} = ${formatValue(r?.observation?.value, m.unit)}.`,
      );
      await load();
    } catch (e) {
      setNotice(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy("");
    }
  };

  const byObjective = report ? metricsByObjective(report.metrics ?? []) : {};
  const s = report?.summary;
  const gapRows = outcomeGapRows(report?.gaps);

  return (
    <div className="phase-page outcomes-page">
      <PhaseHeader
        title="Outcomes"
        purpose="Whether the work moved what it was for."
      />
      {/* A summary line of its own rather than PhaseHeader's meta, which does
          not wrap and ran off the screen at phone width. */}
      {s && s.metrics > 0 && (
        <p className="muted gv-summary">
          {s.on_track} on track · {s.off_track} off track · {s.regressed} regressed · {s.stale} stale · {s.no_data} no
          data
        </p>
      )}
      {error && (
        <p className="error" role="alert">
          {error}
        </p>
      )}
      {!report && !error && <p className="muted">Loading outcomes…</p>}
      {notice && (
        <p className="ok-text" role="status">
          {notice}
        </p>
      )}

      {report && report.objectives.length === 0 && (
        <section className="health-card">
          <div className="muted" style={{ padding: "13px 16px" }}>
            No objectives yet. Add them on <a href={`#/i/${initiativeId}/define`}>Define</a> (or{" "}
            <code>idea objective add "…"</code>) to track whether this work moves anything.
          </div>
        </section>
      )}

      {report && (report.requirement_drift ?? []).length > 0 && (
        <section className="oc-wbf" aria-labelledby="oc-wbf-h">
          <h2 id="oc-wbf-h" className="section-title">
            Works, but fails its objective
          </h2>
          <ul className="health-card gv-list">
            {(report.requirement_drift ?? []).map((d) => (
              <li key={`${d.requirement_code}-${d.objective_code}`} className="health-row">
                <span className="health-sev sev-high">works, fails</span>
                <div className="health-body">
                  <div className="health-title">
                    <a href={lineageHref(initiativeId, "requirement", d.requirement_code)}>{d.requirement_code}</a> is
                    verified, but its objective{" "}
                    <a href={lineageHref(initiativeId, "objective", d.objective_code)}>{d.objective_code}</a> is not
                    moving
                    {d.metric_codes.length > 0 && (
                      <>
                        {" "}
                        (
                        {d.metric_codes.map((c, i) => (
                          <span key={c}>
                            {i > 0 && ", "}
                            <a href={lineageHref(initiativeId, "metric", c)}>{c}</a>
                          </span>
                        ))}
                        )
                      </>
                    )}
                  </div>
                  {d.detail && <div className="health-sub">{d.detail}</div>}
                </div>
              </li>
            ))}
          </ul>
        </section>
      )}

      {report?.objectives.map((o) => (
        <section key={o.id} className="oc-objective" aria-labelledby={`oc-${o.id}`}>
          <div className="oc-obj-head">
            <a className="idchip chip-accent" href={lineageHref(initiativeId, "objective", o.code)}>
              {o.code}
            </a>
            <h2 id={`oc-${o.id}`} className="oc-obj-title">
              {o.statement}
            </h2>
            {o.status !== "active" && <span className="tag">{o.status}</span>}
          </div>
          {o.rationale && <p className="health-sub oc-obj-sub">{o.rationale}</p>}
          <LineagePanel projectId={projectId} initiativeId={initiativeId} type="objective" id={o.id} code={o.code} />
          {(byObjective[o.id] ?? []).length === 0 ? (
            <p className="muted">
              No metric measures this objective yet — add one on <a href={`#/i/${initiativeId}/define`}>Define</a>.
            </p>
          ) : (
            <div className="oc-metrics">
              {(byObjective[o.id] ?? []).map((m) => (
                <MetricCard
                  key={m.id}
                  projectId={projectId}
                  initiativeId={initiativeId}
                  metric={m}
                  observations={obs[m.id] ?? []}
                  csCodes={csCodes}
                  busy={busy === m.id}
                  onRecord={record}
                  onMeasure={measure}
                />
              ))}
            </div>
          )}
        </section>
      ))}

      {gapRows.length > 0 && (
        <section aria-labelledby="oc-gaps-h">
          <h2 id="oc-gaps-h" className="section-title">
            Gaps in the outcome chain
          </h2>
          <ul className="health-card gv-list">
            {gapRows.map((g) => (
              <li key={g.label} className="health-row">
                <span className="health-sev sev-low">gap</span>
                <div className="health-body">
                  <div className="health-title">{g.label}</div>
                  <div className="health-sub">{g.codes.join(", ")}</div>
                </div>
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}

function MetricCard({
  projectId,
  initiativeId,
  metric: m,
  observations,
  csCodes,
  busy,
  onRecord,
  onMeasure,
}: {
  projectId: string;
  initiativeId: string;
  metric: OutcomeMetric;
  observations: MetricObservation[];
  csCodes: Record<string, string>;
  busy: boolean;
  onRecord: (m: OutcomeMetric, v: number, note: string) => Promise<boolean>;
  onMeasure: (m: OutcomeMetric) => void;
}) {
  const [value, setValue] = useState("");
  const [note, setNote] = useState("");
  const [formError, setFormError] = useState("");
  const st = m.status;
  const drift = st?.drift ?? "no_data";
  const progress = metricProgress(m);
  const rows = observationRows(observations, csCodes);
  const series = byObservedAt(observations).map((o) => o.value);
  const base = st?.effective_baseline ?? m.baseline;
  const valueId = `oc-val-${m.id}`;
  const noteId = `oc-note-${m.id}`;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const v = parseNumber(value);
    if (v === undefined) {
      setFormError(`Enter a number to record for ${m.code}.`);
      return;
    }
    setFormError("");
    if (await onRecord(m, v, note.trim())) {
      setValue("");
      setNote("");
    }
  };

  return (
    <article className="oc-metric" aria-labelledby={`oc-m-${m.id}`}>
      <div className="oc-m-head">
        <a className="code" href={lineageHref(initiativeId, "metric", m.code)}>
          {m.code}
        </a>
        <h3 id={`oc-m-${m.id}`} className="oc-m-name">
          {m.name}
        </h3>
        <span className={`idchip ${driftClass(drift)}`}>{driftLabel(drift)}</span>
        {st?.guardrail_breached && <span className="idchip chip-failing">guardrail breached</span>}
        {st?.achieved && <span className="idchip pill-verified">target met</span>}
      </div>
      <div className="oc-m-grid">
        <div className="oc-m-figs">
          <div className="oc-latest">
            <span className="oc-latest-v">{formatValue(st?.latest, m.unit)}</span>
            <span className="muted"> latest{st?.latest_at ? ` · ${new Date(st.latest_at).toLocaleDateString()}` : ""}</span>
          </div>
          <div className="muted">{metricRange(m)}</div>
          {progress.fraction !== null && (
            <div className="oc-progress">
              <progress
                max={100}
                value={Math.round(progress.fraction * 100)}
                aria-label={`${m.code} progress from baseline to target`}
              />
              <span className="muted">{Math.round(progress.fraction * 100)}% of the way</span>
            </div>
          )}
          <div className="muted oc-src">
            {sourceLabel(m)}
            {m.cadence ? ` · every ${m.cadence}` : ""}
            {st?.next_due_at ? ` · next due ${new Date(st.next_due_at).toLocaleDateString()}` : ""}
          </div>
        </div>
        <Sparkline
          values={series}
          target={m.target}
          baseline={base ?? undefined}
          label={`${m.code} history: ${series.length} observation${series.length === 1 ? "" : "s"}${
            series.length ? `, from ${series[0]} to ${series[series.length - 1]}` : ""
          }; target ${m.target}`}
        />
      </div>
      {(st?.reasons ?? []).length > 0 && (
        <ul className="oc-reasons">
          {(st?.reasons ?? []).map((r, i) => (
            <li key={i}>{r}</li>
          ))}
        </ul>
      )}

      <div className="oc-m-actions">
        <form className="oc-record" onSubmit={submit}>
          <label htmlFor={valueId} className="sr-only">
            Value for {m.code}
          </label>
          <input
            id={valueId}
            className="text-input"
            inputMode="decimal"
            placeholder={`value${m.unit ? ` (${m.unit})` : ""}`}
            value={value}
            onChange={(e) => setValue(e.target.value)}
          />
          <label htmlFor={noteId} className="sr-only">
            Note for {m.code}
          </label>
          <input
            id={noteId}
            className="text-input"
            placeholder="note (optional)"
            value={note}
            onChange={(e) => setNote(e.target.value)}
          />
          <button type="submit" className="jc-secondary" disabled={busy || !value.trim()}>
            Record value
          </button>
        </form>
        {isMeasurable(m) && (
          <button type="button" className="jc-primary" disabled={busy} onClick={() => onMeasure(m)}>
            {busy ? "Measuring…" : "Measure"}
          </button>
        )}
      </div>
      {formError && <p className="error">{formError}</p>}

      {rows.length > 0 && (
        <details className="oc-history">
          <summary>Observation history · {rows.length}</summary>
          <table className="lv-table">
            <caption className="sr-only">{m.code} observations, newest first</caption>
            <thead>
              <tr>
                <th scope="col">When</th>
                <th scope="col">Value</th>
                <th scope="col">Change</th>
                <th scope="col">Landed since previous</th>
                <th scope="col">Note</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => (
                <tr key={r.observation.id}>
                  <td>{new Date(r.observation.observed_at).toLocaleString()}</td>
                  <td>{formatValue(r.observation.value, m.unit)}</td>
                  <td>{r.delta === undefined ? "—" : r.delta > 0 ? `+${r.delta}` : String(r.delta)}</td>
                  <td>
                    {r.changeSets.length === 0 ? (
                      <span className="muted">—</span>
                    ) : (
                      r.changeSets.map((c) => (
                        <NodeLink key={c.id} initiativeId={initiativeId} type="change_set" id={c.id} label={c.code} />
                      ))
                    )}
                  </td>
                  <td>{r.observation.note ?? ""}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      )}
      <LineagePanel projectId={projectId} initiativeId={initiativeId} type="metric" id={m.id} code={m.code} />
    </article>
  );
}

// Sparkline is the observation series as an inline SVG polyline with the
// target (dashed) and baseline (dotted) as reference lines. It is an image
// with a text summary; the history table carries the exact values.
function Sparkline({ values, target, baseline, label }: { values: number[]; target: number; baseline?: number; label: string }) {
  const W = 180;
  const H = 48;
  if (values.length === 0) {
    return <div className="oc-spark oc-spark-empty muted">no observations yet</div>;
  }
  const sp = sparkline(values, W, H, { pad: 4, target, baseline });
  const last = sp.points[sp.points.length - 1];
  return (
    <svg className="oc-spark" viewBox={`0 0 ${W} ${H}`} width={W} height={H} role="img" aria-label={label}>
      <title>{label}</title>
      {sp.baselineY !== undefined && <line className="oc-spark-base" x1={0} x2={W} y1={sp.baselineY} y2={sp.baselineY} />}
      {sp.targetY !== undefined && <line className="oc-spark-target" x1={0} x2={W} y1={sp.targetY} y2={sp.targetY} />}
      {sp.points.length > 1 && <polyline className="oc-spark-line" points={sp.path} fill="none" />}
      {sp.points.map((p, i) => (
        <circle key={i} className="oc-spark-dot" cx={p.x} cy={p.y} r={i === sp.points.length - 1 ? 3 : 1.8} />
      ))}
      {last && <circle className="oc-spark-last" cx={last.x} cy={last.y} r={3} />}
    </svg>
  );
}
