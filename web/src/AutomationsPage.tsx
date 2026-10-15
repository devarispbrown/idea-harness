import { useCallback, useEffect, useState } from "react";
import { get, post, put } from "./api";
import {
  evalFormFrom,
  evalPutBody,
  lastRunText,
  nextRunText,
  relative,
  runCost,
  runDetail,
  validateEvalForm,
  type Automation,
  type EvalForm,
} from "./automations";

// AutomationsPage is #/automations: every background job the daemon can run
// on a schedule, each off until a person turns it on -- Linear inbound
// polling, scheduled metric measurement, scheduled eval -- with its schedule,
// last and next run, history (status and cost) and Run now. Every toggle is a
// harness.yaml write (PUT .../automations/{name}); Run now runs one job once,
// enabled or not (POST .../automations/{name}/run).
export default function AutomationsPage({ projectId }: { projectId: string }) {
  const [jobs, setJobs] = useState<Automation[] | null>(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState<Record<string, { text: string; ok: boolean }>>({});
  const [now, setNow] = useState(() => new Date());
  // The switch flips at once and is put back if the write is refused --
  // the same optimistic shape as Settings' git toggles.
  const [pending, setPending] = useState<Record<string, boolean>>({});
  const base = `/projects/${encodeURIComponent(projectId)}/automations`;

  const load = useCallback(async () => {
    try {
      const v = await get<{ automations: Automation[] }>(base);
      setJobs(v.automations);
      setError("");
      setNow(new Date());
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [base]);

  useEffect(() => {
    load();
    const id = setInterval(load, 5000);
    return () => clearInterval(id);
  }, [load]);

  const say = (name: string, text: string, ok: boolean) => setNotice((n) => ({ ...n, [name]: { text, ok } }));

  const toggle = async (a: Automation, enabled: boolean) => {
    setPending((p) => ({ ...p, [a.name]: enabled }));
    try {
      await put(`${base}/${a.name}`, { enabled });
      say(a.name, enabled ? `${a.label} is on.` : `${a.label} is off.`, true);
    } catch (e) {
      say(a.name, e instanceof Error ? e.message : String(e), false);
    }
    await load();
    setPending((p) => {
      const next = { ...p };
      delete next[a.name];
      return next;
    });
  };

  const runNow = async (a: Automation, target?: string) => {
    try {
      const r = await post<{ runs: number }>(`${base}/${a.name}/run`, target ? { target } : {});
      say(a.name, r.runs === 1 ? "Started." : `Started ${r.runs} runs.`, true);
    } catch (e) {
      say(a.name, e instanceof Error ? e.message : String(e), false);
    }
    setTimeout(load, 500);
  };

  return (
    <div className="phase-page automations-page">
      <header className="automations-head">
        <h1>Automations</h1>
        <p className="muted">
          Background jobs the daemon runs on a schedule. Every one is off until you turn it on, and nothing here runs
          without that. Run now runs a job once, whether or not its schedule is on.
        </p>
      </header>
      {error && <p className="error">{error}</p>}
      {jobs?.map((a) => (
        <section key={a.name} className="card automation-card" data-automation={a.name} aria-labelledby={`auto-${a.name}`}>
          <div className="automation-top">
            <h2 id={`auto-${a.name}`}>{a.label}</h2>
            <label className="w3-toggle-row automation-switch">
              <input
                type="checkbox"
                role="switch"
                aria-checked={pending[a.name] ?? a.enabled}
                checked={pending[a.name] ?? a.enabled}
                disabled={a.name === "linear_inbound" && !a.available && !a.enabled}
                onChange={(e) => toggle(a, e.target.checked)}
              />
              {(pending[a.name] ?? a.enabled) ? "On" : "Off"}
            </label>
          </div>
          <p className="muted">{a.description}</p>
          <dl className="automation-facts">
            <dt>Schedule</dt>
            <dd>{a.schedule}</dd>
            <dt>Next</dt>
            <dd data-testid={`next-${a.name}`}>{nextRunText(a, now)}</dd>
            <dt>Last</dt>
            <dd>{lastRunText(a, now)}</dd>
          </dl>
          {!a.available && a.unavailable && <p className="warn-text">{a.unavailable}</p>}

          {a.name === "eval" && <EvalSettingsForm job={a} base={base} onSaved={(m, ok) => { say(a.name, m, ok); load(); }} />}

          {a.name === "metric_measurement" && a.targets && a.targets.length > 0 && (
            <table className="automation-table">
              <caption className="muted">Metrics with a command or url source</caption>
              <thead>
                <tr>
                  <th scope="col">Metric</th>
                  <th scope="col">Source</th>
                  <th scope="col">Cadence</th>
                  <th scope="col">Last</th>
                  <th scope="col">Next</th>
                  <th scope="col">
                    <span className="sr-only">Run</span>
                  </th>
                </tr>
              </thead>
              <tbody>
                {a.targets.map((t) => (
                  <tr key={t.metric_id}>
                    <td>
                      <a href={`#/i/${t.initiative_id}/outcomes`}>{t.code}</a> {t.name}
                    </td>
                    <td>{t.source}</td>
                    <td>{t.cadence || "—"}</td>
                    <td>{t.last_at ? relative(new Date(t.last_at), now) : "never"}</td>
                    <td>{t.skipped ? <span className="warn-text">{t.skipped}</span> : !a.enabled ? "off" : t.next_at ? relative(new Date(t.next_at), now) : "—"}</td>
                    <td>
                      <button className="ghost" onClick={() => runNow(a, t.metric_id)}>
                        Measure now
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}

          <div className="row">
            <button className="cs-secondary" disabled={a.running || (!a.available && a.name !== "metric_measurement")} onClick={() => runNow(a)}>
              {a.running ? "Running…" : "Run now"}
            </button>
            {a.name === "eval" && <span className="muted">Spends real money, up to the per-run cap.</span>}
          </div>
          {notice[a.name] && (
            <p className={notice[a.name].ok ? "ok-text" : "error"} role="status">
              {notice[a.name].text}
            </p>
          )}

          <details className="automation-history" open={a.history.length > 0 && a.history.length <= 5}>
            <summary>History ({a.history.length})</summary>
            {a.note && <p className="muted">{a.note}</p>}
            {a.history.length === 0 ? (
              <p className="muted">No runs yet.</p>
            ) : (
              <table className="automation-table">
                <thead>
                  <tr>
                    <th scope="col">When</th>
                    <th scope="col">Trigger</th>
                    <th scope="col">Status</th>
                    <th scope="col">Cost</th>
                    <th scope="col">Result</th>
                  </tr>
                </thead>
                <tbody>
                  {a.history.map((r) => (
                    <tr key={r.id} className={`run-${r.status}`}>
                      <td title={new Date(r.started_at_ms).toLocaleString()}>{relative(new Date(r.started_at_ms), now)}</td>
                      <td>{r.trigger === "manual" ? "Run now" : "schedule"}</td>
                      <td className={r.status === "failed" ? "error" : r.status === "succeeded" ? "ok-text" : "muted"}>{r.status}</td>
                      <td>{runCost(r)}</td>
                      <td>{runDetail(r)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </details>
        </section>
      ))}
    </div>
  );
}

function EvalSettingsForm({ job, base, onSaved }: { job: Automation; base: string; onSaved: (msg: string, ok: boolean) => void }) {
  const [form, setForm] = useState<EvalForm>(() => evalFormFrom(job.settings));
  const [errors, setErrors] = useState<Record<string, string>>({});
  const set = (k: keyof EvalForm, v: string) => setForm((f) => ({ ...f, [k]: v }));
  const save = async () => {
    const e = validateEvalForm(form);
    setErrors(e);
    if (Object.keys(e).length) return;
    try {
      await put(`${base}/eval`, evalPutBody(form));
      onSaved("Eval settings saved.", true);
    } catch (err) {
      onSaved(err instanceof Error ? err.message : String(err), false);
    }
  };
  const field = (k: keyof EvalForm, label: string, placeholder: string, mode?: "decimal" | "numeric") => (
    <>
      <label className="field-label" htmlFor={`eval-${k}`}>
        {label}
      </label>
      <input
        id={`eval-${k}`}
        className="text-input"
        inputMode={mode}
        placeholder={placeholder}
        value={form[k]}
        onChange={(e) => set(k, e.target.value)}
        {...(errors[k] ? { "aria-invalid": true, "aria-describedby": `eval-${k}-error` } : {})}
      />
      {errors[k] && (
        <p className="error" id={`eval-${k}-error`}>
          {errors[k]}
        </p>
      )}
    </>
  );
  return (
    <div className="eval-settings">
      {field("model", "Model", "openai/gpt-4o-mini")}
      {field("maxCostUSD", "Cost cap per run (USD)", "2", "decimal")}
      {field("intervalHours", "Every (hours)", "24", "numeric")}
      <details>
        <summary>Suite, cases and baseline</summary>
        {field("suite", "Suite file (relative to the project root; empty for the built-in suite)", "")}
        {field("cases", "Cases (empty runs every case)", "todo-cli, notes")}
        {field("baseline", "Baseline report (relative; empty compares with the previous run)", "evals/baseline.json")}
      </details>
      <div className="row">
        <button onClick={save}>Save eval settings</button>
      </div>
    </div>
  );
}
