import { useCallback, useEffect, useState } from "react";
import { put, get, resource } from "./api";
import {
  agentCaveat,
  capInputError,
  formatUSD,
  meterFraction,
  meterLabel,
  meterTone,
  parseCap,
  raiseHint,
  showInShell,
  type BudgetStatus,
} from "./budget";

// BudgetMeter is the spending-cap meter (GET .../initiatives/{id}/budget).
//
// variant "shell" is the compact strip the app shell shows on every phase,
// and only when there is a cap (or the initiative is stopped at one); an
// uncapped initiative's spend is already in the sidebar's cost line.
// variant "build" is the full card on Build, with the controls: set this
// initiative's own cap, or clear it back to the project default. Raising a
// cap past the spend returns a stopped initiative to where it stopped.
export default function BudgetMeter({
  projectId,
  initiativeId,
  variant,
  onChanged,
}: {
  projectId: string;
  initiativeId: string;
  variant: "shell" | "build";
  onChanged?: () => void;
}) {
  const [budget, setBudget] = useState<BudgetStatus | null>(null);
  const [capText, setCapText] = useState("");
  const [error, setError] = useState("");
  const [saved, setSaved] = useState("");
  const [saving, setSaving] = useState(false);

  const load = useCallback(async () => {
    try {
      setBudget(await get<BudgetStatus>(resource(projectId, initiativeId, "budget")));
    } catch {
      // A daemon that predates budgets, or a transient failure: no meter.
      setBudget(null);
    }
  }, [projectId, initiativeId]);

  useEffect(() => {
    load();
    const id = setInterval(load, variant === "shell" ? 10000 : 5000);
    return () => clearInterval(id);
  }, [load, variant]);

  if (variant === "shell") {
    if (!showInShell(budget) || !budget) return null;
    const tone = meterTone(budget);
    return (
      <div className={`budget-shell budget-${tone}`} role="status" aria-label="Spending cap">
        <span className="budget-shell-label">Budget</span>
        <Bar budget={budget} />
        <span className="budget-shell-text">
          {budget.parked ? "Stopped at its spending cap — " : ""}
          {meterLabel(budget)}
          {budget.agent_not_counted ? " · agent spend not counted" : ""}
        </span>
        {budget.parked && (
          <a className="budget-shell-link" href={`#/i/${initiativeId}/build`}>
            Raise the cap
          </a>
        )}
      </div>
    );
  }

  const save = async (body: { cap_usd: number } | { clear: true }) => {
    setSaving(true);
    setError("");
    setSaved("");
    try {
      const res = await put<{ budget: BudgetStatus; resumed: boolean }>(resource(projectId, initiativeId, "budget"), body);
      setBudget(res.budget);
      setCapText("");
      setSaved(
        res.resumed
          ? "Cap raised — the initiative is back where it stopped. Run the step again."
          : "clear" in body
            ? "Back to the project default."
            : `Cap set to ${formatUSD(body.cap_usd)}.`,
      );
      onChanged?.();
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setSaving(false);
    }
  };

  const submitCap = () => {
    const msg = capInputError(capText, false);
    if (msg) {
      setError(msg);
      return;
    }
    save({ cap_usd: parseCap(capText) });
  };

  if (!budget) return null;
  const caveat = agentCaveat(budget);
  return (
    <section className={`card budget-card budget-${meterTone(budget)}`} aria-labelledby="budget-card-title">
      <h2 id="budget-card-title">Spending cap</h2>
      {budget.parked && (
        <p className="warn-text" role="alert">
          This initiative reached its cap and stopped before the next model or agent call. Raise the cap above{" "}
          {formatUSD(budget.spent_usd)} to send it back to where it stopped.
        </p>
      )}
      <div className="budget-row">
        <Bar budget={budget} />
        <span>{meterLabel(budget)}</span>
      </div>
      <p className="muted">
        Model calls {formatUSD(budget.model_usd)}
        {budget.agent_usd !== null ? ` · coding agent ${formatUSD(budget.agent_usd)}` : ""}
        {budget.remaining_usd !== null ? ` · ${formatUSD(budget.remaining_usd)} left` : ""}
      </p>
      {caveat && <p className="warn-text">{caveat}</p>}
      <div className="row budget-controls">
        <label className="field-label" htmlFor="budget-cap-input">
          This initiative's cap (USD)
        </label>
        <input
          id="budget-cap-input"
          className="text-input budget-input"
          inputMode="decimal"
          placeholder={budget.parked ? String(raiseHint(budget)) : budget.cap_usd !== null ? String(budget.cap_usd) : "e.g. 5"}
          value={capText}
          onChange={(e) => setCapText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") submitCap();
          }}
        />
        <button disabled={saving} onClick={submitCap}>
          {budget.parked ? "Raise cap" : "Set cap"}
        </button>
        {budget.initiative_cap_usd !== null && (
          <button className="ghost" disabled={saving} onClick={() => save({ clear: true })}>
            Use project default
            {budget.project_default_usd > 0 ? ` (${formatUSD(budget.project_default_usd)})` : " (none)"}
          </button>
        )}
      </div>
      <p className="muted">
        {budget.source === "none"
          ? "No cap. Set one here, or a default for every initiative in Settings → Budget."
          : "Checked before every model call and before the coding agent starts; a call already running finishes."}
      </p>
      {saved && <p className="ok-text">{saved}</p>}
      {error && <p className="error">{error}</p>}
    </section>
  );
}

function Bar({ budget }: { budget: BudgetStatus }) {
  const pct = Math.round(meterFraction(budget) * 100);
  return (
    <span
      className="budget-bar"
      role="meter"
      aria-label="Spent against the cap"
      aria-valuemin={0}
      aria-valuemax={100}
      aria-valuenow={budget.cap_usd === null ? 0 : pct}
      aria-valuetext={meterLabel(budget)}
    >
      <span className="budget-bar-fill" style={{ width: `${budget.cap_usd === null ? 0 : pct}%` }} />
    </span>
  );
}
