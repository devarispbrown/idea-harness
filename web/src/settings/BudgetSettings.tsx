import { useCallback, useEffect, useState } from "react";
import { get, put } from "../api";
import { capInputError, formatUSD, meterLabel, parseCap, type BudgetStatus } from "../budget";

interface ProjectBudget {
  default_initiative_usd: number;
  initiatives: { initiative_id: string; title: string; status: string; budget: BudgetStatus }[];
}

// BudgetSettings is Settings → Budget: the project's default per-initiative
// spending cap (budget.default_initiative_usd in harness.yaml) and every
// initiative's meter. One initiative's own cap is set on its Build page.
export default function BudgetSettings({ projectId }: { projectId: string }) {
  const [view, setView] = useState<ProjectBudget | null>(null);
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");

  const load = useCallback(async () => {
    try {
      const v = await get<ProjectBudget>(`/projects/${encodeURIComponent(projectId)}/budget`);
      setView(v);
      setText(v.default_initiative_usd > 0 ? String(v.default_initiative_usd) : "");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  }, [projectId]);

  useEffect(() => {
    load();
  }, [load]);

  const save = async (raw: string) => {
    setError("");
    setSaved("");
    const value = raw.trim() === "" ? "0" : raw;
    const msg = capInputError(value, true);
    if (msg) {
      setError(msg);
      return;
    }
    try {
      const res = await put<{ resumed: string[] }>(`/projects/${encodeURIComponent(projectId)}/budget`, {
        default_initiative_usd: parseCap(value),
      });
      const v = parseCap(value);
      setSaved(
        (v === 0 ? "Project default removed: initiatives without their own cap are uncapped." : `Every initiative without its own cap is now capped at ${formatUSD(v)}.`) +
          (res.resumed.length ? ` ${res.resumed.length} stopped initiative(s) went back to where they stopped.` : ""),
      );
      await load();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    }
  };

  return (
    <section className="card" aria-labelledby="budget-settings-title">
      <h2 id="budget-settings-title">Budget</h2>
      <p className="muted">
        A spending cap per initiative, checked before every model call and before the coding agent starts. Reaching it
        stops the run and waits for you; raising it sends the initiative back to where it stopped. Hermes and OMP usually
        report no cost, so their spend is not counted — the meter says so when that happens.
      </p>
      <label className="field-label" htmlFor="budget-default-input">
        Default cap per initiative (USD, empty for none)
      </label>
      <div className="row">
        <input
          id="budget-default-input"
          className="text-input budget-input"
          inputMode="decimal"
          placeholder="none"
          value={text}
          onChange={(e) => setText(e.target.value)}
        />
        <button onClick={() => save(text)}>Save default</button>
        {view && view.default_initiative_usd > 0 && (
          <button className="ghost" onClick={() => save("0")}>
            Remove default
          </button>
        )}
      </div>
      {view && view.initiatives.length > 0 && (
        <ul className="list budget-list">
          {view.initiatives.map((i) => (
            <li key={i.initiative_id}>
              <a href={`#/i/${i.initiative_id}/build`}>{i.title}</a>{" "}
              <span className={i.budget.parked || i.budget.exceeded ? "warn-text" : "muted"}>
                {meterLabel(i.budget)}
                {i.budget.parked ? " — stopped at its cap" : ""}
                {i.budget.agent_not_counted ? " · agent spend not counted" : ""}
              </span>
            </li>
          ))}
        </ul>
      )}
      {saved && <p className="ok-text">{saved}</p>}
      {error && <p className="error">{error}</p>}
    </section>
  );
}
